-- 033_guest_offboard.up.sql
-- Guest links (spec 9.2 / D12): one agent session, expiring, single-use by
-- default. The accepting signed-in user gets a grant on that session only.
-- Offboarding transfers agent_sessions.owner_user_id inside the org; this
-- table does not store titles, summaries, or file names.

CREATE TABLE IF NOT EXISTS guest_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    token TEXT NOT NULL UNIQUE,
    created_by TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    single_use BOOLEAN NOT NULL DEFAULT true,
    used_at TIMESTAMPTZ,
    grantee_user_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_guest_links_session ON guest_links (session_id);
