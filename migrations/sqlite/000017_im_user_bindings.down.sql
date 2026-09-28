DROP TABLE IF EXISTS im_binding_requests;

ALTER TABLE im_channel_sessions RENAME TO im_channel_sessions_binding;
CREATE TABLE im_channel_sessions (
    id VARCHAR(36) PRIMARY KEY,
    platform VARCHAR(20) NOT NULL DEFAULT '',
    user_id VARCHAR(128) NOT NULL DEFAULT '',
    chat_id VARCHAR(128) NOT NULL DEFAULT '',
    session_id VARCHAR(36) NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tenant_id INTEGER NOT NULL,
    agent_id VARCHAR(36) DEFAULT '',
    im_channel_id VARCHAR(36) DEFAULT '',
    thread_id VARCHAR(128) NOT NULL DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    metadata TEXT DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
INSERT INTO im_channel_sessions (
    id, platform, user_id, chat_id, session_id, tenant_id, agent_id,
    im_channel_id, thread_id, status, metadata, created_at, updated_at, deleted_at
)
SELECT
    session_map.id,
    COALESCE(json_extract(session_map.metadata, '$.legacy_platform'), channel.platform, ''),
    COALESCE(
        json_extract(session_map.metadata, '$.legacy_external_user_id'),
        substr(binding.external_user_id, 1, 128),
        ''
    ),
    COALESCE(
        json_extract(session_map.metadata, '$.legacy_chat_id'),
        CASE WHEN session_map.binding_id IS NOT NULL THEN '__binding__:' || session_map.binding_id END,
        ''
    ),
    session_map.session_id,
    session_map.tenant_id,
    CASE
        WHEN json_type(session_map.metadata, '$.legacy_agent_id') IS NULL THEN ''
        ELSE json_extract(session_map.metadata, '$.legacy_agent_id')
    END,
    session_map.im_channel_id,
    COALESCE(json_extract(session_map.metadata, '$.legacy_thread_id'), ''),
    session_map.status,
    session_map.metadata,
    session_map.created_at,
    session_map.updated_at,
    session_map.deleted_at
FROM im_channel_sessions_binding AS session_map
LEFT JOIN im_user_bindings AS binding ON binding.id = session_map.binding_id
LEFT JOIN im_channels AS channel ON channel.id = binding.im_channel_id;
DROP TABLE im_channel_sessions_binding;

CREATE UNIQUE INDEX idx_channel_lookup
    ON im_channel_sessions(platform, user_id, chat_id, tenant_id, agent_id)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_channel_thread_lookup
    ON im_channel_sessions(platform, chat_id, thread_id, tenant_id, agent_id)
    WHERE deleted_at IS NULL AND thread_id != '';
CREATE INDEX idx_im_channel_tenant ON im_channel_sessions(tenant_id);
CREATE INDEX idx_im_channel_session ON im_channel_sessions(session_id);
CREATE INDEX idx_im_channel_sessions_channel ON im_channel_sessions(im_channel_id)
    WHERE im_channel_id != '';

DROP TABLE IF EXISTS im_user_bindings;

ALTER TABLE im_channels RENAME TO im_channels_system;
CREATE TABLE im_channels (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL DEFAULT 0,
    agent_id VARCHAR(36) NOT NULL DEFAULT '',
    platform VARCHAR(20) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    mode VARCHAR(20) NOT NULL DEFAULT 'websocket',
    output_mode VARCHAR(20) NOT NULL DEFAULT 'stream',
    credentials TEXT NOT NULL DEFAULT '{}',
    knowledge_base_id VARCHAR(36) DEFAULT '',
    bot_identity VARCHAR(255) NOT NULL DEFAULT '',
    session_mode VARCHAR(20) NOT NULL DEFAULT 'user',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
INSERT INTO im_channels (
    id, tenant_id, agent_id, platform, name, enabled, mode, output_mode,
    credentials, knowledge_base_id, bot_identity, session_mode,
    created_at, updated_at, deleted_at
)
SELECT
    channel.id,
    COALESCE(backup.tenant_id, 0),
    COALESCE(backup.agent_id, ''),
    channel.platform,
    channel.name,
    COALESCE(backup.enabled, channel.enabled),
    channel.mode,
    channel.output_mode,
    channel.credentials,
    CASE
        WHEN backup.id IS NULL THEN ''
        ELSE backup.knowledge_base_id
    END,
    channel.bot_identity,
    COALESCE(backup.session_mode, 'user'),
    channel.created_at,
    channel.updated_at,
    channel.deleted_at
FROM im_channels_system AS channel
LEFT JOIN im_channel_legacy_config_backup AS backup ON backup.id = channel.id;
DROP TABLE im_channels_system;
DROP TABLE im_channel_legacy_config_backup;

CREATE INDEX idx_im_channels_tenant ON im_channels(tenant_id);
CREATE INDEX idx_im_channels_agent ON im_channels(agent_id);
CREATE UNIQUE INDEX idx_im_channels_bot_identity
    ON im_channels(bot_identity)
    WHERE deleted_at IS NULL AND bot_identity != '';
