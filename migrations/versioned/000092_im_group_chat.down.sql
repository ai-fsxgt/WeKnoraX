-- Group bindings have no representation in the previous schema.
DELETE FROM im_channel_sessions
WHERE binding_id IN (SELECT id FROM im_user_bindings WHERE chat_type = 'group');
DELETE FROM im_user_bindings WHERE chat_type = 'group';

DROP INDEX IF EXISTS idx_im_channel_sessions_binding_active;
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS context_key;
CREATE UNIQUE INDEX idx_im_channel_sessions_binding_active
    ON im_channel_sessions(binding_id)
    WHERE deleted_at IS NULL AND binding_id IS NOT NULL;

DROP INDEX IF EXISTS idx_im_user_bindings_external_active;
ALTER TABLE im_user_bindings DROP CONSTRAINT IF EXISTS chk_im_user_bindings_chat_type;
ALTER TABLE im_user_bindings DROP COLUMN IF EXISTS chat_type;
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, external_user_id)
    WHERE revoked_at IS NULL;

ALTER TABLE im_channels DROP COLUMN IF EXISTS allow_group_chat;
