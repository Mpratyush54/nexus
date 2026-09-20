-- Signup email OTP pending rows (email verification before account create).
CREATE TABLE IF NOT EXISTS signup_otps (
  email TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  code_hash TEXT NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS signup_otps_expires_at_idx ON signup_otps (expires_at);
