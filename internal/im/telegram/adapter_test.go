package telegram

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
)

var testBot = botIdentity{ID: 9001, Username: "test_bot"}

func TestParseTelegramMessage_ForumTopicThread(t *testing.T) {
	msg := &telegramMsg{
		MessageID:       100,
		MessageThreadID: 42, // Forum topic ID
		From:            &telegramUser{ID: 1001, FirstName: "Alice"},
		Chat:            telegramChat{ID: -9999, Type: "supergroup"},
		Text:            "@test_bot hello",
	}

	incoming := parseTelegramMessage(msg, testBot)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	if incoming.ThreadID != "42" {
		t.Errorf("ThreadID = %q, want %q", incoming.ThreadID, "42")
	}
	if incoming.Content != "hello" {
		t.Errorf("Content = %q, want %q", incoming.Content, "hello")
	}
}

func TestParseTelegramMessage_NonForumGroup(t *testing.T) {
	msg := &telegramMsg{
		MessageID:       200,
		MessageThreadID: 0, // not a Forum group
		From:            &telegramUser{ID: 1002, FirstName: "Bob"},
		Chat:            telegramChat{ID: -8888, Type: "group"},
		Text:            "hello",
	}

	incoming := parseTelegramMessage(msg, testBot)
	if incoming != nil {
		t.Fatal("expected unaddressed group message to be ignored")
	}
}

func TestParseTelegramMessage_DirectMessage(t *testing.T) {
	msg := &telegramMsg{
		MessageID: 300,
		From:      &telegramUser{ID: 1003, FirstName: "Carol"},
		Chat:      telegramChat{ID: 1003, Type: "private"},
		Text:      "hi bot",
	}

	incoming := parseTelegramMessage(msg, testBot)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	if incoming.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty for DM", incoming.ThreadID)
	}
	if incoming.ChatType != im.ChatTypeDirect {
		t.Errorf("ChatType = %q, want %q", incoming.ChatType, im.ChatTypeDirect)
	}
}

func TestParseTelegramMessage_NilMessage(t *testing.T) {
	incoming := parseTelegramMessage(nil, testBot)
	if incoming != nil {
		t.Error("expected nil for nil message")
	}
}

func TestParseTelegramMessage_Document(t *testing.T) {
	msg := &telegramMsg{
		MessageID:       400,
		MessageThreadID: 7, // in a Forum topic
		From:            &telegramUser{ID: 1004, FirstName: "Dave"},
		Chat:            telegramChat{ID: -7777, Type: "supergroup"},
		Caption:         "@test_bot 总结文件",
		Document: &telegramDoc{
			FileID:   "doc-123",
			FileName: "report.pdf",
			FileSize: 2048,
		},
	}

	incoming := parseTelegramMessage(msg, testBot)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	if incoming.ThreadID != "7" {
		t.Errorf("ThreadID = %q, want %q", incoming.ThreadID, "7")
	}
	if incoming.MessageType != im.MessageTypeFile {
		t.Errorf("MessageType = %q, want %q", incoming.MessageType, im.MessageTypeFile)
	}
	if incoming.Content != "总结文件" {
		t.Errorf("Content = %q, want %q", incoming.Content, "总结文件")
	}
}

func TestParseUpdate_NilMessage(t *testing.T) {
	update := &telegramUpdate{UpdateID: 1, Message: nil}
	incoming := parseUpdate(update, testBot)
	if incoming != nil {
		t.Error("expected nil for nil update.Message")
	}
}

func TestParseTelegramMessage_GroupAddressing(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		replyUserID int64
		wantContent string
		wantMessage bool
	}{
		{name: "plain command", text: "/help", wantContent: "/help", wantMessage: true},
		{name: "addressed command", text: "/clear@Test_Bot", wantContent: "/clear", wantMessage: true},
		{name: "other bot command", text: "/clear@other_bot", wantMessage: false},
		{name: "bot mention", text: "@test_bot 请回答", wantContent: "请回答", wantMessage: true},
		{name: "other mention", text: "@other_bot 请回答", wantMessage: false},
		{name: "reply to bot", text: "继续", replyUserID: testBot.ID, wantContent: "继续", wantMessage: true},
		{name: "reply to user", text: "继续", replyUserID: 1234, wantMessage: false},
		{name: "binding code", text: "WK-AT5XB3RZ47CRYOP2", wantContent: "WK-AT5XB3RZ47CRYOP2", wantMessage: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &telegramMsg{
				MessageID: 500,
				From:      &telegramUser{ID: 1005, FirstName: "Eve"},
				Chat:      telegramChat{ID: -6666, Type: "group"},
				Text:      tt.text,
			}
			if tt.replyUserID != 0 {
				msg.ReplyToMessage = &telegramMsg{From: &telegramUser{ID: tt.replyUserID}}
			}

			incoming := parseTelegramMessage(msg, testBot)
			if !tt.wantMessage {
				if incoming != nil {
					t.Fatal("expected group message to be ignored")
				}
				return
			}
			if incoming == nil {
				t.Fatal("expected group message to be accepted")
			}
			if incoming.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", incoming.Content, tt.wantContent)
			}
		})
	}
}
