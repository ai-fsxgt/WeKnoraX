package im

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// unifiedSessionContextKey keeps direct conversations separate from every
// group, participant and thread. JSON framing avoids ambiguous ID separators;
// hashing keeps arbitrary platform IDs within the context_key column limit.
func unifiedSessionContextKey(msg *IncomingMessage) string {
	parts := []string{string(msg.ChatType), strings.TrimSpace(msg.UserID)}
	if msg.ChatType == ChatTypeGroup {
		parts = append(parts, strings.TrimSpace(msg.ChatID), strings.TrimSpace(msg.ThreadID))
	}
	encoded, _ := json.Marshal(parts) // A slice of strings is always JSON-serializable.
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func withUnifiedParticipant(ctx context.Context, binding *UserBinding, msg *IncomingMessage) context.Context {
	principal := types.Principal{
		Type: types.PrincipalIMParticipant,
		ID:   binding.ID + ":" + unifiedSessionContextKey(msg),
	}
	ctx = types.WithPrincipal(ctx, principal)
	ctx = context.WithValue(ctx, types.UserIDContextKey, principal.StorageID())
	return context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleViewer)
}

func unifiedSessionMetadata(msg *IncomingMessage) types.JSON {
	metadata, _ := json.Marshal(map[string]string{
		"user_id":   strings.TrimSpace(msg.UserID),
		"user_name": strings.TrimSpace(msg.UserName),
		"chat_type": string(msg.ChatType),
		"chat_id":   strings.TrimSpace(msg.ChatID),
		"thread_id": strings.TrimSpace(msg.ThreadID),
	})
	return types.JSON(metadata)
}

// validateQABinding also rejects a unified turn whose conversation was cleared
// while it was queued or generating. The stored messages remain history only.
func (s *Service) validateQABinding(ctx context.Context, bindingID, sessionID string) error {
	binding, _, err := s.ValidateActiveBinding(ctx, bindingID)
	if err != nil || binding.BindingScope != BindingScopeChannel {
		return err
	}
	var count int64
	if err := s.db.WithContext(ctx).Model(&ChannelSession{}).
		Where("binding_id = ? AND session_id = ?", bindingID, sessionID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// A cutoff cancels all already-queued turns without consuming the next message
// sent after /stop or /clear. Keep it until expiry for workers on other instances.
func (s *Service) unifiedRequestStopped(ctx context.Context, req *qaRequest) (bool, error) {
	if s.redis == nil {
		return false, nil
	}
	cutoff, err := s.redis.Get(ctx, RedisKeyStop+req.userKey).Int64()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return req.enqueuedAt.UnixNano() <= cutoff, nil
}
