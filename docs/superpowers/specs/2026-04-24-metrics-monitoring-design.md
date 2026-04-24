# Metrics Monitoring Service — Design Spec

**Date:** 2026-04-24
**Scope:** Instrument event_analytics and snapchat POCs; run a shared Prometheus + Grafana + Alertmanager stack via Docker Compose.

---

## Requirements

### Functional
1. **Ingestion** — services expose `/metrics` in Prometheus text format (pull model)
2. **Storage** — Prometheus TSDB with 15-day retention
3. **Visualization** — Grafana with provisioned datasources and dashboards (no manual UI setup)
4. **Alerts** — Prometheus alert rules evaluated every 1m; Alertmanager routes to a local webhook receiver

### Non-Functional
- `docker compose up` in `monitoring/` starts the full stack with zero manual config
- Dashboards and alert rules are config-as-code (version-controlled YAML/JSON)
- Adding a new service requires only a scrape target entry in `prometheus.yml`

### Out of Scope
- Authentication on `/metrics` endpoints (dev prototype)
- Long-term storage beyond 15 days
- Push model / Pushgateway (not needed; all services are long-running)

---

## Architecture

```
  ┌──────────────────────────────┐   ┌──────────────────────────────┐
  │  event_analytics/            │   │  snapchat/                   │
  │  ├─ ingestion (Go)  :9100    │   │  ├─ gateway     (Go) :9200   │
  │  └─ aggregator (Py) :9101    │   │  ├─ registration (Go) :9201  │
  └────────────┬─────────────────┘   │  ├─ auth        (Go) :9202   │
               │                     │  └─ ingestion   (Py) :9203   │
               └──────────┬──────────┘
                          │  GET /metrics (every 15s)
                          ▼
               ┌─────────────────────┐
               │     Prometheus      │  :9090
               │  scrape + TSDB      │
               │  15-day retention   │
               └──────┬──────────────┘
                      │
          ┌───────────┼────────────┐
          ▼           ▼            ▼
    ┌──────────┐  ┌────────┐  ┌───────────────┐
    │ Grafana  │  │ Alert  │  │ node_exporter │
    │  :3000   │  │Manager │  │  :9900        │
    │dashboards│  │ :9093  │  │ host metrics  │
    └──────────┘  └────────┘  └───────────────┘
                       │
                       ▼
              ┌─────────────────┐
              │ Webhook Receiver│  :9999
              │  (Go, logs      │
              │   alert payload)│
              └─────────────────┘
```

---

## File Layout

```
monitoring/
  prometheus/
    prometheus.yml          # scrape configs + alerting rules reference
    alerts/
      event_analytics.yml   # alert rules for event_analytics services
      snapchat.yml          # alert rules for snapchat services
  grafana/
    provisioning/
      datasources/
        prometheus.yml      # auto-provision Prometheus datasource
      dashboards/
        dashboards.yml      # auto-load dashboard JSON files
    dashboards/
      event_analytics.json  # pre-built dashboard
      snapchat.json         # pre-built dashboard
  alertmanager/
    config.yml              # route all alerts to webhook receiver
  webhook/
    main.go                 # tiny Go server that logs alert payloads
    Dockerfile
  docker-compose.yml        # Prometheus + Grafana + Alertmanager + webhook + node_exporter
```

---

## Metrics Per Service

### event_analytics — ingestion (Go)

Expose on `:9100/metrics` alongside the existing `:8100` API port.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `ea_http_requests_total` | Counter | method, path, status | Total HTTP requests |
| `ea_http_request_duration_seconds` | Histogram | method, path | Request latency (buckets: 5ms–10s) |
| `ea_kafka_events_produced_total` | Counter | — | Events successfully published to Kafka |
| `ea_kafka_produce_errors_total` | Counter | — | Failed Kafka produce calls |

### event_analytics — aggregator (Python)

Expose on `:9101/metrics` via a background HTTP thread (main loop is the Kafka consumer).

| Metric | Type | Labels | Description |
|---|---|---|---|
| `ea_kafka_messages_consumed_total` | Counter | event_name | Messages consumed from raw-events topic |
| `ea_redis_increments_total` | Counter | — | Successful Redis ZINCRBY calls |
| `ea_aggregation_errors_total` | Counter | — | Processing errors (decode, Redis, etc.) |
| `ea_kafka_consumer_lag` | Gauge | — | Estimated consumer lag (messages behind head) |

### snapchat — gateway (Go)

Expose on `:9200/metrics`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_http_requests_total` | Counter | method, path, status | Total proxied requests |
| `snap_http_request_duration_seconds` | Histogram | method, path | End-to-end proxy latency |
| `snap_rate_limit_rejections_total` | Counter | — | Requests rejected by token bucket |
| `snap_auth_failures_total` | Counter | — | Failed JWT validations |

### snapchat — registration (Go)

Expose on `:9201/metrics`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_registrations_total` | Counter | status (success/failure) | Registration attempts |
| `snap_sms_sent_total` | Counter | — | OTP SMS dispatched |

### snapchat — auth (Go)

Expose on `:9202/metrics`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_tokens_issued_total` | Counter | type (access/refresh) | Tokens minted |
| `snap_auth_failures_total` | Counter | reason (invalid_creds/expired) | Auth failures |

### snapchat — ingestion (Python)

Expose on `:9203/metrics`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `snap_http_requests_total` | Counter | method, path, status | HTTP requests to ingestion |
| `snap_snaps_uploaded_total` | Counter | — | Snap content accepted |
| `snap_kafka_events_produced_total` | Counter | — | Events published to Kafka |

---

## Alert Rules

### event_analytics.yml

| Rule | Expression | For | Severity |
|---|---|---|---|
| `EAHighErrorRate` | `rate(ea_http_requests_total{status=~"5.."}[5m]) / rate(ea_http_requests_total[5m]) > 0.05` | 5m | warning |
| `EAKafkaProducerErrors` | `rate(ea_kafka_produce_errors_total[5m]) > 0` | 5m | critical |
| `EAHighLatencyP99` | `histogram_quantile(0.99, rate(ea_http_request_duration_seconds_bucket[5m])) > 1` | 5m | warning |
| `EAAggregationErrors` | `rate(ea_aggregation_errors_total[5m]) > 0` | 5m | warning |

### snapchat.yml

| Rule | Expression | For | Severity |
|---|---|---|---|
| `SnapHighErrorRate` | `rate(snap_http_requests_total{status=~"5.."}[5m]) / rate(snap_http_requests_total[5m]) > 0.05` | 5m | warning |
| `SnapRateLimitSpike` | `rate(snap_rate_limit_rejections_total[5m]) > 10` | 2m | warning |
| `SnapHighLatencyP99` | `histogram_quantile(0.99, rate(snap_http_request_duration_seconds_bucket[5m])) > 1` | 5m | warning |
| `SnapAuthFailureSpike` | `rate(snap_auth_failures_total[5m]) > 5` | 2m | critical |

---

## Pull vs Push Model

### Why Pull (Prometheus scrape) works here

All services are long-running processes (HTTP servers or Kafka consumers). They expose a `/metrics` endpoint at all times. Prometheus controls the scrape rate — services are never blocked by a slow collector. Any service can be debugged with `curl :PORT/metrics` without touching Prometheus.

### Where pull breaks at scale

| Problem | Threshold | Symptom |
|---|---|---|
| Scrape fan-out | >2K targets | Scrape duration exceeds 15s interval; Prometheus falls behind |
| Memory (head block) | >10M active series | OOM or multi-minute query latency |
| Cardinality explosion | High-cardinality labels (user_id, trace_id) | Each unique label value = new time series; storage blows up |
| Network | 100K pods × 10KB payload | ~1GB/scrape cycle on internal network |

### The right model at 100K pods (Snap-style)

```
Pod
 └─ StatsD UDP (fire-and-forget, microsecond overhead)
      ↓
 Envoy sidecar (per-pod pre-aggregation, 10s flush window)
      ↓
 Metrics ingestion tier
      ↓
 M3DB (horizontally scalable TSDB, handles 100M+ series)
```

Pre-aggregation at the sidecar collapses 1,000 raw request events into one `p50/p99/p999` data point per flush window — cardinality stays bounded regardless of traffic volume. M3DB (or VictoriaMetrics in open-source) handles the write throughput and long-term retention that vanilla Prometheus cannot.

**Scaling boundary for this prototype:** Switch to push + remote_write into VictoriaMetrics or M3DB when pod count exceeds ~2K or active time series exceed ~5M.

---

## Grafana Dashboards

Two provisioned dashboards (JSON, checked in):

**event_analytics dashboard panels:**
- Request rate (req/s) over time
- Error rate (% 5xx) over time
- p50 / p99 request latency
- Kafka events produced/s
- Kafka produce errors/s
- Redis increments/s
- Consumer lag

**snapchat dashboard panels:**
- Gateway request rate + error rate
- Gateway p99 latency
- Rate limit rejections/s
- Auth failures/s
- Registrations/s (success vs failure)
- Snaps uploaded/s

---

## Deployment

```bash
# Start monitoring stack
cd monitoring/
docker compose up -d

# Prometheus UI
open http://localhost:9090

# Grafana (admin/admin)
open http://localhost:3000

# Alertmanager
open http://localhost:9093
```

Services in the existing POCs expose `/metrics` automatically when built with the instrumented code. The monitoring `docker-compose.yml` uses `host.docker.internal` to reach services running outside its own compose network (or both compose files share an external Docker network).

---

## What is NOT built

- Authentication on `/metrics` endpoints
- Pushgateway (not needed for long-running services)
- Custom query API (PromQL via Prometheus HTTP API is sufficient)
- Persistent Grafana state beyond provisioned dashboards
