package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/version"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	demoPbPath := flag.String("proto", "demo/demo.pb", "Protobuf 描述集路径")
	flag.Parse()

	log.Println("==========================================================")
	log.Println(" 开始清除 Code-Contract 数据并根据 Demo 进程拓扑与 Proto 治理基线重构数据...")
	log.Println("==========================================================")

	// 1. 加载配置
	if err := config.LoadConfig(*configPath); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 2. 初始化数据库连接
	if err := models.InitDB(); err != nil {
		log.Fatalf("初始化数据库连接失败: %v", err)
	}

	if err := seedDemoData(models.DB, *demoPbPath); err != nil {
		log.Fatalf("执行 Demo 数据重构失败: %v", err)
	}

	log.Println("==========================================================")
	log.Println(" 全部 Demo 进程、架构拓扑与 Proto 全生命周期治理数据重构完成！")
	log.Println("==========================================================")
}

func mustCreate(db *gorm.DB, label string, value interface{}) error {
	if err := db.Create(value).Error; err != nil {
		return fmt.Errorf("%s 失败: %w", label, err)
	}
	return nil
}

func createFunctionalGroup(db *gorm.DB, fg *models.FunctionalGroup) error {
	if err := db.Create(fg).Error; err != nil {
		return fmt.Errorf("创建功能组 %s 失败: %w", fg.Code, err)
	}
	fg.Path = fmt.Sprintf("/%d/", fg.ID)
	if err := db.Model(fg).Update("path", fg.Path).Error; err != nil {
		return fmt.Errorf("更新功能组 %s 路径失败: %w", fg.Code, err)
	}
	return nil
}

func seedDemoData(db *gorm.DB, demoPbPath string) error {
	// 3. 清理所有 contract_* 业务数据表 (保留平台公共 users/departments/sys_audit_logs 与 contract_settings)
	log.Println("[1/9] 清空历史 contract_* 业务数据表...")
	truncateSQL := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", strings.Join(models.BusinessTableNames(), ", "))
	if err := db.Exec(truncateSQL).Error; err != nil {
		return fmt.Errorf("清空表失败: %w", err)
	}

	// 4. 重建默认公共 Agent (demo-local-agent)
	log.Println("[2/9] 重建默认本地 Agent...")
	agent := models.Agent{
		Name:            "demo-local-agent",
		Endpoint:        "http://127.0.0.1:9091",
		Scope:           models.ScopeShared,
		Status:          models.AgentStatusOnline,
		KernelVersion:   "Linux",
		HasEbpfCap:      true,
		CapDetail:       "EUID=0 (Full root privileges)",
		AgentVersion:    version.Short(),
		ProtocolVersion: "1.0",
		MountsRevision:  1,
		CreatedBy:       1,
	}
	if err := mustCreate(db, "创建 Agent", &agent); err != nil {
		return err
	}
	log.Printf("  • 已创建 Agent [%s] (ID: %d)", agent.Name, agent.ID)

	// 5. 重建子系统与功能组 (含 M22 ADR-18 架构层级水位 LayerLevel)
	log.Println("[3/9] 重建子系统与功能组（含架构分层水位）...")
	subsysEcommerce := models.Subsystem{
		Code:        "ECOMMERCE",
		Name:        "电商交易微服务系统",
		LayerLevel:  2,
		Description: "基于 CROS RPC 协议构建的高并发电商分布式微服务集群（含交易中枢、账户资产、仓储履约与多渠道支付 LIB 契约）",
		CreatedBy:   1,
	}
	if err := mustCreate(db, "创建电商子系统", &subsysEcommerce); err != nil {
		return err
	}

	subsysClient := models.Subsystem{
		Code:        "CLIENT",
		Name:        "客户端交互子系统",
		LayerLevel:  4,
		Description: "包含 TUI 交互式控制台终端与压力测试流量发生器",
		CreatedBy:   1,
	}
	if err := mustCreate(db, "创建客户端子系统", &subsysClient); err != nil {
		return err
	}

	fgTrade := models.FunctionalGroup{
		SubsystemID: subsysEcommerce.ID,
		Code:        "TRADE_CORE",
		Name:        "交易中枢与网关",
		Description: "负责核心交易流程编排、两阶段履约锁定与分布式事务补偿",
		Level:       1,
		CreatedBy:   1,
	}
	if err := createFunctionalGroup(db, &fgTrade); err != nil {
		return err
	}

	fgAsset := models.FunctionalGroup{
		SubsystemID: subsysEcommerce.ID,
		Code:        "ASSET_GROUP",
		Name:        "账户与资产服务",
		Description: "用户账本资产管理、余额扣减与退款逆向入账",
		Level:       1,
		CreatedBy:   1,
	}
	if err := createFunctionalGroup(db, &fgAsset); err != nil {
		return err
	}

	fgInventory := models.FunctionalGroup{
		SubsystemID: subsysEcommerce.ID,
		Code:        "INVENTORY_GROUP",
		Name:        "仓储与履约服务",
		Description: "商品实时行情、库存预占锁定与出库实扣确认",
		Level:       1,
		CreatedBy:   1,
	}
	if err := createFunctionalGroup(db, &fgInventory); err != nil {
		return err
	}

	fgPayment := models.FunctionalGroup{
		SubsystemID: subsysEcommerce.ID,
		Code:        "PAYMENT_GROUP",
		Name:        "支付通道与接口契约组",
		Description: "包含统一支付 LIB 契约库以及支付宝/微信等多渠道独立实现实例",
		Level:       1,
		CreatedBy:   1,
	}
	if err := createFunctionalGroup(db, &fgPayment); err != nil {
		return err
	}

	fgCli := models.FunctionalGroup{
		SubsystemID: subsysClient.ID,
		Code:        "CLI_TOOLS",
		Name:        "终端工具组",
		Description: "包含键盘交互式终端与测试场景发压组件",
		Level:       1,
		CreatedBy:   1,
	}
	if err := createFunctionalGroup(db, &fgCli); err != nil {
		return err
	}

	log.Printf("  • 已创建 2 个子系统与 5 个功能组")

	// 6. 重建标准架构物理进程组与蓝图 (ArchProcessGroups & ArchProcesses)
	log.Println("[4/9] 重建标准架构物理进程组与进程蓝图 (ADR-09, M10)...")
	apgGateway := models.ArchProcessGroup{
		Code:        "GROUP_GATEWAY",
		Name:        "网关与交易中枢组",
		Description: "承接外部业务流量并编排微服务调用",
		SortOrder:   1,
		CreatedBy:   1,
	}
	if err := mustCreate(db, "创建进程组 GROUP_GATEWAY", &apgGateway); err != nil {
		return err
	}

	apgDownstream := models.ArchProcessGroup{
		Code:        "GROUP_DOWNSTREAM",
		Name:        "核心下游微服务组",
		Description: "高并发核心领域微服务（账户与库存履约）",
		SortOrder:   2,
		CreatedBy:   1,
	}
	if err := mustCreate(db, "创建进程组 GROUP_DOWNSTREAM", &apgDownstream); err != nil {
		return err
	}

	apgClient := models.ArchProcessGroup{
		Code:        "GROUP_CLIENT",
		Name:        "交互终端与发压组",
		Description: "测试控制台终端与流量发生器",
		SortOrder:   3,
		CreatedBy:   1,
	}
	if err := mustCreate(db, "创建进程组 GROUP_CLIENT", &apgClient); err != nil {
		return err
	}

	apSutOrder := models.ArchProcess{
		GroupID:                 apgGateway.ID,
		Code:                    "PROC_SUT_ORDER",
		Name:                    "sut-order (订单核心网关)",
		DefaultListenPort:       9001,
		DefaultCmdPattern:       "sut-order",
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
		SortOrder:               1,
		Description:             "电商交易被测核心网关服务，监听 9001 端口",
		CreatedBy:               1,
	}
	if err := mustCreate(db, "创建架构进程 PROC_SUT_ORDER", &apSutOrder); err != nil {
		return err
	}

	apBackendAccount := models.ArchProcess{
		GroupID:                 apgDownstream.ID,
		Code:                    "PROC_BACKEND_ACCOUNT",
		Name:                    "backend-account (资产服务)",
		DefaultListenPort:       9002,
		DefaultCmdPattern:       "backend-account",
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
		SortOrder:               2,
		Description:             "核心下游账户资产服务，监听 9002 端口",
		CreatedBy:               1,
	}
	if err := mustCreate(db, "创建架构进程 PROC_BACKEND_ACCOUNT", &apBackendAccount); err != nil {
		return err
	}

	apBackendInventory := models.ArchProcess{
		GroupID:                 apgDownstream.ID,
		Code:                    "PROC_BACKEND_INVENTORY",
		Name:                    "backend-inventory (库存服务)",
		DefaultListenPort:       9003,
		DefaultCmdPattern:       "backend-inventory",
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
		SortOrder:               3,
		Description:             "核心下游仓储库存服务，监听 9003 端口",
		CreatedBy:               1,
	}
	if err := mustCreate(db, "创建架构进程 PROC_BACKEND_INVENTORY", &apBackendInventory); err != nil {
		return err
	}

	apBackendPayAlipay := models.ArchProcess{
		GroupID:                 apgDownstream.ID,
		Code:                    "PROC_BACKEND_PAYMENT_ALIPAY",
		Name:                    "backend-payment-alipay (支付宝支付实例)",
		DefaultListenPort:       9004,
		DefaultCmdPattern:       "channel=ALIPAY",
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
		SortOrder:               4,
		Description:             "支付 LIB 契约的支付宝实现实例，监听 9004 端口",
		CreatedBy:               1,
	}
	if err := mustCreate(db, "创建架构进程 PROC_BACKEND_PAYMENT_ALIPAY", &apBackendPayAlipay); err != nil {
		return err
	}

	apBackendPayWechat := models.ArchProcess{
		GroupID:                 apgDownstream.ID,
		Code:                    "PROC_BACKEND_PAYMENT_WECHAT",
		Name:                    "backend-payment-wechat (微信支付实例)",
		DefaultListenPort:       9005,
		DefaultCmdPattern:       "channel=WECHAT",
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
		SortOrder:               5,
		Description:             "支付 LIB 契约的微信支付实现实例，监听 9005 端口",
		CreatedBy:               1,
	}
	if err := mustCreate(db, "创建架构进程 PROC_BACKEND_PAYMENT_WECHAT", &apBackendPayWechat); err != nil {
		return err
	}

	apClientTui := models.ArchProcess{
		GroupID:                 apgClient.ID,
		Code:                    "PROC_CLIENT_TUI",
		Name:                    "client (TUI交互客户端)",
		DefaultListenPort:       0,
		DefaultCmdPattern:       "client",
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
		SortOrder:               6,
		Description:             "TUI 终端交互客户端，发压与交易触发器",
		CreatedBy:               1,
	}
	if err := mustCreate(db, "创建架构进程 PROC_CLIENT_TUI", &apClientTui); err != nil {
		return err
	}

	log.Printf("  • 已创建 3 个架构物理进程组与 6 个标准架构进程蓝图")

	// 7. 重建功能模块（L1 基础契约库 -> L2 领域服务/实现 -> L3 交易中枢网关 -> L4 终端客户端）
	log.Println("[5/9] 重建功能模块、服务契约与依赖拓扑并执行层级单调性与无环验证...")
	modOrder := models.Module{
		SubsystemID:   subsysEcommerce.ID,
		GroupID:       fgTrade.ID,
		ArchProcessID: &apSutOrder.ID,
		LayerLevel:    3,
		Code:          "ORDER_CORE",
		Name:          "订单交易核心模块",
		Description:   "负责订单生命周期、两阶段预占与状态流转（L3 编排网关层）",
		CreatedBy:     1,
	}
	if err := mustCreate(db, "创建模块 ORDER_CORE", &modOrder); err != nil {
		return err
	}

	modAccount := models.Module{
		SubsystemID:   subsysEcommerce.ID,
		GroupID:       fgAsset.ID,
		ArchProcessID: &apBackendAccount.ID,
		LayerLevel:    2,
		Code:          "ACCOUNT_CORE",
		Name:          "账户资产核心模块",
		Description:   "资金账本与扣减补偿服务（L2 领域核心层）",
		CreatedBy:     1,
	}
	if err := mustCreate(db, "创建模块 ACCOUNT_CORE", &modAccount); err != nil {
		return err
	}

	modInventory := models.Module{
		SubsystemID:   subsysEcommerce.ID,
		GroupID:       fgInventory.ID,
		ArchProcessID: &apBackendInventory.ID,
		LayerLevel:    2,
		Code:          "INVENTORY_CORE",
		Name:          "仓储库存履约模块",
		Description:   "库存两阶段预占与释放管理（L2 领域核心层）",
		CreatedBy:     1,
	}
	if err := mustCreate(db, "创建模块 INVENTORY_CORE", &modInventory); err != nil {
		return err
	}

	modClient := models.Module{
		SubsystemID:   subsysClient.ID,
		GroupID:       fgCli.ID,
		ArchProcessID: &apClientTui.ID,
		LayerLevel:    4,
		Code:          "CLIENT_TUI",
		Name:          "客户端交互终端模块",
		Description:   "TUI 控制台客户端与录制触发入口（L4 终端接入层）",
		CreatedBy:     1,
	}
	if err := mustCreate(db, "创建模块 CLIENT_TUI", &modClient); err != nil {
		return err
	}

	modLibPayment := models.Module{
		SubsystemID: subsysEcommerce.ID,
		GroupID:     fgPayment.ID,
		ModuleKind:  models.ModuleKindContractLib,
		LayerLevel:  1,
		Code:        "LIB_PAYMENT_CONTRACT",
		Name:        "统一支付通道接口契约库",
		Description: "ADR-16 v2.0 示例接口模块（CONTRACT_LIB，L1 基础契约层，仅承载统一 .pb 服务契约）",
		CreatedBy:   1,
	}
	if err := mustCreate(db, "创建模块 LIB_PAYMENT_CONTRACT", &modLibPayment); err != nil {
		return err
	}

	modPayAlipay := models.Module{
		SubsystemID:   subsysEcommerce.ID,
		GroupID:       fgPayment.ID,
		ArchProcessID: &apBackendPayAlipay.ID,
		ModuleKind:    models.ModuleKindBusiness,
		LayerLevel:    2,
		Code:          "PAYMENT_ALIPAY",
		Name:          "支付宝渠道支付模块",
		Description:   "支付 LIB 契约的支付宝业务实现（L2，发布自定义服务名 demo.payment.ALIPAY_PaymentService）",
		CreatedBy:     1,
	}
	if err := mustCreate(db, "创建模块 PAYMENT_ALIPAY", &modPayAlipay); err != nil {
		return err
	}

	modPayWechat := models.Module{
		SubsystemID:   subsysEcommerce.ID,
		GroupID:       fgPayment.ID,
		ArchProcessID: &apBackendPayWechat.ID,
		ModuleKind:    models.ModuleKindBusiness,
		LayerLevel:    2,
		Code:          "PAYMENT_WECHAT",
		Name:          "微信渠道支付模块",
		Description:   "支付 LIB 契约的微信支付业务实现（L2，发布自定义服务名 demo.payment.WECHAT_PaymentService）",
		CreatedBy:     1,
	}
	if err := mustCreate(db, "创建模块 PAYMENT_WECHAT", &modPayWechat); err != nil {
		return err
	}

	modMap := map[string]models.Module{
		"ORDER_CORE":           modOrder,
		"ACCOUNT_CORE":         modAccount,
		"INVENTORY_CORE":       modInventory,
		"CLIENT_TUI":           modClient,
		"LIB_PAYMENT_CONTRACT": modLibPayment,
		"PAYMENT_ALIPAY":       modPayAlipay,
		"PAYMENT_WECHAT":       modPayWechat,
	}

	// 7.0 多实现接口契约定义 (ServiceContract)
	payContract := models.ServiceContract{
		DefinerModuleID: modLibPayment.ID,
		ServiceName:     "demo.payment.PaymentService",
		ProvideMode:     models.ContractProvideModeMultiImpl,
		ImplKeyLabel:    "支付渠道编码 (channel)",
		Description:     "统一支付渠道接口 LIB 契约，各实现实例可自定义运行期服务名并共用 .pb 解码",
		CreatedBy:       1,
	}
	if err := mustCreate(db, "创建支付多实现契约", &payContract); err != nil {
		return err
	}

	// 7.1 服务契约 (ModuleServices)
	svcOrder := models.ModuleService{
		ModuleID:        modOrder.ID,
		ServiceName:     "demo.order.OrderService",
		DiffIgnorePaths: datatypes.JSON([]byte("[]")),
		Description:     "CROS RPC 订单微服务对外契约",
		CreatedBy:       1,
	}
	if err := mustCreate(db, "创建服务契约 demo.order.OrderService", &svcOrder); err != nil {
		return err
	}

	svcAccount := models.ModuleService{
		ModuleID:        modAccount.ID,
		ServiceName:     "demo.account.AccountService",
		DiffIgnorePaths: datatypes.JSON([]byte("[]")),
		Description:     "CROS RPC 账户资产微服务契约",
		CreatedBy:       1,
	}
	if err := mustCreate(db, "创建服务契约 demo.account.AccountService", &svcAccount); err != nil {
		return err
	}

	svcInventory := models.ModuleService{
		ModuleID:        modInventory.ID,
		ServiceName:     "demo.inventory.InventoryService",
		DiffIgnorePaths: datatypes.JSON([]byte("[]")),
		Description:     "CROS RPC 仓储库存微服务契约",
		CreatedBy:       1,
	}
	if err := mustCreate(db, "创建服务契约 demo.inventory.InventoryService", &svcInventory); err != nil {
		return err
	}

	svcPayAlipay := models.ModuleService{
		ModuleID:            modPayAlipay.ID,
		ServiceName:         "demo.payment.ALIPAY_PaymentService",
		ContractID:          &payContract.ID,
		ImplKey:             "ALIPAY",
		IsCustomServiceName: true,
		DiffIgnorePaths:     datatypes.JSON([]byte("[]")),
		Description:         "支付宝渠道运行期自定义服务名实现",
		CreatedBy:           1,
	}
	if err := mustCreate(db, "创建服务契约 ALIPAY_PaymentService", &svcPayAlipay); err != nil {
		return err
	}

	svcPayWechat := models.ModuleService{
		ModuleID:            modPayWechat.ID,
		ServiceName:         "demo.payment.WECHAT_PaymentService",
		ContractID:          &payContract.ID,
		ImplKey:             "WECHAT",
		IsCustomServiceName: true,
		DiffIgnorePaths:     datatypes.JSON([]byte("[]")),
		Description:         "微信渠道运行期自定义服务名实现",
		CreatedBy:           1,
	}
	if err := mustCreate(db, "创建服务契约 WECHAT_PaymentService", &svcPayWechat); err != nil {
		return err
	}

	// 7.2 模块间依赖 (ModuleDependencies)
	depOrderAccount := models.ModuleDependency{
		ConsumerModuleID: modOrder.ID,
		IsExternal:       false,
		ServiceID:        &svcAccount.ID,
		ProviderModuleID: &modAccount.ID,
		Description:      "下单与退款时划扣或返还买家余额",
		CreatedBy:        1,
	}
	if err := mustCreate(db, "创建依赖 ORDER->ACCOUNT", &depOrderAccount); err != nil {
		return err
	}

	depOrderInventory := models.ModuleDependency{
		ConsumerModuleID: modOrder.ID,
		IsExternal:       false,
		ServiceID:        &svcInventory.ID,
		ProviderModuleID: &modInventory.ID,
		Description:      "下单时锁定与实扣库存，退款时释放库存",
		CreatedBy:        1,
	}
	if err := mustCreate(db, "创建依赖 ORDER->INVENTORY", &depOrderInventory); err != nil {
		return err
	}

	depOrderPayDispatch := models.ModuleDependency{
		ConsumerModuleID: modOrder.ID,
		IsExternal:       false,
		ContractID:       &payContract.ID,
		Description:      "DISPATCH 泛化契约依赖：按支付渠道路由调用 ALIPAY 或 WECHAT 实例",
		CreatedBy:        1,
	}
	if err := mustCreate(db, "创建 DISPATCH 依赖 ORDER->PaymentService", &depOrderPayDispatch); err != nil {
		return err
	}

	depOrderUnionPay := models.ModuleDependency{
		ConsumerModuleID: modOrder.ID,
		IsExternal:       true,
		ExternalSystem:   "UNIONPAY",
		ExternalService:  "pay.v1.UnionPayService",
		Description:      "外部银联通道实名结算网关",
		CreatedBy:        1,
	}
	if err := mustCreate(db, "创建外部依赖 UNIONPAY", &depOrderUnionPay); err != nil {
		return err
	}

	depClientOrder := models.ModuleDependency{
		ConsumerModuleID: modClient.ID,
		IsExternal:       false,
		ServiceID:        &svcOrder.ID,
		ProviderModuleID: &modOrder.ID,
		Description:      "TUI 交互控制台发起交易请求",
		CreatedBy:        1,
	}
	if err := mustCreate(db, "创建依赖 CLIENT->ORDER", &depClientOrder); err != nil {
		return err
	}

	if err := topology.ValidateAllTopologyLayerAndCycleConstraints(db); err != nil {
		return fmt.Errorf("拓扑分层水位或无环不变式校验未通过: %w", err)
	}

	// 8. 为本地 Agent 挂载全部 6 个物理业务进程 (contract_processes)
	procs := []models.Process{
		{
			AgentID:             agent.ID,
			ArchProcessID:       apSutOrder.ID,
			EffectiveListenPort: 9001,
			OverrideCmdPattern:  "sut-order",
			AdvertiseIP:         "127.0.0.1",
			ExtraDownstreams:    datatypes.JSON([]byte("[]")),
			ProbeDesiredState:   models.ProbeDesiredStateArmed,
			ProbeStatus:         models.ProbeStatusArmed,
			Description:         "sut-order 核心网关进程实例 (:9001)",
			CreatedBy:           1,
		},
		{
			AgentID:             agent.ID,
			ArchProcessID:       apBackendAccount.ID,
			EffectiveListenPort: 9002,
			OverrideCmdPattern:  "backend-account",
			AdvertiseIP:         "127.0.0.1",
			ExtraDownstreams:    datatypes.JSON([]byte("[]")),
			ProbeDesiredState:   models.ProbeDesiredStateArmed,
			ProbeStatus:         models.ProbeStatusArmed,
			Description:         "backend-account 资产服务进程实例 (:9002)",
			CreatedBy:           1,
		},
		{
			AgentID:             agent.ID,
			ArchProcessID:       apBackendInventory.ID,
			EffectiveListenPort: 9003,
			OverrideCmdPattern:  "backend-inventory",
			AdvertiseIP:         "127.0.0.1",
			ExtraDownstreams:    datatypes.JSON([]byte("[]")),
			ProbeDesiredState:   models.ProbeDesiredStateArmed,
			ProbeStatus:         models.ProbeStatusArmed,
			Description:         "backend-inventory 库存履约进程实例 (:9003)",
			CreatedBy:           1,
		},
		{
			AgentID:             agent.ID,
			ArchProcessID:       apBackendPayAlipay.ID,
			EffectiveListenPort: 9004,
			OverrideCmdPattern:  "channel=ALIPAY",
			AdvertiseIP:         "127.0.0.1",
			ExtraDownstreams:    datatypes.JSON([]byte("[]")),
			ProbeDesiredState:   models.ProbeDesiredStateArmed,
			ProbeStatus:         models.ProbeStatusArmed,
			Description:         "backend-payment ALIPAY 渠道实例 (:9004)",
			CreatedBy:           1,
		},
		{
			AgentID:             agent.ID,
			ArchProcessID:       apBackendPayWechat.ID,
			EffectiveListenPort: 9005,
			OverrideCmdPattern:  "channel=WECHAT",
			AdvertiseIP:         "127.0.0.1",
			ExtraDownstreams:    datatypes.JSON([]byte("[]")),
			ProbeDesiredState:   models.ProbeDesiredStateArmed,
			ProbeStatus:         models.ProbeStatusArmed,
			Description:         "backend-payment WECHAT 渠道实例 (:9005)",
			CreatedBy:           1,
		},
		{
			AgentID:             agent.ID,
			ArchProcessID:       apClientTui.ID,
			EffectiveListenPort: 0,
			OverrideCmdPattern:  "client",
			AdvertiseIP:         "127.0.0.1",
			ExtraDownstreams:    datatypes.JSON([]byte("[]")),
			ProbeDesiredState:   models.ProbeDesiredStateArmed,
			ProbeStatus:         models.ProbeStatusArmed,
			Description:         "client TUI 客户端交互终端实例",
			CreatedBy:           1,
		},
	}

	for _, p := range procs {
		if err := db.Create(&p).Error; err != nil {
			return fmt.Errorf("挂载进程 [%d] 失败: %w", p.ArchProcessID, err)
		}
	}
	if err := db.Model(&agent).Update("mounts_revision", 1).Error; err != nil {
		return fmt.Errorf("更新 Agent mounts_revision 失败: %w", err)
	}
	log.Printf("  • 已创建 7 个功能模块（L1~L4 分层合法）、5 个服务契约、5 项调用依赖及 %d 个物理挂载进程", len(procs))

	// 9. 初始化解码注册中心并构建 Proto 全生命周期治理、文档符号、黄金回归基线、静态巡检与门禁通知数据
	decoderReg, err := decoder.NewRegistry(db)
	if err != nil {
		return fmt.Errorf("初始化 Protobuf 解码注册中心失败: %w", err)
	}

	return seedGovernanceAndLifecycleData(db, decoderReg, demoPbPath, modMap, apSutOrder.ID)
}
