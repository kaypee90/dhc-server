package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

const (
	serviceName = "dhc-server"
	meterName   = "dhc-server"

	// Attribute keys on every health check data point.
	attrSource = "source"
	attrCheck  = "check"
)

// Version is the service version reported as service.version. Override at
// build time with: go build -ldflags "-X main.Version=1.2.3"
var Version = "dev"

// Telemetry owns the OpenTelemetry meter provider and the instruments that
// turn stored health check reports into standard metrics.
//
// The same data goes out two ways:
//   - Prometheus scrape endpoint (/metrics), for Prometheus, Grafana Alloy,
//     the Datadog Agent's OpenMetrics check and the Collector's prometheus
//     receiver.
//   - OTLP push, for the OpenTelemetry Collector, the Datadog Agent's OTLP
//     intake, Grafana Cloud and any other OTLP backend.
type Telemetry struct {
	provider    *sdkmetric.MeterProvider
	promHandler http.Handler
	reports     metric.Int64Counter
}

// NewTelemetry builds the meter provider. extraReaders lets tests attach a
// manual reader.
func NewTelemetry(ctx context.Context, cfg Config, store *Store, extraReaders ...sdkmetric.Reader) (*Telemetry, error) {
	// Defaults first, then OTEL_SERVICE_NAME / OTEL_RESOURCE_ATTRIBUTES so
	// operators can override them the standard way.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(Version),
		),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	opts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		// The SDK keeps 2000 attribute sets per instrument by default and
		// folds the rest into an "overflow" point. Allow every series the
		// server accepts, plus the overflow slot.
		sdkmetric.WithCardinalityLimit(cfg.MaxSeries + 1),
	}
	t := &Telemetry{}

	if cfg.PrometheusEnabled {
		// A private registry keeps the endpoint to our own metrics.
		registry := prom.NewRegistry()
		exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
		if err != nil {
			return nil, fmt.Errorf("create prometheus exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(exporter))
		t.promHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	}

	if cfg.OTLPEnabled {
		// Endpoint, headers (e.g. API keys), TLS, timeout, compression and
		// temporality all come from the standard OTEL_EXPORTER_OTLP_*
		// variables; the export interval from OTEL_METRIC_EXPORT_INTERVAL.
		var exporter sdkmetric.Exporter
		if cfg.OTLPProtocol == "grpc" {
			exporter, err = otlpmetricgrpc.New(ctx)
		} else {
			exporter, err = otlpmetrichttp.New(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("create OTLP exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)))
	}

	for _, r := range extraReaders {
		opts = append(opts, sdkmetric.WithReader(r))
	}

	t.provider = sdkmetric.NewMeterProvider(opts...)
	if err := t.registerInstruments(store, cfg.MaxSeries); err != nil {
		_ = t.provider.Shutdown(ctx)
		return nil, err
	}
	return t, nil
}

func (t *Telemetry) registerInstruments(store *Store, maxSeries int) error {
	meter := t.provider.Meter(meterName, metric.WithInstrumentationVersion(Version))

	// Unit "{status}" is an annotation, so Prometheus adds no unit suffix:
	// the metric is exported as dhc_health_check_status.
	status, err := meter.Int64ObservableGauge("dhc.health_check.status",
		metric.WithDescription("Latest value reported by each Django health check."),
		metric.WithUnit("{status}"))
	if err != nil {
		return err
	}
	lastReport, err := meter.Float64ObservableGauge("dhc.health_check.last_report_time",
		metric.WithDescription("Unix time of the latest report from each Django health check. Use it to alert on checks that stopped reporting."),
		metric.WithUnit("s"))
	if err != nil {
		return err
	}
	// The counter has no per-series attributes. A cumulative counter never
	// forgets an attribute set, so as checks come and go over the server's
	// lifetime it would grow past the cardinality limit. Per-check detail
	// lives in the gauges, which only cover series still in the database.
	t.reports, err = meter.Int64Counter("dhc.health_check.reports",
		metric.WithDescription("Number of health check reports received since the server started."),
		metric.WithUnit("{report}"))
	if err != nil {
		return err
	}

	// The gauges are read from the database at collection time, so every
	// reader (scrape or push) sees the latest stored value of each series.
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		rows, err := store.LatestPerSeries(ctx, maxSeries)
		if err != nil {
			return fmt.Errorf("read latest health checks: %w", err)
		}
		for _, r := range rows {
			attrs := metric.WithAttributes(
				attribute.String(attrSource, r.Source),
				attribute.String(attrCheck, r.Label),
			)
			o.ObserveInt64(status, int64(r.Value), attrs)
			o.ObserveFloat64(lastReport, float64(r.ReportedAt.UnixNano())/1e9, attrs)
		}
		return nil
	}, status, lastReport)
	return err
}

// RecordReport counts one accepted report.
func (t *Telemetry) RecordReport(ctx context.Context) {
	t.reports.Add(ctx, 1)
}

// PrometheusHandler serves the scrape endpoint, or nil when it is disabled.
func (t *Telemetry) PrometheusHandler() http.Handler {
	return t.promHandler
}

// Shutdown flushes pending OTLP exports and stops the readers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if err := t.provider.Shutdown(ctx); err != nil && !errors.Is(err, sdkmetric.ErrReaderShutdown) {
		return err
	}
	return nil
}
