-- Secure Document Sharing App: initial schema
-- Works as a golang-migrate file AND can be pasted into pgAdmin's Query Tool.

CREATE EXTENSION IF NOT EXISTS citext;

-- ---------------------------------------------------------------
-- Users and auth
-- ---------------------------------------------------------------
CREATE TABLE users (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email              CITEXT NOT NULL UNIQUE,
    password_hash      TEXT NOT NULL,
    terms_accepted_at  TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Refresh tokens are stored hashed, never in plaintext.
CREATE TABLE refresh_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    replaced_by  UUID REFERENCES refresh_tokens(id),  -- rotation / reuse detection
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens(user_id);

-- Who accepted which terms and when (GDPR consent log).
CREATE TABLE consent_log (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_name TEXT NOT NULL,       -- e.g. 'terms', 'privacy_policy'
    version       TEXT NOT NULL,
    accepted_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------
-- Documents (file bytes live in Azure Blob; only metadata is here)
-- ---------------------------------------------------------------
CREATE TABLE documents (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    content_type     TEXT NOT NULL,
    size_bytes       BIGINT NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 104857600),
    storage_url      TEXT NOT NULL,                 -- blob path
    -- encryption metadata (envelope encryption)
    enc_algorithm    TEXT NOT NULL DEFAULT 'AES-256-GCM',
    wrapped_dek      BYTEA NOT NULL,                -- DEK wrapped by the Key Vault KEK
    kek_id           TEXT NOT NULL,                 -- Key Vault key id + version
    nonce            BYTEA NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ
);
CREATE INDEX idx_documents_owner ON documents(owner_id, created_at DESC) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------
-- Shares
-- ---------------------------------------------------------------
CREATE TABLE document_shares (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    recipient_email  CITEXT NOT NULL,
    token_hash       TEXT NOT NULL UNIQUE,          -- store a hash, give the raw token only to the sender
    expires_at       TIMESTAMPTZ NOT NULL,
    max_opens        INTEGER NOT NULL DEFAULT 1 CHECK (max_opens > 0),
    open_count       INTEGER NOT NULL DEFAULT 0 CHECK (open_count >= 0),
    revoked_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (open_count <= max_opens)
);
CREATE INDEX idx_shares_document ON document_shares(document_id);

-- One-time passwords for recipient verification (hashed, short lived).
CREATE TABLE otp_challenges (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    share_id     UUID NOT NULL REFERENCES document_shares(id) ON DELETE CASCADE,
    code_hash    TEXT NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_otp_share ON otp_challenges(share_id);

-- ---------------------------------------------------------------
-- Audit log, partitioned by month.
-- No foreign keys on purpose: logs must survive document/user changes.
-- The partition key must be part of the primary key.
-- ---------------------------------------------------------------
CREATE TABLE audit_logs (
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id      UUID,
    document_id  UUID,
    share_id     UUID,
    action       TEXT NOT NULL,
    result       TEXT NOT NULL DEFAULT 'success',
    ip_address   INET,
    user_agent   TEXT,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE TABLE audit_logs_default PARTITION OF audit_logs DEFAULT;

CREATE TABLE audit_logs_2026_11 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE audit_logs_2026_12 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE audit_logs_2027_01 PARTITION OF audit_logs
    FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');

CREATE INDEX idx_audit_document ON audit_logs(document_id, created_at DESC);
CREATE INDEX idx_audit_user ON audit_logs(user_id, created_at DESC);