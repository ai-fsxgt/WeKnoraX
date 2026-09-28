DROP INDEX IF EXISTS idx_im_channel_sessions_binding;
DROP INDEX IF EXISTS idx_im_channel_sessions_binding_active;

ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS platform VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS user_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS chat_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS thread_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS agent_id VARCHAR(36) DEFAULT '';
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS binding_id;

CREATE UNIQUE INDEX idx_channel_lookup
    ON im_channel_sessions(platform, user_id, chat_id, tenant_id, agent_id)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_channel_thread_lookup
    ON im_channel_sessions(platform, chat_id, thread_id, tenant_id, agent_id)
    WHERE deleted_at IS NULL AND thread_id != '';

DROP TABLE IF EXISTS im_binding_requests;
DROP TABLE IF EXISTS im_user_bindings;

ALTER TABLE im_channels ADD COLUMN IF NOT EXISTS tenant_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE im_channels ADD COLUMN IF NOT EXISTS agent_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE im_channels ADD COLUMN IF NOT EXISTS knowledge_base_id VARCHAR(36) DEFAULT '';
ALTER TABLE im_channels ADD COLUMN IF NOT EXISTS session_mode VARCHAR(20) NOT NULL DEFAULT 'user';
ALTER TABLE im_channels DROP COLUMN IF EXISTS created_by;

CREATE INDEX idx_im_channels_tenant ON im_channels(tenant_id);
CREATE INDEX idx_im_channels_agent ON im_channels(agent_id);
