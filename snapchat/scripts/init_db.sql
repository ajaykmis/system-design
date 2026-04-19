-- Snapchat MVP Schema

-- Users table (Registration service owns this)
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone           VARCHAR(20) UNIQUE NOT NULL,
    phone_hash      VARCHAR(64) NOT NULL,
    display_name    VARCHAR(50),
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    status          VARCHAR(20) DEFAULT 'active'
);
CREATE INDEX idx_users_phone_hash ON users(phone_hash);

-- Verification codes (Registration service)
CREATE TABLE verification_codes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      VARCHAR(64) UNIQUE NOT NULL,
    phone           VARCHAR(20) NOT NULL,
    code            VARCHAR(6) NOT NULL,
    attempts        INT DEFAULT 0,
    max_attempts    INT DEFAULT 3,
    expires_at      TIMESTAMPTZ NOT NULL,
    verified        BOOLEAN DEFAULT FALSE,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    device_id       VARCHAR(128)
);
CREATE INDEX idx_verification_request ON verification_codes(request_id);
CREATE INDEX idx_verification_phone ON verification_codes(phone);

-- Content metadata (Ingestion service owns this)
CREATE TABLE content (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id      UUID REFERENCES users(id),
    title           VARCHAR(255) NOT NULL,
    description     TEXT,
    category        VARCHAR(50),
    media_url       VARCHAR(512),
    embedding       BYTEA,
    status          VARCHAR(20) DEFAULT 'active',
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_content_creator ON content(creator_id);
CREATE INDEX idx_content_created ON content(created_at DESC);
CREATE INDEX idx_content_category ON content(category);

-- Refresh tokens (Auth service)
CREATE TABLE refresh_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID REFERENCES users(id),
    token_hash      VARCHAR(64) NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    revoked         BOOLEAN DEFAULT FALSE
);
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens(user_id);
