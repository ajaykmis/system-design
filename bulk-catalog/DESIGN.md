# Async Bulk Catalog Update Pipeline

Merchants upload large product catalog files (500M+ records) via pre-signed S3 URLs.
Workers consume S3 events from SQS, stream-process files in chunks, upsert the catalog,
and produce per-row error reports. The API server never touches file bytes.

---

## Job State Machine

```
CREATED → UPLOAD_IN_PROGRESS → PROCESSING → COMPLETED
                                          → COMPLETED_WITH_ERRORS
                                          → FAILED (retryable via SQS)
```

- `COMPLETED_WITH_ERRORS` — some rows failed validation; error report available for download
- `FAILED` — system-level failure (worker crash, corrupt file, DLQ exhausted)
- Threshold: `failed_rows / total_rows > 0.5` → `FAILED`, not `COMPLETED_WITH_ERRORS`

---

## Schema

```sql
bulk_jobs (
  id UUID PK, merchant_id, status,
  total_rows, processed_rows, failed_rows,
  error_report_url,
  idempotency_key UNIQUE,          -- merchant-provided, 24h TTL
  processing_deadline TIMESTAMPTZ, -- started_at + max_allowed; heartbeat guard
  expires_at TIMESTAMPTZ,          -- upload window (1hr from CREATED)
  created_at, updated_at
)

bulk_job_files (
  id UUID PK, job_id FK,
  s3_key, part_number,
  status (PENDING|PROCESSING|DONE|FAILED),
  row_count, created_at
)
```

Indexes: `(merchant_id, created_at DESC)`, `(status, updated_at)` (reaper query).

---

## API

| Endpoint | Purpose |
|---|---|
| `POST /bulk-jobs` | Create job, return pre-signed S3 multipart URLs |
| `POST /bulk-jobs/{id}/complete` | Merchant signals upload done → trigger CompleteMultipartUpload |
| `GET /bulk-jobs/{id}` | Poll status + progress |
| `GET /bulk-jobs/{id}/errors` | Redirect to pre-signed S3 error report URL |

**Idempotency:** `Idempotency-Key` header on POST. Same key + same merchant → return existing job (200). Duplicate rejected silently, not as error.

**`/complete` is the explicit handoff** — server calls `CompleteMultipartUpload`, transitions to `PROCESSING`, fires S3 ObjectCreated → SQS. Avoids relying on client to call AWS directly.

---

## Worker Design

```
SQS message consumed
  → start heartbeat goroutine (extend visibility every 5min, check deadline)
  → GET s3_key as byte stream (never buffer full file)
  → line scanner → accumulate chunk (1000 rows)
  → validate chunk → upsert valid rows (ON CONFLICT DO UPDATE)
  → flush failed rows to error_buffer
  → UPDATE bulk_jobs SET processed_rows += N, failed_rows += M every chunk
  → [EOF] flush error buffer → write error report to S3
  → UPDATE bulk_jobs status → COMPLETED / COMPLETED_WITH_ERRORS
  → DeleteMessage from SQS
  → stop heartbeat
```

**Idempotency on replay:** upserts are `ON CONFLICT (merchant_id, sku_id) DO UPDATE`. Replaying the full file on crash is safe. Progress resets to 0 on retry; error report is truncated and rewritten.

**Chunk size tradeoff:** smaller = more DB writes + finer progress; larger = fewer writes + coarser progress. Crash recovery re-scans full file regardless.

---

## Retry Strategy

| Level | Mechanism |
|---|---|
| Row failure | No retry — goes to error report. Merchant fixes and re-submits. |
| Transient chunk error | Exponential backoff (1s, 2s, 4s), then give up → let SQS redeliver |
| Worker crash | SQS visibility timeout expires → redeliver. Max 3 attempts → DLQ → job FAILED |
| Stuck worker | `processing_deadline` in job row. Heartbeat stops extending at deadline → SQS redelivers |

---

## Timeout Reaper

A cron job (every 5 min) handles two cases:

**Abandoned uploads** — merchant never called `/complete`:
```sql
UPDATE bulk_jobs SET status = 'FAILED'
WHERE status = 'UPLOAD_IN_PROGRESS' AND expires_at < NOW()
```

**Stuck processing** — worker died silently without SQS knowing (e.g. VM terminated, no graceful shutdown):
```sql
UPDATE bulk_jobs SET status = 'FAILED'
WHERE status = 'PROCESSING' AND processing_deadline < NOW()
```

Reaper only marks `FAILED` — it does not requeue. SQS handles redelivery independently via visibility timeout. Reaper is the reconciliation layer for cases SQS can't see (job DB state diverged from queue state).

---

## Architecture

```
  Merchant / Client
        │
        │ POST /bulk-jobs  (Idempotency-Key header)
        ▼
  ┌─────────────────┐   INSERT bulk_jobs     ┌───────────────────┐
  │   API Server    │──────────────────────► │    Job DB         │
  │                 │◄────────────────────── │   (PostgreSQL)    │
  └────────┬────────┘   pre-signed URLs      └────────┬──────────┘
           │                                          ▲
           │  Return pre-signed S3 multipart URLs     │ UPDATE status
           ▼                                          │ processed_rows
  Merchant uploads parts                              │ failed_rows
  directly to S3                              ┌───────┴──────────┐
           │                                  │     Workers      │
           │                                  │  (Go pool, k8s)  │
           ▼                                  └───────┬──────────┘
  ┌─────────────────┐                                 │
  │  S3 Upload      │  POST /complete →               │ consume
  │  Bucket         │  CompleteMultipartUpload         │
  └────────┬────────┘         │                       │
           │                  ▼               ┌───────┴──────────┐
           │           ObjectCreated ────────►│      SQS         │
           │                                  └───────┬──────────┘
           │                                          │ visibility timeout
           │                                          │ + heartbeat extend
           │                                          ▼
           │                                  ┌───────────────────┐
           │  GET s3_key (byte stream) ◄───── │  Worker process   │
           │                                  │  ┌─────────────┐  │
           │                                  │  │line scanner │  │
           │                                  │  │chunk buffer │  │
           │                                  │  │validate     │  │
           │                                  │  │upsert batch │  │
           │                                  │  └─────────────┘  │
           │                                  └──┬──────────┬─────┘
           │                                     │          │
           │                          ┌──────────▼──┐  ┌────▼─────────────┐
           │                          │  Catalog DB │  │ S3 Error Report  │
           │                          │  / Search   │  │ Bucket           │
           │                          │  Index      │  │ (error.csv)      │
           │                          └─────────────┘  └──────────────────┘
           │
  ┌────────▼────────┐
  │  Timeout Reaper │  cron every 5min
  │  (cron)         │  → mark abandoned UPLOAD_IN_PROGRESS as FAILED
  └─────────────────┘  → mark stuck PROCESSING past deadline as FAILED
```

| Component | Scales by |
|---|---|
| API Servers | Horizontal, stateless |
| Workers | Horizontal, stateless — scale on SQS queue depth |
| SQS | Managed, unlimited throughput |
| Job DB | Read replicas for status polling; primary for writes |
| Catalog DB | Sharded by merchant_id (500M records) |
| S3 | Managed, unlimited |
