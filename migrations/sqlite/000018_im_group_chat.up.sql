ALTER TABLE im_channels
    ADD COLUMN allow_group_chat INTEGER NOT NULL DEFAULT 0;

ALTER TABLE im_user_bindings
    ADD COLUMN chat_type TEXT NOT NULL DEFAULT 'direct'
    CHECK (chat_type IN ('direct', 'group'));

DROP INDEX IF EXISTS idx_im_user_bindings_external_active;
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, chat_type, external_user_id)
    WHERE revoked_at IS NULL;

ALTER TABLE im_channel_sessions
    ADD COLUMN context_key TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_im_channel_sessions_binding_active;
CREATE UNIQUE INDEX idx_im_channel_sessions_binding_active
    ON im_channel_sessions(binding_id, context_key)
    WHERE deleted_at IS NULL AND binding_id IS NOT NULL;
