-- Move IM bot installations to system scope and add per-user workspace bindings.
DO $$ BEGIN RAISE NOTICE '[Migration 000091] Creating per-user IM bindings'; END $$;

CREATE TABLE im_channel_legacy_config_backup (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT      NOT NULL,
    agent_id          VARCHAR(36) NOT NULL,
    knowledge_base_id VARCHAR(36),
    session_mode      VARCHAR(20) NOT NULL,
    enabled           BOOLEAN     NOT NULL
);

INSERT INTO im_channel_legacy_config_backup (
    id, tenant_id, agent_id, knowledge_base_id, session_mode, enabled
)
SELECT id, tenant_id, agent_id, knowledge_base_id, session_mode, enabled
FROM im_channels;

-- Existing channel ownership cannot be widened safely. Keep the installation
-- configuration for administrator review, but require explicit re-enablement.
UPDATE im_channels SET enabled = FALSE;

ALTER TABLE im_channels
    ADD COLUMN IF NOT EXISTS created_by VARCHAR(36) NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_im_channels_tenant;
DROP INDEX IF EXISTS idx_im_channels_agent;
ALTER TABLE im_channels DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE im_channels DROP COLUMN IF EXISTS agent_id;
ALTER TABLE im_channels DROP COLUMN IF EXISTS knowledge_base_id;
ALTER TABLE im_channels DROP COLUMN IF EXISTS session_mode;

CREATE TABLE im_user_bindings (
    id                     VARCHAR(36)  PRIMARY KEY,
    im_channel_id          VARCHAR(36)  NOT NULL REFERENCES im_channels(id) ON DELETE CASCADE,
    external_user_id       VARCHAR(255) NOT NULL,
    external_user_name     VARCHAR(255) NOT NULL DEFAULT '',
    user_id                VARCHAR(36)  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id              BIGINT       NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    status                 VARCHAR(20)  NOT NULL DEFAULT 'active',
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    revoked_at             TIMESTAMPTZ,
    CONSTRAINT chk_im_user_bindings_status CHECK (status IN ('active', 'revoked'))
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
    id                       VARCHAR(36)  PRIMARY KEY,
    code_hash                VARCHAR(64)  NOT NULL UNIQUE,
    im_channel_id            VARCHAR(36)  NOT NULL REFERENCES im_channels(id) ON DELETE CASCADE,
    user_id                  VARCHAR(36)  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tenant_id                BIGINT       NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    status                   VARCHAR(20)  NOT NULL DEFAULT 'pending',
    claimed_external_user_id VARCHAR(255) NOT NULL DEFAULT '',
    binding_id               VARCHAR(36)  REFERENCES im_user_bindings(id) ON DELETE SET NULL,
    expires_at               TIMESTAMPTZ  NOT NULL,
    consumed_at              TIMESTAMPTZ,
    cancelled_at             TIMESTAMPTZ,
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_im_binding_requests_status CHECK (status IN ('pending', 'consumed', 'cancelled'))
);

CREATE INDEX idx_im_binding_requests_owner
    ON im_binding_requests(user_id, created_at DESC);
CREATE INDEX idx_im_binding_requests_channel_status
    ON im_binding_requests(im_channel_id, status, expires_at);
CREATE UNIQUE INDEX idx_im_binding_requests_user_pending
    ON im_binding_requests(im_channel_id, user_id)
    WHERE status = 'pending';

-- Preserve old session provenance in metadata, then make new continuity depend
-- exclusively on binding_id. Legacy rows intentionally remain unbound.
UPDATE im_channel_sessions
SET metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object(
    'legacy_platform', platform,
    'legacy_external_user_id', user_id,
    'legacy_chat_id', chat_id,
    'legacy_thread_id', thread_id,
    'legacy_agent_id', agent_id
);

DROP INDEX IF EXISTS idx_channel_lookup;
DROP INDEX IF EXISTS idx_channel_thread_lookup;
ALTER TABLE im_channel_sessions
    ADD COLUMN IF NOT EXISTS binding_id VARCHAR(36) REFERENCES im_user_bindings(id) ON DELETE CASCADE;
UPDATE im_channel_sessions SET im_channel_id = '' WHERE im_channel_id IS NULL;
ALTER TABLE im_channel_sessions ALTER COLUMN im_channel_id SET NOT NULL;
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS platform;
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS user_id;
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS chat_id;
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS thread_id;
ALTER TABLE im_channel_sessions DROP COLUMN IF EXISTS agent_id;

CREATE UNIQUE INDEX idx_im_channel_sessions_binding_active
    ON im_channel_sessions(binding_id)
    WHERE deleted_at IS NULL AND binding_id IS NOT NULL;
CREATE INDEX idx_im_channel_sessions_binding
    ON im_channel_sessions(binding_id);

COMMENT ON TABLE im_channels IS 'System-managed IM bot installations';
COMMENT ON TABLE im_user_bindings IS 'Verified IM identities bound to WeKnora users and workspaces';
COMMENT ON TABLE im_binding_requests IS 'Short-lived single-use requests for establishing IM user bindings';

DO $$ BEGIN RAISE NOTICE '[Migration 000091] Per-user IM bindings ready'; END $$;
