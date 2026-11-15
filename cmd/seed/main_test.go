package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/internal/server/dashboard"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/protodoc"
	"code-contract/internal/server/topology"
	"code-contract/models"
)

func TestSeedDemoData_CompleteGraphAndDecoderAliases(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}

	demoPbPath := filepath.Join("..", "..", "demo", "demo.pb")

	// 连续执行两次验证 TRUNCATE + 重建幂等性
	for run := 1; run <= 2; run++ {
		if err := seedDemoData(db, demoPbPath); err != nil {
			t.Fatalf("第 %d 次执行 seedDemoData 失败: %v", run, err)
		}
	}

	// 1. 验证模块、LayerLevel 与架构分层防环约束
	var modules []models.Module
	if err := db.Find(&modules).Error; err != nil {
		t.Fatalf("查询模块失败: %v", err)
	}
	if len(modules) != 7 {
		t.Fatalf("期望 7 个功能模块，实际 %d", len(modules))
	}

	expectedLayers := map[string]int{
		"LIB_PAYMENT_CONTRACT": 1,
		"ACCOUNT_CORE":         2,
		"INVENTORY_CORE":       2,
		"PAYMENT_ALIPAY":       2,
		"PAYMENT_WECHAT":       2,
		"ORDER_CORE":           3,
		"CLIENT_TUI":           4,
	}
	modByCode := make(map[string]models.Module, len(modules))
	for _, m := range modules {
		modByCode[m.Code] = m
		wantLayer, ok := expectedLayers[m.Code]
		if !ok {
			t.Fatalf("发现未预期模块 %s", m.Code)
		}
		if m.LayerLevel != wantLayer {
			t.Fatalf("模块 %s layer_level=%d，期望 %d", m.Code, m.LayerLevel, wantLayer)
		}
	}

	if err := topology.ValidateAllTopologyLayerAndCycleConstraints(db); err != nil {
		t.Fatalf("拓扑分层或无环约束校验失败: %v", err)
	}

	libMod := modByCode["LIB_PAYMENT_CONTRACT"]
	if libMod.ModuleKind != models.ModuleKindContractLib {
		t.Fatalf("LIB_PAYMENT_CONTRACT module_kind 错误: got %q", libMod.ModuleKind)
	}

	// 2. 验证 ServiceContract 与多渠道自定义服务名实现
	var contract models.ServiceContract
	if err := db.Where("service_name = ?", "demo.payment.PaymentService").First(&contract).Error; err != nil {
		t.Fatalf("查询支付多实现契约失败: %v", err)
	}
	if contract.ProvideMode != models.ContractProvideModeMultiImpl || contract.DefinerModuleID != libMod.ID {
		t.Fatalf("支付契约属性不匹配: %+v", contract)
	}

	for _, tc := range []struct {
		serviceName string
		implKey     string
	}{
		{"demo.payment.ALIPAY_PaymentService", "ALIPAY"},
		{"demo.payment.WECHAT_PaymentService", "WECHAT"},
	} {
		var svc models.ModuleService
		if err := db.Where("service_name = ?", tc.serviceName).First(&svc).Error; err != nil {
			t.Fatalf("查询服务实现 %s 失败: %v", tc.serviceName, err)
		}
		if svc.ContractID == nil || *svc.ContractID != contract.ID {
			t.Fatalf("服务 %s 未关联支付契约 %d: %+v", tc.serviceName, contract.ID, svc)
		}
		if !svc.IsCustomServiceName || svc.ImplKey != tc.implKey {
			t.Fatalf("服务 %s 自定义服务名标记或 impl_key 错误: %+v", tc.serviceName, svc)
		}
	}

	// 3. 验证 ORDER_CORE -> PaymentService DISPATCH 依赖与 6 个物理挂载进程
	var dispatchCount int64
	if err := db.Model(&models.ModuleDependency{}).Where("contract_id = ?", contract.ID).Count(&dispatchCount).Error; err != nil {
		t.Fatalf("统计 DISPATCH 依赖失败: %v", err)
	}
	if dispatchCount != 1 {
		t.Fatalf("期望 1 条 DISPATCH 契约依赖，实际 %d", dispatchCount)
	}

	var procCount int64
	if err := db.Model(&models.Process{}).Count(&procCount).Error; err != nil {
		t.Fatalf("统计挂载进程失败: %v", err)
	}
	if procCount != 6 {
		t.Fatalf("期望 6 个挂载物理进程，实际 %d", procCount)
	}

	// 4. 验证 Protobuf 解码注册中心通过契约别名映射解析自定义服务名
	reg, err := decoder.NewRegistry(db)
	if err != nil {
		t.Fatalf("初始化 decoder.Registry 失败: %v", err)
	}
	for _, svcName := range []string{
		"demo.payment.ALIPAY_PaymentService",
		"demo.payment.WECHAT_PaymentService",
	} {
		for _, opName := range []string{"AuthorizePay", "RefundPay"} {
			op := reg.LookupOperation(svcName, opName)
			if op == nil {
				t.Fatalf("decoder 未能通过 LIB 契约别名解析 %s:%s", svcName, opName)
			}
			if op.ServiceName != svcName {
				t.Fatalf("LookupOperation 返回 ServiceName=%q，期望 %q", op.ServiceName, svcName)
			}
		}
	}

	// 5. 验证逻辑/物理拓扑图可正常构建且含 REALIZATION 边
	logicalGraph, err := topology.BuildTopologyGraph(db, topology.TopologyViewLogical)
	if err != nil {
		t.Fatalf("构建 LOGICAL 拓扑图失败: %v", err)
	}
	realizationCount := 0
	for _, edge := range logicalGraph.Edges {
		if edge.Kind == topology.EdgeKindRealization {
			realizationCount++
		}
	}
	if realizationCount != 2 {
		t.Fatalf("期望 LOGICAL 拓扑图含 2 条 REALIZATION 边，实际 %d", realizationCount)
	}

	physicalGraph, err := topology.BuildTopologyGraph(db, topology.TopologyViewPhysical)
	if err != nil {
		t.Fatalf("构建 PHYSICAL 拓扑图失败: %v", err)
	}
	if len(physicalGraph.Nodes) == 0 || len(physicalGraph.Edges) == 0 {
		t.Fatalf("PHYSICAL 拓扑图为空: nodes=%d edges=%d", len(physicalGraph.Nodes), len(physicalGraph.Edges))
	}

	// 6. 验证 Git 仓库、4 个 Bundle、只读源码文件及多版本历史
	var repoCount int64
	if err := db.Model(&models.GitRepo{}).Count(&repoCount).Error; err != nil {
		t.Fatalf("查询 GitRepo 失败: %v", err)
	}
	if repoCount != 1 {
		t.Fatalf("期望 1 个 GitRepo，实际 %d", repoCount)
	}

	var bundles []models.GitProtoBundle
	if err := db.Order("id ASC").Find(&bundles).Error; err != nil {
		t.Fatalf("查询 GitProtoBundle 失败: %v", err)
	}
	if len(bundles) != 4 {
		t.Fatalf("期望 4 个 GitProtoBundle，实际 %d", len(bundles))
	}
	for _, b := range bundles {
		if b.LastSchemaID == nil || *b.LastSchemaID == 0 {
			t.Fatalf("Bundle %s 缺少关联 last_schema_id", b.BundleName)
		}
	}

	var protoFileCount int64
	if err := db.Model(&models.ProtoFile{}).Where("is_deleted = false").Count(&protoFileCount).Error; err != nil {
		t.Fatalf("统计 ProtoFile 失败: %v", err)
	}
	if protoFileCount != 5 {
		t.Fatalf("期望 5 个活跃 ProtoFile，实际 %d", protoFileCount)
	}

	var versionCount int64
	if err := db.Model(&models.ProtoFileVersion{}).Count(&versionCount).Error; err != nil {
		t.Fatalf("统计 ProtoFileVersion 失败: %v", err)
	}
	if versionCount != 7 {
		t.Fatalf("期望 7 条 ProtoFileVersion 快照（order/payment 各含 2 版本），实际 %d", versionCount)
	}

	// 7. 验证 4 个激活的 ProtoSchema (GIT_SYNC) 与 14 个 ProtoOperation
	var activeSchemas []models.ProtoSchema
	if err := db.Where("is_active = ?", true).Find(&activeSchemas).Error; err != nil {
		t.Fatalf("查询活跃 ProtoSchema 失败: %v", err)
	}
	if len(activeSchemas) != 4 {
		t.Fatalf("期望 4 个活跃 ProtoSchema，实际 %d", len(activeSchemas))
	}
	for _, sc := range activeSchemas {
		if sc.SourceType != models.ProtoSourceGitSync {
			t.Fatalf("Schema %s SourceType=%q，期望 %q", sc.Name, sc.SourceType, models.ProtoSourceGitSync)
		}
	}

	var activeOpCount int64
	if err := db.Model(&models.ProtoOperation{}).Where("is_active = ?", true).Count(&activeOpCount).Error; err != nil {
		t.Fatalf("统计活跃 ProtoOperation 失败: %v", err)
	}
	if activeOpCount != 14 {
		t.Fatalf("期望 14 个活跃 ProtoOperation，实际 %d", activeOpCount)
	}

	// 8. 验证 ProtoDocSymbol 文档提取、标签 (@owner/@error_code/@deprecated) 与黄金样本解码联动
	var ownerTaggedCount int64
	if err := db.Model(&models.ProtoDocSymbol{}).Where("owner_tag <> ''").Count(&ownerTaggedCount).Error; err != nil {
		t.Fatalf("统计带 @owner 标签符号失败: %v", err)
	}
	if ownerTaggedCount == 0 {
		t.Fatalf("未提取到任何带 @owner 标签的 ProtoDocSymbol")
	}

	var deprecatedCount int64
	if err := db.Model(&models.ProtoDocSymbol{}).Where("is_deprecated = ?", true).Count(&deprecatedCount).Error; err != nil {
		t.Fatalf("统计弃用标记符号失败: %v", err)
	}
	if deprecatedCount == 0 {
		t.Fatalf("未提取到任何带 deprecated/@deprecated 标记的 ProtoDocSymbol")
	}

	docDetail, err := protodoc.GetServiceDocDetail(db, "demo.order.OrderService", nil)
	if err != nil {
		t.Fatalf("获取 demo.order.OrderService 文档详情失败: %v", err)
	}
	if docDetail.OwnerTag != "trade-order-team" {
		t.Fatalf("OrderService OwnerTag=%q，期望 trade-order-team", docDetail.OwnerTag)
	}
	if !docDetail.HasSourceInfo {
		t.Fatalf("OrderService 描述集应包含 SourceCodeInfo")
	}
	goldenDecodedOps := 0
	for _, m := range docDetail.Methods {
		if len(m.ErrorCodes) == 0 {
			t.Fatalf("OrderService 方法 %s 缺少解析后的 @error_code", m.Operation)
		}
		for _, gs := range m.GoldenSamples {
			if gs.DecodeStatus != "DECODED_JSON" {
				t.Fatalf("方法 %s 黄金样本解码状态=%q，期望 DECODED_JSON", m.Operation, gs.DecodeStatus)
			}
			goldenDecodedOps++
		}
	}
	if goldenDecodedOps != 3 {
		t.Fatalf("OrderService 期望匹配 3 条 DECODED_JSON 黄金样本，实际 %d", goldenDecodedOps)
	}

	// 9. 验证静态合规巡检报告、AST 重复结构体探测、MR 门禁战报、站内通知与大盘概览
	var reports []models.CheckReport
	if err := db.Order("id DESC").Find(&reports).Error; err != nil {
		t.Fatalf("查询 CheckReport 失败: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("期望 1 份 CheckReport，实际 %d", len(reports))
	}
	if reports[0].ErrorCount != 0 {
		t.Fatalf("期望演示 Proto 静态巡检 0 个 ERROR，实际 %d", reports[0].ErrorCount)
	}
	if reports[0].DuplicateGroupCount < 1 {
		t.Fatalf("期望静态巡检至少检出 1 个 AST 重复结构体簇，实际 %d", reports[0].DuplicateGroupCount)
	}

	var mrRuns []models.MRCheckRun
	if err := db.Order("mr_iid ASC").Find(&mrRuns).Error; err != nil {
		t.Fatalf("查询 MRCheckRun 失败: %v", err)
	}
	if len(mrRuns) != 3 {
		t.Fatalf("期望 3 条 MRCheckRun，实际 %d", len(mrRuns))
	}
	if mrRuns[0].Status != models.MRCheckStatusPassed ||
		mrRuns[1].Status != models.MRCheckStatusFailed ||
		mrRuns[2].Status != models.MRCheckStatusOverridden {
		t.Fatalf("MRCheckRun 状态不符合预期: [%s, %s, %s]", mrRuns[0].Status, mrRuns[1].Status, mrRuns[2].Status)
	}

	var notifications []models.Notification
	if err := db.Order("id ASC").Find(&notifications).Error; err != nil {
		t.Fatalf("查询 Notification 失败: %v", err)
	}
	if len(notifications) != 3 {
		t.Fatalf("期望 3 条 Notification，实际 %d", len(notifications))
	}
	unreadCount := 0
	for _, n := range notifications {
		if !n.IsRead {
			unreadCount++
		}
		if !strings.HasPrefix(n.LinkURL, "/proto-governance") {
			t.Fatalf("通知 %q LinkURL=%q 未指向新版 /proto-governance 工作台", n.Title, n.LinkURL)
		}
	}
	if unreadCount != 2 {
		t.Fatalf("期望 2 条未读站内信通知，实际 %d", unreadCount)
	}

	overview, err := dashboard.GetOverview(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("获取 dashboard.GetOverview 失败: %v", err)
	}
	if overview.StaticCompliance.TotalProtoFiles != 5 {
		t.Fatalf("StaticCompliance.TotalProtoFiles=%d，期望 5", overview.StaticCompliance.TotalProtoFiles)
	}
	if overview.StaticCompliance.DuplicateGroupCount < 1 {
		t.Fatalf("StaticCompliance.DuplicateGroupCount=%d，期望 >= 1", overview.StaticCompliance.DuplicateGroupCount)
	}
	if overview.StaticCompliance.PendingVocabTerms != 2 {
		t.Fatalf("StaticCompliance.PendingVocabTerms=%d，期望 2", overview.StaticCompliance.PendingVocabTerms)
	}
	if overview.StaticCompliance.RecentMRBlockedCount != 1 {
		t.Fatalf("StaticCompliance.RecentMRBlockedCount=%d，期望 1", overview.StaticCompliance.RecentMRBlockedCount)
	}
}
