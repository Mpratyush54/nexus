-- 052_diagnostic_reports.up.sql
-- Problem reports sent by the web portal, desktop app, Go core and CLI
-- (POST /v1/diagnostics). Text fields are redacted by the server before
-- they are written (tokens, emails, home paths). Rows are kept 30 days;
-- the server's retention loop deletes older ones.
--
-- user_id links a report to the signed-in account and is cleared, not
-- cascaded, when the account is deleted, so the crash data stays useful
-- without pointing at anyone. Client IP addresses are never stored.
-- fingerprint groups the same problem: kind + source + app version + top
-- stack frames (see internal/diagnostics).

CREATE TABLE IF NOT EXISTS diagnostic_reports (
    id BIGSERIAL PRIMARY KEY,
    report_id TEXT NOT NULL UNIQUE,
    fingerprint TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('crash', 'error', 'user_report')),
    source TEXT NOT NULL CHECK (source IN ('desktop', 'core', 'web', 'cli')),
    app_version TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    stack TEXT NOT NULL DEFAULT '',
    logs JSONB NOT NULL DEFAULT '[]'::jsonb,
    user_note TEXT NOT NULL DEFAULT '',
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    request_id TEXT NOT NULL DEFAULT '',
    client_ts TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Admin list groups by fingerprint over a recent window, newest first.
CREATE INDEX IF NOT EXISTS idx_diagnostic_reports_fp_received
    ON diagnostic_reports (fingerprint, received_at DESC);
-- Retention deletes by age.
CREATE INDEX IF NOT EXISTS idx_diagnostic_reports_received
    ON diagnostic_reports (received_at);
