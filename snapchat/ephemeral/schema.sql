-- Ephemeral Messaging Schema
-- In the MVP, these are in-memory. This schema documents the production target.

-- Snap message metadata (NOT the content itself)
CREATE TABLE snap_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    from_user_id    UUID NOT NULL REFERENCES users(id),
    to_user_id      UUID NOT NULL REFERENCES users(id),
    blob_ref        VARCHAR(256) NOT NULL,     -- pointer to encrypted blob in object storage
    key_id          VARCHAR(256) NOT NULL,      -- pointer to encryption key in KMS
    state           VARCHAR(20) NOT NULL DEFAULT 'PENDING',  -- PENDING, DELIVERED, OPENED, EXPIRED
    ttl_after_open  INT NOT NULL DEFAULT 10,    -- seconds content is viewable after opening
    max_views       INT NOT NULL DEFAULT 1,
    view_count      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL,       -- hard TTL (30 days from creation, or ttl_after_open from first open)
    opened_at       TIMESTAMPTZ,
    expired_at      TIMESTAMPTZ,

    CONSTRAINT chk_state CHECK (state IN ('PENDING', 'DELIVERED', 'OPENED', 'EXPIRED')),
    CONSTRAINT chk_views CHECK (view_count <= max_views)
);

CREATE INDEX idx_snap_to_user_state ON snap_messages(to_user_id, state);
CREATE INDEX idx_snap_expires ON snap_messages(expires_at) WHERE state != 'EXPIRED';
CREATE INDEX idx_snap_from_user ON snap_messages(from_user_id, created_at DESC);

-- Encryption key metadata (actual key material lives in KMS, not here)
CREATE TABLE encryption_keys (
    key_id          VARCHAR(256) PRIMARY KEY,
    message_id      UUID NOT NULL REFERENCES snap_messages(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    destroyed_at    TIMESTAMPTZ,                -- set when crypto-shredded
    is_destroyed    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX idx_keys_destroyed ON encryption_keys(is_destroyed) WHERE is_destroyed = FALSE;

-- Blob metadata (actual blob lives in S3/GCS, not here)
CREATE TABLE blob_refs (
    ref             VARCHAR(256) PRIMARY KEY,
    message_id      UUID NOT NULL REFERENCES snap_messages(id),
    size_bytes      BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,
    is_deleted      BOOLEAN NOT NULL DEFAULT FALSE
);

-- Screenshot events (social deterrent — recorded for sender notification)
CREATE TABLE screenshot_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id      UUID NOT NULL REFERENCES snap_messages(id),
    user_id         UUID NOT NULL REFERENCES users(id),  -- who took the screenshot
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    device_type     VARCHAR(20)  -- ios, android
);

CREATE INDEX idx_screenshot_message ON screenshot_events(message_id);

-- View to help the reaper find expired snaps efficiently
CREATE VIEW expired_snaps AS
SELECT id, blob_ref, key_id
FROM snap_messages
WHERE state != 'EXPIRED'
  AND expires_at < NOW();
