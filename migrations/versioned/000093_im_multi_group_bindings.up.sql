-- Allow one account to bind multiple groups while keeping direct chat unique.
DROP INDEX IF EXISTS idx_im_user_bindings_user_active;
CREATE UNIQUE INDEX idx_im_user_bindings_user_active
    ON im_user_bindings(im_channel_id, user_id)
    WHERE revoked_at IS NULL AND chat_type = 'direct';
