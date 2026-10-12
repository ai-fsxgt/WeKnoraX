-- Fail explicitly instead of reinterpreting unified grants and their histories.
CREATE TEMP TABLE im_unified_downgrade_guard (
    unified_rows INTEGER CONSTRAINT cannot_downgrade_with_unified_im_data CHECK (unified_rows = 0)
);
INSERT INTO im_unified_downgrade_guard
SELECT (SELECT COUNT(*) FROM im_user_bindings WHERE binding_scope = 'channel')
     + (SELECT COUNT(*) FROM im_channels WHERE binding_mode = 'unified');
DROP TABLE im_unified_downgrade_guard;

DROP INDEX IF EXISTS idx_im_user_bindings_channel_active;
DROP INDEX IF EXISTS idx_im_user_bindings_external_active;
DROP INDEX IF EXISTS idx_im_user_bindings_user_active;
ALTER TABLE im_user_bindings DROP COLUMN binding_scope;
ALTER TABLE im_channels DROP COLUMN binding_mode;
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, chat_type, external_user_id)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX idx_im_user_bindings_user_active
    ON im_user_bindings(im_channel_id, user_id)
    WHERE revoked_at IS NULL AND chat_type = 'direct';
