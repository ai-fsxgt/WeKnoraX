-- Mirrors versioned migration 000091_im_user_bindings.
CREATE TABLE im_channel_legacy_config_backup (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    agent_id VARCHAR(36) NOT NULL,
    knowledge_base_id VARCHAR(36),
    session_mode VARCHAR(20) NOT NULL,
    enabled INTEGER NOT NULL
);
INSERT INTO im_channel_legacy_config_backup (
    id, tenant_id, agent_id, knowledge_base_id, session_mode, enabled
)
SELECT id, tenant_id, agent_id, knowledge_base_id, session_mode, enabled
FROM im_channels;

UPDATE im_channels SET enabled = 0;

ALTER TABLE im_channels RENAME TO im_channels_legacy;
CREATE TABLE im_channels (
    id VARCHAR(36) PRIMARY KEY,
    platform VARCHAR(20) NOT NULL,
    name VARCHAR(255) NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 0,
    mode VARCHAR(20) NOT NULL DEFAULT 'websocket',
    output_mode VARCHAR(20) NOT NULL DEFAULT 'stream',
    credentials TEXT NOT NULL DEFAULT '{}',
    bot_identity VARCHAR(255) NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
INSERT INTO im_channels (
    id, platform, name, enabled, mode, output_mode, credentials,
    bot_identity, created_by, created_at, updated_at, deleted_at
)
SELECT
    id, platform, name, 0, mode, output_mode, credentials,
    bot_identity, '', created_at, updated_at, deleted_at
FROM im_channels_legacy;
DROP TABLE im_channels_legacy;

CREATE UNIQUE INDEX idx_im_channels_bot_identity
    ON im_channels(bot_identity)
    WHERE deleted_at IS NULL AND bot_identity != '';

CREATE TABLE im_user_bindings (
    id TEXT PRIMARY KEY,
    im_channel_id TEXT NOT NULL REFERENCES im_channels(id) ON DELETE CASCADE,
    external_user_id TEXT NOT NULL,
    external_user_name TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    revoked_at DATETIME
);
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, external_user_id)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX idx_im_user_bindings_user_active
    ON im_user_bindings(im_channel_id, user_id)
    WHERE revoked_at IS NULL;
CREATE INDEX idx_im_user_bindings_user
    ON im_user_bindings(user_id, tenant_id);
CREATE INDEX idx_im_user_bindings_tenant
    ON im_user_bindings(tenant_id);

CREATE TABLE im_binding_requests (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL UNIQUE,
    im_channel_id TEXT NOT NULL REFERENCES im_channels(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'consumed', 'cancelled')),
    claimed_external_user_id TEXT NOT NULL DEFAULT '',
    binding_id TEXT REFERENCES im_user_bindings(id) ON DELETE SET NULL,
    expires_at DATETIME NOT NULL,
    consumed_at DATETIME,
    cancelled_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_im_binding_requests_owner
    ON im_binding_requests(user_id, created_at DESC);
CREATE INDEX idx_im_binding_requests_channel_status
    ON im_binding_requests(im_channel_id, status, expires_at);
CREATE UNIQUE INDEX idx_im_binding_requests_user_pending
    ON im_binding_requests(im_channel_id, user_id)
    WHERE status = 'pending';

ALTER TABLE im_channel_sessions RENAME TO im_channel_sessions_legacy;
CREATE TABLE im_channel_sessions (
    id VARCHAR(36) PRIMARY KEY,
    binding_id VARCHAR(36) REFERENCES im_user_bindings(id) ON DELETE CASCADE,
    session_id VARCHAR(36) NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tenant_id INTEGER NOT NULL,
    im_channel_id VARCHAR(36) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    metadata TEXT DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
INSERT INTO im_channel_sessions (
    id, binding_id, session_id, tenant_id, im_channel_id, status,
    metadata, created_at, updated_at, deleted_at
)
SELECT
    id, NULL, session_id, tenant_id, im_channel_id, status,
    json_set(
        CASE WHEN json_valid(metadata) THEN COALESCE(metadata, '{}') ELSE '{}' END,
        '$.legacy_platform', platform,
        '$.legacy_external_user_id', user_id,
        '$.legacy_chat_id', chat_id,
        '$.legacy_thread_id', thread_id,
        '$.legacy_agent_id', agent_id
    ),
    created_at, updated_at, deleted_at
FROM im_channel_sessions_legacy;
DROP TABLE im_channel_sessions_legacy;

CREATE UNIQUE INDEX idx_im_channel_sessions_binding_active
    ON im_channel_sessions(binding_id)
    WHERE deleted_at IS NULL AND binding_id IS NOT NULL;
CREATE INDEX idx_im_channel_sessions_binding ON im_channel_sessions(binding_id);
CREATE INDEX idx_im_channel_tenant ON im_channel_sessions(tenant_id);
CREATE INDEX idx_im_channel_session ON im_channel_sessions(session_id);
CREATE INDEX idx_im_channel_sessions_channel ON im_channel_sessions(im_channel_id);
