package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const invalidIMPlatformError = "platform must be one of: 'dingtalk', 'feishu', 'lark', 'qqbot', " +
	"'slack', 'telegram', 'wechat', 'wecom'"

// IMHandler handles IM platform callback requests and channel CRUD.
type IMHandler struct {
	imService *im.Service
}

// NewIMHandler creates a new IM handler.
func NewIMHandler(imService *im.Service) *IMHandler {
	return &IMHandler{
		imService: imService,
	}
}

// CreateIMChannel creates a system-managed bot installation.
//
// CreateIMChannel godoc
// @Summary      创建 IM 渠道
// @Description  创建系统级 IM 机器人配置
// @Tags         IM 渠道
// @Accept       json
// @Produce      json
// @Param        request  body      map[string]interface{}  true  "渠道配置"
// @Success      200      {object}  map[string]interface{}  "创建后的渠道"
// @Failure      400      {object}  map[string]interface{}  "请求参数或渠道配置错误"
// @Failure      409      {object}  map[string]interface{}  "机器人身份已存在"
// @Security     Bearer
// @Router       /system/admin/im-channels [post]
func (h *IMHandler) CreateIMChannel(c *gin.Context) {
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		Platform       string     `json:"platform" binding:"required"`
		Name           string     `json:"name"`
		Mode           string     `json:"mode"`
		OutputMode     string     `json:"output_mode"`
		AllowGroupChat bool       `json:"allow_group_chat"`
		Credentials    types.JSON `json:"credentials"`
		Enabled        *bool      `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !im.SupportsSystemChannel(req.Platform) {
		c.JSON(http.StatusBadRequest, gin.H{"error": invalidIMPlatformError})
		return
	}

	channel := &im.IMChannel{
		Platform:       req.Platform,
		Name:           strings.TrimSpace(req.Name),
		Mode:           req.Mode,
		OutputMode:     req.OutputMode,
		AllowGroupChat: req.AllowGroupChat,
		Credentials:    req.Credentials,
		CreatedBy:      userID,
		Enabled:        false,
	}
	if req.Enabled != nil {
		channel.Enabled = *req.Enabled
	}
	// WeChat uses long-polling mode and full output only
	if req.Platform == "wechat" {
		channel.Mode = "longpoll"
		channel.OutputMode = "full"
	} else if channel.Mode == "" {
		channel.Mode = "websocket"
		if channel.OutputMode == "" {
			channel.OutputMode = "stream"
		}
	}
	if channel.Credentials == nil {
		channel.Credentials = types.JSON("{}")
	}

	if err := h.imService.CreateChannel(channel); err != nil {
		logger.Errorf(c.Request.Context(), "[IM] Create channel failed: %v", err)
		if errors.Is(err, im.ErrChannelIdentityMissing) || errors.Is(err, im.ErrChannelConfigurationInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if strings.HasPrefix(err.Error(), "duplicate_bot:") {
			c.JSON(http.StatusConflict, gin.H{"error": strings.TrimPrefix(err.Error(), "duplicate_bot: ")})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create channel"})
		return
	}

	writeIMChannelResponse(c, channel)
}

// ListIMChannels lists all system-managed bot installations.
//
// ListIMChannels godoc
// @Summary      获取 IM 渠道列表
// @Description  获取全部系统级 IM 机器人配置及凭据 ID，不返回密钥
// @Tags         IM 渠道
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "渠道列表"
// @Security     Bearer
// @Router       /system/admin/im-channels [get]
func (h *IMHandler) ListIMChannels(c *gin.Context) {
	channels, err := h.imService.ListChannels()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list channels"})
		return
	}

	summaries, err := im.SummarizeIMChannels(channels)
	if err != nil {
		logger.Errorf(c.Request.Context(), "[IM] Summarize channels failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to summarize channels"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summaries})
}

// UpdateIMChannel updates an IM channel.
//
// UpdateIMChannel godoc
// @Summary      更新 IM 渠道
// @Description  更新系统级 IM 机器人的名称、模式、群聊开关或凭证
// @Tags         IM 渠道
// @Accept       json
// @Produce      json
// @Param        id       path      string                  true  "渠道 ID"
// @Param        request  body      map[string]interface{}  true  "渠道配置更新字段"
// @Success      200      {object}  map[string]interface{}  "更新后的渠道"
// @Failure      400      {object}  map[string]interface{}  "请求参数错误"
// @Failure      404      {object}  map[string]interface{}  "渠道不存在"
// @Security     Bearer
// @Router       /system/admin/im-channels/{id} [put]
func (h *IMHandler) UpdateIMChannel(c *gin.Context) {
	channelID := c.Param("id")
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel id is required"})
		return
	}

	channel, err := h.imService.GetChannelByID(channelID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}

	var req struct {
		Name           *string    `json:"name"`
		Mode           *string    `json:"mode"`
		OutputMode     *string    `json:"output_mode"`
		AllowGroupChat *bool      `json:"allow_group_chat"`
		Credentials    types.JSON `json:"credentials"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Name != nil {
		channel.Name = strings.TrimSpace(*req.Name)
	}
	if req.Mode != nil {
		channel.Mode = *req.Mode
	}
	if req.OutputMode != nil {
		channel.OutputMode = *req.OutputMode
	}
	if req.AllowGroupChat != nil {
		channel.AllowGroupChat = *req.AllowGroupChat
	}
	if req.Credentials != nil {
		mergedCredentials, mergeErr := mergeIMCredentials(channel.Credentials, req.Credentials)
		if mergeErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "credentials must be a JSON object"})
			return
		}
		channel.Credentials = mergedCredentials
	}
	if channel.Platform == string(im.PlatformWeChat) {
		channel.Mode = "longpoll"
		channel.OutputMode = "full"
	}

	if err := h.imService.UpdateChannel(channel); err != nil {
		logger.Errorf(c.Request.Context(), "[IM] Update channel failed: %v", err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		if errors.Is(err, im.ErrChannelIdentityMissing) || errors.Is(err, im.ErrChannelConfigurationInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if strings.HasPrefix(err.Error(), "duplicate_bot:") {
			c.JSON(http.StatusConflict, gin.H{"error": strings.TrimPrefix(err.Error(), "duplicate_bot: ")})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update channel"})
		return
	}

	writeIMChannelResponse(c, channel)
}

func writeIMChannelResponse(c *gin.Context, channel *im.IMChannel) {
	summary, err := im.SummarizeIMChannel(*channel)
	if err != nil {
		logger.Errorf(c.Request.Context(), "[IM] Summarize channel failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to summarize channel"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

func mergeIMCredentials(current, update types.JSON) (types.JSON, error) {
	currentValues := make(map[string]any)
	if len(current) > 0 {
		if err := json.Unmarshal(current, &currentValues); err != nil {
			return nil, err
		}
	}
	if currentValues == nil {
		currentValues = make(map[string]any)
	}

	updateValues := make(map[string]any)
	if err := json.Unmarshal(update, &updateValues); err != nil {
		return nil, err
	}
	for key, value := range updateValues {
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		currentValues[key] = value
	}

	return json.Marshal(currentValues)
}

// DeleteIMChannel deletes an IM channel.
//
// DeleteIMChannel godoc
// @Summary      删除 IM 渠道
// @Description  删除指定 IM 渠道
// @Tags         IM 渠道
// @Produce      json
// @Param        id   path      string                  true  "渠道 ID"
// @Success      200  {object}  map[string]interface{}  "success: true"
// @Failure      400  {object}  map[string]interface{}  "请求参数错误"
// @Failure      404  {object}  map[string]interface{}  "渠道不存在"
// @Security     Bearer
// @Router       /system/admin/im-channels/{id} [delete]
func (h *IMHandler) DeleteIMChannel(c *gin.Context) {
	channelID := c.Param("id")
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel id is required"})
		return
	}

	if err := h.imService.DeleteChannel(channelID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete channel"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ToggleIMChannel toggles the enabled state of an IM channel.
//
// ToggleIMChannel godoc
// @Summary      启用/停用 IM 渠道
// @Description  切换指定 IM 渠道的启用状态
// @Tags         IM 渠道
// @Produce      json
// @Param        id   path      string                  true  "渠道 ID"
// @Success      200  {object}  map[string]interface{}  "更新后的渠道"
// @Failure      400  {object}  map[string]interface{}  "请求参数错误"
// @Failure      404  {object}  map[string]interface{}  "渠道不存在"
// @Security     Bearer
// @Router       /system/admin/im-channels/{id}/toggle [post]
func (h *IMHandler) ToggleIMChannel(c *gin.Context) {
	channelID := c.Param("id")
	if channelID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel id is required"})
		return
	}

	channel, err := h.imService.ToggleChannel(channelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		if errors.Is(err, im.ErrChannelIdentityMissing) || errors.Is(err, im.ErrChannelConfigurationInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to toggle channel"})
		return
	}

	writeIMChannelResponse(c, channel)
}

// ListBindableIMChannels lists the credential-free bot directory available to
// the current signed-in user.
//
// ListBindableIMChannels godoc
// @Summary      获取可绑定的 IM 渠道
// @Description  获取当前用户可发起账号绑定的已启用机器人列表
// @Tags         IM 绑定
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "可绑定渠道列表"
// @Security     Bearer
// @Router       /im/channels [get]
func (h *IMHandler) ListBindableIMChannels(c *gin.Context) {
	channels, err := h.imService.ListBindableChannels()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list bindable channels"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": channels})
}

// CreateIMBindingRequest starts the web-to-IM one-time-code flow.
//
// CreateIMBindingRequest godoc
// @Summary      创建 IM 绑定请求
// @Description  为当前用户和工作空间生成一次性 IM 绑定码
// @Tags         IM 绑定
// @Accept       json
// @Produce      json
// @Param        request  body      map[string]interface{}  true  "绑定参数（im_channel_id/tenant_id）"
// @Success      200      {object}  map[string]interface{}  "绑定请求"
// @Failure      400      {object}  map[string]interface{}  "请求参数或渠道状态错误"
// @Failure      403      {object}  map[string]interface{}  "无工作空间访问权限"
// @Security     Bearer
// @Router       /im/binding-requests [post]
func (h *IMHandler) CreateIMBindingRequest(c *gin.Context) {
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req struct {
		IMChannelID string `json:"im_channel_id" binding:"required"`
		TenantID    uint64 `json:"tenant_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result, err := h.imService.CreateBindingRequest(
		c.Request.Context(), userID, strings.TrimSpace(req.IMChannelID), req.TenantID,
	)
	if err != nil {
		writeIMBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// GetIMBindingRequest returns the current user's binding request status.
//
// GetIMBindingRequest godoc
// @Summary      获取 IM 绑定请求
// @Description  获取当前用户指定绑定请求的状态
// @Tags         IM 绑定
// @Produce      json
// @Param        id   path      string                  true  "绑定请求 ID"
// @Success      200  {object}  map[string]interface{}  "绑定请求"
// @Failure      404  {object}  map[string]interface{}  "绑定请求不存在"
// @Security     Bearer
// @Router       /im/binding-requests/{id} [get]
func (h *IMHandler) GetIMBindingRequest(c *gin.Context) {
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	result, err := h.imService.GetBindingRequest(userID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "binding request not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get binding request"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// ListIMBindings lists the current user's active IM bindings.
//
// ListIMBindings godoc
// @Summary      获取 IM 绑定
// @Description  获取当前用户创建的有效个人或群聊绑定
// @Tags         IM 绑定
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "绑定列表"
// @Security     Bearer
// @Router       /im/bindings [get]
func (h *IMHandler) ListIMBindings(c *gin.Context) {
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	bindings, err := h.imService.ListUserBindings(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list bindings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": bindings})
}

// DeleteIMBinding revokes one active IM binding owned by the current user.
//
// DeleteIMBinding godoc
// @Summary      解除 IM 绑定
// @Description  撤销当前用户创建的个人或群聊绑定
// @Tags         IM 绑定
// @Produce      json
// @Param        id   path      string                  true  "绑定 ID"
// @Success      200  {object}  map[string]interface{}  "success: true"
// @Failure      404  {object}  map[string]interface{}  "绑定不存在"
// @Security     Bearer
// @Router       /im/bindings/{id} [delete]
func (h *IMHandler) DeleteIMBinding(c *gin.Context) {
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if err := h.imService.RevokeUserBinding(userID, c.Param("id")); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "binding not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to revoke binding"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func writeIMBindingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, im.ErrBindingUnavailable):
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is unavailable for account binding"})
	case errors.Is(err, im.ErrBindingMembershipInvalid):
		c.JSON(http.StatusForbidden, gin.H{"error": "workspace membership is unavailable"})
	default:
		logger.Errorf(c.Request.Context(), "[IM] Binding request failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create binding request"})
	}
}

func writeIMCallbackACK(c *gin.Context, platform string) {
	if platform == "yunzhijia" {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data": gin.H{
				"type":    2,
				"content": "",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ── Callback handlers ──

// IMCallback handles IM platform callback requests for a specific channel.
// Route: POST /api/v1/im/callback/:channel_id
//
// IMCallback godoc
// @Summary      IM 平台回调
// @Description  接收各 IM 平台的事件回调；走平台自身签名校验，不使用 API Key
// @Tags         IM 回调
// @Accept       json
// @Produce      json
// @Param        channel_id  path      string                  true  "渠道 ID"
// @Success      200         {object}  map[string]interface{}  "处理结果"
// @Failure      400         {object}  map[string]interface{}  "请求参数错误"
// @Failure      401         {object}  map[string]interface{}  "签名校验失败"
// @Router       /im/callback/{channel_id} [get]
// @Router       /im/callback/{channel_id} [post]
func (h *IMHandler) IMCallback(c *gin.Context) {
	ctx := c.Request.Context()
	channelID := c.Param("channel_id")

	// Always validate the durable row before using a cached webhook adapter.
	// This is the correctness fallback when a replica missed Redis invalidation
	// while disconnected, and prevents stale credentials/config from being used.
	adapter, channel, err := h.imService.EnsureChannelAdapter(channelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Errorf(ctx, "[IM] Channel not found for callback: %s", channelID)
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		if errors.Is(err, im.ErrChannelDisabled) {
			logger.Errorf(ctx, "[IM] Channel disabled for callback: %s", channelID)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel is disabled"})
			return
		}
		logger.Errorf(ctx, "[IM] Channel unavailable for callback %s: %v", channelID, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel not available"})
		return
	}

	logger.Infof(ctx, "[IM] Callback received platform=%s path_channel_id=%s", channel.Platform, channelID)
	if channel.Mode != "webhook" {
		logger.Warnf(ctx, "[IM] HTTP callback rejected for non-webhook channel %s", channelID)
		c.JSON(http.StatusNotFound, gin.H{"error": "channel does not accept webhook callbacks"})
		return
	}

	// Handle URL verification
	if adapter.HandleURLVerification(c) {
		return
	}

	// Verify callback signature
	if err := adapter.VerifyCallback(c); err != nil {
		logger.Errorf(ctx, "[IM] Callback verification failed for channel %s: %v", channelID, err)
		c.JSON(http.StatusForbidden, gin.H{"error": "verification failed"})
		return
	}

	// Parse the callback message
	msg, err := adapter.ParseCallback(c)
	if err != nil {
		logger.Errorf(ctx, "[IM] Parse callback failed for channel %s: %v", channelID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "parse failed"})
		return
	}

	// If nil, it's a non-message event - just acknowledge
	if msg == nil {
		if channel.Platform == "mattermost" {
			logger.Infof(ctx, "[IM] Mattermost callback ignored (no message): path_channel_id=%s — check: (1) trigger word must be the *first word* of the post; (2) if channel+trigger are both set, post must be in that channel; (3) bot_user_id must not match the sender", channelID)
		} else {
			logger.Infof(ctx, "[IM] Callback parsed no message to process platform=%s path_channel_id=%s", channel.Platform, channelID)
		}
		writeIMCallbackACK(c, channel.Platform)
		return
	}

	// Respond immediately to avoid platform timeout
	writeIMCallbackACK(c, channel.Platform)

	// Detach from gin request context
	asyncCtx := context.WithoutCancel(ctx)

	// Process message asynchronously
	go func() {
		if err := h.imService.HandleMessage(asyncCtx, msg, channelID); err != nil {
			logger.Errorf(asyncCtx, "[IM] Handle message error for channel %s: %v", channelID, err)
		}
	}()
}
