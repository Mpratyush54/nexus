-- 035_storage_plan_caps: D19 storage_bytes on Free/Pro/Team limits.

UPDATE billing_plans
SET limits = limits || '{"storage_bytes": 5368709120}'::jsonb
WHERE id = 'free';

UPDATE billing_plans
SET limits = limits || '{"storage_bytes": 107374182400}'::jsonb
WHERE id = 'pro';

UPDATE billing_plans
SET limits = limits || '{"storage_bytes": 268435456000}'::jsonb
WHERE id = 'team';
