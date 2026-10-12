-- Do not reinterpret unified grants or their histories as individual bindings.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM im_user_bindings WHERE binding_scope = 'channel')
       OR EXISTS (SELECT 1 FROM im_channels WHERE binding_mode = 'unified') THEN
        RAISE EXCEPTION 'Cannot downgrade while unified IM channels or binding history exist';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_im_user_bindings_channel_active;
DROP INDEX IF EXISTS idx_im_user_bindings_external_active;
DROP INDEX IF EXISTS idx_im_user_bindings_user_active;
ALTER TABLE im_user_bindings DROP COLUMN IF EXISTS binding_scope;
ALTER TABLE im_channels DROP COLUMN IF EXISTS binding_mode;
CREATE UNIQUE INDEX idx_im_user_bindings_external_active
    ON im_user_bindings(im_channel_id, chat_type, external_user_id)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX idx_im_user_bindings_user_active
    ON im_user_bindings(im_channel_id, user_id)
    WHERE revoked_at IS NULL AND chat_type = 'direct';
