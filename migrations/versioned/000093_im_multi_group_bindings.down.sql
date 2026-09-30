-- Restore one active binding per account and bot installation.
-- Prefer the direct binding; otherwise keep the newest group binding.
CREATE TEMP TABLE im_multi_group_rollback_bindings AS
SELECT id
FROM (
    SELECT id, ROW_NUMBER() OVER (
        PARTITION BY im_channel_id, user_id
        ORDER BY CASE WHEN chat_type = 'direct' THEN 0 ELSE 1 END,
                 created_at DESC, id DESC
    ) AS binding_position
    FROM im_user_bindings
    WHERE revoked_at IS NULL
) AS ranked_bindings
WHERE binding_position > 1;

UPDATE im_user_bindings
SET status = 'revoked', revoked_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id IN (SELECT id FROM im_multi_group_rollback_bindings);

UPDATE im_channel_sessions
SET deleted_at = CURRENT_TIMESTAMP
WHERE binding_id IN (SELECT id FROM im_multi_group_rollback_bindings)
  AND deleted_at IS NULL;

DROP TABLE im_multi_group_rollback_bindings;

DROP INDEX IF EXISTS idx_im_user_bindings_user_active;
CREATE UNIQUE INDEX idx_im_user_bindings_user_active
    ON im_user_bindings(im_channel_id, user_id)
    WHERE revoked_at IS NULL;
