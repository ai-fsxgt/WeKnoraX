-- Add channel-level group chat support with one binding per group and
-- one independent conversation context per group member.
ALTER TABLE im_channels
    ADD COLUMN IF NOT EXISTS allow_group_chat BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE im_user_bindings
    ADD COLUMN IF NOT EXISTS chat_type VARCHAR(20) NOT NULL DEFAULT 'direct';
ALTER TABLE im_user_bindings
    ADD CONSTRAINT chk_im_user_bindings_chat_type
    CHECK (chat_type IN ('direct', 'group'));

DROP INDEX IF EXISTS idx_im_user_bindings_external_active;
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, chat_type, external_user_id)
    WHERE revoked_at IS NULL;

ALTER TABLE im_channel_sessions
    ADD COLUMN IF NOT EXISTS context_key VARCHAR(255) NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_im_channel_sessions_binding_active;
CREATE UNIQUE INDEX idx_im_channel_sessions_binding_active
    ON im_channel_sessions(binding_id, context_key)
    WHERE deleted_at IS NULL AND binding_id IS NOT NULL;

COMMENT ON COLUMN im_channels.allow_group_chat IS
    'Whether this bot installation accepts group account bindings and group questions';
COMMENT ON COLUMN im_user_bindings.chat_type IS
    'Conversation type represented by external_user_id: direct user or group chat';
COMMENT ON COLUMN im_channel_sessions.context_key IS
    'Empty for direct chat; sender platform user ID for independent group-member context';
