package im

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	searchMaxResults    = 5
	searchContentMaxLen = 200 // runes shown per result
)

// SearchCommand implements /search <query>.
//
// It runs a hybrid search (vector + keywords) against the knowledge bases in
// the workspace selected by the current user's binding and
// returns the raw matching passages without AI summarisation. This is useful
// when the user needs to inspect source text directly.
type SearchCommand struct {
	sessionService interfaces.SessionService
}

func newSearchCommand(sessionService interfaces.SessionService) *SearchCommand {
	return &SearchCommand{sessionService: sessionService}
}

func (c *SearchCommand) Name() string { return "search" }
func (c *SearchCommand) Description() string {
	return "直接检索知识库原文（不经 AI 总结），例如：/search 退款政策"
}

func (c *SearchCommand) Execute(ctx context.Context, cmdCtx *CommandContext, args []string) (*CommandResult, error) {
	if len(args) == 0 {
		return &CommandResult{
			Content: "请输入搜索内容，例如：`/search 退款政策`",
		}, nil
	}

	query := strings.Join(args, " ")
	if len(cmdCtx.KnowledgeBaseIDs) == 0 {
		return &CommandResult{Content: "当前工作空间没有可用于检索的知识库。"}, nil
	}

	results, err := c.sessionService.SearchKnowledge(ctx, cmdCtx.KnowledgeBaseIDs, nil, nil, query)
	if err != nil {
		return nil, fmt.Errorf("search knowledge: %w", err)
	}

	if len(results) == 0 {
		return &CommandResult{
			Content: fmt.Sprintf("未在知识库中找到与「%s」相关的内容。", query),
		}, nil
	}

	// Cap the number of results shown in IM (wall of text is unhelpful).
	shown := results
	if len(shown) > searchMaxResults {
		shown = shown[:searchMaxResults]
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 **搜索「%s」** — 找到 %d 条结果\n\n", query, len(results)))

	for i, r := range shown {
		// Trim content to a readable length.
		content := []rune(r.Content)
		suffix := ""
		if len(content) > searchContentMaxLen {
			content = content[:searchContentMaxLen]
			suffix = "…"
		}

		// Source label: prefer title, fall back to filename.
		source := r.KnowledgeTitle
		if source == "" {
			source = r.KnowledgeFilename
		}

		sb.WriteString(fmt.Sprintf("**[%d]** %s\n> %s%s\n", i+1, source, string(content), suffix))

		if r.Score > 0 {
			sb.WriteString(fmt.Sprintf("匹配度：%.0f%%\n", r.Score*100))
		}
		sb.WriteString("\n")
	}

	if len(results) > searchMaxResults {
		sb.WriteString(fmt.Sprintf("_（仅显示前 %d 条，共 %d 条）_", searchMaxResults, len(results)))
	}

	return &CommandResult{Content: sb.String()}, nil
}
