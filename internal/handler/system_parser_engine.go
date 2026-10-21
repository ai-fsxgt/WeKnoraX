package handler

import (
	"encoding/json"
	"net/http"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// UpdateSystemParserEngineRequest replaces one engine's configuration and switch.
type UpdateSystemParserEngineRequest struct {
	Enabled *bool                           `json:"enabled" binding:"required"`
	Config  *types.SystemParserEngineConfig `json:"config" binding:"required"`
}

// ListSystemParserEngines lists platform parser settings without credentials.
// @Summary 获取系统解析引擎配置
// @Tags 系统管理
// @Produce json
// @Success 200 {array} types.SystemParserEngineView
// @Router /system/admin/parser-engines [get]
func (h *SystemHandler) ListSystemParserEngines(c *gin.Context) {
	engines, err := h.systemParserSvc.List(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, engines)
}

// UpdateSystemParserEngine updates one platform parser configuration.
// @Summary 保存系统解析引擎配置与启用状态
// @Tags 系统管理
// @Accept json
// @Produce json
// @Param engine path string true "解析引擎名称"
// @Param request body UpdateSystemParserEngineRequest true "配置"
// @Success 200 {object} map[string]interface{}
// @Router /system/admin/parser-engines/{engine} [put]
func (h *SystemHandler) UpdateSystemParserEngine(c *gin.Context) {
	var req UpdateSystemParserEngineRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("Invalid parser configuration"))
		return
	}
	ctx := c.Request.Context()
	name := c.Param("engine")
	if err := h.systemParserSvc.Save(ctx, name, *req.Enabled, *req.Config); err != nil {
		_ = c.Error(err)
		return
	}
	if h.auditSvc != nil {
		actorID, _ := types.UserIDFromContext(ctx)
		details, _ := json.Marshal(map[string]any{"enabled": *req.Enabled})
		_ = h.auditSvc.Log(ctx, &types.AuditLog{
			TenantID: 0, ActorUserID: actorID, ActorRole: systemAuditActorRole(ctx),
			Action: types.AuditActionSystemSettingChanged, TargetType: "system_parser_engine", TargetID: name,
			Outcome: types.AuditOutcomeSuccess, Details: types.JSON(details),
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// CheckSystemParserEngine checks one platform parser using the current draft.
// @Summary 检测系统解析引擎连接
// @Tags 系统管理
// @Accept json
// @Produce json
// @Param engine path string true "解析引擎名称"
// @Param request body types.SystemParserEngineConfig true "待检测的配置"
// @Success 200 {object} types.ParserEngineInfo
// @Router /system/admin/parser-engines/{engine}/check [post]
func (h *SystemHandler) CheckSystemParserEngine(c *gin.Context) {
	var cfg types.SystemParserEngineConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("Invalid parser configuration"))
		return
	}
	info, err := h.systemParserSvc.Check(c.Request.Context(), c.Param("engine"), cfg)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, info)
}
