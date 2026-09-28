package im

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// InfoCommand implements /info.
// It shows the workspace and knowledge-base scope selected by the current
// user's binding.
type InfoCommand struct {
	kbService interfaces.KnowledgeBaseService
}

func newInfoCommand(kbService interfaces.KnowledgeBaseService) *InfoCommand {
	return &InfoCommand{kbService: kbService}
}

// Name returns the command token.
func (c *InfoCommand) Name() string { return "info" }

// Description returns the help text shown for /info.
func (c *InfoCommand) Description() string {
	return "查看当前绑定的工作空间与知识库范围"
}

func (c *InfoCommand) Execute(ctx context.Context, cmdCtx *CommandContext, _ []string) (*CommandResult, error) {
	var sb strings.Builder

	// Note: Feishu card markdown only renders **bold** when it occupies the
	// entire inline segment. "**label：**value" on the same line will show
	// raw asterisks. Always keep bold text self-contained on its own line.

	// ── Header ──
	name := cmdCtx.TenantName
	if name == "" {
		name = "当前工作空间"
	}
	fmt.Fprintf(&sb, "💼 **%s**\n", name)
	sb.WriteString("当前使用该工作空间自有知识库进行快速问答。\n")

	// ── Knowledge bases ──
	sb.WriteString("\n📚 **知识库**\n")
	if len(cmdCtx.KnowledgeBaseIDs) > 0 {
		kbs, err := c.kbService.ListKnowledgeBasesByTenantID(ctx, cmdCtx.TenantID)
		if err == nil {
			nameMap := make(map[string]string, len(kbs))
			for _, kb := range kbs {
				nameMap[kb.ID] = kb.Name
			}
			for _, id := range cmdCtx.KnowledgeBaseIDs {
				label := id
				if n, ok := nameMap[id]; ok {
					label = n
				}
				sb.WriteString(fmt.Sprintf("  · %s\n", label))
			}
		} else {
			fmt.Fprintf(&sb, "  共 %d 个\n", len(cmdCtx.KnowledgeBaseIDs))
		}
	} else {
		sb.WriteString("  当前工作空间没有可用知识库\n")
	}

	// ── Footer ──
	outputLabel := "流式输出"
	if cmdCtx.ChannelOutputMode == "full" {
		outputLabel = "完整输出"
	}
	sb.WriteString(fmt.Sprintf("\n⚙️ **输出模式**\n  %s\n", outputLabel))
	sb.WriteString("\n---\n发送 `/help` 查看所有可用指令")

	return &CommandResult{Content: sb.String()}, nil
}
