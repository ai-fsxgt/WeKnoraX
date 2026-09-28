package im

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IMChannel represents one system-managed bot installation. User and workspace
// access are resolved from UserBinding for every incoming message.
type IMChannel struct {
	ID         string `json:"id"           gorm:"type:varchar(36);primaryKey;default:uuid_generate_v4()"`
	Platform   string `json:"platform"     gorm:"type:varchar(20);not null"`
	Name       string `json:"name"         gorm:"type:varchar(255);not null;default:''"`
	Enabled    bool   `json:"enabled"      gorm:"not null;default:false"`
	Mode       string `json:"mode"         gorm:"type:varchar(20);not null;default:'websocket'"`
	OutputMode string `json:"output_mode"  gorm:"type:varchar(20);not null;default:'stream'"`
	// The partial unique index predicate is intentionally kept beside the field it protects.
	//nolint:lll
	BotIdentity string         `json:"bot_identity" gorm:"type:varchar(255);not null;default:'';uniqueIndex:idx_im_channels_bot_identity,where:deleted_at IS NULL AND bot_identity != ''"`
	Credentials types.JSON     `json:"credentials"  gorm:"type:jsonb;not null;default:'{}'"`
	CreatedBy   string         `json:"created_by"   gorm:"type:varchar(36);not null;default:''"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

func (IMChannel) TableName() string {
	return "im_channels"
}

// IMChannelSummary is the HTTP-safe list shape for IM channels. Credentials
// are never included in list or mutation responses.
type IMChannelSummary struct {
	ID                    string    `json:"id"`
	Platform              string    `json:"platform"`
	Name                  string    `json:"name"`
	Enabled               bool      `json:"enabled"`
	Mode                  string    `json:"mode"`
	OutputMode            string    `json:"output_mode"`
	BotIdentity           string    `json:"bot_identity"`
	PersonalBinding       bool      `json:"personal_binding"`
	CredentialsConfigured bool      `json:"credentials_configured"`
	CreatedBy             string    `json:"created_by"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// SummarizeIMChannel converts a stored channel into its list response shape.
func SummarizeIMChannel(ch IMChannel) IMChannelSummary {
	return IMChannelSummary{
		ID:                    ch.ID,
		Platform:              ch.Platform,
		Name:                  ch.Name,
		Enabled:               ch.Enabled,
		Mode:                  ch.Mode,
		OutputMode:            ch.OutputMode,
		BotIdentity:           ch.BotIdentity,
		PersonalBinding:       SupportsPersonalBinding(ch.Platform),
		CredentialsConfigured: imCredentialsConfigured(ch.Credentials),
		CreatedBy:             ch.CreatedBy,
		CreatedAt:             ch.CreatedAt,
		UpdatedAt:             ch.UpdatedAt,
	}
}

// SummarizeIMChannels converts stored channels into list response shapes.
func SummarizeIMChannels(channels []IMChannel) []IMChannelSummary {
	out := make([]IMChannelSummary, 0, len(channels))
	for _, ch := range channels {
		out = append(out, SummarizeIMChannel(ch))
	}
	return out
}

func imCredentialsConfigured(cred types.JSON) bool {
	s := strings.TrimSpace(string(cred))
	return s != "" && s != "{}"
}

// SupportsPersonalBinding reports whether a platform is allowed to enter the
// shared per-user binding flow. QQ and WeChat stay closed until their required
// multi-user private-chat checks have passed. Mattermost and Yunzhijia are not
// part of this adaptation.
func SupportsPersonalBinding(platform string) bool {
	switch platform {
	case string(PlatformWeCom), string(PlatformFeishu), string(PlatformLark),
		string(PlatformSlack), string(PlatformTelegram), string(PlatformDingtalk):
		return true
	default:
		return false
	}
}

func (ch *IMChannel) BeforeCreate(tx *gorm.DB) error {
	if ch.ID == "" {
		ch.ID = uuid.New().String()
	}
	if ch.Mode == "" {
		if ch.Platform == "mattermost" || ch.Platform == "yunzhijia" {
			ch.Mode = "webhook"
		} else {
			ch.Mode = "websocket"
		}
	}
	if ch.OutputMode == "" {
		ch.OutputMode = "stream"
	}
	ch.BotIdentity = ch.computeBotIdentity()
	return nil
}

// BeforeSave ensures bot_identity is recomputed on every save.
func (ch *IMChannel) BeforeSave(tx *gorm.DB) error {
	ch.BotIdentity = ch.computeBotIdentity()
	return nil
}

// computeBotIdentity derives a unique bot identity string from the channel's
// platform, mode, and credentials. Returns "" if no identity can be extracted.
func (ch *IMChannel) computeBotIdentity() string {
	creds := make(map[string]interface{})
	if err := json.Unmarshal([]byte(ch.Credentials), &creds); err != nil {
		return ""
	}

	str := func(key string) string {
		if v, ok := creds[key]; ok {
			switch val := v.(type) {
			case string:
				return val
			case float64:
				return fmt.Sprintf("%.0f", val)
			}
		}
		return ""
	}

	switch ch.Platform {
	case "wecom":
		switch ch.Mode {
		case "websocket":
			if botID := str("bot_id"); botID != "" {
				return "wecom:ws:" + botID
			}
		case "webhook":
			corpID := str("corp_id")
			agentID := str("corp_agent_id")
			if corpID != "" && agentID != "" {
				return "wecom:wh:" + corpID + ":" + agentID
			}
		}
	// Feishu and Lark app_ids live in separate clouds and never collide, so the
	// platform prefix keeps the same app_id on both from looking like one bot.
	case "feishu", "lark":
		if appID := str("app_id"); appID != "" {
			return ch.Platform + ":" + appID
		}
	case "telegram":
		if botToken := str("bot_token"); botToken != "" {
			// Use the bot ID part (before the colon) as identity.
			if idx := strings.Index(botToken, ":"); idx > 0 {
				return "telegram:" + botToken[:idx]
			}
			return "telegram:" + botToken
		}
	case "dingtalk":
		if clientID := str("client_id"); clientID != "" {
			return "dingtalk:" + clientID
		}
	case "slack":
		if botToken := str("bot_token"); botToken != "" {
			return fmt.Sprintf("slack:%x", sha256.Sum256([]byte(botToken)))
		}
	case "mattermost":
		if tok := str("outgoing_token"); tok != "" {
			return "mattermost:wh:" + tok
		}
	case "wechat":
		if botID := str("ilink_bot_id"); botID != "" {
			return "wechat:" + botID
		}
	case "qqbot":
		if appID := str("app_id"); appID != "" {
			return "qqbot:" + appID
		}
	case "yunzhijia":
		if sendMsgURL := str("send_msg_url"); sendMsgURL != "" {
			parsed, err := url.Parse(sendMsgURL)
			if err != nil {
				return ""
			}
			if token := strings.TrimSpace(parsed.Query().Get("yzjtoken")); token != "" {
				return fmt.Sprintf("yunzhijia:%x", sha256.Sum256([]byte(token)))
			}
		}
	}
	return ""
}

const (
	// IMBindingStatusActive identifies a usable account binding.
	IMBindingStatusActive = "active"
	// IMBindingStatusRevoked identifies a binding that can no longer be used.
	IMBindingStatusRevoked = "revoked"

	// IMBindingRequestPending identifies a binding code that can still be consumed.
	IMBindingRequestPending = "pending"
	// IMBindingRequestConsumed identifies a binding code that completed successfully.
	IMBindingRequestConsumed = "consumed"
	// IMBindingRequestCancelled identifies a binding code replaced before use.
	IMBindingRequestCancelled = "cancelled"
)

// UserBinding maps one verified platform identity to a WeKnora user and an
// explicitly selected workspace.
type UserBinding struct {
	ID               string     `json:"id"               gorm:"type:varchar(36);primaryKey"`
	IMChannelID      string     `json:"im_channel_id"    gorm:"type:varchar(36);not null;index"`
	ExternalUserID   string     `json:"external_user_id" gorm:"type:varchar(255);not null"`
	ExternalUserName string     `json:"external_user_name" gorm:"type:varchar(255);not null;default:''"`
	UserID           string     `json:"user_id"          gorm:"type:varchar(36);not null;index"`
	TenantID         uint64     `json:"tenant_id"        gorm:"not null;index"`
	Status           string     `json:"status"           gorm:"type:varchar(20);not null;default:'active'"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty" gorm:"index"`
}

// TableName returns the binding table name.
func (UserBinding) TableName() string { return "im_user_bindings" }

// BeforeCreate assigns application defaults before inserting a binding.
func (b *UserBinding) BeforeCreate(_ *gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	if b.Status == "" {
		b.Status = IMBindingStatusActive
	}
	return nil
}

// BindingRequest is a short-lived, single-use proof initiated by an
// authenticated web user and completed by a private message to the bot.
type BindingRequest struct {
	ID          string `json:"id"                     gorm:"type:varchar(36);primaryKey"`
	CodeHash    string `json:"-"                      gorm:"type:varchar(64);not null;uniqueIndex"`
	IMChannelID string `json:"im_channel_id"         gorm:"type:varchar(36);not null;index"`
	UserID      string `json:"user_id"                gorm:"type:varchar(36);not null;index"`
	TenantID    uint64 `json:"tenant_id"              gorm:"not null;index"`
	Status      string `json:"status"                 gorm:"type:varchar(20);not null;default:'pending'"`
	//nolint:lll // JSON and database names intentionally mirror the external identity column.
	ClaimedExternalUserID string     `json:"claimed_external_user_id,omitempty" gorm:"type:varchar(255);not null;default:''"`
	BindingID             *string    `json:"binding_id,omitempty"    gorm:"type:varchar(36)"`
	ExpiresAt             time.Time  `json:"expires_at"`
	ConsumedAt            *time.Time `json:"consumed_at,omitempty"`
	CancelledAt           *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// TableName returns the binding request table name.
func (BindingRequest) TableName() string { return "im_binding_requests" }

// BeforeCreate assigns application defaults before inserting a request.
func (r *BindingRequest) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if r.Status == "" {
		r.Status = IMBindingRequestPending
	}
	return nil
}

// ChannelSession maps one binding to its current WeKnora conversation.
type ChannelSession struct {
	ID          string         `json:"id"            gorm:"type:varchar(36);primaryKey;default:uuid_generate_v4()"`
	BindingID   string         `json:"binding_id"    gorm:"type:varchar(36);not null;index"`
	SessionID   string         `json:"session_id"    gorm:"type:varchar(36);not null;index"`
	TenantID    uint64         `json:"tenant_id"     gorm:"not null;index"`
	IMChannelID string         `json:"im_channel_id" gorm:"type:varchar(36);not null;index"`
	Status      string         `json:"status"        gorm:"type:varchar(20);not null;default:'active'"`
	Metadata    types.JSON     `json:"metadata"      gorm:"type:jsonb;default:'{}'"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"deleted_at"    gorm:"index"`
}

func (ChannelSession) TableName() string {
	return "im_channel_sessions"
}

func (cs *ChannelSession) BeforeCreate(tx *gorm.DB) error {
	if cs.ID == "" {
		cs.ID = uuid.New().String()
	}
	if cs.Status == "" {
		cs.Status = "active"
	}
	return nil
}
