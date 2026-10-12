package im

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrChannelAlreadyBound prevents replacing a unified bot's authority with another code.
var ErrChannelAlreadyBound = errors.New("unified bot is already bound; revoke its binding first")

// ErrChannelModeInUse prevents reinterpreting existing bindings in a different mode.
var ErrChannelModeInUse = errors.New("revoke all bot bindings before changing binding mode")

func channelHasUnifiedBinding(tx *gorm.DB, channelID string) (bool, error) {
	var count int64
	err := tx.Model(&UserBinding{}).
		Where("im_channel_id = ? AND binding_scope = ? AND revoked_at IS NULL", channelID, BindingScopeChannel).
		Count(&count).Error
	return count > 0, err
}

// createUnifiedBindingInTx runs under the channel lock. The partial unique index
// also enforces a single active grant at the database boundary.
func createUnifiedBindingInTx(
	tx *gorm.DB,
	request *BindingRequest,
	chatType ChatType,
	externalID, externalName string,
) (*UserBinding, error) {
	binding := &UserBinding{
		IMChannelID:      request.IMChannelID,
		ExternalUserID:   strings.TrimSpace(externalID),
		ExternalUserName: strings.TrimSpace(externalName),
		ChatType:         chatType,
		BindingScope:     BindingScopeChannel,
		UserID:           request.UserID,
		TenantID:         request.TenantID,
		Status:           IMBindingStatusActive,
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(binding)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrChannelAlreadyBound
	}
	return binding, nil
}

func cancelChannelBindingCodes(tx *gorm.DB, channelID string, now time.Time) error {
	return tx.Model(&BindingRequest{}).
		Where("im_channel_id = ? AND status = ?", channelID, IMBindingRequestPending).
		Updates(map[string]any{
			"status":       IMBindingRequestCancelled,
			"cancelled_at": now,
			"updated_at":   now,
		}).Error
}
