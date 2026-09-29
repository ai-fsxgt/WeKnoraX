package im

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

func TestSupportsPersonalBinding(t *testing.T) {
	tests := []struct {
		platform string
		want     bool
	}{
		{"wecom", true},
		{"feishu", true},
		{"lark", true},
		{"slack", true},
		{"telegram", true},
		{"dingtalk", true},
		{"qqbot", false},
		{"wechat", false},
		{"mattermost", false},
		{"yunzhijia", false},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			if got := SupportsPersonalBinding(tt.platform); got != tt.want {
				t.Errorf("SupportsPersonalBinding(%q) = %v, want %v", tt.platform, got, tt.want)
			}
		})
	}
}

func TestIMChannelBeforeCreate_SystemDefaults(t *testing.T) {
	ch := &IMChannel{
		Platform:    "slack",
		Credentials: []byte(`{"bot_token":"token"}`),
	}
	if err := ch.BeforeCreate(&gorm.DB{}); err != nil {
		t.Fatalf("BeforeCreate error: %v", err)
	}
	if ch.Mode != "websocket" {
		t.Errorf("Mode = %q, want websocket", ch.Mode)
	}
	if ch.OutputMode != "stream" {
		t.Errorf("OutputMode = %q, want stream", ch.OutputMode)
	}
}

// BotIdentity carries a unique index, so Feishu and Lark channels must never
// derive the same identity — otherwise a Lark bot would be rejected as a
// duplicate of an unrelated Feishu bot that happens to share an app_id.
func TestComputeBotIdentity_FeishuAndLarkAreDistinct(t *testing.T) {
	const creds = `{"app_id":"cli_a1b2c3","app_secret":"s"}`

	feishu := &IMChannel{Platform: "feishu", Credentials: []byte(creds)}
	lark := &IMChannel{Platform: "lark", Credentials: []byte(creds)}

	feishuID := feishu.computeBotIdentity()
	larkID := lark.computeBotIdentity()

	if feishuID != "feishu:cli_a1b2c3" {
		t.Errorf("feishu identity = %q, want %q", feishuID, "feishu:cli_a1b2c3")
	}
	if larkID != "lark:cli_a1b2c3" {
		t.Errorf("lark identity = %q, want %q", larkID, "lark:cli_a1b2c3")
	}
	if feishuID == larkID {
		t.Errorf("feishu and lark share identity %q", feishuID)
	}
}

func TestComputeBotIdentity_LarkWithoutAppID(t *testing.T) {
	ch := &IMChannel{Platform: "lark", Credentials: []byte(`{"app_secret":"s"}`)}
	if got := ch.computeBotIdentity(); got != "" {
		t.Errorf("identity = %q, want empty when app_id is missing", got)
	}
}

func TestIMChannelComputeBotIdentity_YunzhijiaUsesYZJToken(t *testing.T) {
	makeChannel := func(sendMsgURL string) *IMChannel {
		return &IMChannel{
			Platform: "yunzhijia",
			Credentials: []byte(fmt.Sprintf(
				`{"send_msg_url":%q}`,
				sendMsgURL,
			)),
		}
	}

	first := makeChannel("https://open.yunzhijia.com/gateway/robot/webhook/send?yzjtoken=token-a&foo=1")
	second := makeChannel("https://open.yunzhijia.com/gateway/robot/webhook/send?yzjtoken=token-b&foo=1")
	firstWant := fmt.Sprintf("yunzhijia:%x", sha256.Sum256([]byte("token-a")))
	secondWant := fmt.Sprintf("yunzhijia:%x", sha256.Sum256([]byte("token-b")))
	if first.computeBotIdentity() != firstWant {
		t.Errorf("first identity = %q, want %q", first.computeBotIdentity(), firstWant)
	}
	if second.computeBotIdentity() != secondWant {
		t.Errorf("second identity = %q, want %q", second.computeBotIdentity(), secondWant)
	}
	if first.computeBotIdentity() == second.computeBotIdentity() {
		t.Error("different yzjtoken values must produce different Yunzhijia bot identities")
	}

	sameTokenDifferentQuery := makeChannel(
		"https://open.yunzhijia.com/gateway/robot/webhook/send?foo=2&yzjtoken=token-a",
	)
	if sameTokenDifferentQuery.computeBotIdentity() != first.computeBotIdentity() {
		t.Error("same yzjtoken must produce the same Yunzhijia bot identity")
	}

	missingToken := makeChannel("https://open.yunzhijia.com/gateway/robot/webhook/send?foo=1")
	if missingToken.computeBotIdentity() != "" {
		t.Errorf("missing yzjtoken identity = %q, want empty", missingToken.computeBotIdentity())
	}
}

func TestBindingModelsBeforeCreate(t *testing.T) {
	binding := &UserBinding{}
	if err := binding.BeforeCreate(&gorm.DB{}); err != nil {
		t.Fatalf("binding BeforeCreate error: %v", err)
	}
	if binding.ID == "" || binding.Status != IMBindingStatusActive || binding.ChatType != ChatTypeDirect {
		t.Fatalf("binding defaults not applied: %#v", binding)
	}

	request := &BindingRequest{}
	if err := request.BeforeCreate(&gorm.DB{}); err != nil {
		t.Fatalf("request BeforeCreate error: %v", err)
	}
	if request.ID == "" || request.Status != IMBindingRequestPending {
		t.Fatalf("request defaults not applied: %#v", request)
	}
}
