package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
)

// minAPIKeyLength is the shortest API key accepted. Short keys are easy to
// brute force, so the server refuses to start with one.
const minAPIKeyLength = 32

// Config holds all runtime settings. Every field is read from an environment
// variable so the server can be configured the same way in Docker,
// Kubernetes and local development.
type Config struct {
	// Addr is the address the HTTP server listens on (DHC_ADDR).
	Addr string
	// DatabasePath is the SQLite database file (DHC_DATABASE_PATH).
	DatabasePath string

	// IngestAPIKeys may POST health check results (DHC_INGEST_API_KEYS).
	IngestAPIKeys []string
	// ReadAPIKeys may read metrics, including the Prometheus scrape
	// endpoint (DHC_READ_API_KEYS). Ingest keys can read too.
	ReadAPIKeys []string
	// AuthDisabled turns off API key checks entirely (DHC_AUTH_DISABLED).
	// Only meant for local development.
	AuthDisabled bool

	// CORSAllowedOrigins lists browser origins allowed to call the API
	// (DHC_CORS_ALLOWED_ORIGINS). Empty means CORS is off.
	CORSAllowedOrigins []string
	// TrustedProxies lists proxy IPs/CIDRs whose X-Forwarded-For header is
	// believed (DHC_TRUSTED_PROXIES). Empty means trust no proxy.
	TrustedProxies []string

	// TLSCertFile and TLSKeyFile enable HTTPS when both are set
	// (DHC_TLS_CERT_FILE, DHC_TLS_KEY_FILE).
	TLSCertFile string
	TLSKeyFile  string

	// RateLimitRPS and RateLimitBurst limit requests per client IP
	// (DHC_RATE_LIMIT_RPS, DHC_RATE_LIMIT_BURST). RPS 0 disables it.
	RateLimitRPS   float64
	RateLimitBurst int

	// MaxBodyBytes caps the size of a request body (DHC_MAX_BODY_BYTES).
	MaxBodyBytes int64
	// MaxSeries caps the number of distinct (source, check) pairs, which
	// protects metric backends from a cardinality explosion (DHC_MAX_SERIES).
	MaxSeries int
	// RetentionPeriod is how long reports are kept; 0 (the default) keeps
	// them forever (DHC_RETENTION_PERIOD, a Go duration such as 720h).
	RetentionPeriod time.Duration

	// PrometheusEnabled serves the /metrics scrape endpoint
	// (DHC_PROMETHEUS_ENABLED, or "prometheus" in OTEL_METRICS_EXPORTER).
	PrometheusEnabled bool
	// OTLPEnabled pushes metrics over OTLP. When OTEL_METRICS_EXPORTER is
	// set it decides; otherwise OTLP is on when OTEL_EXPORTER_OTLP_ENDPOINT
	// or OTEL_EXPORTER_OTLP_METRICS_ENDPOINT is set. The remaining
	// OTEL_EXPORTER_OTLP_* variables are read by the OpenTelemetry SDK.
	OTLPEnabled bool
	// OTLPProtocol is "http/protobuf" (default) or "grpc", taken from
	// OTEL_EXPORTER_OTLP_METRICS_PROTOCOL or OTEL_EXPORTER_OTLP_PROTOCOL.
	OTLPProtocol string
}

// TLSEnabled reports whether the server should serve HTTPS.
func (c Config) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// LoadConfigFromEnv reads the configuration from the process environment.
func LoadConfigFromEnv() (Config, error) {
	return LoadConfig(os.Getenv)
}

// LoadConfig reads the configuration using getenv, which makes it testable.
func LoadConfig(getenv func(string) string) (Config, error) {
	var errs []error
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Addr:               env("DHC_ADDR", ":8080"),
		DatabasePath:       env("DHC_DATABASE_PATH", "dhc.db"),
		IngestAPIKeys:      splitList(getenv("DHC_INGEST_API_KEYS")),
		ReadAPIKeys:        splitList(getenv("DHC_READ_API_KEYS")),
		CORSAllowedOrigins: splitList(getenv("DHC_CORS_ALLOWED_ORIGINS")),
		TrustedProxies:     splitList(getenv("DHC_TRUSTED_PROXIES")),
		TLSCertFile:        env("DHC_TLS_CERT_FILE", ""),
		TLSKeyFile:         env("DHC_TLS_KEY_FILE", ""),
	}

	parseBool := func(key string, def bool) bool {
		v, err := strconv.ParseBool(env(key, strconv.FormatBool(def)))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return v
	}
	parseInt := func(key string, def int64) int64 {
		v, err := strconv.ParseInt(env(key, strconv.FormatInt(def, 10)), 10, 64)
		if err != nil || v < 0 {
			errs = append(errs, fmt.Errorf("%s: must be a non-negative integer", key))
		}
		return v
	}

	cfg.AuthDisabled = parseBool("DHC_AUTH_DISABLED", false)
	cfg.PrometheusEnabled = parseBool("DHC_PROMETHEUS_ENABLED", true)
	cfg.RateLimitBurst = int(parseInt("DHC_RATE_LIMIT_BURST", 20))
	cfg.MaxBodyBytes = parseInt("DHC_MAX_BODY_BYTES", 64*1024)
	cfg.MaxSeries = int(parseInt("DHC_MAX_SERIES", 1000))

	rps, err := strconv.ParseFloat(env("DHC_RATE_LIMIT_RPS", "10"), 64)
	if err != nil || rps < 0 {
		errs = append(errs, errors.New("DHC_RATE_LIMIT_RPS: must be a non-negative number"))
	}
	cfg.RateLimitRPS = rps

	retention, err := time.ParseDuration(env("DHC_RETENTION_PERIOD", "0s"))
	if err != nil || retention < 0 {
		errs = append(errs, errors.New("DHC_RETENTION_PERIOD: must be a non-negative duration such as 720h"))
	}
	cfg.RetentionPeriod = retention

	cfg.OTLPEnabled = env("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "") != "" ||
		env("OTEL_EXPORTER_OTLP_ENDPOINT", "") != ""
	// OTEL_METRICS_EXPORTER is the standard way to pick exporters:
	// a comma-separated list of "otlp", "prometheus" or "none".
	if exporters := env("OTEL_METRICS_EXPORTER", ""); exporters != "" {
		cfg.OTLPEnabled, cfg.PrometheusEnabled = false, false
		for _, e := range splitList(exporters) {
			switch strings.ToLower(e) {
			case "otlp":
				cfg.OTLPEnabled = true
			case "prometheus":
				cfg.PrometheusEnabled = true
			case "none":
			default:
				errs = append(errs, fmt.Errorf("OTEL_METRICS_EXPORTER: unsupported exporter %q (use otlp, prometheus or none)", e))
			}
		}
	}
	if parseBool("OTEL_SDK_DISABLED", false) {
		cfg.OTLPEnabled, cfg.PrometheusEnabled = false, false
	}
	cfg.OTLPProtocol = env("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
		env("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"))
	if cfg.OTLPProtocol != "http/protobuf" && cfg.OTLPProtocol != "grpc" {
		errs = append(errs, fmt.Errorf("unsupported OTLP protocol %q: use http/protobuf or grpc", cfg.OTLPProtocol))
	}

	// Fail closed: without keys anyone could write fake health data.
	if !cfg.AuthDisabled {
		if len(cfg.IngestAPIKeys) == 0 {
			errs = append(errs, errors.New("DHC_INGEST_API_KEYS is required (or set DHC_AUTH_DISABLED=true for local development)"))
		}
		for _, k := range append(append([]string{}, cfg.IngestAPIKeys...), cfg.ReadAPIKeys...) {
			if len(k) < minAPIKeyLength {
				errs = append(errs, fmt.Errorf("API keys must be at least %d characters (generate one with: openssl rand -hex 32)", minAPIKeyLength))
				break
			}
		}
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		errs = append(errs, errors.New("DHC_TLS_CERT_FILE and DHC_TLS_KEY_FILE must be set together"))
	}
	if cfg.RateLimitRPS > 0 && cfg.RateLimitBurst < 1 {
		errs = append(errs, errors.New("DHC_RATE_LIMIT_BURST must be at least 1 when rate limiting is on"))
	}
	if len(cfg.CORSAllowedOrigins) > 0 {
		// cors.New panics on a bad origin, so check here instead.
		c := cors.Config{AllowOrigins: cfg.CORSAllowedOrigins}
		if err := c.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("DHC_CORS_ALLOWED_ORIGINS: %w (origins need a scheme, e.g. https://example.com)", err))
		}
	}
	if cfg.MaxSeries == 0 {
		errs = append(errs, errors.New("DHC_MAX_SERIES must be greater than 0"))
	}
	if cfg.MaxBodyBytes == 0 {
		errs = append(errs, errors.New("DHC_MAX_BODY_BYTES must be greater than 0"))
	}

	return cfg, errors.Join(errs...)
}

// splitList splits a comma-separated value, trimming blanks.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
