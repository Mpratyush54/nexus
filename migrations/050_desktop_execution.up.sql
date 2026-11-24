-- Outbound authenticated execution. Account JWT + a separately protected
-- device credential are both required for worker claims and progress writes.
CREATE TABLE IF NOT EXISTS desktop_execution_devices (
    owner_id TEXT NOT NULL,
    id TEXT NOT NULL,
    key_hash TEXT NOT NULL,
    payload JSONB NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (owner_id, id)
);
CREATE TABLE IF NOT EXISTS desktop_execution_jobs (
    owner_id TEXT NOT NULL,
    id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued','awaiting_approval','running','completed','failed','cancelled','expired')),
    revision BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    PRIMARY KEY (owner_id, id),
    FOREIGN KEY (owner_id, device_id) REFERENCES desktop_execution_devices(owner_id, id)
);
CREATE INDEX IF NOT EXISTS desktop_execution_pending ON desktop_execution_jobs(owner_id, device_id, created_at) WHERE state = 'queued';
CREATE INDEX IF NOT EXISTS desktop_execution_expiry ON desktop_execution_jobs(expires_at);
-- One outstanding request per device, enforced across concurrent API replicas.
CREATE UNIQUE INDEX IF NOT EXISTS desktop_execution_single_active ON desktop_execution_jobs(owner_id,device_id)
    WHERE state IN ('queued','awaiting_approval','running');
