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
    id, session_id, tenant_id, im_channel_id, status, metadata,
    created_at, updated_at, deleted_at
)
SELECT
    id, session_id, tenant_id, im_channel_id, status, metadata,
    created_at, updated_at, deleted_at
FROM im_channel_sessions_binding;
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
    enabled INTEGER NOT NULL DEFAULT 0,
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
    id, platform, name, enabled, mode, output_mode, credentials,
    bot_identity, created_at, updated_at, deleted_at
)
SELECT
    id, platform, name, enabled, mode, output_mode, credentials,
    bot_identity, created_at, updated_at, deleted_at
FROM im_channels_system;
DROP TABLE im_channels_system;

CREATE INDEX idx_im_channels_tenant ON im_channels(tenant_id);
CREATE INDEX idx_im_channels_agent ON im_channels(agent_id);
CREATE UNIQUE INDEX idx_im_channels_bot_identity
    ON im_channels(bot_identity)
    WHERE deleted_at IS NULL AND bot_identity != '';
