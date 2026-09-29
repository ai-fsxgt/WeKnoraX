package im

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/logger"
	mcppkg "github.com/Tencent/WeKnora/internal/mcp"
	"github.com/Tencent/WeKnora/internal/ratelimit"
	"github.com/Tencent/WeKnora/internal/storageurl"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// dedupTTL is how long processed message IDs are retained.
	dedupTTL = 5 * time.Minute
	// dedupCleanupInterval is how often the dedup map is cleaned.
	dedupCleanupInterval = 1 * time.Minute
	// maxContentLength is the maximum allowed message content length.
	maxContentLength = 4096
	// maxQuoteContentLength is the max runes to include from a quoted message.
	maxQuoteContentLength = 500
	// maxIMAttachmentBytes bounds an attachment buffered for IM Q&A.
	maxIMAttachmentBytes = 32 << 20 // 32 MiB
	// maxIMVisionAttachmentBytes prevents large images from expanding into an oversized data URI.
	maxIMVisionAttachmentBytes = 8 << 20 // 8 MiB
	// maxIMAttachmentLines matches the legacy attachment prompt limit.
	maxIMAttachmentLines = 500
	// maxIMAttachmentContentBytes bounds parsed text persisted in the IM message and injected into QA.
	maxIMAttachmentContentBytes = 32 << 10 // 32 KiB
	// imAttachmentReadTimeout bounds downloading and parsing before the QA request runs.
	imAttachmentReadTimeout = time.Minute
	// streamFlushInterval is how often buffered stream content is flushed to the IM platform.
	// This prevents API rate-limiting while keeping perceived latency low.
	streamFlushInterval = 300 * time.Millisecond
	// agentCompleteWaitTimeout bounds how long IM waits for EventAgentComplete after
	// the final answer stream finishes, preventing indefinite hangs.
	agentCompleteWaitTimeout = 10 * time.Second
	// imSiteTitleSettingKey is the existing system branding title used by IM replies.
	imSiteTitleSettingKey = "branding.site_title"
	// defaultIMSystemName keeps replies neutral when no custom site title is configured.
	defaultIMSystemName = "知识库"
)

// imCitationTagRe matches inline citation tags produced by the agent pipeline.
// These tags are rendered as interactive UI in the web frontend but are meaningless
// in IM platforms, so they must be stripped before sending.
var imCitationTagRe = regexp.MustCompile(`<(?:kb|web)\b[^>]*/?>`)

// stripIMCitationTags removes <kb .../> and <web .../> inline citation tags from s.
func stripIMCitationTags(s string) string {
	return imCitationTagRe.ReplaceAllString(s, "")
}

// imageXMLBlockRe matches <image ...>...</image> blocks produced by
// EnrichContentWithImageInfo in the RAG context pipeline. These blocks contain
// metadata for the LLM and must be stripped before sending to IM platforms.
var imageXMLBlockRe = regexp.MustCompile(`(?s)<image\b[^>]*>.*?</image>`)

// imageOriginalRe extracts the original markdown image syntax from <image_original> tags.
var imageOriginalRe = regexp.MustCompile(`<image_original>(.*?)</image_original>`)

// stripImageXMLTags collapses <image> blocks back to plain markdown.
// Extracts the original ![alt](url) from <image_original> when present,
// otherwise drops the block entirely.
func stripImageXMLTags(s string) string {
	return imageXMLBlockRe.ReplaceAllStringFunc(s, func(block string) string {
		if m := imageOriginalRe.FindStringSubmatch(block); len(m) > 1 {
			return m[1]
		}
		return ""
	})
}

// rewriteStorageURLs replaces all storage references in content with HTTP URLs
// so IM clients — which cannot attach WeKnora credentials to an image fetch —
// can render them. See internal/storageurl for the shared implementation.
func rewriteStorageURLs(ctx context.Context, content string, resolver *storageurl.FileServiceResolver) string {
	if resolver == nil {
		return content
	}
	return storageurl.Rewrite(ctx, content, resolver, "IM")
}

// ── Streaming holdback helpers ──
// During streaming, content is flushed in 300ms batches. A storage reference or
// an XML tag may be split across two batches. These helpers detect incomplete
// patterns at the end of a chunk so the caller can hold them back until the
// next flush completes them.

// incompleteXMLTagRe matches the opening of an <image…>, <kb…>, or <web…> tag
// that reaches the end of the string without a closing '>'.
var incompleteXMLTagRe = regexp.MustCompile(
	`<(?:image|image_original|image_caption|image_ocr|kb|web)[^>]*$`,
)

// findIncompleteXMLTag returns the byte offset of a potentially truncated XML
// tag at the tail of s, or -1 if none.
func findIncompleteXMLTag(s string) int {
	loc := incompleteXMLTagRe.FindStringIndex(s)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// holdbackCutoff returns the earliest incomplete-pattern offset at the tail of
// chunk, or len(chunk) if the chunk is safe to flush entirely.
func holdbackCutoff(chunk string) int {
	cutoff := storageurl.HoldbackCutoff(chunk)
	if idx := findIncompleteXMLTag(chunk); idx >= 0 && idx < cutoff {
		cutoff = idx
	}
	return cutoff
}

// formatIMOutboundAnswer strips thinking/tool blocks and applies IM content cleanup.
func formatIMOutboundAnswer(ctx context.Context, raw string, tenant *types.Tenant, defaultFileSvc interfaces.FileService, storageResolvers ...interfaces.StorageBackendResolver) string {
	return cleanIMContent(ctx, FormatIMDisplayContent(raw, StreamDisplayFinal), tenant, defaultFileSvc, storageResolvers...)
}

// cleanIMContent applies all IM-specific content transformations:
//  1. Collapse <image> XML blocks back to plain markdown
//  2. Strip <kb/> and <web/> citation tags
//  3. Rewrite provider:// URLs to HTTP URLs (scheme-aware per tenant config)
func cleanIMContent(ctx context.Context, content string, tenant *types.Tenant, defaultFileSvc interfaces.FileService, storageResolvers ...interfaces.StorageBackendResolver) string {
	content = stripImageXMLTags(content)
	content = stripIMCitationTags(content)
	resolver := newIMFileServiceResolver(tenant, defaultFileSvc, storageResolvers...).WithContext(ctx)
	content = rewriteStorageURLs(ctx, content, resolver)
	return content
}

// newIMFileServiceResolver builds a per-message storage backend resolver. The
// cache lives for one cleanIMContent / outbound message so a long answer does
// not re-create an SDK client for every reference.
func newIMFileServiceResolver(
	tenant *types.Tenant,
	defaultSvc interfaces.FileService,
	storageResolvers ...interfaces.StorageBackendResolver,
) *storageurl.FileServiceResolver {
	return storageurl.NewFileServiceResolver(tenant, defaultSvc, storageResolvers...)
}

func buildIMFileServiceForProvider(
	tenant *types.Tenant,
	provider string,
	defaultSvc interfaces.FileService,
) interfaces.FileService {
	return storageurl.BuildFileServiceForProvider(tenant, provider, defaultSvc)
}

// resolveIMFileServiceForPath is a test/helper entry point without caching.
func resolveIMFileServiceForPath(tenant *types.Tenant, filePath string, defaultSvc interfaces.FileService) interfaces.FileService {
	return newIMFileServiceResolver(tenant, defaultSvc).ResolveFileService(filePath)
}

const (
	// wsLeaderTTL is the TTL for the Redis key used for WebSocket leader election.
	wsLeaderTTL = 15 * time.Second
	// wsLeaderRenewInterval is how often the leader renews its lock.
	wsLeaderRenewInterval = 5 * time.Second
	// wsLeaderRetryInterval is how often non-leader instances try to acquire the lock.
	wsLeaderRetryInterval = 10 * time.Second
	// stopMarkerTTL is the TTL for cross-instance /stop markers in Redis.
	stopMarkerTTL = 30 * time.Second
	// stopPollInterval is how often in-flight workers check for remote /stop signals.
	stopPollInterval = 500 * time.Millisecond
)

// ── Redis key prefixes ──────────────────────────────────────────────────────
// All IM-related Redis keys are defined here for discoverability and to avoid
// scattered string literals across multiple files.
const (
	RedisKeyLeader     = "im:ws:leader:"    // + channelID — WebSocket leader election
	RedisKeyDedup      = "im:dedup:"        // + messageID — message deduplication
	RedisKeyStop       = "im:stop:"         // + userKey   — cross-instance /stop marker (pre-execution)
	RedisKeyInflight   = "im:inflight:"     // + userKey   — maps userKey → sessionID:messageID for cross-instance /stop
	RedisKeyQueueUser  = "im:queue:user:"   // + userKey   — global per-user queue counter
	RedisKeyRateLimit  = "im:ratelimit:"    // + key       — sliding-window rate limiting
	RedisKeyGlobalGate = "im:global:active" // global concurrent worker counter
	// RedisChannelConfig broadcasts durable im_channels mutations so every
	// application replica invalidates its local adapter/config snapshot.
	RedisChannelConfig = "im:channel:config"

	defaultRateLimitWindow      = 60 * time.Second
	defaultRateLimitMaxRequests = 10
)

// ErrChannelDisabled reports that a channel row exists but is disabled, so
// callers can tell it apart from a missing channel or a transient failure.
var ErrChannelDisabled = errors.New("channel is disabled")

// ErrChannelIdentityMissing prevents an enabled installation from running
// without a stable platform identity for duplicate and rebind protection.
var ErrChannelIdentityMissing = errors.New("channel credentials do not contain a stable bot identity")

// ErrChannelConfigurationInvalid prevents an incomplete channel from being enabled.
var ErrChannelConfigurationInvalid = errors.New("channel configuration is invalid")

// channelState holds runtime state for a running IM channel.
type channelState struct {
	Channel      *IMChannel
	Adapter      Adapter
	Cancel       context.CancelFunc // for stopping websocket goroutines
	leaderCancel context.CancelFunc // stops the leader renewal goroutine (nil if not leader)
}

type leaderRetryState struct {
	cancel context.CancelFunc
}

type channelConfigEvent struct {
	ChannelID      string `json:"channel_id"`
	SourceInstance string `json:"source_instance"`
}

// AdapterFactory creates an Adapter from an IMChannel configuration.
// The second return value is an optional cleanup function (e.g., for stopping websocket connections).
type AdapterFactory func(ctx context.Context, channel *IMChannel, msgHandler func(ctx context.Context, msg *IncomingMessage) error) (Adapter, context.CancelFunc, error)

// inflightEntry tracks a running QA request, keyed by userKey in the inflight map.
type inflightEntry struct {
	cancel             context.CancelFunc
	sessionID          string // set after assistant message is created
	assistantMessageID string // set after assistant message is created
}

// Service orchestrates IM message handling:
// 1. Receives a unified IncomingMessage from an Adapter
// 2. Resolves or creates a WeKnora session for the IM channel
// 3. Dispatches slash-commands (/help, /kb, /clear, etc.) without entering QA
// 4. Calls the WeKnora QA pipeline for normal messages
// 5. Collects the streaming answer and sends it back via the Adapter
type Service struct {
	db               *gorm.DB
	sessionService   interfaces.SessionService
	messageService   interfaces.MessageService
	tenantService    interfaces.TenantService
	userService      interfaces.UserService
	memberService    interfaces.TenantMemberService
	organizationSvc  interfaces.OrganizationService
	systemSettingSvc interfaces.SystemSettingService

	// Knowledge services resolve the selected workspace's own and shared knowledge bases.
	kbService      interfaces.KnowledgeBaseService
	kbShareService interfaces.KBShareService

	// oauthManager builds MCP OAuth authorization URLs so IM users can authorize
	// OAuth-enabled MCP services out-of-band (IM cannot resolve the in-conversation
	// prompt). May be nil, in which case a generic console hint is shown instead.
	oauthManager *mcppkg.OAuthManager

	// streamManager writes/reads QA events for distributed stop detection,
	// consistent with the web StopSession mechanism. May be nil in Lite mode
	// (but NewStreamManager always returns at least a memory implementation).
	streamManager interfaces.StreamManager

	// defaultFileSvc is the process-wide storage backend (STORAGE_TYPE / env).
	// Used when tenant StorageEngineConfig cannot build a service for the URL scheme.
	defaultFileSvc  interfaces.FileService
	documentReader  interfaces.DocumentReader
	storageResolver interfaces.StorageBackendResolver

	// cmdRegistry holds all registered slash-commands.
	cmdRegistry *CommandRegistry

	// channels maps channel ID -> running channel state
	channels      map[string]*channelState
	leaderRetries map[string]*leaderRetryState
	mu            sync.RWMutex

	// adapterFactories maps platform name -> factory function
	adapterFactories map[string]AdapterFactory

	// processedMsgs tracks recently processed message IDs to prevent duplicate handling.
	processedMsgs sync.Map

	// rateLimiter enforces per-user sliding window rate limiting.
	// Uses Redis ZSET when available, falls back to local sliding window.
	rateLimiter  *ratelimit.Limiter
	rateLimitMax int

	// inflight tracks in-progress QA requests, keyed by binding context. Allows
	// /stop and binding revocation to abort a running request
	// on this instance and look up (sessionID, messageID) for StreamManager.
	inflight sync.Map // userKey -> *inflightEntry

	// qaQueue manages bounded queuing and worker-pool execution of QA requests,
	// providing backpressure to protect downstream LLM resources.
	qaQueue *qaQueue

	// redis is the optional Redis client for distributed state (dedup, rate
	// limiting, leader election, cross-instance /stop). When nil the service
	// falls back to local in-memory state (single-instance / Lite mode).
	redis *redis.Client

	// instanceID uniquely identifies this service instance for leader election.
	instanceID string

	stopCh         chan struct{}
	stopOnce       sync.Once
	subscriberOnce sync.Once
	stopped        atomic.Bool
}

// nonTextTypeLabel maps a message type to a Chinese label for LLM instructions.
var nonTextTypeLabel = map[string]string{
	"image": "图片",
	"file":  "文件",
	"video": "视频",
	"voice": "语音",
}

// formatQuotedContext formats a QuotedMessage into a labeled string for LLM context.
// Returns empty string if quote is nil.
// For non-text quotes, generates an instruction telling the LLM to acknowledge
// the unprocessable content instead of a placeholder that causes hallucination.
func formatQuotedContext(quote *QuotedMessage) string {
	if quote == nil {
		return ""
	}
	// Non-text quote: generate instruction, not content placeholder.
	if quote.NonTextType != "" {
		label := nonTextTypeLabel[quote.NonTextType]
		if label == "" {
			label = "该类型的"
		}
		return "用户引用了一条" + label + "消息，但你无法查看该内容。请直接告知用户你目前无法处理" + label + "消息，建议用户用文字描述问题。不要猜测该消息的内容。"
	}
	if quote.Content == "" {
		return ""
	}
	content := quote.Content
	runes := []rune(content)
	if len(runes) > maxQuoteContentLength {
		content = string(runes[:maxQuoteContentLength]) + "..."
	}
	// Prevent quoted content from escaping the XML tag boundary.
	content = strings.ReplaceAll(content, "</quoted_message>", "")
	label := "以下是用户引用的一条历史消息，仅作为上下文参考："
	if quote.IsBotMessage {
		label = "以下是用户引用的你（机器人）之前的回复，仅作为上下文参考："
	}
	return label + "\n<quoted_message>\n" + content + "\n</quoted_message>"
}

// withIMIdentity injects the bound WeKnora account and its live workspace role
// into the callback context. The binding remains the principal for
// principal-scoped state, while sessions are owned by the actual user ID.
func withIMIdentity(
	ctx context.Context,
	binding *UserBinding,
	member *types.TenantMember,
) context.Context {
	ctx = context.WithValue(ctx, types.TenantIDContextKey, binding.TenantID)
	ctx = context.WithValue(ctx, types.UserIDContextKey, binding.UserID)
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalIMUser, ID: binding.ID})
	ctx = context.WithValue(ctx, types.TenantRoleContextKey, member.Role)
	// IM bots have no live client that can complete an in-conversation MCP OAuth
	// prompt, so mark the context non-interactive: the agent emits a one-shot
	// authorization notice (surfaced in the reply) instead of blocking until the
	// OAuth wait times out for every unauthorized service.
	ctx = types.WithMCPOAuthNonInteractive(ctx)
	return ctx
}

// imMCPAuthService identifies an OAuth-enabled MCP service that the IM user has
// not authorized yet, collected during a turn so an authorization notice can be
// appended to the reply.
type imMCPAuthService struct {
	ID   string
	Name string
}

// mcpOAuthCallbackURL returns the absolute backend callback URL registered with
// the authorization server, derived from APP_EXTERNAL_URL. Empty when unset.
func mcpOAuthCallbackURL() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("APP_EXTERNAL_URL")), "/")
	if base == "" {
		return ""
	}
	return base + "/api/v1/mcp-oauth/callback"
}

// buildIMMCPAuthNotice builds a user-facing authorization notice for the given
// OAuth-enabled MCP services. When the OAuth manager and APP_EXTERNAL_URL are
// available it generates a per-service authorization URL (StartAuthorization)
// the user can open; otherwise it falls back to a console hint. Returns "" when
// there is nothing to report. The user authorizes out-of-band, then re-sends
// their message to use the service (IM cannot resolve the prompt inline).
func (s *Service) buildIMMCPAuthNotice(ctx context.Context, services []imMCPAuthService) string {
	// Deduplicate by service ID, preserving order.
	seen := make(map[string]bool, len(services))
	uniq := make([]imMCPAuthService, 0, len(services))
	for _, svc := range services {
		if svc.ID == "" || seen[svc.ID] {
			continue
		}
		seen[svc.ID] = true
		uniq = append(uniq, svc)
	}
	if len(uniq) == 0 {
		return ""
	}

	tenantID, _ := types.TenantIDFromContext(ctx)
	principal := types.MCPOAuthPrincipalFromContext(ctx)
	redirectURI := mcpOAuthCallbackURL()
	frontendRedirect := strings.TrimSpace(os.Getenv("APP_EXTERNAL_URL"))
	if frontendRedirect == "" {
		frontendRedirect = "/"
	}

	var lines []string
	for _, svc := range uniq {
		name := strings.TrimSpace(svc.Name)
		if name == "" {
			name = svc.ID
		}
		var authURL string
		if s.oauthManager != nil && redirectURI != "" && tenantID != 0 && principal.Valid() {
			url, err := s.oauthManager.StartAuthorizationForService(
				ctx, tenantID, principal, svc.ID, redirectURI, frontendRedirect,
			)
			if err != nil {
				logger.Warnf(ctx, "[IM] Failed to build MCP OAuth URL for service %s: %v", svc.ID, err)
			} else {
				authURL = url
			}
		}
		if authURL != "" {
			lines = append(lines, fmt.Sprintf("• %s：%s", name, authURL))
		} else {
			lines = append(lines, fmt.Sprintf("• %s（请在知识库管理后台完成 OAuth 授权）", name))
		}
	}

	return "⚠️ 以下 MCP 服务需要授权后才能使用，请点击链接完成授权，然后重新发送你的消息：\n" +
		strings.Join(lines, "\n")
}

// appendIMAuthNotice appends an authorization notice to an existing reply body,
// separated by a blank line. When the body is empty the notice becomes the body.
func appendIMAuthNotice(body, notice string) string {
	if notice == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return notice
	}
	return body + "\n\n" + notice
}

func buildIMQARequest(
	session *types.Session,
	query string,
	assistantMessageID string,
	userMessageID string,
	customAgent *types.CustomAgent,
	kbIDs []string,
	quote *QuotedMessage,
	attachments ...types.MessageAttachments,
) *types.QARequest {
	// WebSearchEnabled: the web handler passes this per-request from the
	// frontend toggle; for IM channels the user has no per-message toggle,
	// so we derive it from the agent config (the single source of truth).
	webSearchEnabled := customAgent != nil && customAgent.Config.WebSearchEnabled
	quotedContext := formatQuotedContext(quote)
	var requestAttachments types.MessageAttachments
	if len(attachments) > 0 {
		requestAttachments = attachments[0]
	}
	return &types.QARequest{
		Session:            session,
		Query:              query,
		AssistantMessageID: assistantMessageID,
		CustomAgent:        customAgent,
		KnowledgeBaseIDs:   kbIDs,
		UserMessageID:      userMessageID,
		WebSearchEnabled:   webSearchEnabled,
		QuotedContext:      quotedContext,
		Attachments:        requestAttachments,
	}
}

func buildIMLastRequestState(agentID string, customAgent *types.CustomAgent, kbIDs []string) *types.SessionLastRequestState {
	state := &types.SessionLastRequestState{
		AgentID:          agentID,
		KnowledgeBaseIDs: append([]string(nil), kbIDs...),
	}
	if customAgent == nil {
		return state
	}
	if state.AgentID == "" {
		state.AgentID = customAgent.ID
	}
	state.AgentEnabled = customAgent.IsAgentMode()
	state.ModelID = customAgent.Config.ModelID
	state.WebSearchEnabled = customAgent.Config.WebSearchEnabled
	if len(state.KnowledgeBaseIDs) == 0 && len(customAgent.Config.KnowledgeBases) > 0 {
		state.KnowledgeBaseIDs = append([]string(nil), customAgent.Config.KnowledgeBases...)
	}
	return state
}

func createIMUserMessagePayload(sessionID, content, requestID string, attachments ...types.MessageAttachments) *types.Message {
	var messageAttachments types.MessageAttachments
	if len(attachments) > 0 {
		messageAttachments = attachments[0]
	}
	return &types.Message{
		SessionID:   sessionID,
		Role:        "user",
		Content:     content,
		RequestID:   requestID,
		CreatedAt:   time.Now(),
		IsCompleted: true,
		Channel:     "im",
		Attachments: messageAttachments,
	}
}

// prepareIMAttachments downloads an IM attachment and exposes its parsed text
// and, for images, a bounded data URI to the QA pipeline.
func (s *Service) prepareIMAttachments(
	ctx context.Context,
	msg *IncomingMessage,
	adapter Adapter,
) (types.MessageAttachments, []string, error) {
	if msg.MessageType != MessageTypeFile && msg.MessageType != MessageTypeImage {
		return nil, nil, nil
	}
	if msg.FileSize > maxIMAttachmentBytes {
		return nil, nil, fmt.Errorf("attachment exceeds the %d MiB limit", maxIMAttachmentBytes>>20)
	}
	downloader, ok := adapter.(FileDownloader)
	if !ok {
		return nil, nil, fmt.Errorf("platform %s does not support attachment download", msg.Platform)
	}
	attachmentCtx, cancel := context.WithTimeout(ctx, imAttachmentReadTimeout)
	defer cancel()
	reader, fileName, err := downloader.DownloadFile(attachmentCtx, msg)
	if err != nil {
		return nil, nil, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maxIMAttachmentBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(content) > maxIMAttachmentBytes {
		return nil, nil, fmt.Errorf("attachment exceeds the %d MiB limit", maxIMAttachmentBytes>>20)
	}
	if fileName == "" {
		fileName = msg.FileName
	}
	if msg.MessageType == MessageTypeImage && filepath.Ext(fileName) == "" {
		fileName += ".png"
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(fileName), "."))
	if ext == "" {
		return nil, nil, fmt.Errorf("attachment has no file extension")
	}
	attachment := types.MessageAttachment{FileName: fileName, FileType: "." + ext, FileSize: int64(len(content))}
	request := &types.ReadRequest{FileContent: content, FileName: fileName, FileType: ext}
	var result *types.ReadResult
	isImage := msg.MessageType == MessageTypeImage || docparser.IsImageFormat(ext)
	if isImage && s.documentReader != nil {
		result, err = s.documentReader.Read(attachmentCtx, request)
		if err != nil {
			logger.Warnf(ctx, "[IM] image OCR/document parsing failed, continuing with vision input: %v", err)
			result, err = nil, nil
		}
	}
	if result == nil && docparser.IsSimpleFormat(attachment.FileType) {
		result, err = (&docparser.SimpleFormatReader{}).Read(attachmentCtx, request)
	} else if result == nil && !isImage && s.documentReader != nil {
		result, err = s.documentReader.Read(attachmentCtx, request)
	}
	if err != nil {
		logger.Warnf(ctx, "[IM] attachment parsing failed, continuing with attachment metadata: %v", err)
	}
	if result != nil {
		applyIMAttachmentTruncation(result.MarkdownContent, &attachment)
	}
	var imageURLs []string
	if isImage && len(content) <= maxIMVisionAttachmentBytes {
		mediaType := http.DetectContentType(content)
		if !strings.HasPrefix(mediaType, "image/") {
			return nil, nil, fmt.Errorf("invalid image content type: %s", mediaType)
		}
		imageURLs = []string{"data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(content)}
	} else if isImage {
		logger.Warnf(ctx, "[IM] image is too large for direct vision input: size=%d limit=%d", len(content), maxIMVisionAttachmentBytes)
	}
	return types.MessageAttachments{attachment}, imageURLs, nil
}

func applyIMAttachmentTruncation(content string, attachment *types.MessageAttachment) {
	attachment.LineCount = strings.Count(content, "\n") + 1

	limited := truncateUTF8ByBytes(content, maxIMAttachmentContentBytes)
	lines := strings.SplitN(limited, "\n", maxIMAttachmentLines+1)
	if len(lines) > maxIMAttachmentLines {
		limited = strings.Join(lines[:maxIMAttachmentLines], "\n")
	}

	attachment.Content = limited
	attachment.IsTruncated = len(limited) < len(content)
}

func truncateUTF8ByBytes(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}

	end := maxBytes
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}

func createIMAssistantMessagePayload(sessionID, requestID string) *types.Message {
	return &types.Message{
		SessionID:   sessionID,
		Role:        "assistant",
		RequestID:   requestID,
		CreatedAt:   time.Now(),
		IsCompleted: false,
		Channel:     "im",
	}
}

func collectIMKnowledgeReferences(dst *[]*types.SearchResult, refs interface{}) {
	switch v := refs.(type) {
	case []*types.SearchResult:
		*dst = append(*dst, v...)
	case []interface{}:
		for _, ref := range v {
			if sr, ok := ref.(*types.SearchResult); ok {
				*dst = append(*dst, sr)
			}
		}
	}
}

func sanitizeIMAgentSteps(raw interface{}) types.AgentSteps {
	switch steps := raw.(type) {
	case []types.AgentStep:
		return types.AgentSteps(agenttools.SanitizeAgentStepsForStorage(steps))
	case types.AgentSteps:
		return types.AgentSteps(agenttools.SanitizeAgentStepsForStorage([]types.AgentStep(steps)))
	default:
		return nil
	}
}

func applyIMCompleteDataToMessage(msg *types.Message, data event.AgentCompleteData) {
	if msg == nil {
		return
	}
	if data.MessageID != "" && data.MessageID != msg.ID {
		return
	}
	msg.IsCompleted = true
	msg.AgentDurationMs = data.TotalDurationMs
	if usage, ok := data.Usage.(*types.TokenUsage); ok && usage != nil {
		msg.Usage = usage
	}
	if len(data.KnowledgeRefs) > 0 {
		refs := make([]*types.SearchResult, 0, len(data.KnowledgeRefs))
		collectIMKnowledgeReferences(&refs, data.KnowledgeRefs)
		if len(refs) > 0 {
			msg.KnowledgeReferences = types.References(refs)
		}
	}
	if steps := sanitizeIMAgentSteps(data.AgentSteps); len(steps) > 0 {
		msg.AgentSteps = steps
	}
}

// waitForIMAgentComplete blocks until EventAgentComplete, ctx cancellation, or timeout.
func waitForIMAgentComplete(ctx context.Context, completeDone <-chan struct{}, sessionID string) {
	timer := time.NewTimer(agentCompleteWaitTimeout)
	defer timer.Stop()
	select {
	case <-completeDone:
	case <-ctx.Done():
		logger.Warnf(ctx, "[IM] QA context ended before agent complete event: session=%s", sessionID)
	case <-timer.C:
		logger.Warnf(ctx, "[IM] Timed out waiting for agent complete event: session=%s", sessionID)
	}
}

// pickIMStoredAnswer returns the best available answer text from IM stream buffers.
func pickIMStoredAnswer(candidates ...string) string {
	for _, s := range candidates {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// mergeIMAgentAnswerBuffers copies optimistic/live answers into persistence buffers
// when EventAgentFinalAnswer did not populate answerBuilder (e.g. cancel before complete).
func mergeIMAgentAnswerBuffers(answerBuilder, answerOuter, agentLiveAnswer *strings.Builder, completeFinal string) {
	if answerBuilder.Len() > 0 {
		return
	}
	switch {
	case agentLiveAnswer.Len() > 0:
		live := agentLiveAnswer.String()
		answerBuilder.WriteString(live)
		if answerOuter.Len() == 0 {
			answerOuter.WriteString(live)
		}
	case answerOuter.Len() > 0:
		answerBuilder.WriteString(answerOuter.String())
	case strings.TrimSpace(completeFinal) != "":
		answerBuilder.WriteString(completeFinal)
		answerOuter.WriteString(completeFinal)
	}
}

// resolveIMConfig extracts IM tuning parameters from the application config,
// falling back to built-in defaults for any zero/nil values.
func resolveIMConfig(appCfg *config.Config) (workers, maxQueue, maxPerUser, globalMaxWorkers int, rlWindow time.Duration, rlMax int) {
	workers = defaultWorkers
	maxQueue = defaultMaxQueueSize
	maxPerUser = defaultMaxPerUser
	rlWindow = defaultRateLimitWindow
	rlMax = defaultRateLimitMaxRequests

	if appCfg == nil || appCfg.IM == nil {
		return
	}
	im := appCfg.IM
	if im.Workers > 0 {
		workers = im.Workers
	}
	if im.MaxQueueSize > 0 {
		maxQueue = im.MaxQueueSize
	}
	if im.MaxPerUser > 0 {
		maxPerUser = im.MaxPerUser
	}
	if im.GlobalMaxWorkers > 0 {
		globalMaxWorkers = im.GlobalMaxWorkers
	}
	if im.RateLimitWindow > 0 {
		rlWindow = im.RateLimitWindow
	}
	if im.RateLimitMax > 0 {
		rlMax = im.RateLimitMax
	}
	return
}

// NewService creates a new IM service.
// redisClient may be nil — in that case the service falls back to local
// in-memory state (Lite / single-instance mode).
// cfg may be nil — in that case built-in defaults are used.
func NewService(
	db *gorm.DB,
	sessionService interfaces.SessionService,
	messageService interfaces.MessageService,
	tenantService interfaces.TenantService,
	userService interfaces.UserService,
	memberService interfaces.TenantMemberService,
	organizationService interfaces.OrganizationService,
	systemSetting interfaces.SystemSettingService,
	kbService interfaces.KnowledgeBaseService,
	kbShareService interfaces.KBShareService,
	streamManager interfaces.StreamManager,
	defaultFileSvc interfaces.FileService,
	documentReader interfaces.DocumentReader,
	oauthManager *mcppkg.OAuthManager,
	redisClient *redis.Client,
	appCfg *config.Config,
	storageResolver interfaces.StorageBackendResolver,
) *Service {
	// Resolve IM configuration with defaults.
	workers, maxQueue, maxPerUser, globalMaxWorkers, rlWindow, rlMax := resolveIMConfig(appCfg)

	// Build command registry.
	registry := NewCommandRegistry()
	registry.Register(newHelpCommand(registry))
	registry.Register(newInfoCommand(kbService))
	registry.Register(newSearchCommand(sessionService))
	registry.Register(newStopCommand())
	registry.Register(newClearCommand())

	instanceID := uuid.New().String()
	s := &Service{
		db:               db,
		sessionService:   sessionService,
		messageService:   messageService,
		tenantService:    tenantService,
		userService:      userService,
		memberService:    memberService,
		organizationSvc:  organizationService,
		systemSettingSvc: systemSetting,
		kbService:        kbService,
		kbShareService:   kbShareService,
		streamManager:    streamManager,
		defaultFileSvc:   defaultFileSvc,
		documentReader:   documentReader,
		storageResolver:  storageResolver,
		oauthManager:     oauthManager,
		cmdRegistry:      registry,
		channels:         make(map[string]*channelState),
		leaderRetries:    make(map[string]*leaderRetryState),
		adapterFactories: make(map[string]AdapterFactory),
		rateLimiter:      ratelimit.New(redisClient, RedisKeyRateLimit, rlWindow, instanceID),
		rateLimitMax:     rlMax,
		redis:            redisClient,
		instanceID:       instanceID,
		stopCh:           make(chan struct{}),
	}

	// Initialize the QA worker pool and bounded queue.
	s.qaQueue = newQAQueue(workers, maxQueue, maxPerUser, globalMaxWorkers, s.executeQARequest, redisClient)
	s.qaQueue.Start(s.stopCh)

	// Start periodic cleanup loops.
	// Dedup cleanup is only needed in single-instance mode (local sync.Map);
	// when Redis handles dedup, the TTL on Redis keys handles expiry automatically.
	if redisClient == nil {
		go s.dedupCleanupLoop()
	}
	go s.rateLimiter.StartCleanup(s.stopCh)

	if redisClient != nil {
		globalInfo := "unlimited"
		if globalMaxWorkers > 0 {
			globalInfo = fmt.Sprintf("%d", globalMaxWorkers)
		}
		logger.Infof(context.Background(), "[IM] Multi-instance mode enabled (instance=%s, workers=%d, queue=%d, global_max=%s)",
			s.instanceID[:8], workers, maxQueue, globalInfo)
	} else {
		logger.Infof(context.Background(), "[IM] Single-instance mode (no Redis, workers=%d, queue=%d)",
			workers, maxQueue)
	}

	return s
}

func (s *Service) systemName(ctx context.Context) string {
	if s.systemSettingSvc == nil {
		return defaultIMSystemName
	}
	title := strings.TrimSpace(s.systemSettingSvc.GetString(ctx, imSiteTitleSettingKey, "", ""))
	if title == "" {
		return defaultIMSystemName
	}
	return title
}

// RegisterAdapterFactory registers a factory for creating adapters for a given platform.
func (s *Service) RegisterAdapterFactory(platform string, factory AdapterFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapterFactories[platform] = factory
}

// Stop gracefully shuts down the service, stopping all channels and background goroutines.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		s.stopped.Store(true)
		close(s.stopCh)
		if s.qaQueue != nil {
			s.qaQueue.Stop()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, cs := range s.channels {
			s.stopChannelLocked(id, cs)
		}
		for id, retry := range s.leaderRetries {
			retry.cancel()
			delete(s.leaderRetries, id)
		}
	})
}

// dedupCleanupLoop periodically cleans up expired entries from the dedup map.
func (s *Service) dedupCleanupLoop() {
	ticker := time.NewTicker(dedupCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-dedupTTL)
			s.processedMsgs.Range(func(key, value interface{}) bool {
				if t, ok := value.(time.Time); ok && t.Before(cutoff) {
					s.processedMsgs.Delete(key)
				}
				return true
			})
		case <-s.stopCh:
			return
		}
	}
}

// imImageConfigWarning returns an operator warning when IM channels are active
// but APP_EXTERNAL_URL is unset. resource:// images then render only if the
// storage backend is itself publicly reachable (e.g. a cloud bucket with a
// public endpoint); on the default MinIO (internal minio:9000) or local
// deployment it is not, so images silently break. Advisory only; returns ""
// when there is nothing to warn about. Pure (no receiver/closures) so it is
// unit-testable.
func imImageConfigWarning(activeChannels int, externalURL string) string {
	if activeChannels == 0 || strings.TrimSpace(externalURL) != "" {
		return ""
	}
	return fmt.Sprintf("[IM] %d IM channel(s) active but APP_EXTERNAL_URL is unset; "+
		"resource:// images render only if the storage backend is publicly reachable — "+
		"otherwise set APP_EXTERNAL_URL so they route through nginx /r/",
		activeChannels)
}

// LoadAndStartChannels loads all enabled channels from the database and starts them.
func (s *Service) LoadAndStartChannels() error {
	s.startChannelConfigSubscriber()

	ctx := context.Background()
	var channels []IMChannel
	if err := s.db.Where("enabled = ? AND deleted_at IS NULL", true).Find(&channels).Error; err != nil {
		return fmt.Errorf("load im channels: %w", err)
	}

	if msg := imImageConfigWarning(len(channels), os.Getenv("APP_EXTERNAL_URL")); msg != "" {
		logger.Warnf(ctx, "%s", msg)
	}

	for i := range channels {
		ch := channels[i]
		if err := s.StartChannel(&ch); err != nil {
			logger.Warnf(ctx, "[IM] Failed to start channel %s (%s/%s): %v", ch.ID, ch.Platform, ch.Name, err)
		} else if _, _, active := s.GetChannelAdapter(ch.ID); active {
			// Adapter initialization can launch an asynchronous connection attempt.
			// Do not claim network readiness here; platform connection logs report it.
			logger.Infof(ctx, "[IM] Initialized channel runtime: id=%s platform=%s name=%s mode=%s",
				ch.ID, ch.Platform, ch.Name, ch.Mode)
		} else {
			logger.Infof(ctx, "[IM] Channel runtime is on standby: id=%s platform=%s name=%s mode=%s",
				ch.ID, ch.Platform, ch.Name, ch.Mode)
		}
	}

	logger.Infof(ctx, "[IM] Loaded %d enabled channels", len(channels))
	return nil
}

// startChannelConfigSubscriber listens for durable channel mutations made by
// other application replicas. Redis Pub/Sub provides the fast path; callback
// freshness checks and the leader renewal DB check remain the durable fallback
// if an event is missed while a replica is disconnected.
func (s *Service) startChannelConfigSubscriber() {
	if s.redis == nil {
		return
	}
	s.subscriberOnce.Do(func() {
		go s.channelConfigSubscriberLoop()
	})
}

func (s *Service) channelConfigSubscriberLoop() {
	pubsub := s.redis.Subscribe(context.Background(), RedisChannelConfig)
	defer pubsub.Close()

	messages := pubsub.Channel()
	for {
		select {
		case <-s.stopCh:
			return
		case msg, ok := <-messages:
			if !ok {
				return
			}
			var event channelConfigEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				logger.Warnf(context.Background(), "[IM] Ignore invalid channel config event: %v", err)
				continue
			}
			if event.ChannelID == "" || event.SourceInstance == s.instanceID {
				continue
			}
			s.reloadChannelFromDB(event.ChannelID, "config event")
		}
	}
}

func (s *Service) publishChannelConfigChange(channelID string) {
	if s.redis == nil || channelID == "" || s.stopped.Load() {
		return
	}
	payload, err := json.Marshal(channelConfigEvent{
		ChannelID:      channelID,
		SourceInstance: s.instanceID,
	})
	if err != nil {
		return
	}
	if err := s.redis.Publish(context.Background(), RedisChannelConfig, payload).Err(); err != nil {
		// The database is the source of truth. Subscribers also verify freshness
		// on webhook callbacks / leader renewal, so publication is best-effort.
		logger.Warnf(context.Background(), "[IM] Publish channel config event failed for %s: %v", channelID, err)
	}
}

func (s *Service) reloadChannelFromDB(channelID, reason string) {
	fresh, err := s.GetChannelByID(channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.StopChannel(channelID)
		return
	}
	if err != nil {
		logger.Warnf(context.Background(), "[IM] Reload channel %s after %s failed: %v", channelID, reason, err)
		return
	}
	if !fresh.Enabled {
		s.StopChannel(channelID)
		return
	}
	if _, cached, running := s.GetChannelAdapter(channelID); running && sameChannelRuntimeConfig(cached, fresh) {
		return
	}

	logger.Infof(context.Background(), "[IM] Reloading channel %s after %s", channelID, reason)
	if err := s.StartChannel(fresh); err != nil && !s.stopped.Load() {
		logger.Warnf(context.Background(), "[IM] Reload channel %s after %s failed: %v", channelID, reason, err)
	}
}

// StartChannel creates and registers an adapter for the given channel.
// For WebSocket channels with Redis available, only one instance acquires
// the leader lock and opens the connection; other instances periodically
// retry so they can take over if the leader dies.
func (s *Service) StartChannel(channel *IMChannel) error {
	if s.stopped.Load() {
		return fmt.Errorf("im service is stopped")
	}
	if !channel.Enabled {
		return ErrChannelDisabled
	}

	s.mu.Lock()
	s.stopLeaderRetryLocked(channel.ID)
	factory, ok := s.adapterFactories[channel.Platform]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("no adapter factory for platform: %s", channel.Platform)
	}
	// Stop existing channel if running
	if existing, ok := s.channels[channel.ID]; ok {
		s.stopChannelLocked(channel.ID, existing)
	}
	s.mu.Unlock()

	// For WebSocket / long-poll channels, try leader election to avoid
	// duplicate connections. Only one instance should actively poll or
	// maintain a persistent connection for each channel.
	if (channel.Mode == "websocket" || channel.Mode == "longpoll") && s.redis != nil {
		acquired := s.tryAcquireWSLeader(channel.ID)
		if !acquired {
			logger.Infof(context.Background(),
				"[IM] Channel %s %s owned by another instance, will retry", channel.ID, channel.Mode)
			s.scheduleWSLeaderRetry(channel)
			return nil
		}
	}

	return s.startChannelInternal(channel, factory)
}

// startChannelInternal does the actual adapter creation and registration.
func (s *Service) startChannelInternal(channel *IMChannel, factory AdapterFactory) error {
	// Build the message handler that delegates to HandleMessage with this channel's config
	msgHandler := func(msgCtx context.Context, msg *IncomingMessage) error {
		return s.HandleMessage(msgCtx, msg, channel.ID)
	}

	ctx := context.Background()
	adapter, cancelFn, err := factory(ctx, channel, msgHandler)
	if err != nil {
		s.releaseWSLeader(channel.ID) // release lock on failure
		return fmt.Errorf("create adapter: %w", err)
	}
	cleanup := func() {
		if cancelFn != nil {
			cancelFn()
		}
		s.releaseWSLeader(channel.ID)
	}

	// Start leader renewal goroutine for WebSocket / long-poll channels.
	var leaderCancel context.CancelFunc
	if (channel.Mode == "websocket" || channel.Mode == "longpoll") && s.redis != nil {
		leaderCtx, lCancel := context.WithCancel(context.Background())
		leaderCancel = lCancel
		go s.wsLeaderRenewLoop(leaderCtx, channel.ID)
	}

	s.mu.Lock()
	fresh, stateErr := s.GetChannelByID(channel.ID)
	if stateErr != nil || !fresh.Enabled || !sameChannelRuntimeConfig(channel, fresh) {
		s.mu.Unlock()
		if leaderCancel != nil {
			leaderCancel()
		}
		cleanup()
		if stateErr != nil {
			return stateErr
		}
		if !fresh.Enabled {
			return ErrChannelDisabled
		}
		return fmt.Errorf("channel configuration changed while starting")
	}
	// Stop() may have drained the channel map while the factory was connecting
	// above, since the factory runs unlocked. Re-check under the lock, otherwise
	// this adapter's long connection would outlive process shutdown.
	if s.stopped.Load() {
		s.mu.Unlock()
		if leaderCancel != nil {
			leaderCancel()
		}
		cleanup()
		return fmt.Errorf("im service is stopped")
	}
	// Idempotency: another goroutine may have started this channel while
	// factory was running above (factory is called unlocked). Stop the old
	// state before overwriting so its adapter / long connection doesn't leak.
	if existing, ok := s.channels[channel.ID]; ok {
		s.stopChannelLocked(channel.ID, existing)
	}
	s.channels[channel.ID] = &channelState{
		Channel:      fresh,
		Adapter:      adapter,
		Cancel:       cancelFn,
		leaderCancel: leaderCancel,
	}
	s.mu.Unlock()

	return nil
}

// StopChannel stops and removes a running channel.
func (s *Service) StopChannel(channelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLeaderRetryLocked(channelID)
	if cs, ok := s.channels[channelID]; ok {
		s.stopChannelLocked(channelID, cs)
	}
}

func (s *Service) stopLeaderRetryLocked(channelID string) {
	if retry, ok := s.leaderRetries[channelID]; ok {
		retry.cancel()
		delete(s.leaderRetries, channelID)
	}
}

// stopChannelLocked stops a channel and removes it from the map.
// Caller must hold s.mu.
func (s *Service) stopChannelLocked(channelID string, cs *channelState) {
	if cs.leaderCancel != nil {
		cs.leaderCancel()
	}
	if cs.Cancel != nil {
		cs.Cancel()
	}
	delete(s.channels, channelID)
	// For long-poll channels, do NOT release the leader lock immediately.
	// Let it expire naturally via TTL so the old poll goroutine has time to
	// fully drain before another instance takes over. This prevents a brief
	// dual-writer window where both old and new instances process messages.
	// For websocket channels, the connection closes synchronously, so
	// immediate release is safe.
	if cs.Channel != nil && cs.Channel.Mode == "longpoll" {
		logger.Infof(context.Background(), "[IM] Stopped longpoll channel: id=%s (leader lock will expire via TTL)", channelID)
	} else {
		s.releaseWSLeader(channelID)
		logger.Infof(context.Background(), "[IM] Stopped channel: id=%s", channelID)
	}
}

// ── WebSocket leader election ───────────────────────────────────────────────

// tryAcquireWSLeader attempts to acquire the Redis lock for a WebSocket channel.
// Returns true if this instance is now the leader.
func (s *Service) tryAcquireWSLeader(channelID string) bool {
	if s.redis == nil {
		return true // single-instance mode: always leader
	}
	key := RedisKeyLeader + channelID
	ok, err := s.redis.SetNX(context.Background(), key, s.instanceID, wsLeaderTTL).Result()
	if err != nil {
		logger.Warnf(context.Background(), "[IM] Redis leader election failed for %s: %v; connection will retry without taking leadership", channelID, err)
		return false
	}
	return ok
}

// releaseWSLeader releases the Redis leader lock for a WebSocket channel,
// but only if this instance owns it.
func (s *Service) releaseWSLeader(channelID string) {
	if s.redis == nil {
		return
	}
	key := RedisKeyLeader + channelID
	// Only delete if we own it (compare-and-delete via Lua).
	script := redis.NewScript(`
		if redis.call('GET', KEYS[1]) == ARGV[1] then
			return redis.call('DEL', KEYS[1])
		end
		return 0
	`)
	script.Run(context.Background(), s.redis, []string{key}, s.instanceID)
}

// wsLeaderRenewLoop periodically refreshes the leader lock TTL.
// Stops when ctx is cancelled (channel stopped) or if the lock is lost.
func (s *Service) wsLeaderRenewLoop(ctx context.Context, channelID string) {
	key := RedisKeyLeader + channelID
	ticker := time.NewTicker(wsLeaderRenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Only renew if we still own the lock.
			script := redis.NewScript(`
				if redis.call('GET', KEYS[1]) == ARGV[1] then
					redis.call('PEXPIRE', KEYS[1], ARGV[2])
					return 1
				end
				return 0
			`)
			result, err := script.Run(ctx, s.redis, []string{key}, s.instanceID, wsLeaderTTL.Milliseconds()).Int64()
			if err != nil || result == 0 {
				logger.Warnf(context.Background(),
					"[IM] Lost leadership for channel %s, stopping adapter and scheduling recovery", channelID)
				s.handleWSLeadershipLoss(channelID)
				return
			}
			// Still the leader — verify the channel is still active. A
			// delete/disable is served by whichever instance got the HTTP
			// request; without this check the leader would keep the long
			// connection open until process restart. The renew interval
			// bounds the worst-case lag.
			ch, err := s.GetChannelByID(channelID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				logger.Infof(context.Background(),
					"[IM] Channel %s deleted; leader stepping down", channelID)
				s.StopChannel(channelID)
				return
			}
			if err != nil {
				// Transient DB error — don't stop a possibly-healthy channel
				// on a DB hiccup. Skip this round; the next renewal re-checks.
				logger.Warnf(context.Background(),
					"[IM] DB check failed for channel %s during leader renewal: %v (skipping this round)", channelID, err)
				continue
			}
			if !ch.Enabled {
				logger.Infof(context.Background(),
					"[IM] Channel %s disabled; leader stepping down", channelID)
				s.StopChannel(channelID)
				return
			}
			_, cached, running := s.GetChannelAdapter(channelID)
			if !running {
				return
			}
			if !sameChannelRuntimeConfig(cached, ch) {
				logger.Infof(context.Background(),
					"[IM] Channel %s config changed; rebuilding runtime", channelID)
				// StartChannel synchronously stops the old runtime, releases its
				// lease, and then competes to start the fresh configuration. Return
				// because the old renewal context has been cancelled.
				if err := s.StartChannel(ch); err != nil && !s.stopped.Load() {
					logger.Warnf(context.Background(),
						"[IM] Rebuild changed channel %s failed: %v", channelID, err)
				}
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// handleWSLeadershipLoss tears down the adapter that no longer owns its lease
// and puts the enabled channel back into the existing takeover loop. The retry
// loop re-reads the durable channel row before reconnecting, so a concurrent
// delete, disable, or config update cannot resurrect a stale runtime.
func (s *Service) handleWSLeadershipLoss(channelID string) {
	_, channel, running := s.GetChannelAdapter(channelID)
	if !running || channel == nil {
		return
	}

	s.StopChannel(channelID)
	if s.stopped.Load() {
		return
	}
	s.scheduleWSLeaderRetry(channel)
}

func (s *Service) scheduleWSLeaderRetry(channel *IMChannel) {
	retryCtx, cancel := context.WithCancel(context.Background())
	state := &leaderRetryState{cancel: cancel}
	s.mu.Lock()
	if existing, ok := s.leaderRetries[channel.ID]; ok {
		existing.cancel()
	}
	if s.stopped.Load() {
		s.mu.Unlock()
		cancel()
		return
	}
	s.leaderRetries[channel.ID] = state
	s.mu.Unlock()
	go s.wsLeaderRetryLoop(retryCtx, channel, state)
}

// wsLeaderRetryLoop periodically tries to acquire the WebSocket leader lock.
// When it succeeds, it starts the channel adapter. A per-channel retry state
// ensures repeated config events cannot accumulate duplicate retry goroutines.
func (s *Service) wsLeaderRetryLoop(ctx context.Context, channel *IMChannel, state *leaderRetryState) {
	ticker := time.NewTicker(wsLeaderRetryInterval)
	defer ticker.Stop()
	defer func() {
		s.mu.Lock()
		if s.leaderRetries[channel.ID] == state {
			delete(s.leaderRetries, channel.ID)
		}
		s.mu.Unlock()
	}()

	for {
		select {
		case <-ticker.C:
			// Check if channel is already running (another goroutine may have started it).
			if _, _, ok := s.GetChannelAdapter(channel.ID); ok {
				return
			}
			if s.tryAcquireWSLeader(channel.ID) {
				// Re-check the DB before starting: the channel may have been
				// deleted or disabled on another instance while we waited for
				// leadership. The in-memory `channel` is a startup snapshot and
				// won't reflect that, so without this guard we'd resurrect a
				// stopped channel (and reopen its long connection).
				fresh, err := s.GetChannelByID(channel.ID)
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// Channel was actually deleted — give up for good.
					s.releaseWSLeader(channel.ID)
					logger.Infof(context.Background(),
						"[IM] Channel %s deleted while waiting for leadership; aborting leader takeover", channel.ID)
					return
				}
				if err != nil {
					// Transient DB error — don't make a destructive decision.
					// Release the lock for this round and retry on the next tick.
					s.releaseWSLeader(channel.ID)
					logger.Warnf(context.Background(),
						"[IM] DB check failed for channel %s during leader takeover: %v (will retry)", channel.ID, err)
					continue
				}
				if !fresh.Enabled {
					s.releaseWSLeader(channel.ID)
					logger.Infof(context.Background(),
						"[IM] Channel %s disabled while waiting for leadership; aborting leader takeover", channel.ID)
					return
				}
				channel = fresh // use latest config (credentials/mode may have changed)
				logger.Infof(context.Background(),
					"[IM] Acquired leadership for channel %s, starting adapter", channel.ID)
				s.mu.RLock()
				factory, ok := s.adapterFactories[channel.Platform]
				s.mu.RUnlock()
				if !ok {
					return
				}
				if err := s.startChannelInternal(channel, factory); err != nil {
					logger.Warnf(context.Background(),
						"[IM] Failed to start channel %s after acquiring leadership: %v", channel.ID, err)
				}
				return
			}
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		}
	}
}

// ── Cross-instance /stop via StreamManager ───────────────────────────────────
//
// The mechanism mirrors the web StopSession flow:
//   1. /stop writes a stop StreamEvent to StreamManager (keyed by sessionID + messageID)
//   2. A per-request watcher polls StreamManager and cancels the context on detection
//
// A Redis marker (im:stop:{userKey}) is kept as a lightweight pre-execution
// check for requests that haven't created an assistant message yet.

// checkAndClearStopMarker checks if a pre-execution /stop marker exists for
// the given userKey. If found, it deletes the marker and returns true.
func (s *Service) checkAndClearStopMarker(ctx context.Context, userKey string) bool {
	if s.redis == nil {
		return false
	}
	stopKey := RedisKeyStop + userKey
	deleted, err := s.redis.Del(ctx, stopKey).Result()
	if err != nil {
		return false
	}
	return deleted > 0
}

// storeInflightMapping writes the (sessionID, assistantMessageID) to Redis so
// that /stop on any instance can look it up and write to StreamManager.
func (s *Service) storeInflightMapping(ctx context.Context, userKey, sessionID, messageID string) {
	if s.redis == nil {
		return
	}
	val := sessionID + ":" + messageID
	if err := s.redis.Set(ctx, RedisKeyInflight+userKey, val, 10*time.Minute).Err(); err != nil {
		logger.Warnf(ctx, "[IM] Failed to store inflight mapping: %v", err)
	}
}

// clearInflightMapping removes the inflight mapping from Redis.
func (s *Service) clearInflightMapping(ctx context.Context, userKey string) {
	if s.redis == nil {
		return
	}
	s.redis.Del(ctx, RedisKeyInflight+userKey)
}

// loadInflightMapping retrieves (sessionID, messageID) from Redis.
func (s *Service) loadInflightMapping(ctx context.Context, userKey string) (sessionID, messageID string, ok bool) {
	if s.redis == nil {
		return "", "", false
	}
	val, err := s.redis.Get(ctx, RedisKeyInflight+userKey).Result()
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(val, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// writeStopEvent writes a stop event to StreamManager, matching the web
// StopSession pattern. The QA watcher goroutine detects it and cancels.
func (s *Service) writeStopEvent(ctx context.Context, sessionID, messageID string) {
	stopEvt := interfaces.StreamEvent{
		ID:        fmt.Sprintf("stop-%d", time.Now().UnixNano()),
		Type:      types.ResponseType(event.EventStop),
		Content:   "",
		Done:      true,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"session_id": sessionID,
			"message_id": messageID,
			"reason":     "user_requested",
			"source":     "im",
		},
	}
	if err := s.streamManager.AppendEvent(ctx, sessionID, messageID, stopEvt); err != nil {
		logger.Warnf(ctx, "[IM] Failed to write stop event to StreamManager: %v", err)
	}
}

// cancelBindingRequests stops queued and active work for a revoked binding on
// this instance and signals any active worker on another instance.
func (s *Service) cancelBindingRequests(bindingID string) {
	if bindingID == "" {
		return
	}
	requestKeys := []string{bindingID}
	var contextKeys []string
	if err := s.db.Unscoped().Model(&ChannelSession{}).
		Distinct("context_key").
		Where("binding_id = ? AND context_key != ''", bindingID).
		Pluck("context_key", &contextKeys).Error; err != nil {
		logger.Warnf(context.Background(), "[IM] Failed to load binding contexts for cancellation: %v", err)
	}
	for _, contextKey := range contextKeys {
		requestKeys = append(requestKeys, bindingRequestKeyForContext(bindingID, contextKey))
	}

	ctx := context.Background()
	for _, requestKey := range requestKeys {
		if s.qaQueue != nil {
			for s.qaQueue.Remove(requestKey) {
			}
		}
		if raw, loaded := s.inflight.LoadAndDelete(requestKey); loaded {
			raw.(*inflightEntry).cancel()
		}
		if sessionID, messageID, ok := s.loadInflightMapping(ctx, requestKey); ok {
			s.writeStopEvent(ctx, sessionID, messageID)
		}
		if s.redis != nil {
			_ = s.redis.Set(ctx, RedisKeyStop+requestKey, "1", stopMarkerTTL).Err()
		}
	}
}

// watchStreamManagerStop polls StreamManager for stop events and cancels the
// QA context when one is detected. This is the IM equivalent of the web SSE
// handler's stop detection loop. Exits when ctx is done.
func (s *Service) watchStreamManagerStop(ctx context.Context, sessionID, messageID string, cancel context.CancelFunc) {
	ticker := time.NewTicker(stopPollInterval)
	defer ticker.Stop()

	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			events, newOffset, err := s.streamManager.GetEvents(ctx, sessionID, messageID, offset)
			if err != nil {
				continue
			}
			for _, evt := range events {
				if evt.Type == types.ResponseType(event.EventStop) {
					logger.Infof(ctx, "[IM] Stop event from StreamManager, cancelling: session=%s message=%s",
						sessionID, messageID)
					cancel()
					return
				}
			}
			offset = newOffset
		}
	}
}

// GetChannelAdapter returns the adapter and channel config for a given channel ID.
func (s *Service) GetChannelAdapter(channelID string) (Adapter, *IMChannel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.channels[channelID]
	if !ok {
		return nil, nil, false
	}
	return cs.Adapter, cs.Channel, true
}

// EnsureChannelAdapter loads the durable channel row before serving a webhook
// callback. This prevents a replica that missed an invalidation event from
// verifying or processing the callback with stale credentials/configuration.
func (s *Service) EnsureChannelAdapter(channelID string) (Adapter, *IMChannel, error) {
	fresh, err := s.GetChannelByID(channelID)
	if err != nil {
		// A deleted channel may still exist in this replica's runtime map. Do
		// not tear down a healthy runtime on a transient database failure.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.StopChannel(channelID)
		}
		return nil, nil, err
	}
	if !fresh.Enabled {
		s.StopChannel(channelID)
		return nil, fresh, ErrChannelDisabled
	}

	adapter, cached, ok := s.GetChannelAdapter(channelID)
	if !ok || !sameChannelRuntimeConfig(cached, fresh) {
		if err := s.StartChannel(fresh); err != nil {
			return nil, fresh, err
		}
		adapter, cached, ok = s.GetChannelAdapter(channelID)
		if !ok {
			return nil, fresh, fmt.Errorf("channel adapter is not active on this instance")
		}
	}
	return adapter, cached, nil
}

// sameChannelRuntimeConfig compares every field that affects adapter creation
// or message routing. It intentionally does not compare UpdatedAt: PostgreSQL
// timestamp precision can differ from the in-memory value assigned by GORM,
// which would otherwise rebuild webhook adapters on every callback.
func sameChannelRuntimeConfig(cached, fresh *IMChannel) bool {
	if cached == nil || fresh == nil {
		return false
	}
	return cached.ID == fresh.ID &&
		cached.Platform == fresh.Platform &&
		cached.Enabled == fresh.Enabled &&
		cached.Mode == fresh.Mode &&
		cached.OutputMode == fresh.OutputMode &&
		cached.AllowGroupChat == fresh.AllowGroupChat &&
		equalChannelCredentials(cached.Credentials, fresh.Credentials)
}

func equalChannelCredentials(left, right types.JSON) bool {
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return string(left) == string(right)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// GetChannelByID loads a channel from the database.
func (s *Service) GetChannelByID(channelID string) (*IMChannel, error) {
	var ch IMChannel
	if err := s.db.Where("id = ? AND deleted_at IS NULL", channelID).First(&ch).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

// isDuplicate checks if a message has already been processed.
//
// Multi-instance mode (Redis available): uses Redis SetNX for cross-instance
// deduplication. If Redis fails, returns true (fail-closed) to prevent
// duplicate processing across instances — a dropped message can be retried
// by the user, but a duplicate LLM response wastes resources and confuses.
//
// Single-instance mode (no Redis): uses a local sync.Map, which is sufficient
// when only one instance receives messages.
func (s *Service) isDuplicate(ctx context.Context, messageID string) bool {
	if s.redis != nil {
		key := RedisKeyDedup + messageID
		ok, err := s.redis.SetNX(ctx, key, "1", dedupTTL).Result()
		if err == nil {
			return !ok // SetNX returns true when key was newly set (not a duplicate)
		}
		// Redis is configured but failed — fail-closed to avoid cross-instance
		// duplicate processing. The user can simply resend the message.
		logger.Errorf(ctx, "[IM] Redis dedup failed (fail-closed, message dropped): %v", err)
		return true
	}
	// Single-instance mode: local dedup is sufficient.
	_, loaded := s.processedMsgs.LoadOrStore(messageID, time.Now())
	return loaded
}

// HandleMessage processes an incoming IM message end-to-end using channel config.
func (s *Service) HandleMessage(ctx context.Context, msg *IncomingMessage, channelID string) error {
	if msg == nil {
		return errors.New("incoming message is nil")
	}

	// Load the system-managed bot installation before deriving any message key.
	adapter, channel, ok := s.GetChannelAdapter(channelID)
	if !ok {
		ch, err := s.GetChannelByID(channelID)
		if err != nil {
			return fmt.Errorf("channel not found: %s", channelID)
		}
		if err := s.StartChannel(ch); err != nil {
			return fmt.Errorf("start channel %s: %w", channelID, err)
		}
		adapter, channel, ok = s.GetChannelAdapter(channelID)
		if !ok {
			return fmt.Errorf("channel adapter not available after start: %s", channelID)
		}
	}

	if msg.MessageID != "" {
		dedupKey := fmt.Sprintf("%s:%s:%s:%s:%s", channelID, msg.Platform, msg.UserID, msg.ChatID, msg.MessageID)
		if s.isDuplicate(ctx, dedupKey) {
			logger.Infof(ctx, "[IM] Skipping duplicate message: channel=%s message=%s", channelID, msg.MessageID)
			return nil
		}
	}

	if !isStopCommand(msg.Content) {
		rateLimitKey := channelID + ":" + msg.UserID
		if !s.rateLimiter.Allow(ctx, rateLimitKey, s.rateLimitMax) {
			logger.Warnf(ctx, "[IM] Rate limited: channel=%s user=%s", channelID, msg.UserID)
			_ = adapter.SendReply(ctx, msg, &ReplyMessage{
				Content: "您的消息发送过于频繁，请稍后再试。",
				IsFinal: true,
			})
			return nil
		}
	}

	if !SupportsPersonalBinding(channel.Platform) {
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: "当前渠道尚未开放账号绑定。",
			IsFinal: true,
		})
		return nil
	}
	if msg.ChatType == ChatTypeGroup &&
		(!channel.AllowGroupChat || !SupportsGroupChat(channel.Platform, channel.Mode)) {
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: "当前机器人未开启群聊问答，请在机器人单聊中使用。",
			IsFinal: true,
		})
		return nil
	}
	chatType, externalID, externalName, err := bindingIdentity(msg)
	if err != nil {
		return err
	}

	if bindingCode, ok := messageBindingCode(msg.Content, chatType); ok {
		return s.handleBindingCode(ctx, adapter, channelID, chatType, externalID, externalName, bindingCode, msg)
	}

	binding, member, err := s.ResolveActiveBinding(ctx, channelID, chatType, externalID)
	if err != nil {
		systemName := s.systemName(ctx)
		content := fmt.Sprintf("尚未绑定「%s」账号。请先登录「%s」，在 IM 绑定页面选择工作空间，再将生成的绑定码发送给我。",
			systemName, systemName)
		if chatType == ChatTypeGroup {
			content = fmt.Sprintf("当前群尚未绑定知识库账号。请使用该群的专用账号登录「%s」生成绑定码，并在本群发送给机器人。",
				systemName)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			if errors.Is(err, ErrBindingMembershipInvalid) {
				content = fmt.Sprintf("绑定的账号或工作空间权限已失效，请登录「%s」重新绑定。", systemName)
			} else {
				return fmt.Errorf("resolve active binding: %w", err)
			}
		}
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{Content: content, IsFinal: true})
		return nil
	}

	logger.Infof(ctx, "[IM] HandleMessage: channel=%s platform=%s user=%s chat=%s msgtype=%s content_len=%d",
		channelID, msg.Platform, msg.UserID, msg.ChatID, msg.MessageType, len(msg.Content))

	contentRunes := []rune(msg.Content)
	if len(contentRunes) > maxContentLength {
		logger.Warnf(ctx, "[IM] Message too long (%d runes), truncating to %d", len(contentRunes), maxContentLength)
		msg.Content = string(contentRunes[:maxContentLength])
	}

	// ── File/Image message handling ──
	// Attachments are question context only and are never ingested automatically.
	if msg.MessageType == MessageTypeFile || msg.MessageType == MessageTypeImage {
		msg.Content = fileMessageQAContent(msg)
	}

	// Never send an empty query into rewrite/intent classification. Some IM
	// platforms deliver unsupported rich message shapes with no normalized text;
	// allowing those through makes the model infer an unrelated intent from an
	// empty query (for example, rewriting it as a greeting).
	if hint, empty := emptyIncomingMessageReply(msg); empty {
		logger.Infof(ctx, "[IM] Skipping QA for message without content: type=%s raw_type=%s",
			msg.MessageType, msg.Extra["raw_msgtype"])
		if err := adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: hint,
			IsFinal: true,
		}); err != nil {
			logger.Warnf(ctx, "[IM] Failed to send empty-message hint reply: %v", err)
		}
		return nil
	}

	tenant, err := s.tenantService.GetTenantByID(ctx, binding.TenantID)
	if err != nil {
		return fmt.Errorf("get tenant: %w", err)
	}
	sessionCtx := context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	sessionCtx = withIMIdentity(sessionCtx, binding, member)

	kbIDs, err := s.resolveBindingKnowledgeBaseIDs(sessionCtx, binding.TenantID)
	if err != nil {
		return fmt.Errorf("resolve binding knowledge bases: %w", err)
	}

	channelSession, err := s.resolveBindingSession(sessionCtx, msg, binding, channelID)
	if err != nil {
		return fmt.Errorf("resolve session: %w", err)
	}

	// ── Slash-command dispatch ──
	if cmd, args, ok := s.cmdRegistry.Parse(msg.Content); ok {
		return s.handleCommand(
			sessionCtx, cmd, args, msg, adapter, channel, channelSession, binding, tenant.Name, kbIDs,
		)
	}
	// Unrecognised slash-word: show help hint instead of sending to QA.
	if LooksLikeCommand(msg.Content) {
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: "未知指令，发送 `/help` 查看所有可用指令。",
			IsFinal: true,
		})
		return nil
	}

	if len(kbIDs) == 0 {
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: "当前工作空间没有可用于问答的知识库。",
			IsFinal: true,
		})
		return nil
	}

	session, err := s.sessionService.GetSession(sessionCtx, channelSession.SessionID)
	if err != nil {
		// The underlying session may have been deleted from the UI while the
		// ChannelSession mapping still exists (GORM soft-delete does not trigger
		// SQL ON DELETE CASCADE). Recover by soft-deleting the stale mapping and
		// re-creating a fresh session so the IM bot doesn't become permanently
		// unresponsive. (fixes #1046, #1499)
		if isSessionNotFound(err) {
			logger.Warnf(ctx, "[IM] Session %s not found (deleted?), recycling stale channel session %s",
				channelSession.SessionID, channelSession.ID)
			if delErr := s.db.Delete(&ChannelSession{}, "id = ?", channelSession.ID).Error; delErr != nil {
				logger.Warnf(ctx, "[IM] Failed to delete stale channel session %s: %v", channelSession.ID, delErr)
			}
			channelSession, err = s.resolveBindingSession(sessionCtx, msg, binding, channelID)
			if err != nil {
				return fmt.Errorf("resolve session (retry): %w", err)
			}
			session, err = s.sessionService.GetSession(sessionCtx, channelSession.SessionID)
			if err != nil {
				return fmt.Errorf("get session (retry): %w", err)
			}
		} else {
			return fmt.Errorf("get session: %w", err)
		}
	}

	// Title an untitled IM session from its first text message, like web chats.
	// GenerateTitleAsync self-guards on a non-empty title and persists to the DB;
	// nil eventBus is fine (IM has no live stream — the sidebar reloads it).
	if session.Title == "" && strings.TrimSpace(msg.Content) != "" {
		sessionForTitle := *session
		s.sessionService.GenerateTitleAsync(sessionCtx, &sessionForTitle, msg.Content, "", nil)
	}

	s.persistIMLastRequestState(sessionCtx, session.ID, "", nil, kbIDs)

	// 5. Enqueue the QA request into the bounded worker pool.
	// The worker pool controls LLM concurrency and provides backpressure.
	qaCtx, qaCancel := context.WithCancel(sessionCtx)
	userKey := bindingRequestKey(binding.ID, msg)

	req := &qaRequest{
		ctx:       qaCtx,
		cancel:    qaCancel,
		msg:       msg,
		session:   session,
		agent:     nil,
		adapter:   adapter,
		channel:   channel,
		channelID: channelID,
		bindingID: binding.ID,
		kbIDs:     append([]string(nil), kbIDs...),
		tenant:    tenant,
		userKey:   userKey,
	}

	pos, enqueueErr := s.qaQueue.Enqueue(req)
	if enqueueErr != nil {
		qaCancel()
		logger.Warnf(ctx, "[IM] Queue rejected: user=%s reason=%v", msg.UserID, enqueueErr)
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: "当前排队人数较多，请稍后再试。",
			IsFinal: true,
		})
		return nil
	}

	if pos > 0 {
		logger.Infof(ctx, "[IM] Enqueued: user=%s pos=%d depth=%d", msg.UserID, pos, s.qaQueue.Metrics().Depth)
		// In multi-instance mode the local queue position does not reflect global
		// depth, so use a generic "queued" hint instead of an exact number.
		queueMsg := fmt.Sprintf("收到，前面还有 %d 条消息在处理，请稍候 ⏳", pos)
		if s.redis != nil {
			queueMsg = "收到，当前排队中，请稍候 ⏳"
		}
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: queueMsg,
			IsFinal: true,
		})
	} else {
		logger.Infof(ctx, "[IM] Enqueued: user=%s pos=0 (immediate)", msg.UserID)
	}

	return nil
}

func isStopCommand(content string) bool {
	fields := strings.Fields(strings.TrimSpace(content))
	return len(fields) > 0 && strings.EqualFold(fields[0], "/stop")
}

func bindingIdentity(msg *IncomingMessage) (ChatType, string, string, error) {
	if msg == nil {
		return "", "", "", errors.New("incoming message is nil")
	}
	switch msg.ChatType {
	case ChatTypeDirect:
		if strings.TrimSpace(msg.UserID) == "" {
			return "", "", "", errors.New("direct message user id is empty")
		}
		return ChatTypeDirect, strings.TrimSpace(msg.UserID), strings.TrimSpace(msg.UserName), nil
	case ChatTypeGroup:
		if strings.TrimSpace(msg.ChatID) == "" {
			return "", "", "", errors.New("group message chat id is empty")
		}
		if strings.TrimSpace(msg.UserID) == "" {
			return "", "", "", errors.New("group message user id is empty")
		}
		return ChatTypeGroup, strings.TrimSpace(msg.ChatID), "", nil
	default:
		return "", "", "", fmt.Errorf("unsupported IM chat type: %s", msg.ChatType)
	}
}

func (s *Service) handleBindingCode(
	ctx context.Context,
	adapter Adapter,
	channelID string,
	chatType ChatType,
	externalID string,
	externalName string,
	bindingCode string,
	msg *IncomingMessage,
) error {
	_, err := s.ConsumeBindingCode(ctx, channelID, chatType, externalID, externalName, bindingCode)
	systemName := s.systemName(ctx)
	content := fmt.Sprintf("绑定成功，后续将使用你在「%s」中选择的工作空间。", systemName)
	if chatType == ChatTypeGroup {
		content = fmt.Sprintf("群聊绑定成功。群成员将使用当前账号在「%s」中选择的工作空间，并分别维护对话上下文。", systemName)
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrBindingCodeInvalid):
			content = fmt.Sprintf("绑定码无效或已过期，请登录「%s」重新生成。", systemName)
		case errors.Is(err, ErrBindingIdentityInUse):
			content = fmt.Sprintf("当前 IM 账号已绑定其他「%s」账号，请先解除原绑定。", systemName)
			if chatType == ChatTypeGroup {
				content = fmt.Sprintf("当前群已绑定其他「%s」账号，请先解除原绑定。", systemName)
			}
		case errors.Is(err, ErrBindingUserInUse):
			content = fmt.Sprintf("当前「%s」账号已绑定该机器人下的其他私聊或群聊，请先解除原绑定。", systemName)
		case errors.Is(err, ErrBindingMembershipInvalid):
			content = fmt.Sprintf("账号或工作空间权限已失效，请返回「%s」重新选择。", systemName)
		case errors.Is(err, ErrBindingUnavailable):
			content = "当前机器人暂不可绑定，请联系系统管理员。"
		default:
			return fmt.Errorf("consume binding code: %w", err)
		}
	}
	if sendErr := adapter.SendReply(ctx, msg, &ReplyMessage{Content: content, IsFinal: true}); sendErr != nil {
		return fmt.Errorf("send binding result: %w", sendErr)
	}
	return nil
}

// messageBindingCode accepts an exact binding code in private chat. Group
// clients may retain an @mention prefix, so the final token is also checked.
func messageBindingCode(content string, chatType ChatType) (string, bool) {
	content = strings.TrimSpace(content)
	if IsBindingCode(content) {
		return content, true
	}
	if chatType != ChatTypeGroup {
		return "", false
	}
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return "", false
	}
	candidate := fields[len(fields)-1]
	return candidate, IsBindingCode(candidate)
}

func (s *Service) resolveBindingKnowledgeBaseIDs(ctx context.Context, tenantID uint64) ([]string, error) {
	var kbs []*types.KnowledgeBase
	if err := s.db.WithContext(ctx).
		Select("id", "type", "indexing_strategy").
		Where("tenant_id = ? AND is_temporary = ?", tenantID, false).
		Order("created_at DESC").
		Find(&kbs).Error; err != nil {
		return nil, err
	}

	organizations, err := s.organizationSvc.ListTenantOrganizations(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	organizationIDs := make([]string, 0, len(organizations))
	for _, organization := range organizations {
		if organization != nil {
			organizationIDs = append(organizationIDs, organization.ID)
		}
	}
	if len(organizationIDs) > 0 {
		sharedIDsByOrganization, err := s.kbShareService.ListSharedKnowledgeBaseIDsByOrganizations(
			ctx,
			organizationIDs,
			tenantID,
		)
		if err != nil {
			return nil, err
		}
		sharedIDSet := make(map[string]struct{})
		sharedIDs := make([]string, 0)
		for _, organizationID := range organizationIDs {
			for _, kbID := range sharedIDsByOrganization[organizationID] {
				if _, exists := sharedIDSet[kbID]; exists {
					continue
				}
				sharedIDSet[kbID] = struct{}{}
				sharedIDs = append(sharedIDs, kbID)
			}
		}
		if len(sharedIDs) > 0 {
			sharedKBs, err := s.kbService.GetKnowledgeBasesByIDsOnly(ctx, sharedIDs)
			if err != nil {
				return nil, err
			}
			kbs = append(kbs, sharedKBs...)
		}
	}

	filter := agenttools.DeriveKBFilterForAgent(types.AgentModeQuickAnswer, nil)
	ids := make([]string, 0, len(kbs))
	seen := make(map[string]struct{}, len(kbs))
	for _, kb := range kbs {
		if kb == nil || kb.IsTemporary {
			continue
		}
		if _, exists := seen[kb.ID]; exists {
			continue
		}
		kb.EnsureDefaults()
		if !filter.IsEmpty() &&
			!agenttools.KBSatisfiesAgentRequirements(kb.Capabilities(), types.AgentModeQuickAnswer, nil) {
			continue
		}
		seen[kb.ID] = struct{}{}
		ids = append(ids, kb.ID)
	}
	return ids, nil
}

func emptyIncomingMessageReply(msg *IncomingMessage) (string, bool) {
	if msg == nil || strings.TrimSpace(msg.Content) != "" {
		return "", false
	}
	// Image/file events are allowed to arrive without a caption. Do not depend
	// on fileMessageQAContent having already filled Content — a later reorder
	// of HandleMessage must not reject attachments.
	hasAttachment := msg.MessageType == MessageTypeFile ||
		msg.MessageType == MessageTypeImage ||
		strings.TrimSpace(msg.FileKey) != ""
	if hasAttachment {
		return "", false
	}
	rawType := ""
	if msg.Extra != nil {
		rawType = strings.ToLower(strings.TrimSpace(msg.Extra["raw_msgtype"]))
	}
	switch rawType {
	case "audio":
		return "未能识别这条语音中的文字内容。请改用纯文本发送，或再说一遍。", true
	case "video":
		return "暂不支持视频消息。请改用纯文本发送；图片或文件请单独发送。", true
	default:
		return "未能识别这条消息中的文字内容。请改用纯文本发送；图片或文件请单独发送。", true
	}
}

func (s *Service) persistIMLastRequestState(ctx context.Context, sessionID, agentID string, customAgent *types.CustomAgent, kbIDs []string) {
	state := buildIMLastRequestState(agentID, customAgent, kbIDs)
	if err := s.sessionService.UpdateSessionLastRequestState(logger.CloneContext(context.WithoutCancel(ctx)), sessionID, state); err != nil {
		logger.Warnf(ctx, "[IM] persist last_request_state failed for session %s: %v", sessionID, err)
	}
}

// executeQARequest is the worker handler that runs the QA pipeline for a queued request.
// It is called by qaQueue workers and must not block indefinitely.
func (s *Service) executeQARequest(req *qaRequest) {
	ctx := req.ctx
	defer req.cancel()
	if _, _, err := s.ValidateActiveBinding(ctx, req.bindingID); err != nil {
		if isInactiveBindingError(err) {
			logger.Infof(ctx, "[IM] Dropping queued request for inactive binding: binding=%s", req.bindingID)
		} else {
			logger.Errorf(ctx, "[IM] Failed to validate binding before queued request: binding=%s: %v",
				req.bindingID, err)
		}
		return
	}
	if req.queueTimedOut {
		if err := req.adapter.SendReply(ctx, req.msg, &ReplyMessage{
			Content: "您的消息等待超时，请重新发送。",
			IsFinal: true,
		}); err != nil {
			logger.Warnf(ctx, "[IM] Failed to send queue timeout reply: %v", err)
		}
		return
	}

	// Track in-flight request so /stop can cancel it.
	entry := &inflightEntry{cancel: req.cancel}
	s.inflight.Store(req.userKey, entry)
	defer s.inflight.Delete(req.userKey)

	// Check if a pre-execution /stop was issued while this request was queued.
	if s.checkAndClearStopMarker(ctx, req.userKey) {
		logger.Infof(ctx, "[IM] Request cancelled by remote /stop before execution: %s", req.userKey)
		return
	}

	// NOTE: StreamManager-based stop detection is started inside handleMessageStream /
	// runQA after the assistant message is created (that's when we have the
	// sessionID + messageID needed to poll StreamManager).

	attachments, imageURLs, err := s.prepareIMAttachments(ctx, req.msg, req.adapter)
	if err != nil {
		logger.Warnf(ctx, "[IM] attachment preparation failed: %v", err)
		if _, _, bindingErr := s.ValidateActiveBinding(ctx, req.bindingID); bindingErr != nil {
			if isInactiveBindingError(bindingErr) {
				logger.Infof(ctx,
					"[IM] Suppressing attachment error reply for inactive binding: binding=%s", req.bindingID)
			} else {
				logger.Errorf(ctx, "[IM] Failed to validate binding before attachment error reply: binding=%s: %v",
					req.bindingID, bindingErr)
			}
			return
		}
		if sendErr := req.adapter.SendReply(ctx, req.msg, &ReplyMessage{Content: "❌ 无法读取此附件，请重试或改用文字描述。", IsFinal: true}); sendErr != nil {
			logger.Warnf(ctx, "[IM] Failed to send attachment error reply: %v", sendErr)
		}
		return
	}

	// Determine output mode from channel config.
	streamDisabled := req.channel.OutputMode == "full"

	// If the adapter supports streaming and output is not "full", use streaming.
	if !streamDisabled {
		if streamer, ok := req.adapter.(StreamSender); ok {
			if err := s.handleMessageStream(ctx, req.msg, req.session, req.agent, req.kbIDs, attachments, imageURLs,
				streamer, req.adapter, req.userKey, req.bindingID, req.tenant); err != nil {
				logger.Errorf(ctx, "[IM] Stream QA failed: %v", err)
			}
			return
		}
	}

	// Non-streaming fallback: collect full answer then send.
	answer, err := s.runQA(
		ctx, req.session, req.msg.Content, req.agent, req.kbIDs,
		attachments, imageURLs, req.userKey, req.msg.Quote,
	)
	if err != nil {
		logger.Errorf(ctx, "[IM] QA failed: %v, sending fallback reply", err)
		answer = "抱歉，处理您的问题时出现了异常，请稍后再试。"
	}

	if _, _, err := s.ValidateActiveBinding(ctx, req.bindingID); err != nil {
		if isInactiveBindingError(err) {
			logger.Infof(ctx, "[IM] Suppressing reply for inactive binding: binding=%s", req.bindingID)
		} else {
			logger.Errorf(ctx, "[IM] Failed to validate binding before reply: binding=%s: %v",
				req.bindingID, err)
		}
		return
	}
	reply := &ReplyMessage{
		Content: formatIMOutboundAnswer(ctx, answer, req.tenant, s.defaultFileSvc, s.storageResolver),
		IsFinal: true,
	}
	if err := req.adapter.SendReply(ctx, req.msg, reply); err != nil {
		logger.Errorf(ctx, "[IM] Send reply failed: %v", err)
		return
	}

	logger.Infof(ctx, "[IM] Reply sent: channel=%s platform=%s user=%s answer_len=%d",
		req.channelID, req.msg.Platform, req.msg.UserID, len(answer))
}

// handleCommand executes a slash-command and sends the result back to the user.
// It also handles side effects (ActionClear, ActionStop).
func (s *Service) handleCommand(
	ctx context.Context,
	cmd Command,
	args []string,
	msg *IncomingMessage,
	adapter Adapter,
	channel *IMChannel,
	channelSession *ChannelSession,
	binding *UserBinding,
	tenantName string,
	kbIDs []string,
) error {
	cmdCtx := &CommandContext{
		Incoming:          msg,
		Session:           channelSession,
		TenantID:          binding.TenantID,
		TenantName:        tenantName,
		KnowledgeBaseIDs:  append([]string(nil), kbIDs...),
		ChannelOutputMode: channel.OutputMode,
	}

	result, err := cmd.Execute(ctx, cmdCtx, args)
	if err != nil {
		logger.Errorf(ctx, "[IM] Command /%s error: %v", cmd.Name(), err)
	}
	if _, _, bindingErr := s.ValidateActiveBinding(ctx, binding.ID); bindingErr != nil {
		if !errors.Is(bindingErr, gorm.ErrRecordNotFound) &&
			!errors.Is(bindingErr, ErrBindingMembershipInvalid) {
			return fmt.Errorf("validate binding before command delivery: %w", bindingErr)
		}
		logger.Infof(ctx, "[IM] Suppressing command result for inactive binding: binding=%s: %v",
			binding.ID, bindingErr)
		return nil
	}
	if err != nil {
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: "抱歉，执行指令时出现了异常，请稍后再试。",
			IsFinal: true,
		})
		return err
	}

	// Handle service-level side effects.
	switch result.Action {
	case ActionClear:
		// Soft-delete the current ChannelSession so the next IM message
		// starts a completely fresh WeKnora session. Conversation history
		// is keyed by session ID and rebuilt from DB on demand, so no
		// separate cache invalidation step is needed.
		if err := s.db.Model(&ChannelSession{}).
			Where("id = ?", channelSession.ID).
			Update("deleted_at", time.Now()).Error; err != nil {
			logger.Warnf(ctx, "[IM] Failed to soft-delete channel session: %v", err)
		}
	case ActionStop:
		inflightKey := bindingRequestKey(binding.ID, msg)

		// 1. Try local cancel: remove from queue or cancel in-flight.
		var localSessionID, localMessageID string
		localStopped := s.qaQueue.Remove(inflightKey)
		if localStopped {
			logger.Infof(ctx, "[IM] Cancelled queued QA: key=%s", inflightKey)
		} else if raw, loaded := s.inflight.LoadAndDelete(inflightKey); loaded {
			e := raw.(*inflightEntry)
			e.cancel()
			localStopped = true
			localSessionID = e.sessionID
			localMessageID = e.assistantMessageID
			logger.Infof(ctx, "[IM] Cancelled in-flight QA: key=%s", inflightKey)
		}

		// 2. Write stop event to StreamManager (same as web StopSession).
		//    For local stop with known IDs, write directly.
		//    For cross-instance, look up Redis inflight mapping to get IDs.
		sessionID, messageID := localSessionID, localMessageID
		if sessionID == "" || messageID == "" {
			// Try cross-instance lookup.
			sessionID, messageID, _ = s.loadInflightMapping(ctx, inflightKey)
		}
		if sessionID != "" && messageID != "" {
			s.writeStopEvent(ctx, sessionID, messageID)
			logger.Infof(ctx, "[IM] Wrote stop event to StreamManager: session=%s message=%s", sessionID, messageID)
		}

		// 3. Set Redis marker as fallback for requests not yet executing
		//    (no assistant message yet → no StreamManager entry to poll).
		if s.redis != nil {
			s.redis.Set(ctx, RedisKeyStop+inflightKey, "1", stopMarkerTTL)
		}

		if !localStopped && sessionID == "" {
			logger.Infof(ctx, "[IM] Set cross-instance stop marker (no inflight found): key=%s", inflightKey)
		}
	}

	// Send the command reply, respecting the configured output mode.
	sent := false
	if channel.OutputMode != "full" {
		if streamer, ok := adapter.(StreamSender); ok {
			if err := s.sendStreamReply(ctx, msg, streamer, result.Content); err != nil {
				logger.Warnf(ctx, "[IM] Stream reply for command /%s failed, falling back: %v", cmd.Name(), err)
			} else {
				sent = true
			}
		}
	}
	if !sent {
		_ = adapter.SendReply(ctx, msg, &ReplyMessage{
			Content: result.Content,
			IsFinal: true,
		})
	}

	logger.Infof(ctx, "[IM] Command /%s executed: channel=%s user=%s action=%d",
		cmd.Name(), channel.ID, msg.UserID, result.Action)
	return nil
}

// sendStreamReply sends a complete content string via the streaming interface.
func (s *Service) sendStreamReply(ctx context.Context, msg *IncomingMessage, streamer StreamSender, content string) error {
	streamID, err := streamer.StartStream(ctx, msg)
	if err != nil {
		return fmt.Errorf("start stream: %w", err)
	}
	if err := streamer.UpdateStreamContent(ctx, msg, streamID, content); err != nil {
		return fmt.Errorf("update stream content: %w", err)
	}
	if err := streamer.FinalizeStream(ctx, msg, streamID, content); err != nil {
		return fmt.Errorf("finalize stream: %w", err)
	}
	if err := streamer.EndStream(ctx, msg, streamID); err != nil {
		return fmt.Errorf("end stream: %w", err)
	}
	return nil
}

// isSessionNotFound reports whether err indicates the underlying WeKnora
// session no longer exists. The session repository translates GORM's
// ErrRecordNotFound into apperrors.ErrSessionNotFound, so the application
// sentinel is what GetSession returns today; the GORM check is kept as a
// safety net in case a future repository revert bypasses the translation.
func isSessionNotFound(err error) bool {
	return errors.Is(err, apperrors.ErrSessionNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

// resolveSession dispatches to the appropriate session resolution strategy
// based on the channel's session mode.
func buildBindingSessionTitle(msg *IncomingMessage) string {
	var b strings.Builder
	if msg.UserName != "" {
		b.WriteString(msg.UserName)
	} else if msg.UserID != "" {
		b.WriteString("user ")
		b.WriteString(shortID(msg.UserID))
	} else {
		b.WriteString("user")
	}
	if msg.ChatType == ChatTypeDirect {
		b.WriteString(" · dm")
	} else if msg.ChatType == ChatTypeGroup {
		b.WriteString(" · group")
	}
	return b.String()
}

// shortID returns the last 8 characters of id, or id itself when shorter.
// Used to keep long platform IDs readable inside titles without losing uniqueness.
func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func imInitialSessionTitle(msg *IncomingMessage) string {
	if msg.ChatType == ChatTypeGroup {
		return buildBindingSessionTitle(msg)
	}
	if strings.TrimSpace(msg.Content) != "" {
		return ""
	}
	return buildBindingSessionTitle(msg)
}

func bindingSessionContextKey(msg *IncomingMessage) string {
	if msg != nil && msg.ChatType == ChatTypeGroup {
		return strings.TrimSpace(msg.UserID)
	}
	return ""
}

func bindingRequestKeyForContext(bindingID, contextKey string) string {
	if contextKey == "" {
		return bindingID
	}
	return bindingID + ":" + contextKey
}

func bindingRequestKey(bindingID string, msg *IncomingMessage) string {
	return bindingRequestKeyForContext(bindingID, bindingSessionContextKey(msg))
}

// resolveBindingSession gives each direct binding one current conversation and
// each member of a group binding an independent current conversation.
func (s *Service) resolveBindingSession(
	ctx context.Context,
	msg *IncomingMessage,
	binding *UserBinding,
	imChannelID string,
) (*ChannelSession, error) {
	contextKey := bindingSessionContextKey(msg)
	var cs ChannelSession
	result := s.db.Where("binding_id = ? AND context_key = ? AND deleted_at IS NULL", binding.ID, contextKey).
		First(&cs)

	if result.Error == nil {
		return &cs, nil
	}

	if result.Error != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("query channel session: %w", result.Error)
	}

	newSession := &types.Session{
		TenantID:    binding.TenantID,
		UserID:      binding.UserID,
		Title:       imInitialSessionTitle(msg),
		Description: fmt.Sprintf("Auto-created from %s IM integration", msg.Platform),
	}

	createdSession, err := s.sessionService.CreateSession(ctx, newSession)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	cs = ChannelSession{
		BindingID:   binding.ID,
		SessionID:   createdSession.ID,
		TenantID:    binding.TenantID,
		IMChannelID: imChannelID,
		ContextKey:  contextKey,
	}
	if err := s.db.Create(&cs).Error; err != nil {
		if delErr := s.db.Where("id = ?", createdSession.ID).Delete(createdSession).Error; delErr != nil {
			logger.Warnf(ctx, "[IM] Failed to clean up orphaned session %s: %v", createdSession.ID, delErr)
		}
		var existing ChannelSession
		if findErr := s.db.Where(
			"binding_id = ? AND context_key = ? AND deleted_at IS NULL", binding.ID, contextKey,
		).
			First(&existing).Error; findErr != nil {
			return nil, fmt.Errorf("create channel session: %w (lookup fallback: %v)", err, findErr)
		}
		return &existing, nil
	}

	logger.Infof(ctx, "[IM] Created binding session: binding=%s session=%s", binding.ID, createdSession.ID)

	return &cs, nil
}

// ── Agent tool call progress formatting ──────────────────────────────
// These helpers format tool-call / tool-result events as Markdown text
// that is injected into the streaming reply so IM users can see the
// agent's reasoning process in real-time.
// ─────────────────────────────────────────────────────────────────────

// internalToolNames lists tools whose execution should NOT be displayed in IM
// messages because they are internal reasoning aids (thinking, planning) rather
// than user-facing actions.
var internalToolNames = map[string]bool{
	"thinking":   true,
	"todo_write": true,
}

// isToolVisibleToUser returns true if the tool's execution progress should be
// displayed to the IM user. Internal reasoning tools (thinking, planning) are
// hidden.
func isToolVisibleToUser(toolName string) bool {
	return !internalToolNames[toolName]
}

// briefToolSummary extracts a short human-readable summary from tool output.
// Returns empty string if no suitable summary can be extracted.
func briefToolSummary(output string) string {
	const maxRunes = 40
	if output == "" {
		return ""
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	// Skip structured data (JSON, XML, etc.)
	if output[0] == '{' || output[0] == '[' || output[0] == '<' {
		return ""
	}
	// Take first non-empty line
	if idx := strings.IndexByte(output, '\n'); idx >= 0 {
		output = strings.TrimSpace(output[:idx])
	}
	if output == "" {
		return ""
	}
	runes := []rune(output)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return output
}

// handleMessageStream runs the QA pipeline and streams answer chunks to the IM platform
// in real-time via the StreamSender interface. Chunks are batched at streamFlushInterval
// to avoid API rate-limiting.
func (s *Service) handleMessageStream(
	ctx context.Context,
	msg *IncomingMessage,
	session *types.Session,
	customAgent *types.CustomAgent,
	kbIDs []string,
	attachments types.MessageAttachments,
	imageURLs []string,
	streamer StreamSender,
	adapter Adapter,
	userKey string,
	bindingID string,
	tenant *types.Tenant,
) error {
	if _, _, err := s.ValidateActiveBinding(ctx, bindingID); err != nil {
		return err
	}
	// Start the stream on the IM platform (e.g., create Feishu streaming card)
	streamID, err := streamer.StartStream(ctx, msg)
	if err != nil {
		logger.Warnf(ctx, "[IM] StartStream failed, falling back to non-streaming: %v", err)
		return s.fallbackNonStream(
			ctx, msg, session, customAgent, kbIDs, attachments, imageURLs, adapter, userKey, bindingID, tenant,
		)
	}

	// Prepare the QA pipeline
	// No total deadline: each agent round has its own LLMCallTimeout (default 120s).
	// A hard pipeline deadline would kill multi-round agent reasoning prematurely.
	qaCtx, qaCancel := context.WithCancel(ctx)
	defer qaCancel()

	useAgent := customAgent != nil && customAgent.IsAgentMode()
	eventBus := event.NewEventBus()

	var (
		bufMu           sync.Mutex
		reasoningInner  streamSection   // quick QA: model reasoning_content
		agentInner      streamSection   // agent: retracted text + thoughts
		agentLiveAnswer strings.Builder // agent: optimistic answer before tool retract
		answerOuter     strings.Builder // final answer for display persistence
		answerBuilder   strings.Builder // answer persisted to DB
		qaErr           error
		done            = make(chan struct{})
		completeDone    = make(chan struct{})
		closeOnce       sync.Once
		completeOnce    sync.Once
		agentDone       bool
		assistantMsg    *types.Message

		seenToolCalls = make(map[string]bool)
		agentToolIdx  = make(map[string]int)
		pipelineIdx   = make(map[string]int)

		agentToolSteps    []IMToolStep
		pipelineToolSteps []IMToolStep

		agentCompleteFinalAnswer string
		streamedAny              bool

		// mcpAuthServices collects OAuth services that need out-of-band
		// authorization (IM cannot resolve the in-conversation prompt).
		mcpAuthServices []imMCPAuthService
		mcpAuthSeen     = make(map[string]bool)
	)
	closeDone := func() { closeOnce.Do(func() { close(done) }) }
	closeComplete := func() { completeOnce.Do(func() { close(completeDone) }) }

	agentWrite := func(s string) {
		if s == "" {
			return
		}
		agentInner.write(s)
		streamedAny = true
	}
	reasoningWrite := func(s string) {
		if s == "" {
			return
		}
		reasoningInner.write(s)
		streamedAny = true
	}

	// retractAgentLiveAnswer moves the optimistic answer into the think block (Web: superseded preamble).
	retractAgentLiveAnswer := func() {
		if agentLiveAnswer.Len() == 0 {
			return
		}
		if agentInner.text.Len() > 0 {
			agentInner.ensureNewlineBefore()
		}
		agentInner.write(agentLiveAnswer.String())
		agentLiveAnswer.Reset()
	}

	getStreamParts := func() IMStreamParts {
		mode := IMStreamModeQuickQA
		if useAgent {
			mode = IMStreamModeAgent
		}
		return IMStreamParts{
			Mode:              mode,
			PipelineToolSteps: pipelineToolSteps,
			ReasoningInner:    reasoningInner.text.String(),
			AgentInner:        agentInner.text.String(),
			AgentToolSteps:    agentToolSteps,
			LiveAnswer:        agentLiveAnswer.String(),
			Answer:            answerOuter.String(),
		}
	}

	// Subscribe to answer chunks.
	eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentFinalAnswerData)
		if !ok {
			return nil
		}

		bufMu.Lock()
		if useAgent && !agentDone {
			if data.Content != "" {
				agentLiveAnswer.WriteString(data.Content)
				streamedAny = true
			}
		} else {
			answerOuter.WriteString(data.Content)
			answerBuilder.WriteString(data.Content)
			streamedAny = true
		}
		bufMu.Unlock()

		if data.Done {
			closeDone()
		}
		return nil
	})

	eventBus.On(event.EventError, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ErrorData)
		if !ok {
			return nil
		}
		logger.Errorf(ctx, "[IM] QA stream error: %s", data.Error)
		bufMu.Lock()
		qaErr = fmt.Errorf("QA pipeline error: %s", data.Error)
		bufMu.Unlock()
		closeDone()
		closeComplete()
		return nil
	})

	eventBus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentReferencesData)
		if !ok {
			return nil
		}
		bufMu.Lock()
		if assistantMsg != nil {
			refs := []*types.SearchResult(assistantMsg.KnowledgeReferences)
			collectIMKnowledgeReferences(&refs, data.References)
			assistantMsg.KnowledgeReferences = types.References(refs)
		}
		bufMu.Unlock()
		return nil
	})

	eventBus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentCompleteData)
		if !ok {
			return nil
		}
		bufMu.Lock()
		agentDone = true
		agentCompleteFinalAnswer = data.FinalAnswer
		applyIMCompleteDataToMessage(assistantMsg, data)
		mergeIMAgentAnswerBuffers(&answerBuilder, &answerOuter, &agentLiveAnswer, data.FinalAnswer)
		bufMu.Unlock()
		closeComplete()
		return nil
	})

	// Subscribe to agent thought events — stream thinking content into <think> block
	eventBus.On(event.EventAgentThought, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentThoughtData)
		if !ok {
			return nil
		}
		bufMu.Lock()
		if useAgent {
			agentWrite(data.Content)
		} else {
			reasoningWrite(data.Content)
		}
		bufMu.Unlock()
		return nil
	})

	// Subscribe to agent tool call events — write status line into the think block.
	eventBus.On(event.EventAgentToolCall, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentToolCallData)
		if !ok {
			return nil
		}
		if !isToolVisibleToUser(data.ToolName) {
			return nil
		}
		bufMu.Lock()
		if seenToolCalls[data.ToolCallID] {
			bufMu.Unlock()
			return nil
		}
		seenToolCalls[data.ToolCallID] = true
		if !useAgent && IsRAGPipelineToolName(data.ToolName) {
			upsertIMToolStep(&pipelineToolSteps, pipelineIdx, data.ToolCallID, func(step *IMToolStep) {
				step.ToolName = data.ToolName
				step.Pending = true
				step.Arguments = data.Arguments
			})
			streamedAny = true
		} else if useAgent {
			retractAgentLiveAnswer()
			upsertIMToolStep(&agentToolSteps, agentToolIdx, data.ToolCallID, func(step *IMToolStep) {
				step.ToolName = data.ToolName
				step.Pending = true
				step.Arguments = data.Arguments
			})
			streamedAny = true
		}
		bufMu.Unlock()
		logger.Debugf(ctx, "[IM] Tool call streamed to IM: tool=%s id=%s", data.ToolName, data.ToolCallID)
		return nil
	})

	// Subscribe to agent tool result events
	eventBus.On(event.EventAgentToolResult, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentToolResultData)
		if !ok {
			return nil
		}
		if !isToolVisibleToUser(data.ToolName) {
			return nil
		}
		bufMu.Lock()
		if !useAgent && IsRAGPipelineToolName(data.ToolName) {
			upsertIMToolStep(&pipelineToolSteps, pipelineIdx, data.ToolCallID, func(step *IMToolStep) {
				step.ToolName = data.ToolName
				step.Pending = false
				step.Success = data.Success
				step.Data = data.Data
				step.Output = data.Output
			})
			streamedAny = true
		} else if useAgent {
			upsertIMToolStep(&agentToolSteps, agentToolIdx, data.ToolCallID, func(step *IMToolStep) {
				step.ToolName = data.ToolName
				step.Pending = false
				step.Success = data.Success
				step.Data = data.Data
				step.Output = data.Output
			})
			streamedAny = true
		}
		bufMu.Unlock()
		logger.Debugf(ctx, "[IM] Tool result streamed to IM: tool=%s success=%v duration=%dms",
			data.ToolName, data.Success, data.Duration)
		return nil
	})

	// An OAuth-enabled MCP service the IM user has not authorized yet. IM cannot
	// resolve the in-conversation prompt, so collect the service name and append
	// an authorization notice to the final reply (deduped per service).
	eventBus.On(event.EventMCPOAuthRequired, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.MCPOAuthRequiredData)
		if !ok {
			return nil
		}
		bufMu.Lock()
		if !mcpAuthSeen[data.ServiceID] {
			mcpAuthSeen[data.ServiceID] = true
			mcpAuthServices = append(mcpAuthServices, imMCPAuthService{ID: data.ServiceID, Name: data.ServiceName})
		}
		bufMu.Unlock()
		return nil
	})

	// Determine whether to use agent mode (already set above for event handlers).
	requestID := uuid.New().String()

	// Create user message
	userMsg, err := s.messageService.CreateMessage(qaCtx, createIMUserMessagePayload(session.ID, msg.Content, requestID, attachments))
	if err != nil {
		return fmt.Errorf("create user message: %w", err)
	}

	// Create placeholder assistant message
	assistantMsg, err = s.messageService.CreateMessage(qaCtx, createIMAssistantMessagePayload(session.ID, requestID))
	if err != nil {
		return fmt.Errorf("create assistant message: %w", err)
	}

	// Register inflight mapping so cross-instance /stop can find this request
	// and write a stop event to StreamManager.
	if raw, ok := s.inflight.Load(userKey); ok {
		e := raw.(*inflightEntry)
		e.sessionID = session.ID
		e.assistantMessageID = assistantMsg.ID
	}
	s.storeInflightMapping(qaCtx, userKey, session.ID, assistantMsg.ID)
	defer s.clearInflightMapping(ctx, userKey)

	// Start StreamManager stop watcher — mirrors web's handleAgentEventsForSSE
	// stop detection. Cancels qaCtx if a stop event is written by any instance.
	go s.watchStreamManagerStop(qaCtx, session.ID, assistantMsg.ID, qaCancel)

	// Run QA async
	go func() {
		var err error
		req := buildIMQARequest(session, msg.Content, assistantMsg.ID, userMsg.ID, customAgent, kbIDs, msg.Quote, attachments)
		req.ImageURLs = imageURLs
		if req.QuotedContext != "" {
			logger.Debugf(qaCtx, "[IM] QuotedContext set: length=%d", len(req.QuotedContext))
		}
		if useAgent {
			err = s.sessionService.AgentQA(qaCtx, req, eventBus)
		} else {
			err = s.sessionService.KnowledgeQA(qaCtx, req, eventBus)
		}
		if err != nil {
			logger.Errorf(ctx, "[IM] QA stream execution error: %v", err)
			bufMu.Lock()
			qaErr = fmt.Errorf("QA execution error: %w", err)
			bufMu.Unlock()
			closeDone()
			closeComplete()
		}
	}()

	// Flush loop: periodically send buffered content to the IM platform.
	// A holdback mechanism prevents flushing incomplete provider:// URLs or
	// XML tags that straddle a chunk boundary (see holdbackCutoff).
	ticker := time.NewTicker(streamFlushInterval)
	defer ticker.Stop()

	lastDisplay := ""
	var deliveryAuthErr error
	flush := func() bool {
		bufMu.Lock()
		parts := getStreamParts()
		agentRunning := useAgent && !agentDone
		bufMu.Unlock()

		displaySource := FormatIMIntermediateFromParts(parts, agentRunning)
		if displaySource == "" {
			return true
		}

		if cut := holdbackCutoff(displaySource); cut < len(displaySource) {
			displaySource = displaySource[:cut]
		}

		display := cleanIMContent(ctx, displaySource, tenant, s.defaultFileSvc, s.storageResolver)
		if display == lastDisplay {
			return true
		}
		if _, _, err := s.ValidateActiveBinding(ctx, bindingID); err != nil {
			deliveryAuthErr = err
			qaCancel()
			return false
		}
		if err := streamer.UpdateStreamContent(ctx, msg, streamID, display); err != nil {
			logger.Warnf(ctx, "[IM] UpdateStreamContent failed: %v", err)
			return true
		}
		lastDisplay = display
		return true
	}

loop:
	for {
		select {
		case <-ticker.C:
			if !flush() {
				break loop
			}
		case <-done:
			break loop
		case <-qaCtx.Done():
			break loop
		}
	}
	if deliveryAuthErr != nil {
		if err := abortStream(ctx, msg, streamer, streamID); err != nil {
			logger.Warnf(ctx, "[IM] AbortStream after binding validation failed: %v", err)
		}
		if isInactiveBindingError(deliveryAuthErr) {
			logger.Infof(ctx, "[IM] Stopped stream for inactive binding: binding=%s", bindingID)
			return nil
		}
		return fmt.Errorf("validate binding before stream update: %w", deliveryAuthErr)
	}

	if useAgent {
		waitForIMAgentComplete(qaCtx, completeDone, session.ID)
	}

	bufMu.Lock()
	parts := getStreamParts()
	resolvedAnswer := pickIMStoredAnswer(
		answerBuilder.String(),
		answerOuter.String(),
		agentLiveAnswer.String(),
		agentCompleteFinalAnswer,
	)
	if parts.Answer == "" {
		parts.Answer = resolvedAnswer
	}
	answer := resolvedAnswer
	finalErr := qaErr
	noVisibleContent := !streamedAny && strings.TrimSpace(resolvedAnswer) == ""
	authServices := append([]imMCPAuthService(nil), mcpAuthServices...)
	bufMu.Unlock()

	finalDisplay := cleanIMContent(ctx, FormatIMFinalFromParts(parts), tenant, s.defaultFileSvc, s.storageResolver)
	if noVisibleContent || finalDisplay == "" {
		fallback := "抱歉，我暂时无法回答这个问题。"
		if finalErr != nil {
			fallback = "抱歉，处理您的问题时出现了异常，请稍后再试。"
		}
		finalDisplay = fallback
		if answer == "" {
			answer = fallback
		}
	}
	if notice := s.buildIMMCPAuthNotice(ctx, authServices); notice != "" {
		finalDisplay = appendIMAuthNotice(finalDisplay, notice)
		answer = appendIMAuthNotice(answer, notice)
	}

	if _, _, err := s.ValidateActiveBinding(ctx, bindingID); err != nil {
		_ = abortStream(ctx, msg, streamer, streamID)
		if isInactiveBindingError(err) {
			logger.Infof(ctx, "[IM] Suppressing stream finalization for inactive binding: binding=%s", bindingID)
			return nil
		}
		return fmt.Errorf("validate binding before stream finalization: %w", err)
	}
	if err := streamer.FinalizeStream(ctx, msg, streamID, finalDisplay); err != nil {
		logger.Warnf(ctx, "[IM] FinalizeStream failed: %v", err)
	}

	// End the stream
	if err := streamer.EndStream(ctx, msg, streamID); err != nil {
		logger.Warnf(ctx, "[IM] EndStream failed: %v", err)
	}

	if answer == "" {
		answer = "抱歉，我暂时无法回答这个问题。"
	}

	assistantMsg.Content = answer
	assistantMsg.IsCompleted = true
	if err := s.messageService.UpdateMessage(ctx, assistantMsg); err != nil {
		logger.Warnf(ctx, "[IM] Failed to update assistant message: %v", err)
	}

	logger.Infof(ctx, "[IM] Stream reply sent: platform=%s user=%s answer_len=%d", msg.Platform, msg.UserID, len(answer))
	return nil
}

func abortStream(ctx context.Context, msg *IncomingMessage, streamer StreamSender, streamID string) error {
	if aborter, ok := streamer.(StreamAborter); ok {
		return aborter.AbortStream(ctx, msg, streamID)
	}
	return streamer.EndStream(ctx, msg, streamID)
}

// fallbackNonStream is used when streaming initialization fails.
func (s *Service) fallbackNonStream(
	ctx context.Context,
	msg *IncomingMessage,
	session *types.Session,
	customAgent *types.CustomAgent,
	kbIDs []string,
	attachments types.MessageAttachments,
	imageURLs []string,
	adapter Adapter,
	userKey string,
	bindingID string,
	tenant *types.Tenant,
) error {
	answer, err := s.runQA(ctx, session, msg.Content, customAgent, kbIDs, attachments, imageURLs, userKey, msg.Quote)
	if err != nil {
		logger.Errorf(ctx, "[IM] QA fallback failed: %v", err)
		answer = "抱歉，处理您的问题时出现了异常，请稍后再试。"
	}
	if _, _, err := s.ValidateActiveBinding(ctx, bindingID); err != nil {
		if isInactiveBindingError(err) {
			logger.Infof(ctx, "[IM] Suppressing fallback reply for inactive binding: binding=%s", bindingID)
			return nil
		}
		return fmt.Errorf("validate binding before fallback reply: %w", err)
	}

	return adapter.SendReply(ctx, msg, &ReplyMessage{Content: formatIMOutboundAnswer(ctx, answer, tenant, s.defaultFileSvc, s.storageResolver), IsFinal: true})
}

// runQA executes the WeKnora QA pipeline and returns the full answer text.
func (s *Service) runQA(ctx context.Context, session *types.Session, query string, customAgent *types.CustomAgent, kbIDs []string, attachments types.MessageAttachments, imageURLs []string, userKey string, quote *QuotedMessage) (string, error) {
	// Cancellable context (no hard deadline): each agent round has its own
	// LLMCallTimeout. The context can still be cancelled by /stop.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	eventBus := event.NewEventBus()

	// Thread-safe answer collection
	var answerMu sync.Mutex
	var answerBuilder strings.Builder
	var qaErr error
	var mcpAuthServices []imMCPAuthService
	mcpAuthSeen := make(map[string]bool)
	done := make(chan struct{})
	completeDone := make(chan struct{})
	var closeOnce sync.Once
	var completeOnce sync.Once
	closeDone := func() { closeOnce.Do(func() { close(done) }) }
	closeComplete := func() { completeOnce.Do(func() { close(completeDone) }) }

	eventBus.On(event.EventAgentFinalAnswer, func(ctx context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentFinalAnswerData)
		if !ok {
			return nil
		}
		answerMu.Lock()
		answerBuilder.WriteString(data.Content)
		answerMu.Unlock()
		if data.Done {
			closeDone()
		}
		return nil
	})

	eventBus.On(event.EventError, func(ctx context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ErrorData)
		if !ok {
			return nil
		}
		logger.Errorf(ctx, "[IM] QA error: %s", data.Error)
		answerMu.Lock()
		qaErr = fmt.Errorf("QA pipeline error: %s", data.Error)
		answerMu.Unlock()
		closeDone()
		closeComplete()
		return nil
	})

	// Collect OAuth services that need out-of-band authorization (IM cannot
	// resolve the in-conversation prompt); appended to the answer below.
	eventBus.On(event.EventMCPOAuthRequired, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.MCPOAuthRequiredData)
		if !ok {
			return nil
		}
		answerMu.Lock()
		if !mcpAuthSeen[data.ServiceID] {
			mcpAuthSeen[data.ServiceID] = true
			mcpAuthServices = append(mcpAuthServices, imMCPAuthService{ID: data.ServiceID, Name: data.ServiceName})
		}
		answerMu.Unlock()
		return nil
	})

	// Determine whether to use agent mode
	useAgent := customAgent != nil && customAgent.IsAgentMode()

	// Generate a shared RequestID to pair user and assistant messages for history
	requestID := uuid.New().String()

	// Create user message so it appears in conversation history
	userMsg, err := s.messageService.CreateMessage(ctx, createIMUserMessagePayload(session.ID, query, requestID, attachments))
	if err != nil {
		return "", fmt.Errorf("create user message: %w", err)
	}

	// Create a placeholder assistant message
	assistantMsg, err := s.messageService.CreateMessage(ctx, createIMAssistantMessagePayload(session.ID, requestID))
	if err != nil {
		return "", fmt.Errorf("create assistant message: %w", err)
	}

	eventBus.On(event.EventAgentReferences, func(ctx context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentReferencesData)
		if !ok {
			return nil
		}
		answerMu.Lock()
		refs := []*types.SearchResult(assistantMsg.KnowledgeReferences)
		collectIMKnowledgeReferences(&refs, data.References)
		assistantMsg.KnowledgeReferences = types.References(refs)
		answerMu.Unlock()
		return nil
	})

	eventBus.On(event.EventAgentComplete, func(ctx context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentCompleteData)
		if !ok {
			return nil
		}
		answerMu.Lock()
		applyIMCompleteDataToMessage(assistantMsg, data)
		if answerBuilder.Len() == 0 && strings.TrimSpace(data.FinalAnswer) != "" {
			answerBuilder.WriteString(data.FinalAnswer)
		}
		answerMu.Unlock()
		closeComplete()
		return nil
	})

	// Register inflight mapping for cross-instance /stop via StreamManager.
	if raw, ok := s.inflight.Load(userKey); ok {
		e := raw.(*inflightEntry)
		e.sessionID = session.ID
		e.assistantMessageID = assistantMsg.ID
	}
	s.storeInflightMapping(ctx, userKey, session.ID, assistantMsg.ID)
	defer s.clearInflightMapping(ctx, userKey)

	// Start StreamManager stop watcher.
	go s.watchStreamManagerStop(ctx, session.ID, assistantMsg.ID, cancel)

	// Run QA async
	go func() {
		var err error
		req := buildIMQARequest(session, query, assistantMsg.ID, userMsg.ID, customAgent, kbIDs, quote, attachments)
		req.ImageURLs = imageURLs
		if req.QuotedContext != "" {
			logger.Debugf(ctx, "[IM] QuotedContext set: length=%d", len(req.QuotedContext))
		}
		if useAgent {
			err = s.sessionService.AgentQA(ctx, req, eventBus)
		} else {
			err = s.sessionService.KnowledgeQA(ctx, req, eventBus)
		}
		if err != nil {
			logger.Errorf(ctx, "[IM] QA execution error: %v", err)
			answerMu.Lock()
			qaErr = fmt.Errorf("QA execution error: %w", err)
			answerMu.Unlock()
			closeDone()
			closeComplete()
		}
	}()

	// Wait for completion or cancellation (e.g., /stop)
	select {
	case <-done:
		if useAgent {
			waitForIMAgentComplete(ctx, completeDone, session.ID)
		}
	case <-ctx.Done():
		// Mark assistant message as completed to avoid dangling incomplete records
		assistantMsg.Content = "抱歉，回答已被取消。"
		assistantMsg.IsCompleted = true
		// Use a fresh context since the original is cancelled
		if updateErr := s.messageService.UpdateMessage(context.WithoutCancel(ctx), assistantMsg); updateErr != nil {
			logger.Warnf(ctx, "[IM] Failed to update cancelled assistant message: %v", updateErr)
		}
		return "", fmt.Errorf("QA cancelled: %w", ctx.Err())
	}

	answerMu.Lock()
	answer := answerBuilder.String()
	qaError := qaErr
	authServices := append([]imMCPAuthService(nil), mcpAuthServices...)
	answerMu.Unlock()

	if answer == "" && qaError != nil {
		return "", qaError
	}
	if answer == "" {
		answer = "抱歉，我暂时无法回答这个问题。"
	}
	if notice := s.buildIMMCPAuthNotice(ctx, authServices); notice != "" {
		answer = appendIMAuthNotice(answer, notice)
	}

	// Update assistant message with the full answer (including citation tags for web rendering).
	assistantMsg.Content = answer
	assistantMsg.IsCompleted = true
	if err := s.messageService.UpdateMessage(ctx, assistantMsg); err != nil {
		logger.Warnf(ctx, "[IM] Failed to update assistant message: %v", err)
	}

	// Return raw answer — callers apply cleanIMContent with the appropriate FileService.
	return answer, nil
}

// ── CRUD operations for IM channels ──

// ListChannels returns every non-deleted system-managed bot installation.
func (s *Service) ListChannels() ([]IMChannel, error) {
	var channels []IMChannel
	if err := s.db.Where("deleted_at IS NULL").
		Order("created_at DESC").Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

// CreateChannel creates a new IM channel and optionally starts it.
// Returns a duplicate_bot error if the bot identity is already used by another channel.
func (s *Service) CreateChannel(channel *IMChannel) error {
	if err := validateChannel(channel); err != nil {
		return err
	}
	if err := s.checkDuplicateBot(channel, ""); err != nil {
		return err
	}
	if err := s.db.Create(channel).Error; err != nil {
		return err
	}
	if channel.Enabled {
		if err := s.StartChannel(channel); err != nil {
			logger.Warnf(context.Background(), "[IM] Created channel %s but failed to start: %v", channel.ID, err)
		}
	}
	s.publishChannelConfigChange(channel.ID)
	return nil
}

// UpdateChannel updates a channel and restarts it if needed.
// Returns a duplicate_bot error if the bot identity is already used by another channel.
func (s *Service) UpdateChannel(channel *IMChannel) error {
	newIdentity := channel.computeBotIdentity()
	if err := s.checkDuplicateBot(channel, channel.ID); err != nil {
		return err
	}
	var revokedBindingIDs []string
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var stored IMChannel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", channel.ID).
			First(&stored).Error; err != nil {
			return err
		}
		// Enabled is changed only through ToggleChannel. Preserve the locked
		// database value so a concurrent configuration edit cannot undo a toggle.
		channel.Enabled = stored.Enabled
		if err := validateChannel(channel); err != nil {
			return err
		}
		identityChanged := stored.BotIdentity != newIdentity
		if identityChanged {
			now := time.Now().UTC()
			if err := tx.Model(&BindingRequest{}).
				Where("im_channel_id = ? AND status = ?", channel.ID, IMBindingRequestPending).
				Updates(map[string]any{
					"status":       IMBindingRequestCancelled,
					"cancelled_at": now,
					"updated_at":   now,
				}).Error; err != nil {
				return err
			}
			var err error
			revokedBindingIDs, err = revokeChannelBindingsInTx(tx, channel.ID, now)
			if err != nil {
				return err
			}
		}
		return tx.Save(channel).Error
	}); err != nil {
		return err
	}
	for _, bindingID := range revokedBindingIDs {
		s.cancelBindingRequests(bindingID)
	}
	// Restart channel: stop old, start new if enabled
	s.StopChannel(channel.ID)
	if channel.Enabled {
		if err := s.StartChannel(channel); err != nil {
			logger.Warnf(context.Background(), "[IM] Updated channel %s but failed to restart: %v", channel.ID, err)
		}
	}
	s.publishChannelConfigChange(channel.ID)
	return nil
}

// DeleteChannel soft-deletes a system bot installation and stops its runtime.
func (s *Service) DeleteChannel(channelID string) error {
	var revokedBindingIDs []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var channel IMChannel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", channelID).
			First(&channel).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Model(&BindingRequest{}).
			Where("im_channel_id = ? AND status = ?", channelID, IMBindingRequestPending).
			Updates(map[string]any{
				"status":       IMBindingRequestCancelled,
				"cancelled_at": now,
				"updated_at":   now,
			}).Error; err != nil {
			return err
		}
		var err error
		revokedBindingIDs, err = revokeChannelBindingsInTx(tx, channelID, now)
		if err != nil {
			return err
		}
		result := tx.Delete(&channel)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, bindingID := range revokedBindingIDs {
		s.cancelBindingRequests(bindingID)
	}
	s.StopChannel(channelID)
	s.publishChannelConfigChange(channelID)
	return nil
}

// ToggleChannel enables or disables a system bot installation.
func (s *Service) ToggleChannel(channelID string) (*IMChannel, error) {
	var ch IMChannel
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", channelID).
			First(&ch).Error; err != nil {
			return err
		}
		ch.Enabled = !ch.Enabled
		if ch.Enabled && !SupportsSystemChannel(ch.Platform) {
			return fmt.Errorf("%w: platform %s is not supported", ErrChannelConfigurationInvalid, ch.Platform)
		}
		if err := validateChannel(&ch); err != nil {
			return err
		}
		return tx.Save(&ch).Error
	})
	if err != nil {
		return nil, err
	}
	if ch.Enabled {
		if err := s.StartChannel(&ch); err != nil {
			logger.Warnf(context.Background(), "[IM] Failed to start channel %s after enable: %v", ch.ID, err)
		}
	} else {
		s.StopChannel(channelID)
	}
	s.publishChannelConfigChange(channelID)
	return &ch, nil
}

func validateChannel(channel *IMChannel) error {
	if channel.AllowGroupChat && !SupportsGroupChat(channel.Platform, channel.Mode) {
		return fmt.Errorf("%w: platform %s does not support group account binding in %s mode",
			ErrChannelConfigurationInvalid, channel.Platform, channel.Mode)
	}
	credentials, err := ParseCredentials(channel.Credentials)
	if err != nil {
		return fmt.Errorf("%w: credentials must be a JSON object", ErrChannelConfigurationInvalid)
	}
	if channel.OutputMode != "" && channel.OutputMode != "stream" && channel.OutputMode != "full" {
		return fmt.Errorf("%w: unsupported output mode %s", ErrChannelConfigurationInvalid, channel.OutputMode)
	}
	requiredFields, err := requiredChannelCredentialFields(channel.Platform, channel.Mode)
	if err != nil {
		return err
	}
	if !channel.Enabled {
		return nil
	}
	for _, field := range requiredFields {
		if !channelCredentialConfigured(credentials, field) {
			return fmt.Errorf("%w: %s is required", ErrChannelConfigurationInvalid, field)
		}
	}
	if requiresStableBotIdentity(channel.Platform) && channel.computeBotIdentity() == "" {
		return ErrChannelIdentityMissing
	}
	return nil
}

func requiredChannelCredentialFields(platform, mode string) ([]string, error) {
	switch platform {
	case string(PlatformWeCom):
		switch mode {
		case "websocket":
			return []string{"bot_id", "bot_secret"}, nil
		case "webhook":
			return []string{"corp_id", "corp_agent_id", "agent_secret", "token", "encoding_aes_key"}, nil
		}
	case string(PlatformFeishu), string(PlatformLark):
		switch mode {
		case "websocket":
			return []string{"app_id", "app_secret"}, nil
		case "webhook":
			return []string{"app_id", "app_secret", "verification_token"}, nil
		}
	case string(PlatformSlack):
		switch mode {
		case "websocket":
			return []string{"bot_token", "app_token"}, nil
		case "webhook":
			return []string{"bot_token", "signing_secret"}, nil
		}
	case string(PlatformTelegram):
		switch mode {
		case "websocket":
			return []string{"bot_token"}, nil
		case "webhook":
			return []string{"bot_token", "secret_token"}, nil
		}
	case string(PlatformDingtalk):
		if mode == "websocket" || mode == "webhook" {
			return []string{"client_id", "client_secret"}, nil
		}
	case string(PlatformWeChat):
		if mode == "longpoll" {
			return []string{"bot_token", "ilink_bot_id"}, nil
		}
	case string(PlatformQQBot):
		if mode == "websocket" {
			return []string{"app_id", "client_secret"}, nil
		}
	default:
		return nil, nil
	}
	return nil, fmt.Errorf("%w: %s does not support %s mode", ErrChannelConfigurationInvalid, platform, mode)
}

func channelCredentialConfigured(credentials map[string]any, field string) bool {
	if strings.TrimSpace(GetString(credentials, field)) != "" {
		return true
	}
	if field != "corp_agent_id" {
		return false
	}
	value, ok := credentials[field].(float64)
	return ok && value > 0
}

func requiresStableBotIdentity(platform string) bool {
	switch platform {
	case string(PlatformWeCom), string(PlatformFeishu), string(PlatformLark),
		string(PlatformSlack), string(PlatformTelegram), string(PlatformDingtalk),
		string(PlatformWeChat), string(PlatformQQBot):
		return true
	default:
		return false
	}
}

// checkDuplicateBot queries the bot_identity index to see if another active channel
// already uses the same bot. This is an O(1) index lookup, not a full table scan.
// The DB unique index on bot_identity serves as an additional safety net.
// excludeID is the channel's own ID (for updates); pass "" for new channels.
func (s *Service) checkDuplicateBot(channel *IMChannel, excludeID string) error {
	// Compute bot_identity the same way the BeforeSave hook will
	botKey := channel.computeBotIdentity()
	if botKey == "" {
		return nil
	}

	var existing IMChannel
	query := s.db.Where("bot_identity = ? AND deleted_at IS NULL", botKey)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	if err := query.First(&existing).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil // no conflict
		}
		return fmt.Errorf("check duplicate bot: %w", err)
	}
	return fmt.Errorf("duplicate_bot: this bot is already bound to channel %q (%s); each bot can only be connected to one channel", existing.Name, existing.ID)
}

// ── File message handling ──────────────────────────────────────────────
// These methods handle file messages received via IM platforms.
// Files are downloaded from the IM platform, validated, and saved to the
// configured knowledge base asynchronously. The user receives a notification
// at the start and end of processing.
// ────────────────────────────────────────────────────────────────────────

// fileMessageQAContent turns a file-only platform event into a valid QA query.
// IM adapters intentionally leave Content empty for file/image messages, while
// the QA API requires a non-empty query. Preserve a caption when an adapter
// provides one; otherwise identify the uploaded file without claiming that its
// contents have already been read.
func fileMessageQAContent(msg *IncomingMessage) string {
	if strings.TrimSpace(msg.Content) != "" {
		return msg.Content
	}
	fileName := strings.TrimSpace(msg.FileName)
	if fileName == "" {
		fileName = "未命名文件"
	}
	return fmt.Sprintf("我上传了文件「%s」。请确认已收到，并告知我接下来可以如何协助。", fileName)
}
