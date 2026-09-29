package im

import "testing"

func TestShortID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"shorter than 8", "abc", "abc"},
		{"exactly 8", "12345678", "12345678"},
		{"longer than 8 keeps suffix", "aaaaaaaaXXXXXXXX", "XXXXXXXX"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortID(tt.in); got != tt.want {
				t.Errorf("shortID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildBindingSessionTitle(t *testing.T) {
	tests := []struct {
		name string
		msg  *IncomingMessage
		want string
	}{
		{
			name: "direct message with username",
			msg: &IncomingMessage{
				Platform: "feishu",
				UserName: "李四",
				ChatType: ChatTypeDirect,
			},
			want: "李四 · dm",
		},
		{
			name: "no user identity at all",
			msg: &IncomingMessage{
				Platform: "slack",
				ChatType: ChatTypeDirect,
			},
			want: "user · dm",
		},
		{
			name: "group message includes member identity",
			msg: &IncomingMessage{
				Platform: "dingtalk",
				UserName: "张三",
				ChatType: ChatTypeGroup,
			},
			want: "张三 · group",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildBindingSessionTitle(tt.msg); got != tt.want {
				t.Errorf("buildBindingSessionTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIMInitialSessionTitle(t *testing.T) {
	tests := []struct {
		name string
		msg  *IncomingMessage
		want string
	}{
		{
			name: "text message starts untitled so it gets a content-based title later",
			msg: &IncomingMessage{
				Platform: "feishu",
				UserName: "李四",
				ChatType: ChatTypeDirect,
				Content:  "如何配置单点登录?",
			},
			want: "",
		},
		{
			name: "whitespace-only content falls back to the identity title",
			msg: &IncomingMessage{
				Platform: "feishu",
				UserName: "李四",
				ChatType: ChatTypeDirect,
				Content:  "   ",
			},
			want: "李四 · dm",
		},
		{
			name: "non-text message (no content) falls back to the identity title",
			msg: &IncomingMessage{
				Platform: "wecom",
				UserID:   "WeCom_ZhangSan",
				ChatType: ChatTypeDirect,
			},
			want: "user ZhangSan · dm",
		},
		{
			name: "group text keeps a member identity title",
			msg: &IncomingMessage{
				Platform: "dingtalk",
				UserName: "张三",
				UserID:   "user-1",
				ChatType: ChatTypeGroup,
				Content:  "当前有哪些假期？",
			},
			want: "张三 · group",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imInitialSessionTitle(tt.msg); got != tt.want {
				t.Errorf("imInitialSessionTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBindingContextKeys(t *testing.T) {
	direct := &IncomingMessage{ChatType: ChatTypeDirect, UserID: "user-1"}
	if got := bindingRequestKey("binding-1", direct); got != "binding-1" {
		t.Fatalf("direct request key = %q", got)
	}

	group := &IncomingMessage{ChatType: ChatTypeGroup, UserID: "user-1", ChatID: "group-1"}
	if got := bindingSessionContextKey(group); got != "user-1" {
		t.Fatalf("group context key = %q", got)
	}
	if got := bindingRequestKey("binding-1", group); got != "binding-1:user-1" {
		t.Fatalf("group request key = %q", got)
	}

	groupThread := &IncomingMessage{
		ChatType: ChatTypeGroup,
		UserID:   "user-1",
		ChatID:   "group-1",
		ThreadID: "thread-1",
	}
	if got := bindingSessionContextKey(groupThread); got != "user-1:thread-1" {
		t.Fatalf("group thread context key = %q", got)
	}
	if got := bindingRequestKey("binding-1", groupThread); got != "binding-1:user-1:thread-1" {
		t.Fatalf("group thread request key = %q", got)
	}
}
