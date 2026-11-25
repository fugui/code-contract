package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	commonAudit "code-common/backend/audit"
	commonAuth "code-common/backend/auth"
	commonModels "code-common/backend/models"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"github.com/gin-gonic/gin"
)

func currentTopologyOperator(c *gin.Context) (topology.Operator, string) {
	uc := commonAuth.GetUserContext(c)
	if uc == nil {
		return topology.Operator{}, "system"
	}
	return topology.Operator{
		UserID:  uc.UserID,
		IsAdmin: isCurrentUserAdmin(uc),
	}, uc.Username
}

// GetArchInbox handles GET /api/arch/inbox (M32 架构收件箱聚合查询).
func GetArchInbox(c *gin.Context) {
	page, pageSize := parsePagination(c)
	var archProcID uint64
	if raw := strings.TrimSpace(c.Query("arch_process_id")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "arch_process_id 参数无效"})
			return
		}
		archProcID = parsed
	}

	resp, err := topology.BuildArchInbox(c.Request.Context(), models.DB, topology.InboxQuery{
		Kind:          strings.TrimSpace(c.Query("kind")),
		ArchProcessID: archProcID,
		Search:        strings.TrimSpace(c.Query("search")),
		Page:          page,
		PageSize:      pageSize,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

type claimArchServiceItemReq struct {
	ServiceID FlexUint64 `json:"service_id"`
	ModuleID  FlexUint64 `json:"module_id"`
	ImplKey   string     `json:"impl_key"`
}

type claimArchServicesReq struct {
	Items []claimArchServiceItemReq `json:"items"`
}

// ClaimArchServices handles POST /api/arch/services/claim (ModuleOwnerOrAdminGuard + I4).
func ClaimArchServices(c *gin.Context) {
	var req claimArchServicesReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}
	items := make([]topology.ClaimItem, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, topology.ClaimItem{
			ServiceID: it.ServiceID.Uint64(),
			ModuleID:  it.ModuleID.Uint64(),
			ImplKey:   it.ImplKey,
		})
	}

	op, operatorName := currentTopologyOperator(c)
	res, err := topology.ClaimServices(c.Request.Context(), models.DB, op, items)
	if err != nil {
		var mismatch *topology.ClaimPlacementMismatchError
		switch {
		case errors.As(err, &mismatch):
			c.JSON(http.StatusUnprocessableEntity, mismatch)
		case errors.Is(err, topology.ErrClaimForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrModuleServiceNotFound), errors.Is(err, topology.ErrModuleNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrClaimAlreadyClaimed):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrClaimEmptyItems), errors.Is(err, topology.ErrSplitInvalidInput), errors.Is(err, topology.ErrContractLibNoProvide):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	reloadDecoderRegistryQuietly()
	NotifyAttributionJobWorker()

	commonAudit.SetAuditContext(
		c,
		"contract_arch",
		"claim_services",
		commonModels.AuditLevelP1,
		fmt.Sprintf("用户 [%s] 批量认领 %d 项现网发现服务", operatorName, res.ClaimedCount),
		"module_service",
		strconv.FormatUint(items[0].ServiceID, 10),
		fmt.Sprintf("claim_%d_services", res.ClaimedCount),
		req.Items,
		res,
	)
	c.JSON(http.StatusOK, res)
}

type splitImplItemReq struct {
	ArchProcessID FlexUint64 `json:"arch_process_id"`
	ModuleID      FlexUint64 `json:"module_id"`
	ImplKey       string     `json:"impl_key"`
}

type splitMultiImplReq struct {
	SourceServiceID FlexUint64         `json:"source_service_id"`
	DefinerModuleID FlexUint64         `json:"definer_module_id"`
	Implementations []splitImplItemReq `json:"implementations"`
}

// SplitMultiImplServices handles POST /api/arch/services/split-multi-impl (AdminOnly).
func SplitMultiImplServices(c *gin.Context) {
	var req splitMultiImplReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	impls := make([]topology.SplitImplInput, 0, len(req.Implementations))
	for _, it := range req.Implementations {
		impls = append(impls, topology.SplitImplInput{
			ArchProcessID: it.ArchProcessID.Uint64(),
			ModuleID:      it.ModuleID.Uint64(),
			ImplKey:       it.ImplKey,
		})
	}

	op, operatorName := currentTopologyOperator(c)
	res, err := topology.SplitMultiImpl(c.Request.Context(), models.DB, op, topology.SplitMultiImplInput{
		SourceServiceID: req.SourceServiceID.Uint64(),
		DefinerModuleID: req.DefinerModuleID.Uint64(),
		Implementations: impls,
	})
	if err != nil {
		if writeArchLayerOrCycleError(c, err) {
			return
		}
		var mismatch *topology.ClaimPlacementMismatchError
		switch {
		case errors.As(err, &mismatch):
			c.JSON(http.StatusUnprocessableEntity, mismatch)
		case errors.Is(err, topology.ErrClaimForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrModuleServiceNotFound), errors.Is(err, topology.ErrModuleNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrDefinerNotContractLib),
			errors.Is(err, topology.ErrContractImplProcessConflict),
			errors.Is(err, topology.ErrClaimAlreadyClaimed),
			errors.Is(err, topology.ErrServiceNameOccupiedByContract),
			errors.Is(err, topology.ErrModuleAlreadyImplementsContract):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrSplitInvalidInput),
			errors.Is(err, topology.ErrInvalidImplKey),
			errors.Is(err, topology.ErrContractLibNoProvide):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	reloadDecoderRegistryQuietly()
	NotifyAttributionJobWorker()

	contractID := uint64(0)
	contractName := ""
	if res.Contract != nil {
		contractID = res.Contract.ID
		contractName = res.Contract.ServiceName
	}
	commonAudit.SetAuditContext(
		c,
		"contract_arch",
		"split_multi_impl",
		commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 将多宿主服务 [%s] 拆分为 %d 个多实现", operatorName, contractName, len(res.Implementations)),
		"service_contract",
		strconv.FormatUint(contractID, 10),
		contractName,
		req,
		res,
	)
	c.JSON(http.StatusOK, res)
}

type unclaimArchServiceReq struct {
	ServiceID FlexUint64 `json:"service_id"`
}

// UnclaimArchService handles POST /api/arch/services/unclaim (ModuleOwnerOrAdminGuard + I6/I7).
func UnclaimArchService(c *gin.Context) {
	var req unclaimArchServiceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	op, operatorName := currentTopologyOperator(c)
	res, err := topology.UnclaimService(c.Request.Context(), models.DB, op, req.ServiceID.Uint64())
	if err != nil {
		var depRefErr *topology.DependencyReferencedError
		switch {
		case errors.As(err, &depRefErr):
			c.JSON(http.StatusConflict, depRefErr)
		case errors.Is(err, topology.ErrClaimForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrModuleServiceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrUnclaimNotClaimed):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	reloadDecoderRegistryQuietly()

	commonAudit.SetAuditContext(
		c,
		"contract_arch",
		"unclaim_service",
		commonModels.AuditLevelP1,
		fmt.Sprintf("用户 [%s] 取消服务 #%d 认领（结果: %s）", operatorName, req.ServiceID.Uint64(), res.Action),
		"module_service",
		strconv.FormatUint(res.ServiceID, 10),
		fmt.Sprintf("svc_%d", res.ServiceID),
		req,
		res,
	)
	c.JSON(http.StatusOK, res)
}

type decideDepSuggestionReq struct {
	ConsumerArchProcessID FlexUint64  `json:"consumer_arch_process_id"`
	ServiceID             FlexUint64  `json:"service_id"`
	Decision              string      `json:"decision"`
	ConsumerModuleID      *FlexUint64 `json:"consumer_module_id"`
	Rationale             string      `json:"rationale"`
}

// DecideArchDependencySuggestion handles POST /api/arch/dependency-suggestions/decide (AdminOnly).
func DecideArchDependencySuggestion(c *gin.Context) {
	var req decideDepSuggestionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	op, operatorName := currentTopologyOperator(c)
	res, err := topology.DecideDependencySuggestion(c.Request.Context(), models.DB, op, topology.DecideInput{
		ConsumerArchProcessID: req.ConsumerArchProcessID.Uint64(),
		ServiceID:             req.ServiceID.Uint64(),
		Decision:              req.Decision,
		ConsumerModuleID:      req.ConsumerModuleID.Uint64(),
		Rationale:             req.Rationale,
	})
	if err != nil {
		if writeArchLayerOrCycleError(c, err) {
			return
		}
		switch {
		case errors.Is(err, topology.ErrClaimForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrArchProcessNotFound), errors.Is(err, topology.ErrModuleServiceNotFound), errors.Is(err, topology.ErrModuleNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrDuplicateContractDependency), errors.Is(err, topology.ErrDuplicateDependency):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, topology.ErrDecideInvalidInput),
			errors.Is(err, topology.ErrDecideConsumerModuleRequired),
			errors.Is(err, topology.ErrDecideConsumerModuleNotInProcess),
			errors.Is(err, topology.ErrSelfDependency):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	commonAudit.SetAuditContext(
		c,
		"contract_arch",
		"decide_dep_suggestion",
		commonModels.AuditLevelP1,
		fmt.Sprintf("管理员 [%s] 对依赖建议 (proc=%d, svc=%d) 裁决为 %s", operatorName, req.ConsumerArchProcessID.Uint64(), req.ServiceID.Uint64(), res.Decision),
		"dependency_decision",
		fmt.Sprintf("%d_%d", req.ConsumerArchProcessID.Uint64(), req.ServiceID.Uint64()),
		res.Decision,
		req,
		res,
	)
	c.JSON(http.StatusOK, res)
}
