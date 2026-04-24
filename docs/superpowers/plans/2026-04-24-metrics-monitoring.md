# Metrics Monitoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Prometheus metrics to event_analytics and snapchat services, then run a shared Prometheus + Grafana + Alertmanager stack via Docker Compose.

**Architecture:** Each Go and Python service exposes a `/metrics` endpoint on a dedicated port. Prometheus (running in `monitoring/`) scrapes all services via `host.docker.internal` (macOS Docker Desktop). Grafana dashboards are provisioned from JSON files; Alertmanager routes fired alerts to a local webhook receiver.

**Tech Stack:** Go `prometheus/client_golang` v1.20, Python `prometheus_client` 0.21, Prometheus v2.53, Grafana v11.1, Alertmanager v0.27, Docker Compose.

---

## File Map

### New files
| File | Responsibility |
|---|---|
| `monitoring/docker-compose.yml` | Runs Prometheus, Grafana, Alertmanager, webhook, node_exporter |
| `monitoring/prometheus/prometheus.yml` | Scrape targets + alert rule references |
| `monitoring/prometheus/alerts/event_analytics.yml` | Alert rules for EA services |
| `monitoring/prometheus/alerts/snapchat.yml` | Alert rules for Snap services |
| `monitoring/grafana/provisioning/datasources/prometheus.yml` | Auto-provisions Prometheus datasource |
| `monitoring/grafana/provisioning/dashboards/dashboards.yml` | Tells Grafana where to load dashboard JSONs from |
| `monitoring/grafana/dashboards/event_analytics.json` | Event Analytics dashboard |
| `monitoring/grafana/dashboards/snapchat.json` | Snapchat dashboard |
| `monitoring/alertmanager/config.yml` | Routes all alerts to webhook receiver |
| `monitoring/webhook/main.go` | Tiny Go HTTP server that logs Alertmanager payloads |
| `monitoring/webhook/go.mod` | Module file for webhook receiver |
| `monitoring/webhook/Dockerfile` | Builds webhook binary |
| `event_analytics/ingestion/metrics.go` | Metric variable declarations for EA ingestion |
| `snapchat/gateway/metrics.go` | Metric variable declarations for gateway |
| `snapchat/registration/metrics.go` | Metric variable declarations for registration |
| `snapchat/auth/metrics.go` | Metric variable declarations for auth |

### Modified files
| File | Change |
|---|---|
| `event_analytics/ingestion/main.go` | Start metrics server on :9100, wrap handler |
| `event_analytics/ingestion/go.mod` | Add `prometheus/client_golang` |
| `event_analytics/aggregator/main.py` | Add metric vars + `start_http_server(9101)` |
| `event_analytics/aggregator/requirements.txt` | Add `prometheus_client==0.21.1` |
| `snapchat/gateway/main.go` | Start metrics server on :9200, add MetricsMiddleware |
| `snapchat/gateway/middleware.go` | Add `MetricsMiddleware` wrapping statusWriter |
| `snapchat/gateway/ratelimit.go` | Increment rejection counter |
| `snapchat/gateway/go.mod` | Add `prometheus/client_golang` |
| `snapchat/registration/main.go` | Start metrics server on :9201 |
| `snapchat/registration/handler.go` | Increment registration + SMS counters |
| `snapchat/registration/go.mod` | Add `prometheus/client_golang` |
| `snapchat/auth/main.go` | Start metrics server on :9202 |
| `snapchat/auth/handler.go` | Increment token + failure counters |
| `snapchat/auth/go.mod` | Add `prometheus/client_golang` |
| `snapchat/ingestion/main.py` | Add metric vars + `start_http_server(9203)` in lifespan |
| `snapchat/ingestion/requirements.txt` | Add `prometheus_client==0.21.1` |
| `snapchat/docker-compose.yml` | Expose ports 9200–9203 for metrics scraping |

---

## Task 1: monitoring/ infrastructure scaffold

**Files:**
- Create: `monitoring/docker-compose.yml`
- Create: `monitoring/prometheus/prometheus.yml`
- Create: `monitoring/alertmanager/config.yml`
- Create: `monitoring/grafana/provisioning/datasources/prometheus.yml`
- Create: `monitoring/grafana/provisioning/dashboards/dashboards.yml`

- [ ] **Step 1: Create directory tree**

```bash
mkdir -p monitoring/prometheus/alerts \
         monitoring/grafana/provisioning/datasources \
         monitoring/grafana/provisioning/dashboards \
         monitoring/grafana/dashboards \
         monitoring/alertmanager \
         monitoring/webhook
```

- [ ] **Step 2: Write `monitoring/docker-compose.yml`**

```yaml
services:
  prometheus:
    image: prom/prometheus:v2.53.0
    container_name: monitoring-prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.retention.time=15d'
      - '--web.enable-lifecycle'
    volumes:
      - ./prometheus:/etc/prometheus
      - prometheus-data:/prometheus
    ports:
      - "9090:9090"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    restart: unless-stopped

  grafana:
    image: grafana/grafana:11.1.0
    container_name: monitoring-grafana
    environment:
      GF_SECURITY_ADMIN_PASSWORD: admin
      GF_USERS_ALLOW_SIGN_UP: "false"
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning
      - ./grafana/dashboards:/var/lib/grafana/dashboards
      - grafana-data:/var/lib/grafana
    ports:
      - "3000:3000"
    depends_on:
      - prometheus
    restart: unless-stopped

  alertmanager:
    image: prom/alertmanager:v0.27.0
    container_name: monitoring-alertmanager
    volumes:
      - ./alertmanager:/etc/alertmanager
    command:
      - '--config.file=/etc/alertmanager/config.yml'
    ports:
      - "9093:9093"
    restart: unless-stopped

  webhook:
    build: ./webhook
    container_name: monitoring-webhook
    ports:
      - "9999:9999"
    restart: unless-stopped

  node-exporter:
    image: prom/node-exporter:v1.8.1
    container_name: monitoring-node-exporter
    command:
      - '--path.rootfs=/host'
    volumes:
      - /:/host:ro,rslave
    ports:
      - "9900:9100"
    restart: unless-stopped

volumes:
  prometheus-data:
  grafana-data:
```

- [ ] **Step 3: Write `monitoring/prometheus/prometheus.yml`**

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 1m

alerting:
  alertmanagers:
    - static_configs:
        - targets: ['alertmanager:9093']

rule_files:
  - /etc/prometheus/alerts/*.yml

scrape_configs:
  - job_name: 'node'
    static_configs:
      - targets: ['node-exporter:9100']

  - job_name: 'ea-ingestion'
    static_configs:
      - targets: ['host.docker.internal:9100']

  - job_name: 'ea-aggregator'
    static_configs:
      - targets: ['host.docker.internal:9101']

  - job_name: 'snap-gateway'
    static_configs:
      - targets: ['host.docker.internal:9200']

  - job_name: 'snap-registration'
    static_configs:
      - targets: ['host.docker.internal:9201']

  - job_name: 'snap-auth'
    static_configs:
      - targets: ['host.docker.internal:9202']

  - job_name: 'snap-ingestion'
    static_configs:
      - targets: ['host.docker.internal:9203']
```

- [ ] **Step 4: Write `monitoring/alertmanager/config.yml`**

```yaml
global:
  resolve_timeout: 5m

route:
  group_by: ['alertname', 'severity']
  group_wait: 10s
  group_interval: 5m
  repeat_interval: 1h
  receiver: 'webhook'

receivers:
  - name: 'webhook'
    webhook_configs:
      - url: 'http://webhook:9999/alert'
        send_resolved: true
```

- [ ] **Step 5: Write `monitoring/grafana/provisioning/datasources/prometheus.yml`**

```yaml
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    uid: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
    editable: false
```

- [ ] **Step 6: Write `monitoring/grafana/provisioning/dashboards/dashboards.yml`**

```yaml
apiVersion: 1
providers:
  - name: 'default'
    orgId: 1
    folder: ''
    type: file
    disableDeletion: false
    updateIntervalSeconds: 10
    options:
      path: /var/lib/grafana/dashboards
```

- [ ] **Step 7: Commit**

```bash
git add monitoring/
git commit -m "feat: add monitoring/ docker compose scaffold"
```

---

## Task 2: Alert rules

**Files:**
- Create: `monitoring/prometheus/alerts/event_analytics.yml`
- Create: `monitoring/prometheus/alerts/snapchat.yml`

- [ ] **Step 1: Write `monitoring/prometheus/alerts/event_analytics.yml`**

```yaml
groups:
  - name: event_analytics
    rules:
      - alert: EAHighErrorRate
        expr: |
          (
            rate(ea_http_requests_total{status=~"5.."}[5m])
            /
            rate(ea_http_requests_total[5m])
          ) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "EA ingestion error rate above 5%"
          description: "HTTP 5xx rate is {{ $value | humanizePercentage }}"

      - alert: EAKafkaProducerErrors
        expr: rate(ea_kafka_produce_errors_total[5m]) > 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "EA ingestion Kafka produce errors"
          description: "Kafka produce failures detected in event_analytics/ingestion"

      - alert: EAHighLatencyP99
        expr: |
          histogram_quantile(0.99,
            rate(ea_http_request_duration_seconds_bucket[5m])
          ) > 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "EA ingestion p99 latency above 1s"
          description: "p99 latency is {{ $value }}s"

      - alert: EAAggregationErrors
        expr: rate(ea_aggregation_errors_total[5m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "EA aggregator processing errors"
          description: "Aggregation errors detected in event_analytics/aggregator"
```

- [ ] **Step 2: Write `monitoring/prometheus/alerts/snapchat.yml`**

```yaml
groups:
  - name: snapchat
    rules:
      - alert: SnapHighErrorRate
        expr: |
          (
            rate(snap_http_requests_total{status=~"5.."}[5m])
            /
            rate(snap_http_requests_total[5m])
          ) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Snapchat gateway error rate above 5%"
          description: "HTTP 5xx rate is {{ $value | humanizePercentage }}"

      - alert: SnapRateLimitSpike
        expr: rate(snap_rate_limit_rejections_total[5m]) > 10
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "Snapchat gateway rate limit spike"
          description: "Rate limit rejections at {{ $value }} req/s"

      - alert: SnapHighLatencyP99
        expr: |
          histogram_quantile(0.99,
            rate(snap_http_request_duration_seconds_bucket[5m])
          ) > 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Snapchat gateway p99 latency above 1s"
          description: "p99 latency is {{ $value }}s"

      - alert: SnapAuthFailureSpike
        expr: rate(snap_auth_failures_total[5m]) > 5
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Snapchat auth failure spike"
          description: "Auth failures at {{ $value }} req/s"
```

- [ ] **Step 3: Commit**

```bash
git add monitoring/prometheus/alerts/
git commit -m "feat: add Prometheus alert rules for EA and Snap services"
```

---

## Task 3: Grafana dashboard — event_analytics

**Files:**
- Create: `monitoring/grafana/dashboards/event_analytics.json`

- [ ] **Step 1: Write `monitoring/grafana/dashboards/event_analytics.json`**

```json
{
  "annotations": { "list": [] },
  "editable": true,
  "fiscalYearStartMonth": 0,
  "graphTooltip": 0,
  "id": null,
  "links": [],
  "panels": [
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": { "color": { "mode": "palette-classic" }, "custom": { "lineWidth": 2 } },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 0 },
      "id": 1,
      "options": {
        "legend": { "calcs": ["mean", "max"], "displayMode": "table", "placement": "bottom" },
        "tooltip": { "mode": "multi" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum(rate(ea_http_requests_total[5m])) by (path)",
          "legendFormat": "{{path}}",
          "refId": "A"
        }
      ],
      "title": "Request Rate (req/s)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "fixed", "fixedColor": "red" },
          "custom": { "lineWidth": 2 },
          "unit": "percentunit"
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 0 },
      "id": 2,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "single" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum(rate(ea_http_requests_total{status=~\"5..\"}[5m])) / sum(rate(ea_http_requests_total[5m]))",
          "legendFormat": "error rate",
          "refId": "A"
        }
      ],
      "title": "Error Rate (5xx %)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "palette-classic" },
          "custom": { "lineWidth": 2 },
          "unit": "s"
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 8 },
      "id": 3,
      "options": {
        "legend": { "calcs": ["mean", "max"], "displayMode": "table", "placement": "bottom" },
        "tooltip": { "mode": "multi" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.50, sum(rate(ea_http_request_duration_seconds_bucket[5m])) by (le))",
          "legendFormat": "p50",
          "refId": "A"
        },
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.99, sum(rate(ea_http_request_duration_seconds_bucket[5m])) by (le))",
          "legendFormat": "p99",
          "refId": "B"
        }
      ],
      "title": "Request Latency (p50 / p99)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "palette-classic" },
          "custom": { "lineWidth": 2 }
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 8 },
      "id": 4,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "single" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "rate(ea_kafka_events_produced_total[5m])",
          "legendFormat": "produced/s",
          "refId": "A"
        },
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "rate(ea_kafka_produce_errors_total[5m])",
          "legendFormat": "errors/s",
          "refId": "B"
        }
      ],
      "title": "Kafka Produce Rate",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "palette-classic" },
          "custom": { "lineWidth": 2 }
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 16 },
      "id": 5,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "single" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum(rate(ea_kafka_messages_consumed_total[5m])) by (event_name)",
          "legendFormat": "{{event_name}}",
          "refId": "A"
        }
      ],
      "title": "Aggregator Consume Rate (by event_name)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "thresholds" },
          "thresholds": {
            "mode": "absolute",
            "steps": [
              { "color": "green", "value": null },
              { "color": "yellow", "value": 100 },
              { "color": "red", "value": 1000 }
            ]
          }
        },
        "overrides": []
      },
      "gridPos": { "h": 4, "w": 6, "x": 12, "y": 16 },
      "id": 6,
      "options": {
        "colorMode": "background",
        "graphMode": "none",
        "justifyMode": "center",
        "orientation": "auto",
        "reduceOptions": { "calcs": ["lastNotNull"] },
        "textMode": "auto"
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "ea_kafka_consumer_lag",
          "legendFormat": "lag",
          "refId": "A"
        }
      ],
      "title": "Consumer Lag",
      "type": "stat"
    }
  ],
  "refresh": "10s",
  "schemaVersion": 38,
  "tags": ["event-analytics"],
  "templating": { "list": [] },
  "time": { "from": "now-1h", "to": "now" },
  "timepicker": {},
  "timezone": "browser",
  "title": "Event Analytics",
  "uid": "event-analytics",
  "version": 1
}
```

- [ ] **Step 2: Commit**

```bash
git add monitoring/grafana/dashboards/event_analytics.json
git commit -m "feat: add Grafana dashboard for event_analytics"
```

---

## Task 4: Grafana dashboard — snapchat

**Files:**
- Create: `monitoring/grafana/dashboards/snapchat.json`

- [ ] **Step 1: Write `monitoring/grafana/dashboards/snapchat.json`**

```json
{
  "annotations": { "list": [] },
  "editable": true,
  "fiscalYearStartMonth": 0,
  "graphTooltip": 0,
  "id": null,
  "links": [],
  "panels": [
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": { "color": { "mode": "palette-classic" }, "custom": { "lineWidth": 2 } },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 0 },
      "id": 1,
      "options": {
        "legend": { "calcs": ["mean", "max"], "displayMode": "table", "placement": "bottom" },
        "tooltip": { "mode": "multi" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum(rate(snap_http_requests_total[5m])) by (path)",
          "legendFormat": "{{path}}",
          "refId": "A"
        }
      ],
      "title": "Gateway Request Rate (req/s)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "fixed", "fixedColor": "red" },
          "custom": { "lineWidth": 2 },
          "unit": "percentunit"
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 0 },
      "id": 2,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "single" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "sum(rate(snap_http_requests_total{status=~\"5..\"}[5m])) / sum(rate(snap_http_requests_total[5m]))",
          "legendFormat": "error rate",
          "refId": "A"
        }
      ],
      "title": "Gateway Error Rate (5xx %)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "palette-classic" },
          "custom": { "lineWidth": 2 },
          "unit": "s"
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 8 },
      "id": 3,
      "options": {
        "legend": { "calcs": ["mean", "max"], "displayMode": "table", "placement": "bottom" },
        "tooltip": { "mode": "multi" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.50, sum(rate(snap_http_request_duration_seconds_bucket[5m])) by (le))",
          "legendFormat": "p50",
          "refId": "A"
        },
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "histogram_quantile(0.99, sum(rate(snap_http_request_duration_seconds_bucket[5m])) by (le))",
          "legendFormat": "p99",
          "refId": "B"
        }
      ],
      "title": "Gateway Latency (p50 / p99)",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "fixed", "fixedColor": "orange" },
          "custom": { "lineWidth": 2 }
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 8 },
      "id": 4,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "single" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "rate(snap_rate_limit_rejections_total[5m])",
          "legendFormat": "rejections/s",
          "refId": "A"
        }
      ],
      "title": "Rate Limit Rejections",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": {
          "color": { "mode": "fixed", "fixedColor": "red" },
          "custom": { "lineWidth": 2 }
        },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 0, "y": 16 },
      "id": 5,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "single" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "rate(snap_auth_failures_total[5m])",
          "legendFormat": "failures/s",
          "refId": "A"
        }
      ],
      "title": "Auth Failures",
      "type": "timeseries"
    },
    {
      "datasource": { "type": "prometheus", "uid": "prometheus" },
      "fieldConfig": {
        "defaults": { "color": { "mode": "palette-classic" }, "custom": { "lineWidth": 2 } },
        "overrides": []
      },
      "gridPos": { "h": 8, "w": 12, "x": 12, "y": 16 },
      "id": 6,
      "options": {
        "legend": { "calcs": [], "displayMode": "list" },
        "tooltip": { "mode": "multi" }
      },
      "targets": [
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "rate(snap_registrations_total{status=\"success\"}[5m])",
          "legendFormat": "success",
          "refId": "A"
        },
        {
          "datasource": { "type": "prometheus", "uid": "prometheus" },
          "expr": "rate(snap_registrations_total{status=\"failure\"}[5m])",
          "legendFormat": "failure",
          "refId": "B"
        }
      ],
      "title": "Registrations / s",
      "type": "timeseries"
    }
  ],
  "refresh": "10s",
  "schemaVersion": 38,
  "tags": ["snapchat"],
  "templating": { "list": [] },
  "time": { "from": "now-1h", "to": "now" },
  "timepicker": {},
  "timezone": "browser",
  "title": "Snapchat",
  "uid": "snapchat",
  "version": 1
}
```

- [ ] **Step 2: Commit**

```bash
git add monitoring/grafana/dashboards/snapchat.json
git commit -m "feat: add Grafana dashboard for snapchat"
```

---

## Task 5: Webhook receiver

**Files:**
- Create: `monitoring/webhook/main.go`
- Create: `monitoring/webhook/go.mod`
- Create: `monitoring/webhook/Dockerfile`

- [ ] **Step 1: Write `monitoring/webhook/go.mod`**

```
module monitoring/webhook

go 1.22
```

- [ ] **Step 2: Write `monitoring/webhook/main.go`**

```go
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

func alertHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("ERROR reading body: %v", err)
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("ALERT (raw): %s", body)
		w.WriteHeader(http.StatusOK)
		return
	}
	out, _ := json.MarshalIndent(payload, "", "  ")
	log.Printf("ALERT received:\n%s", out)
	w.WriteHeader(http.StatusOK)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/alert", alertHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	log.Println("webhook receiver listening on :9999")
	log.Fatal(http.ListenAndServe(":9999", mux))
}
```

- [ ] **Step 3: Write failing test for `alertHandler`**

Create `monitoring/webhook/main_test.go`:

```go
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAlertHandlerAcceptsValidJSON(t *testing.T) {
	body := bytes.NewBufferString(`{"alerts":[{"status":"firing","labels":{"alertname":"TestAlert"}}]}`)
	req := httptest.NewRequest(http.MethodPost, "/alert", body)
	rr := httptest.NewRecorder()

	alertHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestAlertHandlerRejectsGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/alert", nil)
	rr := httptest.NewRecorder()

	alertHandler(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail (no implementation yet at this point)**

```bash
cd monitoring/webhook && go test ./...
```

Expected: PASS (the implementation is already in main.go above — write test first in practice, both exist here since they're in the same task)

- [ ] **Step 5: Write `monitoring/webhook/Dockerfile`**

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY main.go .
RUN go build -o webhook .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/webhook .
EXPOSE 9999
CMD ["./webhook"]
```

- [ ] **Step 6: Run tests**

```bash
cd monitoring/webhook && go test ./... -v
```

Expected output:
```
--- PASS: TestAlertHandlerAcceptsValidJSON (0.00s)
--- PASS: TestAlertHandlerRejectsGET (0.00s)
PASS
```

- [ ] **Step 7: Commit**

```bash
git add monitoring/webhook/
git commit -m "feat: add webhook receiver for Alertmanager alerts"
```

---

## Task 6: Instrument event_analytics/ingestion (Go)

**Files:**
- Create: `event_analytics/ingestion/metrics.go`
- Modify: `event_analytics/ingestion/main.go`
- Modify: `event_analytics/ingestion/go.mod`

- [ ] **Step 1: Add prometheus dependency**

```bash
cd event_analytics/ingestion
go get github.com/prometheus/client_golang@v1.20.5
```

Expected: `go.mod` and `go.sum` updated.

- [ ] **Step 2: Write `event_analytics/ingestion/metrics.go`**

```go
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	eaHTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ea_http_requests_total",
		Help: "Total HTTP requests to the event analytics ingestion API",
	}, []string{"method", "path", "status"})

	eaHTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ea_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	eaKafkaEventsProduced = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ea_kafka_events_produced_total",
		Help: "Total events successfully published to Kafka",
	})

	eaKafkaProduceErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ea_kafka_produce_errors_total",
		Help: "Total failed Kafka produce calls",
	})
)

// metricsStatusWriter captures the HTTP status code written to the response.
type metricsStatusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *metricsStatusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// instrumentHandler wraps an http.HandlerFunc to record request count and duration.
func instrumentHandler(path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &metricsStatusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		duration := time.Since(start).Seconds()
		eaHTTPRequestsTotal.WithLabelValues(r.Method, path, fmt.Sprintf("%d", sw.status)).Inc()
		eaHTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	}
}
```

- [ ] **Step 3: Update `event_analytics/ingestion/main.go` — start metrics server + increment Kafka counters**

Replace the Kafka produce block and the main() function. The full updated file:

```go
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var producer *kafka.Producer
var topic = "raw-events"

type Event struct {
	EventName  string         `json:"event_name"`
	Timestamp  string         `json:"timestamp"`
	UserID     string         `json:"user_id"`
	DeviceID   string         `json:"device_id"`
	Properties map[string]any `json:"properties"`
}

type IngestRequest struct {
	Events []Event `json:"events"`
}

type IngestResponse struct {
	Accepted  int    `json:"accepted"`
	RequestID string `json:"request_id"`
}

func ingestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if len(req.Events) == 0 {
		http.Error(w, `{"error":"no events"}`, http.StatusBadRequest)
		return
	}

	accepted := 0
	for _, ev := range req.Events {
		if ev.EventName == "" {
			continue
		}

		if ev.Timestamp == "" {
			ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
		}

		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}

		err = producer.Produce(&kafka.Message{
			TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
			Key:            []byte(ev.EventName),
			Value:          data,
		}, nil)
		if err != nil {
			log.Printf("Kafka produce error: %v", err)
			eaKafkaProduceErrors.Inc()
			continue
		}
		eaKafkaEventsProduced.Inc()
		accepted++
	}

	producer.Flush(100)

	reqID := uuid.New().String()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(IngestResponse{Accepted: accepted, RequestID: reqID})
	log.Printf("Ingested %d/%d events (request %s)", accepted, len(req.Events), reqID[:8])
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func main() {
	kafkaAddr := os.Getenv("KAFKA_BOOTSTRAP")
	if kafkaAddr == "" {
		kafkaAddr = "localhost:29092"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8100"
	}
	metricsPort := os.Getenv("METRICS_PORT")
	if metricsPort == "" {
		metricsPort = "9100"
	}

	var err error
	producer, err = kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers": kafkaAddr,
	})
	if err != nil {
		log.Fatalf("Failed to create Kafka producer: %v", err)
	}
	defer producer.Close()

	go func() {
		for e := range producer.Events() {
			if m, ok := e.(*kafka.Message); ok && m.TopicPartition.Error != nil {
				log.Printf("Delivery failed: %v", m.TopicPartition.Error)
			}
		}
	}()

	// Metrics server on a separate port — never behind auth or rate limiting
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())
		log.Printf("Metrics server listening on :%s", metricsPort)
		if err := http.ListenAndServe(":"+metricsPort, metricsMux); err != nil {
			log.Fatalf("Metrics server error: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/events", instrumentHandler("/v1/events", ingestHandler))
	mux.HandleFunc("/health", instrumentHandler("/health", healthHandler))

	log.Printf("Ingestion API listening on :%s (Kafka: %s)", port, kafkaAddr)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
```

- [ ] **Step 4: Verify it builds**

```bash
cd event_analytics/ingestion && go build ./...
```

Expected: no errors, binary produced.

- [ ] **Step 5: Smoke-test the metrics endpoint (requires Kafka running — skip Kafka, just check endpoint)**

```bash
cd event_analytics/ingestion && go run . &
sleep 2
curl -s http://localhost:9100/metrics | grep "^ea_"
kill %1
```

Expected output (subset):
```
ea_http_requests_total
ea_http_request_duration_seconds_bucket
ea_kafka_events_produced_total
ea_kafka_produce_errors_total
```

- [ ] **Step 6: Commit**

```bash
git add event_analytics/ingestion/metrics.go event_analytics/ingestion/main.go event_analytics/ingestion/go.mod event_analytics/ingestion/go.sum
git commit -m "feat: instrument event_analytics/ingestion with Prometheus metrics"
```

---

## Task 7: Instrument event_analytics/aggregator (Python)

**Files:**
- Modify: `event_analytics/aggregator/main.py`
- Modify: `event_analytics/aggregator/requirements.txt`

- [ ] **Step 1: Update `event_analytics/aggregator/requirements.txt`**

```
confluent-kafka==2.5.0
redis==5.0.0
prometheus_client==0.21.1
```

- [ ] **Step 2: Update `event_analytics/aggregator/main.py`**

Add metric declarations after the imports block, and `start_http_server` in main, and counter increments in the processing loop. Full updated file:

```python
"""Pre-aggregation worker — consumes raw events from Kafka,
increments per-minute counters in Redis sorted sets.

This is what makes the dashboard fast: instead of scanning millions of
raw events at query time, we pre-compute counts as events arrive.

Redis key pattern:
  counts:{event_name}:{minute_bucket}
  e.g., counts:install:2026-04-20T10:05

Each key is a sorted set where members are dimension values ("total",
platform names, etc.) and scores are counts.
"""

import json
import logging
import os
import signal

import redis
from confluent_kafka import Consumer, KafkaError
from datetime import datetime, timezone
from prometheus_client import Counter, Gauge, start_http_server

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

KAFKA_BOOTSTRAP = os.getenv("KAFKA_BOOTSTRAP", "localhost:29092")
REDIS_ADDR = os.getenv("REDIS_ADDR", "localhost:6380")
METRICS_PORT = int(os.getenv("METRICS_PORT", "9101"))
TOPIC = "raw-events"
KEY_TTL = 8 * 24 * 3600  # 8 days

running = True

# --- Prometheus metrics ---
MESSAGES_CONSUMED = Counter(
    "ea_kafka_messages_consumed_total",
    "Kafka messages consumed from raw-events topic",
    ["event_name"],
)
REDIS_INCREMENTS = Counter(
    "ea_redis_increments_total",
    "Successful Redis ZINCRBY calls",
)
AGGREGATION_ERRORS = Counter(
    "ea_aggregation_errors_total",
    "Processing errors (decode, Redis, or other)",
)
CONSUMER_LAG = Gauge(
    "ea_kafka_consumer_lag",
    "Estimated consumer lag — messages behind head partition",
)


def signal_handler(sig, frame):
    global running
    running = False


signal.signal(signal.SIGINT, signal_handler)
signal.signal(signal.SIGTERM, signal_handler)


def minute_bucket(timestamp_str: str) -> str:
    """Convert an ISO timestamp to a minute bucket string."""
    try:
        dt = datetime.fromisoformat(timestamp_str.replace("Z", "+00:00"))
    except (ValueError, TypeError):
        dt = datetime.now(timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M")


def process_message(r: redis.Redis, msg_value: bytes) -> None:
    try:
        event = json.loads(msg_value)
    except json.JSONDecodeError as e:
        logger.warning(f"Failed to decode message: {e}")
        AGGREGATION_ERRORS.inc()
        return

    event_name = event.get("event_name", "unknown")
    ts = event.get("timestamp", "")
    bucket = minute_bucket(ts)
    key = f"counts:{event_name}:{bucket}"

    try:
        pipe = r.pipeline()
        pipe.zincrby(key, 1, "total")
        pipe.expire(key, KEY_TTL)
        pipe.execute()
        REDIS_INCREMENTS.inc()
    except redis.RedisError as e:
        logger.error(f"Redis error: {e}")
        AGGREGATION_ERRORS.inc()
        return

    MESSAGES_CONSUMED.labels(event_name=event_name).inc()
    logger.debug(f"Aggregated {event_name} → {key}")


def main() -> None:
    start_http_server(METRICS_PORT)
    logger.info(f"Prometheus metrics listening on :{METRICS_PORT}")

    redis_host, redis_port = REDIS_ADDR.split(":")
    r = redis.Redis(host=redis_host, port=int(redis_port), decode_responses=True)

    consumer = Consumer(
        {
            "bootstrap.servers": KAFKA_BOOTSTRAP,
            "group.id": "pre-aggregation-worker",
            "auto.offset.reset": "earliest",
        }
    )
    consumer.subscribe([TOPIC])
    logger.info(f"Aggregator started (Kafka: {KAFKA_BOOTSTRAP}, Redis: {REDIS_ADDR})")

    try:
        while running:
            msg = consumer.poll(timeout=1.0)
            if msg is None:
                continue
            if msg.error():
                if msg.error().code() == KafkaError._PARTITION_EOF:
                    continue
                logger.error(f"Kafka error: {msg.error()}")
                AGGREGATION_ERRORS.inc()
                continue

            # Update lag estimate: high watermark offset - current offset
            lo, hi = consumer.get_watermark_offsets(msg.topicPartition(), timeout=0.5)
            if hi is not None and msg.offset() is not None:
                CONSUMER_LAG.set(max(0, hi - msg.offset() - 1))

            process_message(r, msg.value())
    finally:
        consumer.close()
        logger.info("Aggregator stopped")


if __name__ == "__main__":
    main()
```

- [ ] **Step 3: Smoke-test metrics endpoint (requires no Kafka)**

```bash
cd event_analytics/aggregator
pip install prometheus_client==0.21.1 -q
python -c "
from prometheus_client import Counter, Gauge, start_http_server
import time
start_http_server(9101)
print('metrics up on :9101')
time.sleep(2)
" &
sleep 1
curl -s http://localhost:9101/metrics | head -5
kill %1
```

Expected: Prometheus exposition text with `# HELP` and `# TYPE` lines.

- [ ] **Step 4: Commit**

```bash
git add event_analytics/aggregator/main.py event_analytics/aggregator/requirements.txt
git commit -m "feat: instrument event_analytics/aggregator with Prometheus metrics"
```

---

## Task 8: Instrument snapchat/gateway (Go)

**Files:**
- Create: `snapchat/gateway/metrics.go`
- Modify: `snapchat/gateway/middleware.go`
- Modify: `snapchat/gateway/ratelimit.go`
- Modify: `snapchat/gateway/main.go`
- Modify: `snapchat/gateway/go.mod`

- [ ] **Step 1: Add prometheus dependency**

```bash
cd snapchat/gateway
go get github.com/prometheus/client_golang@v1.20.5
```

- [ ] **Step 2: Write `snapchat/gateway/metrics.go`**

```go
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	snapHTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "snap_http_requests_total",
		Help: "Total HTTP requests proxied through the gateway",
	}, []string{"method", "path", "status"})

	snapHTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "snap_http_request_duration_seconds",
		Help:    "End-to-end proxy latency in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	snapRateLimitRejections = promauto.NewCounter(prometheus.CounterOpts{
		Name: "snap_rate_limit_rejections_total",
		Help: "Total requests rejected by the token bucket rate limiter",
	})

	snapAuthFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "snap_auth_failures_total",
		Help: "Total failed JWT validations at the gateway",
	})
)

// gatewayStatusWriter captures HTTP status written downstream.
type gatewayStatusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *gatewayStatusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// MetricsMiddleware records request count and latency for every proxied request.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &gatewayStatusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		duration := time.Since(start).Seconds()
		snapHTTPRequestsTotal.WithLabelValues(r.Method, r.URL.Path, fmt.Sprintf("%d", sw.status)).Inc()
		snapHTTPRequestDuration.WithLabelValues(r.Method, r.URL.Path).Observe(duration)
	})
}
```

- [ ] **Step 3: Update `snapchat/gateway/ratelimit.go` — increment rejection counter**

In `RateLimitMiddleware`, add `snapRateLimitRejections.Inc()` when rejecting:

```go
if !limiter.Allow(ip) {
    snapRateLimitRejections.Inc()
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Retry-After", "10")
    w.WriteHeader(http.StatusTooManyRequests)
    w.Write([]byte(`{"error":"rate limit exceeded"}`))
    return
}
```

- [ ] **Step 4: Update `snapchat/gateway/middleware.go` — increment auth failure counter**

In `AuthMiddleware`, add `snapAuthFailures.Inc()` in the two rejection branches:

```go
// Missing header branch
if authHeader == "" {
    snapAuthFailures.Inc()
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusUnauthorized)
    w.Write([]byte(`{"error":"missing Authorization header"}`))
    return
}

// Invalid token branch
if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || !result.Valid {
    snapAuthFailures.Inc()
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusUnauthorized)
    w.Write([]byte(`{"error":"invalid token"}`))
    return
}
```

- [ ] **Step 5: Update `snapchat/gateway/main.go` — start metrics server and add MetricsMiddleware**

Add `metricsPort` env var, start metrics goroutine, and add `MetricsMiddleware` to the handler chain. Updated `main()`:

```go
func main() {
	port := getEnv("PORT", "8080")
	metricsPort := getEnv("METRICS_PORT", "9200")
	registrationURL := getEnv("REGISTRATION_URL", "http://localhost:8081")
	authURL := getEnv("AUTH_URL", "http://localhost:8082")
	ingestionURL := getEnv("INGESTION_URL", "http://localhost:8090")
	rankingURL := getEnv("RANKING_URL", "http://localhost:8092")
	retrievalURL := getEnv("RETRIEVAL_URL", "http://localhost:8091")

	stripPrefix := "/api/v1"
	routes := []Route{
		{Prefix: "/api/v1/register", Backend: registrationURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/verify", Backend: registrationURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/resend", Backend: registrationURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/token/", Backend: authURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/content", Backend: ingestionURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/events", Backend: ingestionURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/feed", Backend: rankingURL, StripPrefix: stripPrefix},
		{Prefix: "/api/v1/debug/", Backend: retrievalURL, StripPrefix: stripPrefix},
	}

	publicPaths := map[string]bool{
		"/api/v1/register":      true,
		"/api/v1/verify":        true,
		"/api/v1/resend":        true,
		"/api/v1/token/refresh": true,
		"/health":               true,
	}

	proxy := NewProxy(routes)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", proxy)

	limiter := NewTokenBucket(10, 20)
	handler := MetricsMiddleware(
		LoggingMiddleware(
			RateLimitMiddleware(limiter,
				AuthMiddleware(authURL, publicPaths, mux),
			),
		),
	)

	// Metrics server on a separate port — bypasses all middleware
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())
		log.Printf("Metrics server listening on :%s", metricsPort)
		log.Fatal(http.ListenAndServe(":"+metricsPort, metricsMux))
	}()

	log.Printf("Gateway listening on :%s", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
```

Also add to the import block: `"github.com/prometheus/client_golang/prometheus/promhttp"`

- [ ] **Step 6: Verify build**

```bash
cd snapchat/gateway && go build ./...
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add snapchat/gateway/
git commit -m "feat: instrument snapchat/gateway with Prometheus metrics"
```

---

## Task 9: Instrument snapchat/registration (Go)

**Files:**
- Create: `snapchat/registration/metrics.go`
- Modify: `snapchat/registration/handler.go`
- Modify: `snapchat/registration/main.go`
- Modify: `snapchat/registration/go.mod`

- [ ] **Step 1: Add prometheus dependency**

```bash
cd snapchat/registration
go get github.com/prometheus/client_golang@v1.20.5
```

- [ ] **Step 2: Write `snapchat/registration/metrics.go`**

```go
package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	snapRegistrationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "snap_registrations_total",
		Help: "Total registration attempts",
	}, []string{"status"})

	snapSMSSentTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "snap_sms_sent_total",
		Help: "Total OTP SMS messages dispatched",
	})
)
```

- [ ] **Step 3: Update `snapchat/registration/handler.go` — add metric increments**

In the `Register` handler method, after a successful OTP send, increment both counters. Find the place where `sms.Send(...)` is called and the registration succeeds, and add:

```go
// After successful sms.Send() call and before returning:
snapSMSSentTotal.Inc()
snapRegistrationsTotal.WithLabelValues("success").Inc()
```

In error paths (before returning an error HTTP response), add:

```go
snapRegistrationsTotal.WithLabelValues("failure").Inc()
```

- [ ] **Step 4: Update `snapchat/registration/main.go` — start metrics server**

Read the current `main()` to find the `ListenAndServe` call. Add the metrics goroutine before it:

```go
metricsPort := getEnv("METRICS_PORT", "9201")

go func() {
    metricsMux := http.NewServeMux()
    metricsMux.Handle("/metrics", promhttp.Handler())
    log.Printf("Metrics listening on :%s", metricsPort)
    log.Fatal(http.ListenAndServe(":"+metricsPort, metricsMux))
}()
```

Add to imports: `"github.com/prometheus/client_golang/prometheus/promhttp"`

- [ ] **Step 5: Verify build**

```bash
cd snapchat/registration && go build ./...
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add snapchat/registration/
git commit -m "feat: instrument snapchat/registration with Prometheus metrics"
```

---

## Task 10: Instrument snapchat/auth (Go)

**Files:**
- Create: `snapchat/auth/metrics.go`
- Modify: `snapchat/auth/handler.go`
- Modify: `snapchat/auth/main.go`
- Modify: `snapchat/auth/go.mod`

- [ ] **Step 1: Add prometheus dependency**

```bash
cd snapchat/auth
go get github.com/prometheus/client_golang@v1.20.5
```

- [ ] **Step 2: Write `snapchat/auth/metrics.go`**

```go
package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	snapTokensIssuedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "snap_tokens_issued_total",
		Help: "Total tokens minted by the auth service",
	}, []string{"type"})

	snapAuthFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "snap_auth_failures_total",
		Help: "Total authentication failures in the auth service",
	}, []string{"reason"})
)
```

- [ ] **Step 3: Update `snapchat/auth/handler.go` — add metric increments**

In the `Issue` handler (issues access + refresh tokens on success):

```go
// After successfully issuing tokens:
snapTokensIssuedTotal.WithLabelValues("access").Inc()
snapTokensIssuedTotal.WithLabelValues("refresh").Inc()
```

In the `Issue` handler failure path (invalid credentials):

```go
snapAuthFailuresTotal.WithLabelValues("invalid_creds").Inc()
```

In the `Refresh` handler failure path (expired/invalid refresh token):

```go
snapAuthFailuresTotal.WithLabelValues("expired").Inc()
```

In the `Validate` handler failure path:

```go
snapAuthFailuresTotal.WithLabelValues("invalid_creds").Inc()
```

- [ ] **Step 4: Update `snapchat/auth/main.go` — start metrics server**

Add the metrics goroutine before `http.ListenAndServe`:

```go
metricsPort := getEnv("METRICS_PORT", "9202")

go func() {
    metricsMux := http.NewServeMux()
    metricsMux.Handle("/metrics", promhttp.Handler())
    log.Printf("Metrics listening on :%s", metricsPort)
    log.Fatal(http.ListenAndServe(":"+metricsPort, metricsMux))
}()
```

Add to imports: `"github.com/prometheus/client_golang/prometheus/promhttp"`

- [ ] **Step 5: Verify build**

```bash
cd snapchat/auth && go build ./...
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add snapchat/auth/
git commit -m "feat: instrument snapchat/auth with Prometheus metrics"
```

---

## Task 11: Instrument snapchat/ingestion (Python/FastAPI)

**Files:**
- Modify: `snapchat/ingestion/main.py`
- Modify: `snapchat/ingestion/requirements.txt`

- [ ] **Step 1: Update `snapchat/ingestion/requirements.txt`**

Add `prometheus_client==0.21.1` to the existing requirements:

```
fastapi==0.115.0
uvicorn==0.30.0
psycopg2-binary==2.9.9
confluent-kafka==2.5.0
numpy==2.0.0
pydantic==2.9.0
prometheus_client==0.21.1
```

- [ ] **Step 2: Add metric declarations and `start_http_server` to `snapchat/ingestion/main.py`**

After the existing imports block, add:

```python
from prometheus_client import Counter, start_http_server

METRICS_PORT = int(os.getenv("METRICS_PORT", "9203"))

# --- Prometheus metrics ---
SNAP_HTTP_REQUESTS = Counter(
    "snap_http_requests_total",
    "Total HTTP requests to the snapchat ingestion service",
    ["method", "path", "status"],
)
SNAP_SNAPS_UPLOADED = Counter(
    "snap_snaps_uploaded_total",
    "Total snap content items accepted",
)
SNAP_KAFKA_PRODUCED = Counter(
    "snap_kafka_events_produced_total",
    "Total events published to Kafka by the snapchat ingestion service",
)
```

In the `lifespan` context manager, start the metrics server before yielding:

```python
@asynccontextmanager
async def lifespan(app: FastAPI):
    start_http_server(METRICS_PORT)
    logger.info(f"Prometheus metrics listening on :{METRICS_PORT}")
    logger.info("Ingestion service starting")
    get_db()
    get_producer()
    yield
```

In each route handler that accepts content or events, increment the appropriate counters. For example, in the content upload endpoint (wherever `get_producer().produce(...)` is called):

```python
# After successful Kafka produce:
SNAP_KAFKA_PRODUCED.inc()
SNAP_SNAPS_UPLOADED.inc()
```

For HTTP request counting, add a FastAPI middleware at the bottom of the middleware section:

```python
@app.middleware("http")
async def metrics_middleware(request: Request, call_next):
    response = await call_next(request)
    SNAP_HTTP_REQUESTS.labels(
        method=request.method,
        path=request.url.path,
        status=str(response.status_code),
    ).inc()
    return response
```

Also add `from fastapi import Request` to the imports if not already present (it is already imported via `FastAPI, HTTPException, Header`).

- [ ] **Step 3: Verify import resolves**

```bash
cd snapchat/ingestion && pip install prometheus_client==0.21.1 -q && python -c "from prometheus_client import Counter, start_http_server; print('ok')"
```

Expected: `ok`

- [ ] **Step 4: Commit**

```bash
git add snapchat/ingestion/main.py snapchat/ingestion/requirements.txt
git commit -m "feat: instrument snapchat/ingestion with Prometheus metrics"
```

---

## Task 12: Expose metrics ports in docker-compose files

**Files:**
- Modify: `snapchat/docker-compose.yml`

- [ ] **Step 1: Update `snapchat/docker-compose.yml` — add metrics port mappings**

For each instrumented service, add a port mapping for its metrics port and set `METRICS_PORT` env var:

```yaml
gateway:
  build: ./gateway
  container_name: snap-gateway
  ports:
    - "8080:8080"
    - "9200:9200"
  environment:
    REGISTRATION_URL: http://registration:8081
    AUTH_URL: http://auth:8082
    INGESTION_URL: http://ingestion:8090
    RANKING_URL: http://ranking:8092
    RETRIEVAL_URL: http://retrieval:8091
    METRICS_PORT: "9200"
  # ... rest unchanged

registration:
  build: ./registration
  container_name: snap-registration
  ports:
    - "8081:8081"
    - "9201:9201"
  environment:
    POSTGRES_DSN: "postgres://snapuser:snappass@postgres:5432/snapchat?sslmode=disable"
    METRICS_PORT: "9201"
  # ... rest unchanged

auth:
  build: ./auth
  container_name: snap-auth
  ports:
    - "8082:8082"
    - "9202:9202"
  environment:
    POSTGRES_DSN: "postgres://snapuser:snappass@postgres:5432/snapchat?sslmode=disable"
    JWT_SECRET: dev-secret-do-not-use-in-prod
    METRICS_PORT: "9202"
  # ... rest unchanged

ingestion:
  build: ./ingestion
  container_name: snap-ingestion
  ports:
    - "8090:8090"
    - "9203:9203"
  environment:
    POSTGRES_DSN: "postgresql://snapuser:snappass@postgres:5432/snapchat"
    KAFKA_BOOTSTRAP: kafka:9092
    METRICS_PORT: "9203"
  # ... rest unchanged
```

- [ ] **Step 2: Commit**

```bash
git add snapchat/docker-compose.yml
git commit -m "feat: expose metrics ports in snapchat docker-compose"
```

---

## Task 13: Smoke test

- [ ] **Step 1: Start the monitoring stack**

```bash
cd monitoring && docker compose up -d
```

Expected: all 5 containers start (prometheus, grafana, alertmanager, webhook, node-exporter).

- [ ] **Step 2: Verify Prometheus UI is up**

```bash
open http://localhost:9090
```

Navigate to Status → Targets. All targets will show `DOWN` until services are running — that's expected.

- [ ] **Step 3: Run ea-ingestion locally and verify metrics**

```bash
cd event_analytics/ingestion && go run . &
sleep 2
curl -s http://localhost:9100/metrics | grep "^ea_"
kill %1
```

Expected (at minimum):
```
ea_http_requests_total
ea_kafka_events_produced_total
ea_kafka_produce_errors_total
ea_http_request_duration_seconds_bucket
```

- [ ] **Step 4: Run ea-aggregator locally and verify metrics**

```bash
cd event_analytics/aggregator && pip install -r requirements.txt -q && python main.py &
sleep 2
curl -s http://localhost:9101/metrics | grep "^ea_"
kill %1
```

Expected:
```
ea_aggregation_errors_total
ea_kafka_consumer_lag
ea_kafka_messages_consumed_total
ea_redis_increments_total
```

- [ ] **Step 5: Run snap-gateway locally and verify metrics**

```bash
cd snapchat/gateway && go run . &
sleep 2
curl -s http://localhost:9200/metrics | grep "^snap_"
kill %1
```

Expected:
```
snap_auth_failures_total
snap_http_request_duration_seconds_bucket
snap_http_requests_total
snap_rate_limit_rejections_total
```

- [ ] **Step 6: Verify Grafana dashboards load**

```bash
open http://localhost:3000
```

Login: `admin` / `admin`. Navigate to Dashboards — both "Event Analytics" and "Snapchat" dashboards should appear pre-loaded. Panels will show "No data" until services send traffic, which is correct.

- [ ] **Step 7: Verify Alertmanager is reachable**

```bash
curl -s http://localhost:9093/-/healthy
```

Expected: `OK`

- [ ] **Step 8: Final commit**

```bash
git add .
git commit -m "feat: complete metrics monitoring stack — Prometheus + Grafana + Alertmanager"
```
