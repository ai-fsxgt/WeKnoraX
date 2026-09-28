package handler

import (
	"strings"
	"testing"
)

// The system channel endpoint intentionally omits platforms that are not part
// of the per-user IM architecture.
func TestValidIMPlatforms_CoversLark(t *testing.T) {
	want := []string{
		"wecom", "feishu", "lark", "slack", "telegram",
		"dingtalk", "wechat", "qqbot",
	}
	for _, platform := range want {
		if !validIMPlatforms[platform] {
			t.Errorf("platform %q is not accepted", platform)
		}
	}
	if validIMPlatforms["nonsense"] {
		t.Error("unknown platform is accepted")
	}
	if validIMPlatforms["mattermost"] || validIMPlatforms["yunzhijia"] {
		t.Error("excluded platform is accepted")
	}
}

// The 400 message is derived from validIMPlatforms; it must not drift as
// platforms are added.
func TestInvalidIMPlatformError_ListsEveryPlatform(t *testing.T) {
	for platform := range validIMPlatforms {
		if !strings.Contains(invalidIMPlatformError, "'"+platform+"'") {
			t.Errorf("error message omits %q: %s", platform, invalidIMPlatformError)
		}
	}
}
