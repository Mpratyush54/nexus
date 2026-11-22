-- Personal memories are one person's preferences, but they used to be saved
-- with visibility 'project', so every project member could read them. New
-- owned personal rows now default to 'private' (creator-only); repair the
-- existing ones the same way. Ownerless rows (no proposed_by or user_id) are
-- left alone: private would make them visible to no one.
UPDATE memory_items
   SET visibility = 'private'
 WHERE level = 'personal'
   AND COALESCE(visibility, 'project') = 'project'
   AND (proposed_by IS NOT NULL OR user_id IS NOT NULL);
