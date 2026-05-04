-- Tenants (multi-tenant support)
CREATE TABLE IF NOT EXISTS tenants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Messages (reusable template content keyed by type)
CREATE TABLE IF NOT EXISTS messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    template_type   TEXT NOT NULL,          -- e.g. LOGIN_MSG, PROMO_OFFER
    locale          TEXT NOT NULL DEFAULT 'en',
    content         TEXT NOT NULL,          -- rendered or raw template body
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (template_type, locale)
);

-- Email jobs
CREATE TABLE IF NOT EXISTS emails (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID NOT NULL REFERENCES tenants(id),
    recipient_user_id   TEXT NOT NULL,
    recipient_address   TEXT NOT NULL,
    category            TEXT NOT NULL CHECK (category IN ('TRANSACTIONAL','PROMOTIONAL')),
    template_type       TEXT NOT NULL,
    template_attributes JSONB NOT NULL DEFAULT '{}',
    locale              TEXT NOT NULL DEFAULT 'en',
    status              TEXT NOT NULL DEFAULT 'PENDING'
                            CHECK (status IN ('PENDING','QUEUED','SENT','FAILED')),
    scheduled_at        TIMESTAMPTZ,        -- NULL means send immediately
    sent_at             TIMESTAMPTZ,
    failure_reason      TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_emails_status    ON emails(status);
CREATE INDEX IF NOT EXISTS idx_emails_tenant    ON emails(tenant_id);
CREATE INDEX IF NOT EXISTS idx_emails_scheduled ON emails(scheduled_at) WHERE scheduled_at IS NOT NULL;

-- Delivery events (for tracking delivery rate)
CREATE TABLE IF NOT EXISTS delivery_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email_id    UUID NOT NULL REFERENCES emails(id),
    event_type  TEXT NOT NULL,  -- QUEUED, SENT, DELIVERED, BOUNCED, FAILED
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata    JSONB NOT NULL DEFAULT '{}'
);

-- Seed a default tenant
INSERT INTO tenants (id, name) VALUES
    ('00000000-0000-0000-0000-000000000001', 'booking-service'),
    ('00000000-0000-0000-0000-000000000002', 'marketing-service'),
    ('00000000-0000-0000-0000-000000000003', 'friending-service')
ON CONFLICT DO NOTHING;
