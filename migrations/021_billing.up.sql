-- 021_billing: plan catalog + subscriptions (personal user or org).
-- Provider is "manual" until a Stripe (or similar) checkout is wired;
-- provider_ref is the future customer/subscription id.

CREATE TABLE IF NOT EXISTS billing_plans (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    price_cents INTEGER NOT NULL DEFAULT 0,
    currency    TEXT NOT NULL DEFAULT 'usd',
    interval    TEXT NOT NULL DEFAULT 'month',
    public      BOOLEAN NOT NULL DEFAULT true,
    rank        INTEGER NOT NULL DEFAULT 0,
    limits      JSONB NOT NULL DEFAULT '{}',
    features    JSONB NOT NULL DEFAULT '[]'
);

INSERT INTO billing_plans (id, name, description, price_cents, currency, interval, public, rank, limits, features)
VALUES
  (
    'free',
    'Free',
    'Personal memory for one team.',
    0, 'usd', 'month', true, 10,
    '{"orgs":1,"projects":3,"members":5,"memories":2000,"github_import":true}',
    '["Project memory","Live presence","GitHub import"]'
  ),
  (
    'pro',
    'Pro',
    'For groups that review and ship together.',
    1600, 'usd', 'month', true, 20,
    '{"orgs":5,"projects":25,"members":25,"memories":0,"github_import":true}',
    '["Everything in Free","25 seats","Unlimited memories","Priority support"]'
  ),
  (
    'team',
    'Team',
    'Org-wide memory with room to grow.',
    4800, 'usd', 'month', true, 30,
    '{"orgs":0,"projects":0,"members":250,"memories":0,"github_import":true}',
    '["Everything in Pro","Unlimited orgs & projects","250 seats","Admin billing"]'
  )
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS subscriptions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_type            TEXT NOT NULL,
    owner_id              TEXT NOT NULL,
    plan_id               TEXT NOT NULL REFERENCES billing_plans(id),
    status                TEXT NOT NULL DEFAULT 'active',
    provider              TEXT NOT NULL DEFAULT 'manual',
    provider_ref          TEXT,
    current_period_start  TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_period_end    TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '30 days'),
    cancel_at_period_end  BOOLEAN NOT NULL DEFAULT false,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_type, owner_id)
);

CREATE INDEX IF NOT EXISTS subscriptions_plan_idx ON subscriptions (plan_id);
CREATE INDEX IF NOT EXISTS subscriptions_status_idx ON subscriptions (status);
