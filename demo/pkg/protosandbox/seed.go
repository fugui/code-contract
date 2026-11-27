package protosandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code-contract/internal/server/gitclient"
	"code-contract/models"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// SeedOptions 控制沙箱数据库播种行为。
type SeedOptions struct {
	IncludeScaleRepos bool
}

// SeedSummary 汇总沙箱播种结果。
type SeedSummary struct {
	RepositoriesUpserted int      `json:"repositories_upserted"`
	SubsystemsUpserted   int      `json:"subsystems_upserted"`
	ModulesUpserted      int      `json:"modules_upserted"`
	ServicesUpserted     int      `json:"services_upserted"`
	VocabTermsUpserted   int      `json:"vocab_terms_upserted"`
	Warnings             []string `json:"warnings,omitempty"`
}

// CleanSummary 汇总沙箱彻底清理结果。
type CleanSummary struct {
	RepositoriesDeleted int64 `json:"repositories_deleted"`
	GitReposDeleted     int64 `json:"git_repos_deleted"`
	SchemasDeleted      int64 `json:"schemas_deleted"`
	ArchModulesDeleted  int64 `json:"arch_modules_deleted"`
	VocabDeleted        int64 `json:"vocab_deleted"`
	CacheRemoved        bool  `json:"cache_removed"`
}

// SeedSandboxDB 幂等播种平台 repositories、三级架构数字孪生、多实现契约、标准词汇与治理配置。
func SeedSandboxDB(ctx context.Context, db *gorm.DB, fixtures []RepoFixture, _ SeedOptions) (*SeedSummary, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	summary := &SeedSummary{}

	dept, user, err := ensureSandboxDeptAndUser(ctx, db)
	if err != nil {
		return nil, err
	}

	// 1. 播种平台 repositories 表（仅 sandbox-* 命名空间）
	for _, fx := range fixtures {
		sg := strings.TrimSpace(fx.ServiceGroup)
		if len(sg) > 30 {
			sg = sg[:30]
		}
		var existing models.Repository
		findErr := db.WithContext(ctx).Where("name = ?", fx.Name).First(&existing).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			rec := models.Repository{
				DepartmentID: dept.ID,
				Name:         fx.Name,
				URL:          fx.RepoURL(),
				HTTPURL:      fx.RepoURL(),
				OwnerID:      user.ID,
				Branch:       fx.Branch,
				ServiceGroup: sg,
				IsActive:     true,
			}
			if cErr := db.WithContext(ctx).Create(&rec).Error; cErr != nil {
				return nil, fmt.Errorf("创建平台仓 %s 失败: %w", fx.Name, cErr)
			}
			summary.RepositoriesUpserted++
		} else if findErr != nil {
			return nil, fmt.Errorf("查询平台仓 %s 失败: %w", fx.Name, findErr)
		} else {
			if uErr := db.WithContext(ctx).Model(&existing).Updates(map[string]any{
				"department_id": dept.ID,
				"url":           fx.RepoURL(),
				"http_url":      fx.RepoURL(),
				"owner_id":      user.ID,
				"branch":        fx.Branch,
				"service_group": sg,
				"is_active":     true,
			}).Error; uErr != nil {
				return nil, fmt.Errorf("更新平台仓 %s 失败: %w", fx.Name, uErr)
			}
			summary.RepositoriesUpserted++
		}
	}

	// 2. 对齐 contract_git_repos 并为特定沙箱仓设置默认治理覆写
	if _, _, _, alignErr := gitclient.AlignPlatformRepositories(ctx, db); alignErr != nil {
		return nil, fmt.Errorf("对齐 contract_git_repos 失败: %w", alignErr)
	}

	// 3. 播种架构数字孪生（SBX_* 子系统 / 功能组 / 进程组 / sbx-* 进程 / 模块 / 服务 / 依赖）
	if err := seedSandboxArchitecture(ctx, db, user.ID, summary); err != nil {
		return nil, err
	}

	// 4. 播种标准词库（SANDBOX_DEMO 领域）
	if err := seedSandboxVocab(ctx, db, user.ID, summary); err != nil {
		return nil, err
	}

	// 5. 增量合并 ADR-27/28 治理配置（保留用户原有全局规则，仅追加沙箱项）
	if err := mergeSandboxGovernanceConfig(ctx, db); err != nil {
		return nil, err
	}

	return summary, nil
}

func ensureSandboxDeptAndUser(ctx context.Context, db *gorm.DB) (models.Department, models.User, error) {
	var dept models.Department
	if err := db.WithContext(ctx).Order("id ASC").First(&dept).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return dept, models.User{}, fmt.Errorf("查询部门失败: %w", err)
		}
		dept = models.Department{Name: "SBX-DEV"}
		if cErr := db.WithContext(ctx).Create(&dept).Error; cErr != nil {
			return dept, models.User{}, fmt.Errorf("创建默认部门失败: %w", cErr)
		}
	}

	var user models.User
	if err := db.WithContext(ctx).Order("id ASC").First(&user).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return dept, user, fmt.Errorf("查询用户失败: %w", err)
		}
		deptID := dept.ID
		user = models.User{
			Username:     "sandbox_bot",
			Name:         "Sandbox Bot",
			Email:        "sandbox_bot@code-contract.local",
			Password:     "!sandbox-disabled!",
			IsActive:     true,
			DepartmentID: &deptID,
		}
		if cErr := db.WithContext(ctx).Create(&user).Error; cErr != nil {
			return dept, user, fmt.Errorf("创建默认用户失败: %w", cErr)
		}
	}
	return dept, user, nil
}

func seedSandboxArchitecture(ctx context.Context, db *gorm.DB, uid uint, summary *SeedSummary) error {
	subSpecs := []struct {
		Code  string
		Name  string
		Layer int
		Desc  string
	}{
		{"SBX_PLATFORM", "沙箱公共基础域", 1, "公共类型与多实现契约定义库"},
		{"SBX_ASSET", "沙箱账户资产域", 2, "用户钱包与账户余额服务"},
		{"SBX_FULFILL", "沙箱库存履约域", 2, "商品 SKU 库存锁定与履约"},
		{"SBX_PAYMENT", "沙箱支付渠道域", 2, "多实现支付网关集群"},
		{"SBX_TRADE", "沙箱交易网关域", 3, "电商订单聚合与 Saga 编排"},
		{"SBX_FABCTRL", "沙箱设备控制域", 2, "脱敏 uService 晶圆运动与光学控制"},
	}

	subByCode := make(map[string]models.Subsystem, len(subSpecs))
	fgBySubCode := make(map[string]models.FunctionalGroup, len(subSpecs))
	for _, sp := range subSpecs {
		var sub models.Subsystem
		err := db.WithContext(ctx).Where("code = ?", sp.Code).First(&sub).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			sub = models.Subsystem{
				Code:        sp.Code,
				Name:        sp.Name,
				OwnerID:     &uid,
				LayerLevel:  sp.Layer,
				Description: sp.Desc,
				CreatedBy:   uid,
				UpdatedBy:   uid,
			}
			if cErr := db.WithContext(ctx).Create(&sub).Error; cErr != nil {
				return fmt.Errorf("创建子系统 %s 失败: %w", sp.Code, cErr)
			}
		} else if err != nil {
			return err
		}
		subByCode[sp.Code] = sub
		summary.SubsystemsUpserted++

		fgCode := sp.Code + "_ROOT"
		var fg models.FunctionalGroup
		fgErr := db.WithContext(ctx).Where("subsystem_id = ? AND code = ?", sub.ID, fgCode).First(&fg).Error
		if errors.Is(fgErr, gorm.ErrRecordNotFound) {
			fg = models.FunctionalGroup{
				SubsystemID: sub.ID,
				Code:        fgCode,
				Name:        sp.Name + "核心组",
				Level:       1,
				Path:        "/",
				SortOrder:   1,
				CreatedBy:   uid,
				UpdatedBy:   uid,
			}
			if cErr := db.WithContext(ctx).Create(&fg).Error; cErr != nil {
				return fmt.Errorf("创建功能组 %s 失败: %w", fgCode, cErr)
			}
			_ = db.WithContext(ctx).Model(&fg).Update("path", fmt.Sprintf("/%d/", fg.ID)).Error
		} else if fgErr != nil {
			return fgErr
		}
		fgBySubCode[sp.Code] = fg
	}

	pgrpSpecs := []struct {
		Code string
		Name string
	}{
		{"SBX_ECOM_CLUSTER", "沙箱电商微前端集群"},
		{"SBX_FAB_CLUSTER", "沙箱设备控制集群"},
	}
	pgrpByCode := make(map[string]models.ArchProcessGroup, len(pgrpSpecs))
	for i, sp := range pgrpSpecs {
		var pg models.ArchProcessGroup
		err := db.WithContext(ctx).Where("code = ?", sp.Code).First(&pg).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			pg = models.ArchProcessGroup{
				Code:      sp.Code,
				Name:      sp.Name,
				SortOrder: i + 1,
				CreatedBy: uid,
				UpdatedBy: uid,
			}
			if cErr := db.WithContext(ctx).Create(&pg).Error; cErr != nil {
				return fmt.Errorf("创建进程组 %s 失败: %w", sp.Code, cErr)
			}
		} else if err != nil {
			return err
		}
		pgrpByCode[sp.Code] = pg
	}

	procSpecs := []struct {
		Code      string
		Name      string
		GroupCode string
		Port      int
		Role      string
		Cmd       string
	}{
		{"sbx-sut-order", "订单中枢网关 (SUT)", "SBX_ECOM_CLUSTER", 9001, models.ArchProcRoleDual, "sut-order"},
		{"sbx-backend-account", "账户资产服务", "SBX_ECOM_CLUSTER", 9002, models.ArchProcRoleServer, "backend-account"},
		{"sbx-backend-inventory", "库存履约服务", "SBX_ECOM_CLUSTER", 9003, models.ArchProcRoleServer, "backend-inventory"},
		{"sbx-payment-alipay", "支付宝支付通道", "SBX_ECOM_CLUSTER", 9004, models.ArchProcRoleServer, "backend-payment --channel=ALIPAY"},
		{"sbx-payment-wechat", "微信支付通道", "SBX_ECOM_CLUSTER", 9005, models.ArchProcRoleServer, "backend-payment --channel=WECHAT"},
		{"sbx-client-tui", "电商终端客户端", "SBX_ECOM_CLUSTER", 0, models.ArchProcRoleClientOnly, "client"},
		{"sbx-motion-ctrl", "晶圆台运动控制进程", "SBX_FAB_CLUSTER", 9101, models.ArchProcRoleServer, "fab-motion-ctrl"},
	}
	procByCode := make(map[string]models.ArchProcess, len(procSpecs))
	for i, sp := range procSpecs {
		var ap models.ArchProcess
		err := db.WithContext(ctx).Where("code = ?", sp.Code).First(&ap).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			ap = models.ArchProcess{
				GroupID:           pgrpByCode[sp.GroupCode].ID,
				Code:              sp.Code,
				Name:              sp.Name,
				DefaultListenPort: sp.Port,
				DefaultCmdPattern: sp.Cmd,
				DeclaredRole:      sp.Role,
				Lifecycle:         models.ArchProcLifecycleConfirmed,
				Origin:            models.ArchProcOriginManual,
				SortOrder:         i + 1,
				CreatedBy:         uid,
				UpdatedBy:         uid,
			}
			if cErr := db.WithContext(ctx).Create(&ap).Error; cErr != nil {
				return fmt.Errorf("创建进程蓝图 %s 失败: %w", sp.Code, cErr)
			}
		} else if err != nil {
			return err
		}
		procByCode[sp.Code] = ap
	}

	modSpecs := []struct {
		Code     string
		Name     string
		SubCode  string
		Kind     string
		ProcCode string
		Desc     string
	}{
		{"SBX_COMMON_LIB", "电商公共基础类型库", "SBX_PLATFORM", models.ModuleKindContractLib, "", "提供 OrderStatusEnum 与 TraceAuditMeta"},
		{"SBX_PAYMENT_LIB", "统一支付多实现契约库", "SBX_PLATFORM", models.ModuleKindContractLib, "", "定义 demo.payment.PaymentService 接口契约"},
		{"SBX_ORDER_CORE", "订单聚合编排模块", "SBX_TRADE", models.ModuleKindBusiness, "sbx-sut-order", "对外提供 demo.order.OrderService"},
		{"SBX_ACCOUNT_CORE", "账户资产核心模块", "SBX_ASSET", models.ModuleKindBusiness, "sbx-backend-account", "对外提供 demo.account.AccountService"},
		{"SBX_INVENTORY_CORE", "库存履约核心模块", "SBX_FULFILL", models.ModuleKindBusiness, "sbx-backend-inventory", "对外提供 demo.inventory.InventoryService"},
		{"SBX_PAYMENT_ALIPAY", "支付宝实现模块", "SBX_PAYMENT", models.ModuleKindBusiness, "sbx-payment-alipay", "PaymentService ALIPAY 实现"},
		{"SBX_PAYMENT_WECHAT", "微信实现模块", "SBX_PAYMENT", models.ModuleKindBusiness, "sbx-payment-wechat", "PaymentService WECHAT 实现"},
		{"SBX_MOTION_CORE", "晶圆运动控制模块", "SBX_FABCTRL", models.ModuleKindBusiness, "sbx-motion-ctrl", "预留模块，用于体验认领 StageMotionService"},
	}
	modByCode := make(map[string]models.Module, len(modSpecs))
	for _, sp := range modSpecs {
		var m models.Module
		err := db.WithContext(ctx).Where("code = ?", sp.Code).First(&m).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var procID *uint64
			if sp.Kind == models.ModuleKindBusiness && sp.ProcCode != "" {
				id := procByCode[sp.ProcCode].ID
				procID = &id
			}
			m = models.Module{
				SubsystemID:   subByCode[sp.SubCode].ID,
				GroupID:       fgBySubCode[sp.SubCode].ID,
				ArchProcessID: procID,
				ModuleKind:    sp.Kind,
				Code:          sp.Code,
				Name:          sp.Name,
				OwnerID:       &uid,
				Description:   sp.Desc,
				CreatedBy:     uid,
				UpdatedBy:     uid,
			}
			if cErr := db.WithContext(ctx).Create(&m).Error; cErr != nil {
				return fmt.Errorf("创建模块 %s 失败: %w", sp.Code, cErr)
			}
		} else if err != nil {
			return err
		}
		modByCode[sp.Code] = m
		summary.ModulesUpserted++
	}

	// 多实现服务契约: demo.payment.PaymentService
	payLib := modByCode["SBX_PAYMENT_LIB"]
	var payContract models.ServiceContract
	scErr := db.WithContext(ctx).Where("service_name = ?", "demo.payment.PaymentService").First(&payContract).Error
	if errors.Is(scErr, gorm.ErrRecordNotFound) {
		payContract = models.ServiceContract{
			ServiceName:     "demo.payment.PaymentService",
			DefinerModuleID: payLib.ID,
			ProvideMode:     models.ContractProvideModeMultiImpl,
			ImplKeyLabel:    "支付渠道",
			Description:     "沙箱多实现支付契约：ALIPAY 与 WECHAT 双实现共享一份 .proto 定义",
			CreatedBy:       uid,
			UpdatedBy:       uid,
		}
		if cErr := db.WithContext(ctx).Create(&payContract).Error; cErr != nil {
			return fmt.Errorf("创建多实现服务契约失败: %w", cErr)
		}
	} else if scErr != nil {
		return scErr
	}

	isSandboxMod := func(modID *uint64) bool {
		if modID == nil {
			return false
		}
		for _, m := range modByCode {
			if m.ID == *modID {
				return true
			}
		}
		return false
	}

	ensureSvc := func(modCode, svcName, protoSvcName, implKey string, contractID *uint64, customName bool) (*models.ModuleService, error) {
		mod := modByCode[modCode]
		var existing models.ModuleService
		err := db.WithContext(ctx).Where("service_name = ?", svcName).First(&existing).Error
		if err == nil {
			if !isSandboxMod(existing.ModuleID) {
				summary.Warnings = append(summary.Warnings, fmt.Sprintf("服务 %s 已被现有非沙箱模块 (id=%v) 认领，保留原归属", svcName, existing.ModuleID))
				return nil, nil
			}
			return &existing, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		modID := mod.ID
		ms := models.ModuleService{
			ModuleID:             &modID,
			ContractID:           contractID,
			ServiceName:          svcName,
			ProtoServiceName:     protoSvcName,
			ImplKey:              implKey,
			IsCustomServiceName:  customName,
			Source:               models.ServiceSourceManual,
			ProtoMatchConfidence: models.ProtoMatchConfExact,
			CreatedBy:            uid,
		}
		if cErr := db.WithContext(ctx).Create(&ms).Error; cErr != nil {
			return nil, fmt.Errorf("创建模块服务 %s 失败: %w", svcName, cErr)
		}
		summary.ServicesUpserted++
		return &ms, nil
	}

	_, err := ensureSvc("SBX_ORDER_CORE", "demo.order.OrderService", "demo.order.OrderService", "", nil, false)
	if err != nil {
		return err
	}
	accSvc, err := ensureSvc("SBX_ACCOUNT_CORE", "demo.account.AccountService", "demo.account.AccountService", "", nil, false)
	if err != nil {
		return err
	}
	invSvc, err := ensureSvc("SBX_INVENTORY_CORE", "demo.inventory.InventoryService", "demo.inventory.InventoryService", "", nil, false)
	if err != nil {
		return err
	}
	if payContract.ID > 0 {
		if _, err := ensureSvc("SBX_PAYMENT_ALIPAY", "demo.payment.ALIPAY_PaymentService", "demo.payment.PaymentService", "ALIPAY", &payContract.ID, true); err != nil {
			return err
		}
		if _, err := ensureSvc("SBX_PAYMENT_WECHAT", "demo.payment.WECHAT_PaymentService", "demo.payment.PaymentService", "WECHAT", &payContract.ID, true); err != nil {
			return err
		}
	}

	// 依赖关系：SBX_ORDER_CORE -> Account / Inventory / PaymentService(MULTI_IMPL)
	orderMod := modByCode["SBX_ORDER_CORE"]
	if accSvc != nil {
		accModID := modByCode["SBX_ACCOUNT_CORE"].ID
		var cnt int64
		_ = db.WithContext(ctx).Model(&models.ModuleDependency{}).
			Where("consumer_module_id = ? AND service_id = ?", orderMod.ID, accSvc.ID).Count(&cnt).Error
		if cnt == 0 {
			_ = db.WithContext(ctx).Create(&models.ModuleDependency{
				ConsumerModuleID: orderMod.ID,
				ServiceID:        &accSvc.ID,
				ProviderModuleID: &accModID,
				Description:      "下单扣减与余额查询",
				CreatedBy:        uid,
			}).Error
		}
	}
	if invSvc != nil {
		invModID := modByCode["SBX_INVENTORY_CORE"].ID
		var cnt int64
		_ = db.WithContext(ctx).Model(&models.ModuleDependency{}).
			Where("consumer_module_id = ? AND service_id = ?", orderMod.ID, invSvc.ID).Count(&cnt).Error
		if cnt == 0 {
			_ = db.WithContext(ctx).Create(&models.ModuleDependency{
				ConsumerModuleID: orderMod.ID,
				ServiceID:        &invSvc.ID,
				ProviderModuleID: &invModID,
				Description:      "TCC 库存锁扣与补偿释放",
				CreatedBy:        uid,
			}).Error
		}
	}
	if payContract.ID > 0 && isSandboxMod(&payContract.DefinerModuleID) {
		var cnt int64
		_ = db.WithContext(ctx).Model(&models.ModuleDependency{}).
			Where("consumer_module_id = ? AND contract_id = ?", orderMod.ID, payContract.ID).Count(&cnt).Error
		if cnt == 0 {
			_ = db.WithContext(ctx).Create(&models.ModuleDependency{
				ConsumerModuleID: orderMod.ID,
				ContractID:       &payContract.ID,
				Description:      "按支付渠道路由至 ALIPAY / WECHAT 多实现",
				CreatedBy:        uid,
			}).Error
		}
	}

	return nil
}

func seedSandboxVocab(ctx context.Context, db *gorm.DB, uid uint, summary *SeedSummary) error {
	terms := []struct {
		Term   string
		DescEn string
		DescZh string
	}{
		{"order", "Order domain identifier", "订单实体"},
		{"sku", "Stock Keeping Unit", "商品库存编码"},
		{"cents", "Monetary amount in cents", "金额分单位"},
		{"audit", "Trace and audit metadata", "全链路审计头"},
		{"stage", "Wafer stage motion axis", "晶圆运动台"},
		{"optics", "Optical alignment module", "光学对准"},
	}
	for _, t := range terms {
		var existing models.VocabularyTerm
		err := db.WithContext(ctx).Where("term = ?", t.Term).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rec := models.VocabularyTerm{
				Term:          t.Term,
				DescriptionEn: t.DescEn,
				DescriptionZh: t.DescZh,
				Aliases:       datatypes.JSON([]byte("[]")),
				SimilarTerms:  datatypes.JSON([]byte("[]")),
				Domain:        SandboxVocabDomain,
				Source:        models.VocabSourceManual,
				IsApproved:    true,
				CreatedBy:     uid,
			}
			if cErr := db.WithContext(ctx).Create(&rec).Error; cErr != nil {
				return fmt.Errorf("创建词库词条 %s 失败: %w", t.Term, cErr)
			}
			summary.VocabTermsUpserted++
		} else if err != nil {
			return err
		}
	}
	return nil
}

var sandboxExcludeGlobs = []string{
	"**/alpha/**",
	"**/beta/**",
	"**/m1/**",
	"**/m2/**",
	"**/m3/**",
}

func mergeSandboxGovernanceConfig(ctx context.Context, db *gorm.DB) error {
	snap, err := gitclient.LoadActiveGovernanceConfig(ctx, db)
	if err != nil {
		return err
	}
	doc := snap.Doc
	if doc.Repos == nil {
		doc.Repos = map[string]gitclient.RepoGovernanceSpec{}
	}

	hasBase := false
	for _, s := range doc.Global.SharedBaseSources {
		if strings.TrimSpace(s.PathHint) == SandboxSharedBaseRel {
			hasBase = true
			break
		}
	}
	if !hasBase {
		doc.Global.SharedBaseSources = append(doc.Global.SharedBaseSources, gitclient.SharedBaseSourceSpec{
			PathHint: SandboxSharedBaseRel,
			Priority: 1,
		})
	}

	doc.Global.ExcludeSubsystems = appendUniqueStr(doc.Global.ExcludeSubsystems, "SBX-3M")
	for _, g := range sandboxExcludeGlobs {
		doc.Global.ExcludeGlobs = appendUniqueStr(doc.Global.ExcludeGlobs, g)
	}

	var invRepo models.GitRepo
	invSpec := doc.Repos["sandbox-inventory-svc"]
	if err := db.WithContext(ctx).Where("name = ?", "sandbox-inventory-svc").First(&invRepo).Error; err == nil {
		invSpec.PlatformRepoID = invRepo.PlatformRepoID
	}
	invSpec.StripPrefixes = []string{"server/pg/proto/"}
	doc.Repos["sandbox-inventory-svc"] = invSpec

	_, _, _, saveErr := gitclient.SaveGovernanceConfig(ctx, db, doc, "proto-sandbox")
	return saveErr
}

func reverseSandboxGovernanceConfig(ctx context.Context, db *gorm.DB) error {
	var cnt int64
	if err := db.WithContext(ctx).Model(&models.ProtoGovernanceConfig{}).Count(&cnt).Error; err != nil || cnt == 0 {
		return nil
	}
	snap, err := gitclient.LoadActiveGovernanceConfig(ctx, db)
	if err != nil || snap == nil {
		return err
	}
	doc := snap.Doc
	changed := false

	filteredBases := make([]gitclient.SharedBaseSourceSpec, 0, len(doc.Global.SharedBaseSources))
	for _, s := range doc.Global.SharedBaseSources {
		if strings.TrimSpace(s.PathHint) == SandboxSharedBaseRel {
			changed = true
			continue
		}
		filteredBases = append(filteredBases, s)
	}
	doc.Global.SharedBaseSources = filteredBases

	if filteredSubs, removed := removeStr(doc.Global.ExcludeSubsystems, "SBX-3M"); removed {
		doc.Global.ExcludeSubsystems = filteredSubs
		changed = true
	}
	for _, g := range sandboxExcludeGlobs {
		if filteredGlobs, removed := removeStr(doc.Global.ExcludeGlobs, g); removed {
			doc.Global.ExcludeGlobs = filteredGlobs
			changed = true
		}
	}
	if _, ok := doc.Repos["sandbox-inventory-svc"]; ok {
		delete(doc.Repos, "sandbox-inventory-svc")
		changed = true
	}
	if !changed {
		return nil
	}
	_, _, _, err = gitclient.SaveGovernanceConfig(ctx, db, doc, "proto-sandbox-clean")
	return err
}

// CleanSandboxDB 严格按沙箱命名空间（sandbox-* / SBX_* / sbx-* / SANDBOX_DEMO）清理数据库与本地缓存。
func CleanSandboxDB(ctx context.Context, db *gorm.DB, codesDir, protosDir string) (*CleanSummary, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	summary := &CleanSummary{}

	var platRepos []models.Repository
	if err := db.WithContext(ctx).Where("url LIKE ? OR name LIKE ?", SandboxURLPrefix+"%", "sandbox-%").Find(&platRepos).Error; err != nil {
		return nil, err
	}
	platIDs := make([]uint64, 0, len(platRepos))
	for _, r := range platRepos {
		platIDs = append(platIDs, uint64(r.ID))
	}

	var gitRepos []models.GitRepo
	if len(platIDs) > 0 {
		_ = db.WithContext(ctx).Where("platform_repo_id IN ? OR name LIKE ?", platIDs, "sandbox-%").Find(&gitRepos).Error
	} else {
		_ = db.WithContext(ctx).Where("name LIKE ?", "sandbox-%").Find(&gitRepos).Error
	}
	gitRepoIDs := make([]uint64, 0, len(gitRepos))
	for _, gr := range gitRepos {
		gitRepoIDs = append(gitRepoIDs, gr.ID)
	}

	var bundles []models.GitProtoBundle
	if len(gitRepoIDs) > 0 {
		_ = db.WithContext(ctx).Where("git_repo_id IN ?", gitRepoIDs).Find(&bundles).Error
	}
	bundleIDs := make([]uint64, 0, len(bundles))
	schemaIDSet := make(map[uint64]struct{})
	for _, b := range bundles {
		bundleIDs = append(bundleIDs, b.ID)
		if b.LastSchemaID != nil && *b.LastSchemaID > 0 {
			schemaIDSet[*b.LastSchemaID] = struct{}{}
		}
	}

	var linkedSchemas []models.ProtoSchema
	if len(gitRepoIDs) > 0 {
		_ = db.WithContext(ctx).Select("id").Where("git_repo_id IN ? OR name LIKE ?", gitRepoIDs, "sandbox-%").Find(&linkedSchemas).Error
	} else {
		_ = db.WithContext(ctx).Select("id").Where("name LIKE ?", "sandbox-%").Find(&linkedSchemas).Error
	}
	for _, sc := range linkedSchemas {
		schemaIDSet[sc.ID] = struct{}{}
	}
	schemaIDs := make([]uint64, 0, len(schemaIDSet))
	for id := range schemaIDSet {
		schemaIDs = append(schemaIDs, id)
	}

	var mrRunIDs []uint64
	if len(gitRepoIDs) > 0 {
		_ = db.WithContext(ctx).Model(&models.MRCheckRun{}).Where("git_repo_id IN ?", gitRepoIDs).Pluck("id", &mrRunIDs).Error
	}

	reportIDSet := make(map[uint64]struct{})
	addReportIDs := func(ids []uint64) {
		for _, id := range ids {
			reportIDSet[id] = struct{}{}
		}
	}
	if len(gitRepoIDs) > 0 {
		var rIDs []uint64
		_ = db.WithContext(ctx).Model(&models.CheckReport{}).Where("git_repo_id IN ?", gitRepoIDs).Pluck("id", &rIDs).Error
		addReportIDs(rIDs)
	}
	if len(bundleIDs) > 0 {
		var rIDs []uint64
		_ = db.WithContext(ctx).Model(&models.CheckReport{}).Where("bundle_id IN ?", bundleIDs).Pluck("id", &rIDs).Error
		addReportIDs(rIDs)
	}
	if len(schemaIDs) > 0 {
		var rIDs []uint64
		_ = db.WithContext(ctx).Model(&models.CheckReport{}).Where("schema_id IN ?", schemaIDs).Pluck("id", &rIDs).Error
		addReportIDs(rIDs)
	}
	if len(mrRunIDs) > 0 {
		var rIDs []uint64
		_ = db.WithContext(ctx).Model(&models.CheckReport{}).Where("mr_check_run_id IN ?", mrRunIDs).Pluck("id", &rIDs).Error
		addReportIDs(rIDs)
	}
	reportIDs := make([]uint64, 0, len(reportIDSet))
	for id := range reportIDSet {
		reportIDs = append(reportIDs, id)
	}

	var protoFileIDs []uint64
	if len(gitRepoIDs) > 0 {
		_ = db.WithContext(ctx).Model(&models.ProtoFile{}).Where("git_repo_id IN ?", gitRepoIDs).Pluck("id", &protoFileIDs).Error
	}

	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(mrRunIDs) > 0 {
			if err := tx.Where("source_type = ? AND source_id IN ?", models.NotifSourceMRCheckRun, mrRunIDs).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
		}
		if len(schemaIDs) > 0 {
			if err := tx.Where("source_type = ? AND source_id IN ?", models.NotifSourceProtoSchema, schemaIDs).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
		}
		if len(reportIDs) > 0 {
			if err := tx.Where("source_type = ? AND source_id IN ?", models.NotifSourceCheckReport, reportIDs).Delete(&models.Notification{}).Error; err != nil {
				return err
			}
		}
		if len(mrRunIDs) > 0 {
			if err := tx.Where("id IN ?", mrRunIDs).Delete(&models.MRCheckRun{}).Error; err != nil {
				return err
			}
		}
		if len(reportIDs) > 0 {
			if err := tx.Where("report_id IN ?", reportIDs).Delete(&models.CheckViolation{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", reportIDs).Delete(&models.CheckReport{}).Error; err != nil {
				return err
			}
		}
		if len(schemaIDs) > 0 {
			if err := tx.Where("schema_id IN ?", schemaIDs).Delete(&models.ProtoDocSymbol{}).Error; err != nil {
				return err
			}
			if err := tx.Where("schema_id IN ?", schemaIDs).Delete(&models.ProtoOperation{}).Error; err != nil {
				return err
			}
		}
		if len(protoFileIDs) > 0 {
			if err := tx.Where("proto_file_id IN ?", protoFileIDs).Delete(&models.ProtoFileVersion{}).Error; err != nil {
				return err
			}
		}
		if len(bundleIDs) > 0 {
			if err := tx.Where("owner_bundle_id IN ?", bundleIDs).Delete(&models.ProtoImportOwner{}).Error; err != nil {
				return err
			}
		}
		if len(gitRepoIDs) > 0 {
			if err := tx.Where("git_repo_id IN ?", gitRepoIDs).Delete(&models.ProtoFile{}).Error; err != nil {
				return err
			}
			if err := tx.Where("git_repo_id IN ?", gitRepoIDs).Delete(&models.GitProtoBundle{}).Error; err != nil {
				return err
			}
		}
		if len(schemaIDs) > 0 {
			res := tx.Where("id IN ?", schemaIDs).Delete(&models.ProtoSchema{})
			if res.Error != nil {
				return res.Error
			}
			summary.SchemasDeleted = res.RowsAffected
		}
		if len(gitRepoIDs) > 0 {
			res := tx.Where("id IN ?", gitRepoIDs).Delete(&models.GitRepo{})
			if res.Error != nil {
				return res.Error
			}
			summary.GitReposDeleted = res.RowsAffected
		}
		if len(platIDs) > 0 {
			res := tx.Where("id IN ?", platIDs).Delete(&models.Repository{})
			if res.Error != nil {
				return res.Error
			}
			summary.RepositoriesDeleted = res.RowsAffected
		}

		// 清理沙箱架构实体 (SBX_* / sbx-*)
		var sbxModIDs []uint64
		_ = tx.Model(&models.Module{}).Where("code LIKE ?", "SBX_%").Pluck("id", &sbxModIDs).Error
		if len(sbxModIDs) > 0 {
			if err := tx.Where("consumer_module_id IN ? OR provider_module_id IN ?", sbxModIDs, sbxModIDs).Delete(&models.ModuleDependency{}).Error; err != nil {
				return err
			}
			if err := tx.Where("module_id IN ?", sbxModIDs).Delete(&models.ModuleService{}).Error; err != nil {
				return err
			}
			var contractIDs []uint64
			_ = tx.Model(&models.ServiceContract{}).Where("definer_module_id IN ?", sbxModIDs).Pluck("id", &contractIDs).Error
			if len(contractIDs) > 0 {
				if err := tx.Where("contract_id IN ?", contractIDs).Delete(&models.ModuleDependency{}).Error; err != nil {
					return err
				}
				if err := tx.Where("contract_id IN ?", contractIDs).Delete(&models.ModuleService{}).Error; err != nil {
					return err
				}
				if err := tx.Where("id IN ?", contractIDs).Delete(&models.ServiceContract{}).Error; err != nil {
					return err
				}
			}
			res := tx.Where("id IN ?", sbxModIDs).Delete(&models.Module{})
			if res.Error != nil {
				return res.Error
			}
			summary.ArchModulesDeleted = res.RowsAffected
		}
		if err := tx.Where("code LIKE ?", "SBX_%").Delete(&models.FunctionalGroup{}).Error; err != nil {
			return err
		}
		if err := tx.Where("code LIKE ?", "SBX_%").Delete(&models.Subsystem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("code LIKE ?", "sbx-%").Delete(&models.ArchProcess{}).Error; err != nil {
			return err
		}
		if err := tx.Where("code LIKE ?", "SBX_%").Delete(&models.ArchProcessGroup{}).Error; err != nil {
			return err
		}

		vRes := tx.Where("domain = ?", SandboxVocabDomain).Delete(&models.VocabularyTerm{})
		if vRes.Error != nil {
			return vRes.Error
		}
		summary.VocabDeleted = vRes.RowsAffected
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	if err := reverseSandboxGovernanceConfig(ctx, db); err != nil {
		return nil, err
	}

	// 清理本地共享缓存中的沙箱目录与死软链
	if strings.TrimSpace(codesDir) != "" {
		absCodes, err := filepath.Abs(filepath.Clean(codesDir))
		if err == nil {
			sandboxDir := filepath.Join(absCodes, SandboxCacheSubdir)
			if _, sErr := os.Stat(sandboxDir); sErr == nil {
				if rmErr := os.RemoveAll(sandboxDir); rmErr == nil {
					summary.CacheRemoved = true
				}
			}
		}
	}
	if strings.TrimSpace(protosDir) != "" {
		treeMgr := gitclient.NewProtoTreeManager(protosDir)
		for _, gid := range gitRepoIDs {
			treeMgr.RemoveRepoTree(gitclient.TreeKindRaw, gid)
			treeMgr.RemoveRepoTree(gitclient.TreeKindClean, gid)
		}
	}
	return summary, nil
}

func appendUniqueStr(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

func removeStr(slice []string, target string) ([]string, bool) {
	out := make([]string, 0, len(slice))
	removed := false
	for _, s := range slice {
		if s == target {
			removed = true
			continue
		}
		out = append(out, s)
	}
	return out, removed
}

var _ = json.Marshal
