package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	l "dhc-server/logging"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

const (
	ingestKey = "ingest-key-0123456789abcdef0123456789abcdef"
	readKey   = "read-key-0123456789abcdef0123456789abcdef00"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func testConfig() Config {
	return Config{
		Addr:              ":0",
		DatabasePath:      ":memory:",
		IngestAPIKeys:     []string{ingestKey},
		ReadAPIKeys:       []string{readKey},
		MaxBodyBytes:      64 * 1024,
		MaxSeries:         1000,
		PrometheusEnabled: true,
		OTLPProtocol:      "http/protobuf",
	}
}

func newTestApp(t *testing.T, cfg Config, readers ...sdkmetric.Reader) *App {
	t.Helper()
	app, err := NewApp(context.Background(), cfg, l.NewNop(), readers...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	return app
}

func doRequest(app *App, method, path, key string, body interface{}) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	switch b := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case []byte:
		reader = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	app.router.ServeHTTP(w, req)
	return w
}

func report(label string, value int, source string) map[string]interface{} {
	return map[string]interface{}{
		"label":       label,
		"value":       value,
		"description": "status from " + source,
		"source":      source,
	}
}

func TestHealthCheckIsPublic(t *testing.T) {
	app := newTestApp(t, testConfig())
	w := doRequest(app, http.MethodGet, "/v1/", "", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Healthy")
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

func TestCreateMetric(t *testing.T) {
	app := newTestApp(t, testConfig())

	w := doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db-check", 1, "dhc-service"))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var stored Metric
	require.NoError(t, app.store.db.First(&stored).Error)
	assert.Equal(t, "db-check", stored.Label)
	assert.Equal(t, 1, stored.Value)
}

func TestCreateMetricAcceptsZeroValue(t *testing.T) {
	app := newTestApp(t, testConfig())
	w := doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db-check", 0, "dhc-service"))
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestCreateMetricAcceptsXAPIKeyHeader(t *testing.T) {
	app := newTestApp(t, testConfig())
	raw, _ := json.Marshal(report("db-check", 1, "svc"))
	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", ingestKey)
	w := httptest.NewRecorder()
	app.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAuthentication(t *testing.T) {
	app := newTestApp(t, testConfig())
	body := report("db-check", 1, "svc")

	cases := []struct {
		name   string
		method string
		path   string
		key    string
		want   int
	}{
		{"write without key", http.MethodPost, "/v1/metrics", "", http.StatusUnauthorized},
		{"write with wrong key", http.MethodPost, "/v1/metrics", "wrong-key-0123456789abcdef0123456789ab", http.StatusUnauthorized},
		{"write with read key", http.MethodPost, "/v1/metrics", readKey, http.StatusUnauthorized},
		{"write with ingest key", http.MethodPost, "/v1/metrics", ingestKey, http.StatusCreated},
		{"read without key", http.MethodGet, "/v1/metrics", "", http.StatusUnauthorized},
		{"read with read key", http.MethodGet, "/v1/metrics", readKey, http.StatusOK},
		{"read with ingest key", http.MethodGet, "/v1/metrics", ingestKey, http.StatusOK},
		{"summary without key", http.MethodGet, "/v1/metrics/summary", "", http.StatusUnauthorized},
		{"latest without key", http.MethodGet, "/v1/metrics/latest", "", http.StatusUnauthorized},
		{"scrape without key", http.MethodGet, "/metrics", "", http.StatusUnauthorized},
		{"scrape with read key", http.MethodGet, "/metrics", readKey, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b interface{}
			if tc.method == http.MethodPost {
				b = body
			}
			w := doRequest(app, tc.method, tc.path, tc.key, b)
			assert.Equal(t, tc.want, w.Code, w.Body.String())
			if tc.want == http.StatusUnauthorized {
				assert.NotEmpty(t, w.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestAuthDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.AuthDisabled = true
	app := newTestApp(t, cfg)
	assert.Equal(t, http.StatusCreated, doRequest(app, http.MethodPost, "/v1/metrics", "", report("a", 1, "b")).Code)
	assert.Equal(t, http.StatusOK, doRequest(app, http.MethodGet, "/metrics", "", nil).Code)
}

func TestCreateMetricValidation(t *testing.T) {
	app := newTestApp(t, testConfig())

	cases := []struct {
		name string
		body interface{}
	}{
		{"missing value", map[string]interface{}{"label": "a", "description": "d", "source": "s"}},
		{"missing label", map[string]interface{}{"value": 1, "description": "d", "source": "s"}},
		{"label too long", report(strings.Repeat("x", 129), 1, "s")},
		{"control character in source", report("a", 1, "evil\nsource")},
		{"blank source", report("a", 1, "   ")},
		{"zero-width space", report("web\u200b", 1, "s")},
		{"bidi override", report("a", 1, "\u202esvc")},
		{"line separator", report("a\u2028b", 1, "s")},
		{"value not a number", map[string]interface{}{"label": "a", "value": "one", "description": "d", "source": "s"}},
		{"malformed JSON", []byte(`{"label":`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

func TestCreateMetricBodyTooLarge(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 100
	app := newTestApp(t, cfg)
	w := doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("a", 1, strings.Repeat("s", 120)))
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestSeriesLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSeries = 2
	app := newTestApp(t, cfg)

	// Many reports for one series must not use up the series budget.
	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusCreated, doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("a", 1, "svc")).Code)
	}
	assert.Equal(t, http.StatusCreated, doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("b", 1, "svc")).Code)
	// An existing series can keep reporting.
	assert.Equal(t, http.StatusCreated, doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("a", 0, "svc")).Code)
	// A third series is refused.
	assert.Equal(t, http.StatusUnprocessableEntity, doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("c", 1, "svc")).Code)
}

func TestRateLimit(t *testing.T) {
	cfg := testConfig()
	cfg.RateLimitRPS = 1
	cfg.RateLimitBurst = 2
	app := newTestApp(t, cfg)

	assert.Equal(t, http.StatusOK, doRequest(app, http.MethodGet, "/v1/", "", nil).Code)
	assert.Equal(t, http.StatusOK, doRequest(app, http.MethodGet, "/v1/", "", nil).Code)
	w := doRequest(app, http.MethodGet, "/v1/", "", nil)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
}

func TestGetMetrics(t *testing.T) {
	app := newTestApp(t, testConfig())
	require.Equal(t, http.StatusCreated, doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("celery-check", 1, "celery-check-source")).Code)

	w := doRequest(app, http.MethodGet, "/v1/metrics", readKey, nil)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "instrumentation_library_metrics")
	assert.Contains(t, body, `"name":"celery-check"`)
	assert.Contains(t, body, `"source":"celery-check-source"`)

	var resp GetMetricsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	ts := resp.Metrics[0].LibraryMetrics[0].Metrics[0].Timestamp
	_, err := time.Parse(time.RFC3339Nano, ts)
	assert.NoError(t, err, "timestamp should be RFC 3339")

	assert.Equal(t, http.StatusBadRequest, doRequest(app, http.MethodGet, "/v1/metrics?limit=0", readKey, nil).Code)
	assert.Equal(t, http.StatusBadRequest, doRequest(app, http.MethodGet, "/v1/metrics?limit=abc", readKey, nil).Code)
}

func TestGetMetricsSummary(t *testing.T) {
	app := newTestApp(t, testConfig())
	for i := 0; i < 2; i++ {
		require.Equal(t, http.StatusCreated, doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("celery-check", 1, "celery-check-source")).Code)
	}
	w := doRequest(app, http.MethodGet, "/v1/metrics/summary", readKey, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"label":"celery-check","value":1,"count":2`)
}

func TestGetLatest(t *testing.T) {
	app := newTestApp(t, testConfig())
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 1, "svc"))
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 0, "svc"))

	w := doRequest(app, http.MethodGet, "/v1/metrics/latest", readKey, nil)
	require.Equal(t, http.StatusOK, w.Code)
	var resp LatestResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, 0, resp.Data[0].Value)
}

func TestPrometheusEndpoint(t *testing.T) {
	app := newTestApp(t, testConfig())
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 1, "svc"))
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 0, "svc"))

	w := doRequest(app, http.MethodGet, "/metrics", readKey, nil)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "# TYPE dhc_health_check_status gauge")
	assert.Regexp(t, `dhc_health_check_status\{[^}]*check="db"[^}]*source="svc"[^}]*\} 0\n`, body)
	assert.Contains(t, body, "dhc_health_check_last_report_time_seconds{")
	assert.Regexp(t, `dhc_health_check_reports_total\{[^}]*\} 2\n`, body)
	assert.NotRegexp(t, `dhc_health_check_reports_total\{[^}]*check=`, body, "counter must not carry per-series attributes")
	assert.Contains(t, body, `service_name="dhc-server"`)
}

func TestPrometheusDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.PrometheusEnabled = false
	app := newTestApp(t, cfg)
	assert.Equal(t, http.StatusNotFound, doRequest(app, http.MethodGet, "/metrics", readKey, nil).Code)
}

// TestOpenTelemetryExport reads metrics the way an OTLP exporter would, to
// check the instruments, attributes and resource.
func TestOpenTelemetryExport(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	app := newTestApp(t, testConfig(), reader)
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 1, "svc"))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	svc, ok := rm.Resource.Set().Value("service.name")
	require.True(t, ok)
	assert.Equal(t, "dhc-server", svc.AsString())

	found := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			found[m.Name] = true
			if m.Name == "dhc.health_check.status" {
				g, ok := m.Data.(metricdata.Gauge[int64])
				require.True(t, ok)
				require.Len(t, g.DataPoints, 1)
				dp := g.DataPoints[0]
				assert.Equal(t, int64(1), dp.Value)
				src, _ := dp.Attributes.Value(attribute.Key(attrSource))
				chk, _ := dp.Attributes.Value(attribute.Key(attrCheck))
				assert.Equal(t, "svc", src.AsString())
				assert.Equal(t, "db", chk.AsString())
			}
		}
	}
	assert.True(t, found["dhc.health_check.status"])
	assert.True(t, found["dhc.health_check.last_report_time"])
	assert.True(t, found["dhc.health_check.reports"])
}

func TestRetentionPurge(t *testing.T) {
	app := newTestApp(t, testConfig())
	old := Metric{Label: "old", Value: 1, Description: "d", Source: "s"}
	require.NoError(t, app.store.db.Create(&old).Error)
	require.NoError(t, app.store.db.Model(&old).UpdateColumn("created_at", time.Now().Add(-48*time.Hour)).Error)
	fresh := Metric{Label: "new", Value: 1, Description: "d", Source: "s"}
	require.NoError(t, app.store.db.Create(&fresh).Error)

	n, err := app.store.PurgeOlderThan(context.Background(), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	var count int64
	app.store.db.Unscoped().Model(&Metric{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	_, err := LoadConfig(env(map[string]string{}))
	assert.ErrorContains(t, err, "DHC_INGEST_API_KEYS is required")

	_, err = LoadConfig(env(map[string]string{"DHC_INGEST_API_KEYS": "short"}))
	assert.ErrorContains(t, err, "at least 32 characters")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "DHC_TLS_CERT_FILE": "c.pem"}))
	assert.ErrorContains(t, err, "must be set together")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json"}))
	assert.ErrorContains(t, err, "unsupported OTLP protocol")

	_, err = LoadConfig(env(map[string]string{"DHC_INGEST_API_KEYS": ingestKey, "DHC_READ_API_KEYS": "short"}))
	assert.ErrorContains(t, err, "at least 32 characters")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "DHC_RATE_LIMIT_BURST": "0"}))
	assert.ErrorContains(t, err, "DHC_RATE_LIMIT_BURST must be at least 1")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "DHC_CORS_ALLOWED_ORIGINS": "example.com"}))
	assert.ErrorContains(t, err, "DHC_CORS_ALLOWED_ORIGINS")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "DHC_MAX_SERIES": "-1"}))
	assert.ErrorContains(t, err, "DHC_MAX_SERIES")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "maybe"}))
	assert.ErrorContains(t, err, "DHC_AUTH_DISABLED")

	_, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "OTEL_METRICS_EXPORTER": "zipkin"}))
	assert.ErrorContains(t, err, "unsupported exporter")

	cfg, err := LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "OTEL_METRICS_EXPORTER": "otlp"}))
	require.NoError(t, err)
	assert.True(t, cfg.OTLPEnabled)
	assert.False(t, cfg.PrometheusEnabled)

	cfg, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "OTEL_METRICS_EXPORTER": "none", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318"}))
	require.NoError(t, err)
	assert.False(t, cfg.OTLPEnabled)
	assert.False(t, cfg.PrometheusEnabled)

	cfg, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "OTEL_SDK_DISABLED": "true"}))
	require.NoError(t, err)
	assert.False(t, cfg.PrometheusEnabled)

	cfg, err = LoadConfig(env(map[string]string{"DHC_AUTH_DISABLED": "true", "OTEL_EXPORTER_OTLP_ENDPOINT": "   "}))
	require.NoError(t, err)
	assert.False(t, cfg.OTLPEnabled, "whitespace-only endpoint must not enable OTLP")

	cfg, err = LoadConfig(env(map[string]string{
		"DHC_INGEST_API_KEYS":         ingestKey + ", " + ingestKey + "2",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
	}))
	require.NoError(t, err)
	assert.Len(t, cfg.IngestAPIKeys, 2)
	assert.True(t, cfg.OTLPEnabled)
	assert.Equal(t, "http/protobuf", cfg.OTLPProtocol)
	assert.True(t, cfg.PrometheusEnabled)
	assert.Equal(t, time.Duration(0), cfg.RetentionPeriod, "retention must default to keep forever")
	assert.Equal(t, ":8080", cfg.Addr)
}

func TestKeySet(t *testing.T) {
	ks := newKeySet([]string{"alpha"}, []string{"beta"})
	assert.True(t, ks.contains("alpha"))
	assert.True(t, ks.contains("beta"))
	assert.False(t, ks.contains("gamma"))
	assert.False(t, ks.contains(""))
}

func TestRateLimitSkipsAuthenticatedRequests(t *testing.T) {
	cfg := testConfig()
	cfg.RateLimitRPS = 1
	cfg.RateLimitBurst = 1
	app := newTestApp(t, cfg)

	// Use up the anonymous bucket for this IP.
	doRequest(app, http.MethodGet, "/v1/", "", nil)
	assert.Equal(t, http.StatusTooManyRequests, doRequest(app, http.MethodGet, "/v1/", "", nil).Code)
	// Clients with a valid key from the same IP are unaffected.
	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusOK, doRequest(app, http.MethodGet, "/v1/metrics/latest", readKey, nil).Code)
	}
	// A wrong key counts as anonymous.
	assert.Equal(t, http.StatusTooManyRequests, doRequest(app, http.MethodGet, "/v1/metrics", "wrong-key-0123456789abcdef0123456789ab", nil).Code)
}

func TestRateLimiterEvictsLeastRecentlySeen(t *testing.T) {
	l := newIPRateLimiter(1, 1, 3)
	assert.True(t, l.allow("10.0.0.1"))
	assert.False(t, l.allow("10.0.0.1"), "10.0.0.1 is now limited")

	// A flood of new addresses evicts only the oldest entries...
	assert.True(t, l.allow("10.0.0.2"))
	assert.True(t, l.allow("10.0.0.3"))
	assert.False(t, l.allow("10.0.0.1"), "touching 10.0.0.1 keeps it in the table")
	assert.True(t, l.allow("10.0.0.4")) // evicts 10.0.0.2, the least recently seen
	assert.Len(t, l.clients, 3)
	// ...and does not reset clients that are still active.
	assert.False(t, l.allow("10.0.0.1"))
}

func TestRateLimiterGroupsIPv6By64(t *testing.T) {
	l := newIPRateLimiter(1, 1, 100)
	assert.True(t, l.allow("2001:db8:1:2::1"))
	assert.False(t, l.allow("2001:db8:1:2::ffff"), "same /64 shares a bucket")
	assert.True(t, l.allow("2001:db8:1:3::1"), "different /64 has its own bucket")
	assert.Equal(t, "10.0.0.1", clientKey("::ffff:10.0.0.1"))
}

func TestForwardedForOnlyFromTrustedProxies(t *testing.T) {
	send := func(app *App, xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/", nil)
		req.RemoteAddr = "10.1.1.1:1234"
		req.Header.Set("X-Forwarded-For", xff)
		w := httptest.NewRecorder()
		app.router.ServeHTTP(w, req)
		return w.Code
	}

	cfg := testConfig()
	cfg.RateLimitRPS, cfg.RateLimitBurst = 1, 1
	untrusted := newTestApp(t, cfg)
	assert.Equal(t, http.StatusOK, send(untrusted, "1.1.1.1"))
	assert.Equal(t, http.StatusTooManyRequests, send(untrusted, "2.2.2.2"), "spoofed X-Forwarded-For must be ignored")

	cfg.TrustedProxies = []string{"10.1.1.0/24"}
	trusted := newTestApp(t, cfg)
	assert.Equal(t, http.StatusOK, send(trusted, "1.1.1.1"))
	assert.Equal(t, http.StatusOK, send(trusted, "2.2.2.2"), "clients behind a trusted proxy get their own buckets")
}

func TestLatestSkipsSoftDeletedRows(t *testing.T) {
	app := newTestApp(t, testConfig())
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 1, "svc"))
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 0, "svc"))
	var newest Metric
	require.NoError(t, app.store.db.Order("id DESC").First(&newest).Error)
	require.NoError(t, app.store.db.Delete(&newest).Error)

	rows, err := app.store.LatestPerSeries(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].Value)
}

func TestRetentionComparesTimesNotStrings(t *testing.T) {
	app := newTestApp(t, testConfig())
	// Rows in the old cgo driver's layout with a local offset, and in the
	// new layout. Both are 1 hour old in real time.
	hourAgo := time.Now().Add(-time.Hour)
	ny := time.FixedZone("EST", -5*3600)
	for i, ts := range []string{
		hourAgo.In(ny).Format("2006-01-02 15:04:05.999999999-07:00"),
		hourAgo.UTC().Format(time.RFC3339Nano),
	} {
		m := Metric{Label: "l", Value: i, Description: "d", Source: "s"}
		require.NoError(t, app.store.db.Create(&m).Error)
		require.NoError(t, app.store.db.Exec("UPDATE metrics SET created_at = ? WHERE id = ?", ts, m.ID).Error)
	}

	n, err := app.store.PurgeOlderThan(context.Background(), time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "rows newer than the cutoff must survive regardless of format or offset")

	n, err = app.store.PurgeOlderThan(context.Background(), time.Now().Add(-30*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestManySeriesExportWithoutOverflow(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSeries = 2100
	reader := sdkmetric.NewManualReader()
	app := newTestApp(t, cfg, reader)

	rows := make([]Metric, 2100)
	for i := range rows {
		rows[i] = Metric{Label: "check", Value: 1, Description: "d", Source: "svc-" + strconv.Itoa(i)}
	}
	require.NoError(t, app.store.db.CreateInBatches(rows, 500).Error)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "dhc.health_check.status" {
				g := m.Data.(metricdata.Gauge[int64])
				assert.Len(t, g.DataPoints, 2100)
				for _, dp := range g.DataPoints {
					_, overflow := dp.Attributes.Value("otel.metric.overflow")
					require.False(t, overflow)
				}
				return
			}
		}
	}
	t.Fatal("status gauge not found")
}

// TestOTLPPush checks that OTLP export honours the standard OTEL_* variables
// and flushes on shutdown.
func TestOTLPPush(t *testing.T) {
	type received struct {
		path, auth, contentType string
	}
	got := make(chan received, 10)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- received{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer%20collector-token")
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "3600000")

	cfg := testConfig()
	cfg.OTLPEnabled = true
	app, err := NewApp(context.Background(), cfg, l.NewNop())
	require.NoError(t, err)
	doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("db", 1, "svc"))
	require.NoError(t, app.Close(context.Background()))

	select {
	case r := <-got:
		assert.Equal(t, "/v1/metrics", r.path)
		assert.Equal(t, "Bearer collector-token", r.auth)
		assert.Equal(t, "application/x-protobuf", r.contentType)
	case <-time.After(5 * time.Second):
		t.Fatal("collector received nothing")
	}
}

func TestNonBearerAuthorizationIsRejected(t *testing.T) {
	app := newTestApp(t, testConfig())
	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Basic "+readKey)
	req.Header.Set("X-API-Key", readKey)
	w := httptest.NewRecorder()
	app.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "an Authorization header that is not Bearer must not fall back to X-API-Key")
}

func TestChunkedBodyTooLarge(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 100
	app := newTestApp(t, cfg)
	raw, _ := json.Marshal(report("a", 1, strings.Repeat("s", 200)))
	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(string(raw)))
	req.ContentLength = -1 // unknown length, as with chunked encoding
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ingestKey)
	w := httptest.NewRecorder()
	app.router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestSafeTextAllowsUnicodeLetters(t *testing.T) {
	app := newTestApp(t, testConfig())
	w := doRequest(app, http.MethodPost, "/v1/metrics", ingestKey, report("Cache backend: défaut", 1, strings.Repeat("é", 128)))
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

// encoding/json replaces invalid UTF-8 with U+FFFD before validation, so
// raw invalid bytes can never be stored or exported.
func TestInvalidUTF8IsReplaced(t *testing.T) {
	app := newTestApp(t, testConfig())
	w := doRequest(app, http.MethodPost, "/v1/metrics", ingestKey,
		[]byte("{\"label\":\"a\xff\",\"value\":1,\"description\":\"d\",\"source\":\"s\"}"))
	require.Equal(t, http.StatusCreated, w.Code)
	var stored Metric
	require.NoError(t, app.store.db.First(&stored).Error)
	assert.Equal(t, "a\uFFFD", stored.Label)
}
