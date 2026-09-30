-- 035_storage_plan_caps down: drop storage_bytes from plan limits.

UPDATE billing_plans
SET limits = limits - 'storage_bytes'
WHERE id IN ('free', 'pro', 'team');
