-- GitHub repository link + user mapping (issue #167 / Phase 7).
CREATE TABLE IF NOT EXISTS github_links (
  project_id     UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  owner          TEXT NOT NULL,
  repo           TEXT NOT NULL,
  access_token   TEXT,
  sync_mode      TEXT NOT NULL DEFAULT 'manual', -- manual | auto | one_time
  connected_by   UUID REFERENCES users(id),
  connected_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_import_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS github_user_map (
  github_login TEXT PRIMARY KEY,
  github_id    BIGINT,
  user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
  email        TEXT,
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS github_user_map_user_id_idx ON github_user_map (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS github_user_map_email_idx ON github_user_map (lower(email)) WHERE email IS NOT NULL;
