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

catalog (
  merchant_id, sku_id  -- composite PK / shard key
  title, price, description, updated_at
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

**Idempotency:** `Idempotency-Key` header on POST. Same key + same merchant → return existing job (200).

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

**Chunk size tradeoff:** smaller = more DB writes + finer progress; larger = fewer writes + coarser progress. Crash recovery re-scans full file regardless — chunk size does not affect crash recovery cost.

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

**Stuck processing** — worker died silently without SQS knowing (e.g. VM terminated):
```sql
UPDATE bulk_jobs SET status = 'FAILED'
WHERE status = 'PROCESSING' AND processing_deadline < NOW()
```

Reaper only marks `FAILED` — does not requeue. SQS handles redelivery via visibility timeout. Reaper reconciles cases where job DB state has diverged from queue state.

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

---

## Staff-Level Follow-ups

### Correctness & Edge Cases

**Partial file + `/complete`?**
Check `Content-Length` from S3 `HeadObject` against `total_size_bytes` declared at job creation. If mismatch, reject `/complete` with 400. Alternatively, require a row count in the request and validate after streaming.

**Concurrent conflicting SKU updates (two jobs, same merchant, overlapping SKUs)?**
Last write wins by default — whichever worker commits last sets the final value. If strict ordering matters, add an `updated_at` condition to the upsert: `DO UPDATE SET ... WHERE catalog.updated_at < EXCLUDED.updated_at`. Two overlapping jobs from the same merchant is a merchant error; document it.

**Error report write fails mid-stream?**
After processing, verify the error report S3 key exists via `HeadObject`. If missing, mark job `FAILED` and redeliver. Never set `error_report_url` until the write is confirmed.

**Progress update fails after upsert succeeds?**
Progress is best-effort UX, not a correctness guarantee. Catalog rows are already written (idempotent on replay). Final `total_rows` is set at EOF so the terminal state is accurate regardless of mid-run failures.

**Encoding issues?**
Read first 4 bytes for BOM detection (`EF BB BF` = UTF-8, `FF FE` = UTF-16). Normalize to UTF-8 before parsing. Reject files where encoding is ambiguous. Surface as a job-level error, not row-level.

---

### Scale & Performance

**Catalog DB sharding at 500M+ records?**
Shard by `merchant_id` — it's the natural tenant boundary, all queries include it, gives even distribution. Use consistent hashing so adding shards only migrates `1/n` of merchants. Never shard by `sku_id` — scatters a single merchant's catalog across all shards.

**Write amplification per chunk?**
Current: 1 INSERT per row = 1000 round trips per chunk. Fix: use PostgreSQL `COPY` or multi-row `INSERT ... VALUES ($1,$2),($3,$4)...`. `COPY` is 10–50× faster for bulk loads. Batching reduces DB connections from millions to thousands at 10k concurrent jobs.

**SQS autoscaling metric?**
Scale on `ApproximateNumberOfMessagesVisible` + `ApproximateAgeOfOldestMessage`. Queue depth alone is misleading — 1000 messages of 10-row files ≠ 1000 messages of 500M-row files. Better: workers emit a `rows_remaining_estimate` custom metric. Use k8s KEDA with a SQS scaler.

**Lost update risk if parallelizing within a job?**
Use atomic increments: `SET processed_rows = processed_rows + $chunk_size` — never read-modify-write. Postgres handles concurrent atomic increments correctly.

---

### Operational & Failure Modes

**Job stuck near reaper boundary — how to observe?**
Emit `job_age_in_status_seconds{status="PROCESSING"}` metric on every reaper run. Alert on p99 > threshold. `updated_at` on `GET /bulk-jobs/{id}` also exposes staleness to the merchant directly.

**Silent semantic corruption from a bad deploy?**
Shadow-write to a staging catalog table on deploy, diff against previous version before promoting. Track `avg_price_per_merchant` as a metric — sudden shifts after deploy are a signal. Store parser version per job so you can identify which jobs were affected and replay them.

**Postgres primary failover mid-processing?**
Workers error, heartbeats stop, SQS redelivers. Risk: jobs that finished processing but lost their final status `UPDATE` stay `PROCESSING` forever — caught by the reaper. After failover, run a manual reaper pass immediately rather than waiting 5 minutes.

**Merchant re-submitting failed rows — stale data risk?**
`ON CONFLICT DO UPDATE` handles this cleanly — re-submitted rows overwrite stale values; rows not in the re-submit are untouched. For full-replace mode (delete rows not in the new file), need a `replace_mode` flag that tombstones missing SKUs after the new batch commits atomically.

---

### Design Tradeoffs

**SQS vs Kafka?**
SQS: managed, no ops, at-least-once, 14-day retention. Kafka: arbitrary replay, strict per-partition ordering, higher throughput, but requires cluster ops. SQS is correct here — jobs are independent, ordering doesn't matter. You lose replay beyond 14 days and the ability to audit "which version of a SKU was ingested when."

**S3 staging vs direct API streaming?**
Direct streaming: API server holds connection for duration of 50GB file, collapses upload and processing, retries restart from byte 0. S3 staging: decouples upload from processing, merchant retries just the upload, durable copy for replay. Tradeoff flips only below ~1MB — use a sync endpoint for those.

**Last write wins — when is it wrong?**
Wrong when multiple systems write the same SKU fields independently (e.g. pricing service + catalog service both update `price`). In that case, use field-level merge or optimistic locking, not row-level last-write-wins.

**`/complete` vs pure S3 events?**
`/complete` gives explicit intent, server-side validation before committing multipart, and decouples upload protocol from processing trigger. For a mobile SDK, `/complete` is better — mobile clients are unreliable. For a server-side pipeline, either works.

---

### Cross-System Thinking

**Elasticsearch lag after bulk update?**
Debezium CDC → Kafka → ES sink has ~5–30s lag. Mitigations: return `indexed_at` in job status; write `catalog_version` to ES and expose it; offer a webhook callback when indexing catches up. Never guarantee real-time consistency — document the lag SLA.

**Where does the design fail at Pinterest scale (100M+ products, 10k merchants)?**
Single-tenant PostgreSQL for catalog breaks first. At billions of rows you need Cassandra or DynamoDB for the catalog itself, Postgres only for job state. The ingest worker's batch upsert to a single DB becomes the bottleneck — fix by writing to a Kafka topic and fanning out to sharded catalog stores downstream.

**Billing on retries — how to avoid double-billing?**
Emit a billing event per successfully upserted row (not per row scanned) to a Kafka billing topic. Billing event key = `(job_id, sku_id)` — billing consumer deduplicates on this. Write billing event in the same DB transaction as the upsert — if the transaction rolls back, no event is emitted. Failed rows are never billed.

**Platform product — API contract and versioning?**
CSV schema versioned via `schema_version` in job creation request. Worker dispatches to a versioned parser; old versions supported for N months with deprecation notice in job status. SLA: `COMPLETED` within `2 × file_size_gb` hours at p99. Expose `POST /bulk-jobs/validate` for dry-run validation without writing. Breaking changes require a new schema version — never break existing.

---

## Improving for More Scale

| Bottleneck | Fix |
|---|---|
| Single catalog DB | Shard by `merchant_id` using Vitess or DynamoDB |
| Row-by-row INSERT | Switch to PostgreSQL `COPY` or multi-row batch INSERT (10–50× faster) |
| No intra-job parallelism | Split file into N parts at upload; each part = independent SQS message; merge progress atomically |
| Large vs small file fairness | Two SQS queues sized by `total_size_bytes`; separate worker pools |
| Error report write is synchronous | Write failed rows to Kafka topic; dedicated error-report service flushes to S3 |
| Progress polling is pull-based | Add webhooks or SSE for push notifications on status change |
| Search consistency gap | Write `catalog_version` to ES; expose consistency horizon in API response |
| Cron-based reaper | Replace with WAL-based streaming reconciliation that reacts to stuck transitions in real time |
