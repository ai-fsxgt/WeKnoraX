package im

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestSameChannelRuntimeConfigUsesOnlyRuntimeFields(t *testing.T) {
	left := &IMChannel{
		ID: "im-1", Platform: "slack", Enabled: true, Mode: "websocket",
		OutputMode: "stream", Name: "first", CreatedBy: "user-1",
		Credentials: types.JSON(`{"token":"same"}`),
	}
	right := *left
	right.Name = "renamed"
	right.CreatedBy = "user-2"
	if !sameChannelRuntimeConfig(left, &right) {
		t.Fatal("presentation-only fields should not rebuild the channel runtime")
	}

	right.Credentials = types.JSON(`{"token":"changed"}`)
	if sameChannelRuntimeConfig(left, &right) {
		t.Fatal("credential changes must rebuild the channel runtime")
	}
}
