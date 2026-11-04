package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	l "dhc-server/logging"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// App is a fully wired server: database, telemetry and HTTP router.
type App struct {
	cfg       Config
	logger    *l.Logger
	store     *Store
	telemetry *Telemetry
	router    *gin.Engine
}

var registerValidatorsOnce sync.Once

func registerValidators() {
	registerValidatorsOnce.Do(func() {
		if v, ok := binding.Validator.Engine().(*validator.Validate); ok {
			_ = v.RegisterValidation("safetext", safeText)
		}
	})
}

// requestLogger logs one line per request. It logs the route pattern, not
// the raw URL, and never logs headers, so API keys do not end up in logs.
func requestLogger(logger *l.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		logger.Info("request",
			l.LogArg{Key: "method", Value: c.Request.Method},
			l.LogArg{Key: "route", Value: route},
			l.LogArg{Key: "status_code", Value: c.Writer.Status()},
			l.LogArg{Key: "latency", Value: time.Since(start)},
			l.LogArg{Key: "client_ip", Value: c.ClientIP()},
		)
	}
}

// NewApp opens the database, sets up telemetry and builds the router.
func NewApp(ctx context.Context, cfg Config, logger *l.Logger, extraReaders ...sdkmetric.Reader) (*App, error) {
	registerValidators()

	store, err := OpenStore(cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	telemetry, err := NewTelemetry(ctx, cfg, store, extraReaders...)
	if err != nil {
		_ = store.Close()
		return nil, err
	}

	app := &App{cfg: cfg, logger: logger, store: store, telemetry: telemetry}
	if app.router, err = app.buildRouter(); err != nil {
		_ = telemetry.Shutdown(ctx)
		_ = store.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) buildRouter() (*gin.Engine, error) {
	router := gin.New()
	// Only trust X-Forwarded-For from configured proxies. Otherwise any
	// client could fake its IP and dodge the rate limiter.
	if err := router.SetTrustedProxies(a.cfg.TrustedProxies); err != nil {
		return nil, fmt.Errorf("DHC_TRUSTED_PROXIES: %w", err)
	}

	router.Use(gin.Recovery())
	router.Use(requestLogger(a.logger))
	router.Use(securityHeaders(a.cfg.TLSEnabled()))
	if len(a.cfg.CORSAllowedOrigins) > 0 {
		router.Use(cors.New(cors.Config{
			AllowOrigins: a.cfg.CORSAllowedOrigins,
			AllowMethods: []string{http.MethodGet, http.MethodPost},
			AllowHeaders: []string{"Authorization", "Content-Type", "X-API-Key"},
			MaxAge:       12 * time.Hour,
		}))
	}
	if a.cfg.RateLimitRPS > 0 {
		var authenticated func(*http.Request) bool
		if !a.cfg.AuthDisabled {
			allKeys := newKeySet(a.cfg.IngestAPIKeys, a.cfg.ReadAPIKeys)
			authenticated = func(r *http.Request) bool { return allKeys.contains(tokenFromRequest(r)) }
		}
		limiter := newIPRateLimiter(a.cfg.RateLimitRPS, a.cfg.RateLimitBurst, maxTrackedClients)
		router.Use(limiter.middleware(authenticated))
	}
	router.Use(limitBody(a.cfg.MaxBodyBytes))

	// Writers need an ingest key; readers need a read or an ingest key.
	writeAuth, readAuth := gin.HandlerFunc(func(c *gin.Context) { c.Next() }), gin.HandlerFunc(func(c *gin.Context) { c.Next() })
	if a.cfg.AuthDisabled {
		a.logger.Warn("API key authentication is DISABLED; do not run like this in production")
	} else {
		writeAuth = requireAPIKey(newKeySet(a.cfg.IngestAPIKeys))
		readAuth = requireAPIKey(newKeySet(a.cfg.IngestAPIKeys, a.cfg.ReadAPIKeys))
	}

	h := &VersionOneHandler{
		store:     a.store,
		telemetry: a.telemetry,
		logger:    a.logger,
		maxSeries: a.cfg.MaxSeries,
	}

	v1 := router.Group("/v1")
	{
		// Liveness stays public so load balancers can probe it.
		v1.GET("/", h.HealthCheck)
		v1.POST("/metrics", writeAuth, h.CreateMetric)
		v1.GET("/metrics", readAuth, h.GetMetrics)
		v1.GET("/metrics/latest", readAuth, h.GetLatest)
		v1.GET("/metrics/summary", readAuth, h.GetMetricsSummary)
	}

	if promHandler := a.telemetry.PrometheusHandler(); promHandler != nil {
		router.GET("/metrics", readAuth, gin.WrapH(promHandler))
	}
	return router, nil
}

// Close flushes telemetry and closes the database.
func (a *App) Close(ctx context.Context) error {
	return errors.Join(a.telemetry.Shutdown(ctx), a.store.Close())
}

// runRetention deletes old reports every hour until ctx is cancelled.
func (a *App) runRetention(ctx context.Context) {
	if a.cfg.RetentionPeriod == 0 {
		return
	}
	purge := func() {
		n, err := a.store.PurgeOlderThan(ctx, time.Now().Add(-a.cfg.RetentionPeriod))
		if err != nil {
			if ctx.Err() == nil {
				a.logger.Error("retention purge failed", l.LogArg{Key: "error", Value: err.Error()})
			}
			return
		}
		if n > 0 {
			a.logger.Info("retention purge", l.LogArg{Key: "deleted", Value: n})
		}
	}
	purge()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purge()
		}
	}
}

func run() error {
	logger, err := l.NewLogger()
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}

	if os.Getenv(gin.EnvGinMode) == "" {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := NewApp(ctx, cfg, logger)
	if err != nil {
		return err
	}
	go app.runRetention(ctx)

	// Timeouts stop slow clients from holding connections open forever.
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server starting",
			l.LogArg{Key: "addr", Value: cfg.Addr},
			l.LogArg{Key: "tls", Value: cfg.TLSEnabled()},
			l.LogArg{Key: "prometheus", Value: cfg.PrometheusEnabled},
			l.LogArg{Key: "otlp", Value: cfg.OTLPEnabled},
			l.LogArg{Key: "otlp_protocol", Value: cfg.OTLPProtocol},
		)
		var err error
		if cfg.TLSEnabled() {
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = app.Close(context.Background())
			return err
		}
	case <-ctx.Done():
		// Restore default signal handling so a second Ctrl-C exits at once.
		stop()
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return errors.Join(srv.Shutdown(shutdownCtx), app.Close(shutdownCtx))
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dhc-server:", err)
		os.Exit(1)
	}
}
