CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE bulk_jobs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id         TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'CREATED',
    total_rows          INTEGER,
    processed_rows      INTEGER NOT NULL DEFAULT 0,
    failed_rows         INTEGER NOT NULL DEFAULT 0,
    error_report_url    TEXT,
    idempotency_key     TEXT UNIQUE,
    processing_deadline TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '1 hour',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE bulk_job_files (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id      UUID NOT NULL REFERENCES bulk_jobs(id),
    s3_key      TEXT NOT NULL,
    part_number INTEGER NOT NULL DEFAULT 1,
    status      TEXT NOT NULL DEFAULT 'PENDING',
    row_count   INTEGER,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE catalog (
    merchant_id TEXT NOT NULL,
    sku_id      TEXT NOT NULL,
    title       TEXT,
    price       TEXT,
    description TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (merchant_id, sku_id)
);

-- reaper query: abandoned uploads + stuck processing
CREATE INDEX ON bulk_jobs (status, updated_at);
CREATE INDEX ON bulk_jobs (merchant_id, created_at DESC);
CREATE INDEX ON bulk_job_files (job_id);
