package handlers

import (
	"net/http"
	"strings"

	commonAuth "code-common/backend/auth"
	commonModels "code-common/backend/models"
	"code-contract/internal/server/noise"
	"code-contract/models"

	"github.com/gin-gonic/gin"
)

// GetNoiseRulesHandler 读取双源噪音规则配置与推荐模板 (GET /api/noise-rules)
func GetNoiseRulesHandler(c *gin.Context) {
	if models.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未就绪"})
		return
	}

	cfg, err := noise.LoadEffectiveNoiseConfig(c.Request.Context(), models.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, cfg)
}

// AppendGlobalNoiseRuleHandler 将服务或通配规则追加至全局噪音名单 (POST /api/admin/noise-rules/append, AdminOnly)
func AppendGlobalNoiseRuleHandler(c *gin.Context) {
	if models.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未就绪"})
		return
	}

	var req struct {
		Rule string `json:"rule" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求载荷格式错误，rule 不能为空"})
		return
	}

	uc := commonAuth.GetUserContext(c)
	operator := "admin"
	if uc != nil && strings.TrimSpace(uc.Name) != "" {
		operator = uc.Name
	} else if uc != nil && strings.TrimSpace(uc.Username) != "" {
		operator = uc.Username
	}

	beforeCfg, _ := noise.LoadEffectiveNoiseConfig(c.Request.Context(), models.DB)
	afterCfg, err := noise.AppendGlobalNoiseRule(c.Request.Context(), models.DB, req.Rule, operator)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	recordAudit(
		c,
		"append_noise_rule",
		commonModels.AuditLevelP1,
		"追加全局噪音服务规则: "+strings.TrimSpace(req.Rule),
		"module_settings",
		0,
		noise.SettingKeyNoiseServiceRules,
		beforeCfg,
		afterCfg,
	)

	c.JSON(http.StatusOK, afterCfg)
}
