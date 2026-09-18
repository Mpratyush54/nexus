-- 020_platform: Super Admin allowlist + app release registry.
--
-- Platform Super Admin is orthogonal to project OWNER/ADMIN and org ADMIN.
-- Bootstrap additional operators with PLATFORM_ADMIN_USERNAMES (comma-separated)
-- even before any row exists here. Releases back the CLI/desktop update
-- channel (GET /platform/releases/latest) and the Super Admin console.

CREATE TABLE IF NOT EXISTS platform_admins (
    user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    granted_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS app_releases (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    app          TEXT NOT NULL,
    version      TEXT NOT NULL,
    channel      TEXT NOT NULL DEFAULT 'stable',
    notes        TEXT,
    git_sha      TEXT,
    artifacts    JSONB NOT NULL DEFAULT '[]',
    published_by UUID REFERENCES users(id),
    published_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    yanked       BOOLEAN NOT NULL DEFAULT false,
    UNIQUE (app, version)
);

CREATE INDEX IF NOT EXISTS app_releases_app_channel_idx
    ON app_releases (app, channel, published_at DESC);
