-- Password reset OTPs (forgot password).
CREATE TABLE IF NOT EXISTS password_reset_otps (
  email TEXT PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS password_reset_otps_expires_at_idx ON password_reset_otps (expires_at);
