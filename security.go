package main

import (
	"container/list"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"golang.org/x/time/rate"
)

// keySet holds SHA-256 digests of API keys. Comparing fixed-length digests
// with subtle.ConstantTimeCompare avoids leaking key contents or length
// through response timing.
type keySet [][sha256.Size]byte

func newKeySet(keys ...[]string) keySet {
	var ks keySet
	for _, group := range keys {
		for _, k := range group {
			ks = append(ks, sha256.Sum256([]byte(k)))
		}
	}
	return ks
}

func (ks keySet) contains(token string) bool {
	if token == "" {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	found := 0
	// Check every key so the time taken does not depend on which matched.
	for i := range ks {
		found |= subtle.ConstantTimeCompare(digest[:], ks[i][:])
	}
	return found == 1
}

// tokenFromRequest reads "Authorization: Bearer <key>" or "X-API-Key: <key>".
func tokenFromRequest(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		scheme, token, ok := strings.Cut(auth, " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// requireAPIKey rejects requests without one of the allowed keys.
func requireAPIKey(keys keySet) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !keys.contains(tokenFromRequest(c.Request)) {
			c.Header("WWW-Authenticate", `Bearer realm="dhc-server"`)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid API key"})
			return
		}
		c.Next()
	}
}

// securityHeaders sets headers that stop browsers from sniffing, framing or
// caching API responses.
func securityHeaders(tls bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		if tls {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// limitBody caps request body size. Reading past the limit fails, and the
// handler answers 413.
func limitBody(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// ipRateLimiter limits requests that do not carry a valid API key, with one
// token bucket per client address. It protects against key guessing and
// anonymous floods. Requests with a valid key are not limited here: behind a
// proxy many legitimate clients can share one IP, and anonymous traffic must
// not be able to push them into 429s. Authenticated clients are still bound
// by the body size and series limits.
type ipRateLimiter struct {
	mu         sync.Mutex
	limit      rate.Limit
	burst      int
	maxClients int
	clients    map[string]*list.Element
	order      *list.List // front = most recently seen
}

type clientLimiter struct {
	key     string
	limiter *rate.Limiter
}

// maxTrackedClients bounds memory if many distinct addresses show up.
const maxTrackedClients = 10000

func newIPRateLimiter(rps float64, burst, maxClients int) *ipRateLimiter {
	return &ipRateLimiter{
		limit:      rate.Limit(rps),
		burst:      burst,
		maxClients: maxClients,
		clients:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

// clientKey groups IPv6 addresses by their /64 prefix, because a single
// host is usually given a whole /64 and could otherwise rotate addresses to
// get a fresh bucket each time.
func clientKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

func (l *ipRateLimiter) allow(ip string) bool {
	key := clientKey(ip)

	l.mu.Lock()
	defer l.mu.Unlock()

	if el, ok := l.clients[key]; ok {
		l.order.MoveToFront(el)
		return el.Value.(*clientLimiter).limiter.Allow()
	}

	// Evict only the least recently seen client when full. Clearing the
	// whole table would let a flood of new addresses reset everyone's limit.
	if l.order.Len() >= l.maxClients {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.clients, oldest.Value.(*clientLimiter).key)
	}
	cl := &clientLimiter{key: key, limiter: rate.NewLimiter(l.limit, l.burst)}
	l.clients[key] = l.order.PushFront(cl)
	return cl.limiter.Allow()
}

// middleware applies the limit. authenticated reports whether a request
// carries a valid API key; nil means every request is limited.
func (l *ipRateLimiter) middleware(authenticated func(*http.Request) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticated != nil && authenticated(c.Request) {
			c.Next()
			return
		}
		if !l.allow(c.ClientIP()) {
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}

// safeText accepts only printable text: letters, marks, numbers,
// punctuation, symbols and the ASCII space. It rejects invalid UTF-8,
// blank values, control characters and invisible format characters such as
// zero-width spaces, bidi overrides and line separators. These values become
// metric labels, so invisible characters could create look-alike series that
// spoof dashboards and alerts.
func safeText(fl validator.FieldLevel) bool {
	s := fl.Field().String()
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
