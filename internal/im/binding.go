package im

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	bindingCodePrefix = "WK-"
	bindingCodeBytes  = 10
	bindingCodeTTL    = 5 * time.Minute
)

var (
	// ErrBindingCodeInvalid means a binding code is malformed, expired, or already used.
	ErrBindingCodeInvalid = errors.New("invalid or expired binding code")
	// ErrBindingIdentityInUse prevents one IM identity from belonging to multiple accounts.
	ErrBindingIdentityInUse = errors.New("IM identity is already bound to another account")
	// ErrBindingUserInUse prevents one account from binding multiple identities on the same bot.
	ErrBindingUserInUse = errors.New("account is already bound to another identity for this bot")
	// ErrBindingUnavailable means the selected bot cannot accept personal bindings.
	ErrBindingUnavailable = errors.New("IM channel is unavailable for personal binding")
	// ErrBindingMembershipInvalid means the user can no longer access the selected workspace.
	ErrBindingMembershipInvalid = errors.New("workspace membership is unavailable")
)

// BindingRequestResult is safe to return to the authenticated user who
// initiated a request. Code is populated only by CreateBindingRequest.
type BindingRequestResult struct {
	ID          string    `json:"id"`
	Code        string    `json:"code,omitempty"`
	IMChannelID string    `json:"im_channel_id"`
	Platform    string    `json:"platform"`
	ChannelName string    `json:"channel_name"`
	TenantID    uint64    `json:"tenant_id"`
	Status      string    `json:"status"`
	BindingID   *string   `json:"binding_id,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// UserBindingView is the authenticated user's projection of an active binding.
type UserBindingView struct {
	ID               string    `json:"id"`
	IMChannelID      string    `json:"im_channel_id"`
	Platform         string    `json:"platform"`
	ChannelName      string    `json:"channel_name"`
	ExternalUserID   string    `json:"external_user_id"`
	ExternalUserName string    `json:"external_user_name"`
	TenantID         uint64    `json:"tenant_id"`
	TenantName       string    `json:"tenant_name"`
	CreatedAt        time.Time `json:"created_at"`
}

// BindableChannel is the public, credential-free projection shown to signed-in
// users on the binding page.
type BindableChannel struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
}

func generateBindingCode() (string, error) {
	raw := make([]byte, bindingCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate binding code: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return bindingCodePrefix + encoded, nil
}

func normalizeBindingCode(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !strings.HasPrefix(code, bindingCodePrefix) || len(code) != len(bindingCodePrefix)+16 {
		return "", false
	}
	for _, r := range strings.TrimPrefix(code, bindingCodePrefix) {
		if (r < 'A' || r > 'Z') && (r < '2' || r > '7') {
			return "", false
		}
	}
	return code, true
}

func hashBindingCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// IsBindingCode reports whether a private text message is reserved for the
// binding flow. Malformed WK-prefixed values are still consumed by that flow so
// they can never reach the model as ordinary prompts.
func IsBindingCode(content string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(content)), bindingCodePrefix)
}

func (s *Service) validateBindingAccess(
	ctx context.Context,
	userID string,
	tenantID uint64,
) (*types.TenantMember, error) {
	if s.userService == nil || s.memberService == nil || s.tenantService == nil {
		return nil, ErrBindingMembershipInvalid
	}
	user, err := s.userService.GetUserByID(ctx, userID)
	if err != nil || user == nil || !user.IsActive {
		return nil, ErrBindingMembershipInvalid
	}
	member, err := s.memberService.GetMembership(ctx, userID, tenantID)
	if err != nil || member == nil || member.Status != types.TenantMemberStatusActive {
		return nil, ErrBindingMembershipInvalid
	}
	tenant, err := s.tenantService.GetTenantByID(ctx, tenantID)
	if err != nil || tenant == nil || !strings.EqualFold(strings.TrimSpace(tenant.Status), "active") {
		return nil, ErrBindingMembershipInvalid
	}
	return member, nil
}

func (s *Service) getBindableChannel(channelID string) (*IMChannel, error) {
	channel, err := s.GetChannelByID(channelID)
	if err != nil {
		return nil, err
	}
	if !channel.Enabled || !SupportsPersonalBinding(channel.Platform) {
		return nil, ErrBindingUnavailable
	}
	return channel, nil
}

// ListBindableChannels lists enabled system bot installations that have passed
// the product capability gate for personal private-chat binding.
func (s *Service) ListBindableChannels() ([]BindableChannel, error) {
	platforms := []string{
		string(PlatformWeCom), string(PlatformFeishu), string(PlatformLark),
		string(PlatformSlack), string(PlatformTelegram), string(PlatformDingtalk),
	}
	var channels []IMChannel
	if err := s.db.Where("enabled = ? AND platform IN ? AND deleted_at IS NULL", true, platforms).
		Order("platform ASC, name ASC, created_at ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	result := make([]BindableChannel, 0, len(channels))
	for _, channel := range channels {
		result = append(result, BindableChannel{ID: channel.ID, Platform: channel.Platform, Name: channel.Name})
	}
	return result, nil
}

// CreateBindingRequest creates one short-lived code and cancels older pending
// codes for the same user and bot installation.
func (s *Service) CreateBindingRequest(
	ctx context.Context,
	userID, channelID string,
	tenantID uint64,
) (*BindingRequestResult, error) {
	if _, err := s.validateBindingAccess(ctx, userID, tenantID); err != nil {
		return nil, err
	}
	channel, err := s.getBindableChannel(channelID)
	if err != nil {
		return nil, err
	}
	code, err := generateBindingCode()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	request := &BindingRequest{
		ID:          uuid.New().String(),
		CodeHash:    hashBindingCode(code),
		IMChannelID: channel.ID,
		UserID:      userID,
		TenantID:    tenantID,
		Status:      IMBindingRequestPending,
		ExpiresAt:   now.Add(bindingCodeTTL),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var user types.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			Where("id = ?", userID).
			First(&user).Error; err != nil {
			return err
		}
		if err := tx.Model(&BindingRequest{}).
			Where("user_id = ? AND im_channel_id = ? AND status = ?", userID, channel.ID, IMBindingRequestPending).
			Updates(map[string]any{
				"status":       IMBindingRequestCancelled,
				"cancelled_at": now,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		return tx.Create(request).Error
	})
	if err != nil {
		return nil, fmt.Errorf("create binding request: %w", err)
	}
	return bindingRequestResult(request, channel, code), nil
}

// GetBindingRequest returns only a request owned by the current web user.
func (s *Service) GetBindingRequest(userID, requestID string) (*BindingRequestResult, error) {
	var request BindingRequest
	if err := s.db.Where("id = ? AND user_id = ?", requestID, userID).First(&request).Error; err != nil {
		return nil, err
	}
	var channel IMChannel
	if err := s.db.Unscoped().Where("id = ?", request.IMChannelID).First(&channel).Error; err != nil {
		return nil, err
	}
	return bindingRequestResult(&request, &channel, ""), nil
}

func bindingRequestResult(request *BindingRequest, channel *IMChannel, code string) *BindingRequestResult {
	status := request.Status
	if status == IMBindingRequestPending && !request.ExpiresAt.After(time.Now()) {
		status = "expired"
	}
	return &BindingRequestResult{
		ID:          request.ID,
		Code:        code,
		IMChannelID: request.IMChannelID,
		Platform:    channel.Platform,
		ChannelName: channel.Name,
		TenantID:    request.TenantID,
		Status:      status,
		BindingID:   request.BindingID,
		ExpiresAt:   request.ExpiresAt,
	}
}

// ConsumeBindingCode atomically establishes a binding for the verified sender
// of a private IM message.
func (s *Service) ConsumeBindingCode(
	ctx context.Context,
	channelID, externalUserID, externalUserName, code string,
) (*UserBinding, error) {
	code, ok := normalizeBindingCode(code)
	if !ok || strings.TrimSpace(externalUserID) == "" {
		return nil, ErrBindingCodeInvalid
	}
	channel, err := s.getBindableChannel(channelID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var binding *UserBinding
	var replacedBindingID string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var request BindingRequest
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("code_hash = ? AND im_channel_id = ? AND status = ? AND expires_at > ?",
				hashBindingCode(code), channel.ID, IMBindingRequestPending, now).
			First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBindingCodeInvalid
			}
			return err
		}
		if _, err := s.validateBindingAccess(ctx, request.UserID, request.TenantID); err != nil {
			return err
		}

		var current UserBinding
		err := tx.Where("im_channel_id = ? AND external_user_id = ? AND revoked_at IS NULL",
			channel.ID, externalUserID).First(&current).Error
		if err == nil {
			if current.UserID != request.UserID {
				return ErrBindingIdentityInUse
			}
			replacedBindingID = current.ID
			if err := revokeBindingInTx(tx, &current, now); err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var userBinding UserBinding
		err = tx.Where("im_channel_id = ? AND user_id = ? AND revoked_at IS NULL",
			channel.ID, request.UserID).First(&userBinding).Error
		if err == nil {
			return ErrBindingUserInUse
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		binding = &UserBinding{
			ID:               uuid.New().String(),
			IMChannelID:      channel.ID,
			ExternalUserID:   strings.TrimSpace(externalUserID),
			ExternalUserName: strings.TrimSpace(externalUserName),
			UserID:           request.UserID,
			TenantID:         request.TenantID,
			Status:           IMBindingStatusActive,
		}
		if err := tx.Create(binding).Error; err != nil {
			return err
		}
		result := tx.Model(&BindingRequest{}).
			Where("id = ? AND status = ?", request.ID, IMBindingRequestPending).
			Updates(map[string]any{
				"status":                   IMBindingRequestConsumed,
				"claimed_external_user_id": binding.ExternalUserID,
				"binding_id":               binding.ID,
				"consumed_at":              now,
				"updated_at":               now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrBindingCodeInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if replacedBindingID != "" {
		s.cancelBindingRequests(replacedBindingID)
	}
	return binding, nil
}

func revokeBindingInTx(tx *gorm.DB, binding *UserBinding, now time.Time) error {
	updates := map[string]any{
		"status": IMBindingStatusRevoked, "revoked_at": now, "updated_at": now,
	}
	if err := tx.Model(&UserBinding{}).Where("id = ? AND revoked_at IS NULL", binding.ID).
		Updates(updates).Error; err != nil {
		return err
	}
	return tx.Model(&ChannelSession{}).Where("binding_id = ? AND deleted_at IS NULL", binding.ID).
		Update("deleted_at", now).Error
}

func revokeChannelBindingsInTx(tx *gorm.DB, channelID string, now time.Time) ([]string, error) {
	var bindingIDs []string
	if err := tx.Model(&UserBinding{}).
		Where("im_channel_id = ? AND revoked_at IS NULL", channelID).
		Pluck("id", &bindingIDs).Error; err != nil {
		return nil, err
	}
	if len(bindingIDs) == 0 {
		return nil, nil
	}
	updates := map[string]any{
		"status": IMBindingStatusRevoked, "revoked_at": now, "updated_at": now,
	}
	if err := tx.Model(&UserBinding{}).
		Where("id IN ?", bindingIDs).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&ChannelSession{}).
		Where("binding_id IN ? AND deleted_at IS NULL", bindingIDs).
		Update("deleted_at", now).Error; err != nil {
		return nil, err
	}
	return bindingIDs, nil
}

// ListUserBindings returns active bindings owned by one web user.
func (s *Service) ListUserBindings(userID string) ([]UserBindingView, error) {
	var rows []UserBindingView
	err := s.db.Table("im_user_bindings AS b").
		Select(`b.id, b.im_channel_id, c.platform, c.name AS channel_name,
                b.external_user_id, b.external_user_name, b.tenant_id,
                t.name AS tenant_name, b.created_at`).
		Joins("JOIN im_channels AS c ON c.id = b.im_channel_id").
		Joins("JOIN tenants AS t ON t.id = b.tenant_id").
		Where("b.user_id = ? AND b.revoked_at IS NULL AND c.deleted_at IS NULL", userID).
		Order("b.created_at DESC").Scan(&rows).Error
	return rows, err
}

// RevokeUserBinding revokes only a binding owned by the authenticated user.
func (s *Service) RevokeUserBinding(userID, bindingID string) error {
	now := time.Now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var binding UserBinding
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND revoked_at IS NULL", bindingID, userID).
			First(&binding).Error; err != nil {
			return err
		}
		return revokeBindingInTx(tx, &binding, now)
	})
	if err == nil {
		s.cancelBindingRequests(bindingID)
	}
	return err
}

// ResolveActiveBinding loads and validates the current live authority for an
// incoming verified platform identity.
func (s *Service) ResolveActiveBinding(
	ctx context.Context,
	channelID, externalUserID string,
) (*UserBinding, *types.TenantMember, error) {
	var binding UserBinding
	if err := s.db.Where(
		"im_channel_id = ? AND external_user_id = ? AND status = ? AND revoked_at IS NULL",
		channelID, externalUserID, IMBindingStatusActive,
	).First(&binding).Error; err != nil {
		return nil, nil, err
	}
	member, err := s.validateBindingAccess(ctx, binding.UserID, binding.TenantID)
	if err != nil {
		return nil, nil, err
	}
	return &binding, member, nil
}

// ValidateActiveBinding reloads a binding and its live account/workspace
// authority. Workers call it before work and before delivery so revocation or
// membership changes take effect without waiting for a new IM message.
func (s *Service) ValidateActiveBinding(
	ctx context.Context,
	bindingID string,
) (*UserBinding, *types.TenantMember, error) {
	var binding UserBinding
	if err := s.db.Table("im_user_bindings AS b").
		Joins("JOIN im_channels AS c ON c.id = b.im_channel_id").
		Where(
			"b.id = ? AND b.status = ? AND b.revoked_at IS NULL AND c.enabled = ? AND c.deleted_at IS NULL",
			bindingID, IMBindingStatusActive, true,
		).Select("b.*").Scan(&binding).Error; err != nil {
		return nil, nil, err
	}
	if binding.ID == "" {
		return nil, nil, gorm.ErrRecordNotFound
	}
	member, err := s.validateBindingAccess(ctx, binding.UserID, binding.TenantID)
	if err != nil {
		return nil, nil, err
	}
	return &binding, member, nil
}
