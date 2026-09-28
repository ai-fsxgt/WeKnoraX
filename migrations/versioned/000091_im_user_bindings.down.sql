DROP INDEX IF EXISTS idx_im_channel_sessions_binding;
DROP INDEX IF EXISTS idx_im_channel_sessions_binding_active;

ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS platform VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS user_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS chat_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS thread_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE im_channel_sessions ADD COLUMN IF NOT EXISTS agent_id VARCHAR(36) DEFAULT '';

UPDATE im_channel_sessions AS session_map
SET platform = channel.platform,
    user_id = LEFT(binding.external_user_id, 128),
    chat_id = '__binding__:' || binding.id
FROM im_user_bindings AS binding
JOIN im_channels AS channel ON channel.id = binding.im_channel_id
WHERE session_map.binding_id = binding.id;

UPDATE im_channel_sessions
SET platform = COALESCE(metadata ->> 'legacy_platform', ''),
    user_id = COALESCE(metadata ->> 'legacy_external_user_id', ''),
    chat_id = COALESCE(metadata ->> 'legacy_chat_id', ''),
    thread_id = COALESCE(metadata ->> 'legacy_thread_id', ''),
    agent_id = metadata ->> 'legacy_agent_id'
WHERE binding_id IS NULL;

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

ALTER TABLE im_channels
    ADD CONSTRAINT chk_im_channels_session_mode
    CHECK (session_mode IN ('user', 'thread'));

UPDATE im_channels AS channel
SET tenant_id = backup.tenant_id,
    agent_id = backup.agent_id,
    knowledge_base_id = backup.knowledge_base_id,
    session_mode = backup.session_mode,
    enabled = backup.enabled
FROM im_channel_legacy_config_backup AS backup
WHERE channel.id = backup.id;

DROP TABLE im_channel_legacy_config_backup;
ALTER TABLE im_channels DROP COLUMN IF EXISTS created_by;

CREATE INDEX idx_im_channels_tenant ON im_channels(tenant_id);
CREATE INDEX idx_im_channels_agent ON im_channels(agent_id);
