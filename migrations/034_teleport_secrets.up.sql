-- Teleport inbox and wrapped secret data keys (spec 7.7, D18).
-- Wrapped keys are ciphertext. Plaintext data keys are not stored.

CREATE TABLE IF NOT EXISTS teleports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    from_user_id TEXT NOT NULL,
    to_user_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'sent',
    preview TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_teleports_to
    ON teleports (to_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS secret_keys (
    blob_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    wrapped BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS secret_key_grants (
    blob_id TEXT NOT NULL REFERENCES secret_keys(blob_id) ON DELETE CASCADE,
    grantee_user_id TEXT NOT NULL,
    PRIMARY KEY (blob_id, grantee_user_id)
);
