package slack

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	slacklib "github.com/slack-go/slack"
)

func TestParseIncomingMessage_ThreadID(t *testing.T) {
	tests := []struct {
		name          string
		messageTS     string
		threadTS      string
		wantThreadID  string
		wantMessageID string
	}{
		{
			name:          "top-level message uses own timestamp",
			messageTS:     "1234567890.123456",
			threadTS:      "1234567890.123456",
			wantThreadID:  "1234567890.123456",
			wantMessageID: "1234567890.123456",
		},
		{
			name:          "threaded reply keeps event timestamp",
			messageTS:     "1234567890.123456",
			threadTS:      "1234567890.000001",
			wantThreadID:  "1234567890.000001",
			wantMessageID: "1234567890.123456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := parseIncomingMessage(
				"U123", "C456", "hello", tt.messageTS, tt.threadTS, im.ChatTypeGroup, nil,
			)

			if msg.ThreadID != tt.wantThreadID {
				t.Errorf("ThreadID = %q, want %q", msg.ThreadID, tt.wantThreadID)
			}
			if msg.MessageID != tt.wantMessageID {
				t.Errorf("MessageID = %q, want %q", msg.MessageID, tt.wantMessageID)
			}
		})
	}
}

func TestParseIncomingMessage_ThreadID_WithFiles(t *testing.T) {
	files := []slacklib.File{
		{ID: "F123", Name: "test.pdf", Size: 1024, Mimetype: "application/pdf"},
	}

	msg := parseIncomingMessage(
		"U123", "C456", "", "1234567890.999", "1234567890.999", im.ChatTypeDirect, files,
	)

	if msg.ThreadID != "1234567890.999" {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, "1234567890.999")
	}
	if msg.MessageType != im.MessageTypeFile {
		t.Errorf("MessageType = %q, want %q", msg.MessageType, im.MessageTypeFile)
	}
}

func TestParseIncomingMessage_MentionStripping(t *testing.T) {
	// Ensure mention stripping doesn't affect ThreadID
	msg := parseIncomingMessage(
		"U123", "C456", "<@U999> hello world", "ts-123", "ts-123", im.ChatTypeGroup, nil,
	)

	if msg.ThreadID != "ts-123" {
		t.Errorf("ThreadID = %q, want %q", msg.ThreadID, "ts-123")
	}
	if msg.Content != "hello world" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello world")
	}
}
