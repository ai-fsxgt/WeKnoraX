package handler

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
)

// The system channel endpoint intentionally omits platforms that are not part
// of the new IM architecture.
func TestSupportedSystemPlatforms_CoversLark(t *testing.T) {
	want := []string{
		"wecom", "feishu", "lark", "slack", "telegram",
		"dingtalk", "wechat", "qqbot",
	}
	for _, platform := range want {
		if !im.SupportsSystemChannel(platform) {
			t.Errorf("platform %q is not accepted", platform)
		}
	}
	if im.SupportsSystemChannel("nonsense") {
		t.Error("unknown platform is accepted")
	}
	if im.SupportsSystemChannel("mattermost") || im.SupportsSystemChannel("yunzhijia") {
		t.Error("excluded platform is accepted")
	}
}

// The 400 message must not drift from the supported platform list.
func TestInvalidIMPlatformError_ListsEveryPlatform(t *testing.T) {
	platforms := []string{
		"wecom", "feishu", "lark", "slack", "telegram",
		"dingtalk", "wechat", "qqbot",
	}
	for _, platform := range platforms {
		if !strings.Contains(invalidIMPlatformError, "'"+platform+"'") {
			t.Errorf("error message omits %q: %s", platform, invalidIMPlatformError)
		}
	}
}
