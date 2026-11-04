# Django Health Check Monitoring Server

A small Go server that receives health check results from the Django Health Check Monitoring library and publishes them as **OpenTelemetry metrics**. You can send them to Prometheus, Grafana, Datadog, the OpenTelemetry Collector or any other OTLP-compatible backend.

```
Django apps ──POST /v1/metrics──▶ dhc-server ──┬── GET /metrics (Prometheus scrape)
                                  (SQLite)     └── OTLP push (HTTP or gRPC)
```

## Metrics

| OpenTelemetry name | Prometheus name | Type | Meaning |
|---|---|---|---|
| `dhc.health_check.status` | `dhc_health_check_status` | Gauge | Latest value reported by each check |
| `dhc.health_check.last_report_time` | `dhc_health_check_last_report_time_seconds` | Gauge | Unix time of the latest report; use it to alert on checks that stopped reporting |
| `dhc.health_check.reports` | `dhc_health_check_reports_total` | Counter | Reports received since the server started (no per-check attributes) |

The two gauges have two attributes: `source` (the reporting app) and `check` (the report's `label`). The counter has none, because a cumulative counter never forgets an attribute set and would grow without bound as checks come and go. The resource includes `service.name=dhc-server` and `service.version`. You can override or extend these with `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`.

## Quick start

```sh
# Generate keys: one for the Django apps (write), one for scrapers (read).
export DHC_INGEST_API_KEYS=$(openssl rand -hex 32)
export DHC_READ_API_KEYS=$(openssl rand -hex 32)
go run .
```

Send a report:

```sh
curl -X POST http://localhost:8080/v1/metrics \
  -H "Authorization: Bearer $DHC_INGEST_API_KEYS" \
  -H "Content-Type: application/json" \
  -d '{"label":"DatabaseBackend","value":0,"description":"working","source":"web"}'
```

Read it as Prometheus metrics:

```sh
curl -H "Authorization: Bearer $DHC_READ_API_KEYS" http://localhost:8080/metrics
```

## Connecting backends

Ready-to-edit configs are in [`examples/`](examples).

| Backend | Pull (scrape `/metrics`) | Push (OTLP) |
|---|---|---|
| **Prometheus** | [`examples/prometheus.yml`](examples/prometheus.yml) | Through the Collector, or Prometheus's OTLP receiver (`--web.enable-otlp-receiver`) |
| **Grafana** | Add Prometheus as a data source, or scrape with Grafana Alloy | `OTEL_EXPORTER_OTLP_ENDPOINT` = your Grafana Cloud OTLP gateway, with `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic%20<token>` |
| **Datadog** | Agent OpenMetrics check: [`examples/datadog-openmetrics.yaml`](examples/datadog-openmetrics.yaml) | Agent OTLP intake: `OTEL_EXPORTER_OTLP_ENDPOINT=http://datadog-agent:4318` and `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta` |
| **OTel Collector** | `prometheus` receiver | `otlp` receiver: [`examples/otel-collector.yaml`](examples/otel-collector.yaml) |

[`examples/alerts.yml`](examples/alerts.yml) has Prometheus alert rules for failing checks and checks that stopped reporting.

OTLP push is turned on by setting `OTEL_EXPORTER_OTLP_ENDPOINT` or `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`. Pull and push can run at the same time. These standard OpenTelemetry variables are supported:

- `OTEL_METRICS_EXPORTER`: comma-separated `otlp`, `prometheus` or `none`. When set, it overrides `DHC_PROMETHEUS_ENABLED` and endpoint-based detection.
- `OTEL_SDK_DISABLED=true` turns off all metric export.
- `OTEL_EXPORTER_OTLP_PROTOCOL` / `OTEL_EXPORTER_OTLP_METRICS_PROTOCOL`: `http/protobuf` (default) or `grpc`. `http/json` is not supported.
- `OTEL_EXPORTER_OTLP_[METRICS_]ENDPOINT`, `_HEADERS`, `_TIMEOUT`, `_COMPRESSION`, `_CERTIFICATE`, `_CLIENT_CERTIFICATE`, `_CLIENT_KEY`, `_INSECURE`, `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE`
- `OTEL_METRIC_EXPORT_INTERVAL`, `OTEL_METRIC_EXPORT_TIMEOUT`
- `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`

Header values are URL-decoded, so write `Authorization=Basic%20<token>`. Pending metrics are flushed on shutdown.

## HTTP API

| Method & path | Auth | Description |
|---|---|---|
| `GET /v1/` | none | Liveness check; also pings the database |
| `POST /v1/metrics` | ingest key | Store a report: `{label, value, description, source}`, all required |
| `GET /v1/metrics?limit=N` | read or ingest key | Stored reports grouped by source, newest first (default 1000, max 10000) |
| `GET /v1/metrics/latest` | read or ingest key | Latest report for each source/check pair |
| `GET /v1/metrics/summary` | read or ingest key | Count of reports for each (label, value) |
| `GET /metrics` | read or ingest key | Prometheus/OpenMetrics scrape endpoint |

Send the key as `Authorization: Bearer <key>` or `X-API-Key: <key>`. If an `Authorization` header is present but isn't `Bearer`, the request is rejected; it does not fall back to `X-API-Key`.

Validation for `POST /v1/metrics`:
- `label` and `source` are 1–128 characters, `description` up to 1024. Only printable text is accepted. Control characters and invisible characters (zero-width spaces, bidi overrides, line separators) are rejected, so look-alike series can't spoof dashboards.
- `value` is an integer, and `0` is accepted.
- Bodies over `DHC_MAX_BODY_BYTES` get `413`.
- Reports that would create a new source/label pair beyond `DHC_MAX_SERIES` get `422`.

## Security

- **Authentication is required by default.** The server refuses to start without `DHC_INGEST_API_KEYS`. Keys must be at least 32 characters and are compared in constant time. Write and read keys are kept separate, so a scraper's key can't submit fake results.
- **Rate limiting** applies to requests without a valid API key (key guessing, anonymous floods), one bucket per client IP, with IPv6 grouped by /64. Requests with a valid key are not rate limited, so anonymous traffic can't push your apps into 429s. `X-Forwarded-For` is only trusted from `DHC_TRUSTED_PROXIES`.
  - **Behind a load balancer or ingress**, set `DHC_TRUSTED_PROXIES` to its address range. Otherwise every anonymous client (including health probes) shares the proxy's single bucket.
- **Input limits:** maximum body size, field lengths, rejection of control characters, and a cap on series so nobody can blow up your metrics backend's cardinality.
- **Built-in TLS** (TLS 1.2+) with `DHC_TLS_CERT_FILE`/`DHC_TLS_KEY_FILE`, or terminate TLS at a reverse proxy.
- **Hardened HTTP:** server timeouts against slow clients, security headers, CORS off unless origins are listed, and internal errors are never returned to clients.
- **Logs** record the route, status, latency and client IP. They never record headers or keys.
- **Container:** a static binary on distroless that runs as a non-root user, and `.dockerignore` keeps local databases out of the image.
- **Retention:** set `DHC_RETENTION_PERIOD` (for example `720h`) to delete old reports and keep the database bounded. It is off by default so upgrading never deletes existing history. Turn it on deliberately; the first purge runs at startup.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `DHC_INGEST_API_KEYS` | (required) | Comma-separated keys allowed to POST reports |
| `DHC_READ_API_KEYS` | none | Comma-separated keys allowed to read metrics |
| `DHC_AUTH_DISABLED` | `false` | Turn off authentication. **Local development only.** |
| `DHC_ADDR` | `:8080` | Listen address |
| `DHC_DATABASE_PATH` | `dhc.db` | SQLite file |
| `DHC_TLS_CERT_FILE` / `DHC_TLS_KEY_FILE` | none | Serve HTTPS |
| `DHC_RATE_LIMIT_RPS` / `DHC_RATE_LIMIT_BURST` | `10` / `20` | Requests per second per IP for requests without a valid key; RPS `0` disables, burst must be ≥ 1 |
| `DHC_TRUSTED_PROXIES` | none | Proxy IPs/CIDRs allowed to set `X-Forwarded-For` |
| `DHC_CORS_ALLOWED_ORIGINS` | none | Browser origins allowed to call the API, with scheme (e.g. `https://dash.example.com`) |
| `DHC_MAX_BODY_BYTES` | `65536` | Largest request body |
| `DHC_MAX_SERIES` | `1000` | Maximum distinct source/label pairs |
| `DHC_RETENTION_PERIOD` | `0` (keep forever) | How long to keep reports, e.g. `720h`. Recommended in production. |
| `DHC_PROMETHEUS_ENABLED` | `true` | Serve `/metrics` |
| `OTEL_*` | | Standard OpenTelemetry settings (see above) |

## Docker

```sh
docker build -t dhc-server .
docker run -p 8080:8080 -v dhc-data:/data \
  -e DHC_INGEST_API_KEYS=... -e DHC_READ_API_KEYS=... dhc-server
```

The container runs as UID/GID 65532. Named Docker volumes inherit the right ownership. For bind mounts, `chown 65532:65532` the host directory. On Kubernetes, set `securityContext.fsGroup: 65532`.

## Local development

- Hot reload: `DHC_AUTH_DISABLED=true air -c .air.toml`
- `make run` runs with authentication disabled
- `make test` runs the tests with the race detector, `make lint` runs vet, `make vuln` runs govulncheck
- `make build` / `make build-linux` produce static binaries (no cgo needed)

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
