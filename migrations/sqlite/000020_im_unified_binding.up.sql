ALTER TABLE im_channels
    ADD COLUMN binding_mode TEXT NOT NULL DEFAULT 'flexible'
    CHECK (binding_mode IN ('flexible', 'unified'));
ALTER TABLE im_user_bindings
    ADD COLUMN binding_scope TEXT NOT NULL DEFAULT 'conversation'
    CHECK (binding_scope IN ('conversation', 'channel'));

DROP INDEX IF EXISTS idx_im_user_bindings_external_active;
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, chat_type, external_user_id)
    WHERE revoked_at IS NULL AND binding_scope = 'conversation';
DROP INDEX IF EXISTS idx_im_user_bindings_user_active;
CREATE UNIQUE INDEX idx_im_user_bindings_user_active
    ON im_user_bindings(im_channel_id, user_id)
    WHERE revoked_at IS NULL AND chat_type = 'direct' AND binding_scope = 'conversation';
CREATE UNIQUE INDEX idx_im_user_bindings_channel_active
    ON im_user_bindings(im_channel_id)
    WHERE revoked_at IS NULL AND binding_scope = 'channel';
