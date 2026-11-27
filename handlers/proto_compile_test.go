package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	commonAudit "code-common/backend/audit"
	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/internal/server/decoder"
	"code-contract/models"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestProtoCompile_LinksDirectlyIntoBreakingChangeRadar(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)
	adminUser := createTestUserInDB(t, db, "admin_compile", config.AdminRole)
	normalUser := createTestUserInDB(t, db, "normal_compile", "user")
	adminToken := generateTestJWT(t, adminUser)
	normalToken := generateTestJWT(t, normalUser)

	// 0. 准备下游血缘消费关系：MOD_ORDER (PROVIDE order.v1.OrderService) <- MOD_GATEWAY (DEPEND)
	sub := models.Subsystem{Code: "SUB_COMPILE_TEST", Name: "编译雷达子系统", CreatedBy: adminUser.ID}
	db.Create(&sub)
	grp := models.FunctionalGroup{SubsystemID: sub.ID, Code: "GRP_COMPILE", Name: "编译雷达组", Path: "/GRP_COMPILE", CreatedBy: adminUser.ID}
	db.Create(&grp)
	procGrp := models.ArchProcessGroup{Code: "PG_COMPILE", Name: "编译雷达进程组", CreatedBy: adminUser.ID}
	db.Create(&procGrp)
	archProc := models.ArchProcess{GroupID: procGrp.ID, Code: "sut-gateway", Name: "网关标准进程", CreatedBy: adminUser.ID}
	db.Create(&archProc)

	modProv := models.Module{
		SubsystemID: sub.ID,
		GroupID:     grp.ID,
		Code:        "MOD_ORDER",
		Name:        "订单服务模块",
		ModuleKind:  models.ModuleKindBusiness,
		CreatedBy:   adminUser.ID,
	}
	db.Create(&modProv)

	modCons := models.Module{
		SubsystemID:   sub.ID,
		GroupID:       grp.ID,
		ArchProcessID: &archProc.ID,
		Code:          "MOD_GATEWAY",
		Name:          "边界网关模块",
		ModuleKind:    models.ModuleKindBusiness,
		OwnerID:       &adminUser.ID,
		CreatedBy:     adminUser.ID,
	}
	db.Create(&modCons)

	modSvc := models.ModuleService{
		ModuleID:    models.Uint64Ptr(modProv.ID),
		ServiceName: "order.v1.OrderService",
		CreatedBy:   adminUser.ID,
	}
	db.Create(&modSvc)

	dep := models.ModuleDependency{
		ConsumerModuleID: modCons.ID,
		ServiceID:        &modSvc.ID,
		CreatedBy:        adminUser.ID,
	}
	db.Create(&dep)

	// 1. ADR-29 废止在线 HTTP POST /api/proto-schemas/compile 入口，管理员与普通用户均应收到 404
	userReqBody, _ := json.Marshal(map[string]any{
		"name": "order-compile",
		"files": []map[string]string{
			{"path": "order.proto", "content": `syntax = "proto3"; package order.v1; message Empty {}`},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/proto-schemas/compile", bytes.NewReader(userReqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+normalToken)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("已废止的 compile 路由应返回 404，实测 %d: %s", resp.Code, resp.Body.String())
	}

	reg, err := handlers.GetDecoderRegistry()
	if err != nil {
		t.Fatalf("GetDecoderRegistry 失败: %v", err)
	}

	// 2. 路径穿越拦截 -> decoder.ErrPathTraversalDenied
	if _, _, _, pErr := reg.CompileAndSaveSchema(context.Background(), decoder.CompileProtoBundleInput{
		Name:       "order-compile",
		SourceType: models.ProtoSourceGitSync,
		Files: map[string]string{
			"../escape.proto": `syntax = "proto3"; package p; message M {}`,
		},
	}); pErr == nil || !errors.Is(pErr, decoder.ErrPathTraversalDenied) {
		t.Fatalf("路径穿越应返回 ErrPathTraversalDenied，实测: %v", pErr)
	}

	// 3. 语法错误定位行号/列号 -> SchemaCompileError + 非零行号诊断
	if _, _, _, sErr := reg.CompileAndSaveSchema(context.Background(), decoder.CompileProtoBundleInput{
		Name:       "order-compile",
		SourceType: models.ProtoSourceGitSync,
		Files: map[string]string{
			"order/bad.proto": "syntax = \"proto3\";\npackage order.v1;\nmessage BadReq {\n  string missing_tag = ;\n}\n",
		},
	}); sErr == nil {
		t.Fatalf("语法错误应编译失败")
	} else {
		var compErr *decoder.CompileError
		if !errors.As(sErr, &compErr) || len(compErr.Diagnostics) == 0 || compErr.Diagnostics[0].Line <= 0 {
			t.Errorf("语法错误应返回 CompileError 与非零行号诊断，实测: %v", sErr)
		}
	}

	// 4. 编译并激活 BASE_LIB (0-Service common/base.proto)
	if _, _, _, err := compileTestProtoSchema(t, db, "common-base-online", "BASE_LIB", "", map[string]string{
		"common/base.proto": "syntax = \"proto3\";\npackage common.v1;\nmessage Meta {\n  string req_id = 1;\n}\n",
	}, true, false, ""); err != nil {
		t.Fatalf("编译并激活 BASE_LIB v1 失败: %v", err)
	}

	// 5. 编译并激活业务包 v1（引用 Tier 3 common/base.proto 并保留 SourceCodeInfo）
	orderV1Schema, v1Ops, docCnt, err := compileTestProtoSchema(t, db, "order-compile", "BUSINESS", "v1.0.0", map[string]string{
		"order/order.proto": `syntax = "proto3";
package order.v1;
import "common/base.proto";
message CreateOrderReq {
  common.v1.Meta meta = 1;
  string sku = 2;
  int64 qty = 3;
}
message CreateOrderRsp {
  string order_id = 1;
}
service OrderService {
  rpc CreateOrder(CreateOrderReq) returns (CreateOrderRsp);
}
`,
	}, true, false, "")
	if err != nil {
		t.Fatalf("编译并激活 BUSINESS v1 失败: %v", err)
	}
	if orderV1Schema.SourceType != models.ProtoSourceGitSync || len(v1Ops) != 1 || docCnt <= 0 {
		t.Errorf("v1 编译结果异常: src=%s ops=%d doc=%d", orderV1Schema.SourceType, len(v1Ops), docCnt)
	}

	// 验证 GET /api/proto-schemas?source_type=GIT_SYNC 过滤与字段返回
	listReq := httptest.NewRequest(http.MethodGet, "/api/proto-schemas?source_type=GIT_SYNC", nil)
	listReq.Header.Set("Authorization", "Bearer "+normalToken)
	listResp := httptest.NewRecorder()
	r.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK || !bytes.Contains(listResp.Body.Bytes(), []byte("GIT_SYNC")) {
		t.Fatalf("GET /api/proto-schemas?source_type=GIT_SYNC 失败: code=%d body=%s", listResp.Code, listResp.Body.String())
	}

	// 7. 编译删字段而不保留 reserved 的 v2 草稿并尝试 HTTP 激活 -> 触发 F-26 409 BREAKING_CHANGE_DETECTED
	orderV2Draft, _, _, err := compileTestProtoSchema(t, db, "order-compile", "BUSINESS", "", map[string]string{
		"order/order.proto": `syntax = "proto3";
package order.v1;
import "common/base.proto";
message CreateOrderReq {
  common.v1.Meta meta = 1;
  string sku = 2;
}
message CreateOrderRsp {
  string order_id = 1;
}
service OrderService {
  rpc CreateOrder(CreateOrderReq) returns (CreateOrderRsp);
}
`,
	}, false, false, "")
	if err != nil {
		t.Fatalf("编译 v2 草稿失败: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/proto-schemas/%d/activate", orderV2Draft.ID), bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("破坏性 v2 激活应被 F-26 拦截返回 409，实测 %d: %s", resp.Code, resp.Body.String())
	}
	var conflictResp struct {
		Code               string                          `json:"code"`
		BreakingRules      []decoder.BreakingRuleViolation `json:"breaking_rules"`
		AffectedDependents []decoder.AffectedDependent     `json:"affected_dependents"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &conflictResp)
	if conflictResp.Code != "BREAKING_CHANGE_DETECTED" || len(conflictResp.BreakingRules) == 0 || len(conflictResp.AffectedDependents) == 0 {
		t.Fatalf("应返回 BREAKING_CHANGE_DETECTED 及下游受影响模块，实测: %+v", conflictResp)
	}
	if conflictResp.AffectedDependents[0].ModuleCode != "MOD_GATEWAY" {
		t.Errorf("期望下游受影响模块为 MOD_GATEWAY，实测: %+v", conflictResp.AffectedDependents)
	}

	// 8. 同时验证 BASE_LIB 变更 (common/base.proto req_id string -> int64) 触发反向闭包影响 MOD_ORDER & MOD_GATEWAY
	_, _, _, baseBreakErr := compileTestProtoSchema(t, db, "common-base-online", "BASE_LIB", "", map[string]string{
		"common/base.proto": "syntax = \"proto3\";\npackage common.v1;\nmessage Meta {\n  int64 req_id = 1;\n}\n",
	}, true, false, "")
	var baseBreakTyped *decoder.SchemaBreakingError
	if !errors.As(baseBreakErr, &baseBreakTyped) {
		t.Fatalf("BASE_LIB 破坏性变更应返回 SchemaBreakingError，实测: %v", baseBreakErr)
	}
	seenMod := make(map[string]bool)
	for _, ad := range baseBreakTyped.AffectedDependents {
		seenMod[ad.ModuleCode] = true
	}
	if !seenMod["MOD_ORDER"] || !seenMod["MOD_GATEWAY"] {
		t.Errorf("BASE_LIB 破坏性变更应透过 import 闭包命中 MOD_ORDER 与 MOD_GATEWAY，实测: %+v", baseBreakTyped.AffectedDependents)
	}

	// 9. allow_breaking 逃生舱：原因过短 -> 400；原因合法 -> 200 forced_breaking=true + P1 审计
	shortReasonBody, _ := json.Marshal(map[string]any{
		"allow_breaking":  true,
		"breaking_reason": "short",
	})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/proto-schemas/%d/activate", orderV2Draft.ID), bytes.NewReader(shortReasonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("allow_breaking 缺合法 reason 应返回 400，实测 %d: %s", resp.Code, resp.Body.String())
	}

	validReasonBody, _ := json.Marshal(map[string]any{
		"allow_breaking":  true,
		"breaking_reason": "已通知网关调用方协同下线 qty 字段",
	})
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/proto-schemas/%d/activate", orderV2Draft.ID), bytes.NewReader(validReasonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("合法 reason 逃生舱激活应返回 200，实测 %d: %s", resp.Code, resp.Body.String())
	}
	var escapeResp struct {
		ForcedBreaking bool `json:"forced_breaking"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &escapeResp); err != nil {
		t.Fatalf("反序列化 escapeResp 失败: %v", err)
	}
	if !escapeResp.ForcedBreaking {
		t.Errorf("期望响应 forced_breaking=true，实测: %s", resp.Body.String())
	}
	_ = commonAudit.Close(context.Background())
	var actAudits []commonModels.SysAuditLog
	_ = db.Where("module = ? AND action = ?", config.ModuleKey, "activate").Find(&actAudits).Error
	if len(actAudits) == 0 {
		t.Errorf("期望写入至少 1 条 activate 审计记录")
	}
}

// TestSC13_ProtoCompileClosureLayerGuardAndGraphExport SC-13 端到端闭环验收：
// 虚拟 .proto 闭环编译 & 跨 Bundle 外部导入 SourceCodeInfo 剥离 -> BASE_LIB 反向闭包影响雷达 ->
// Subsystem/Module *int layer_level 分层约束 / REALIZATION & DISPATCH 反向校核 / 有向防环 409 ->
// /api/proto-schemas/dependencies-graph 与 /api/arch/topology/graph 层级元数据输出。
func TestSC13_ProtoCompileClosureLayerGuardAndGraphExport(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)
	admin := createTestUserInDB(t, db, "sc13_admin", config.AdminRole)
	token := generateTestJWT(t, admin)

	doJSON := func(method, path string, payload any) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			var err error
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatalf("Marshal 请求载荷失败: %v", err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		return resp
	}

	// 1. 架构分层初始化：SubInfra(10), SubBiz(20), SubGw(30) 及各自模块/独立物理进程 (满足 A-09)
	pg := models.ArchProcessGroup{Code: "PG_SC13", Name: "SC13进程组", CreatedBy: admin.ID}
	if err := db.Create(&pg).Error; err != nil {
		t.Fatalf("创建进程组失败: %v", err)
	}
	procInfra := models.ArchProcess{GroupID: pg.ID, Code: "proc-sc13-infra", Name: "SC13基础进程", CreatedBy: admin.ID}
	procBiz := models.ArchProcess{GroupID: pg.ID, Code: "proc-sc13-biz", Name: "SC13业务进程", CreatedBy: admin.ID}
	procGw := models.ArchProcess{GroupID: pg.ID, Code: "proc-sc13-gw", Name: "SC13网关进程", CreatedBy: admin.ID}
	for _, p := range []*models.ArchProcess{&procInfra, &procBiz, &procGw} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("创建进程失败: %v", err)
		}
	}

	type idLayerResp struct {
		ID         uint64 `json:"id"`
		LayerLevel int    `json:"layer_level"`
	}
	createSubAndMod := func(subCode string, subLayer int, modCode, kind string, procID *uint64) (uint64, uint64, uint64) {
		sr := doJSON(http.MethodPost, "/api/arch/subsystems", map[string]any{
			"code": subCode, "name": subCode + "-name", "layer_level": subLayer,
		})
		if sr.Code != http.StatusCreated {
			t.Fatalf("创建子系统 %s 失败: %d %s", subCode, sr.Code, sr.Body.String())
		}
		var subObj idLayerResp
		if err := json.Unmarshal(sr.Body.Bytes(), &subObj); err != nil {
			t.Fatalf("解析子系统响应失败: %v", err)
		}
		gr := doJSON(http.MethodPost, "/api/arch/functional-groups", map[string]any{
			"subsystem_id": subObj.ID, "code": "GRP_" + subCode, "name": "组_" + subCode,
		})
		if gr.Code != http.StatusCreated {
			t.Fatalf("创建功能组失败: %d %s", gr.Code, gr.Body.String())
		}
		var grpObj idLayerResp
		if err := json.Unmarshal(gr.Body.Bytes(), &grpObj); err != nil {
			t.Fatalf("解析功能组响应失败: %v", err)
		}
		modPayload := map[string]any{
			"group_id":    grpObj.ID,
			"code":        modCode,
			"name":        modCode + "-名称",
			"module_kind": kind,
			"owner_id":    admin.ID,
		}
		if procID != nil {
			modPayload["process_id"] = *procID
		}
		mr := doJSON(http.MethodPost, "/api/arch/modules", modPayload)
		if mr.Code != http.StatusCreated {
			t.Fatalf("创建模块 %s 失败: %d %s", modCode, mr.Code, mr.Body.String())
		}
		var modObj idLayerResp
		if err := json.Unmarshal(mr.Body.Bytes(), &modObj); err != nil {
			t.Fatalf("解析模块响应失败: %v", err)
		}
		return subObj.ID, grpObj.ID, modObj.ID
	}

	subInfraID, grpInfraID, modInfraID := createSubAndMod("SC13_INFRA", 10, "MOD_SC13_INFRA", "BUSINESS", &procInfra.ID)
	_, _, modBizID := createSubAndMod("SC13_BIZ", 20, "MOD_SC13_BIZ", "BUSINESS", &procBiz.ID)
	_, _, modGwID := createSubAndMod("SC13_GW", 30, "MOD_SC13_GW", "BUSINESS", &procGw.ID)
	_, _, modContractLibID := createSubAndMod("SC13_LIB", 20, "LIB_SC13_PAY", "CONTRACT_LIB", nil)

	// 验证 Module *int 更新：显式设 15 -> 省略保留 15 -> 显式 0 重置为继承
	if resp := doJSON(http.MethodPut, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10), map[string]any{
		"group_id": grpInfraID, "code": "MOD_SC13_INFRA", "name": "MOD_SC13_INFRA-名称", "layer_level": 15,
	}); resp.Code != http.StatusOK {
		t.Fatalf("设置 Module layer_level=15 失败: %d %s", resp.Code, resp.Body.String())
	}
	if resp := doJSON(http.MethodPut, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10), map[string]any{
		"group_id": grpInfraID, "code": "MOD_SC13_INFRA", "name": "MOD_SC13_INFRA-改名",
	}); resp.Code != http.StatusOK {
		t.Fatalf("省略 layer_level PUT 失败: %d %s", resp.Code, resp.Body.String())
	} else {
		var updated idLayerResp
		if err := json.Unmarshal(resp.Body.Bytes(), &updated); err != nil || updated.LayerLevel != 15 {
			t.Fatalf("省略 layer_level 应保持 15，实测 %+v err=%v", updated, err)
		}
	}
	if resp := doJSON(http.MethodPut, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10), map[string]any{
		"group_id": grpInfraID, "code": "MOD_SC13_INFRA", "name": "MOD_SC13_INFRA-名称", "layer_level": 0,
	}); resp.Code != http.StatusOK {
		t.Fatalf("重置 layer_level=0 失败: %d %s", resp.Code, resp.Body.String())
	} else {
		var updated idLayerResp
		if err := json.Unmarshal(resp.Body.Bytes(), &updated); err != nil || updated.LayerLevel != 0 {
			t.Fatalf("显式传入 0 应重置为 0，实测 %+v err=%v", updated, err)
		}
	}

	// 2. 纯 Go protocompile 多包编译：BASE_LIB -> order-domain -> pay-domain (跨 Bundle 外部依赖跳过与裁剪)
	if _, _, _, cErr := compileTestProtoSchema(t, db, "sc13-common-base", "BASE_LIB", "", map[string]string{
		"common/v1/base.proto": `syntax = "proto3";
package common.v1;
// 公共追踪上下文元数据
message TraceMeta {
  // 链路追踪ID
  string trace_id = 1;
}
`,
	}, true, false, ""); cErr != nil {
		t.Fatalf("SC13 编译 BASE_LIB 失败: %v", cErr)
	}

	if _, _, _, cErr := compileTestProtoSchema(t, db, "sc13-order-domain", "BUSINESS", "", map[string]string{
		"order/v1/order.proto": `syntax = "proto3";
package order.v1;
import "common/v1/base.proto";
message OrderReq { common.v1.TraceMeta meta = 1; string sku = 2; }
message OrderRsp { string id = 1; }
// 订单领域服务
service OrderService {
  rpc PlaceOrder(OrderReq) returns (OrderRsp);
}
`,
	}, true, false, ""); cErr != nil {
		t.Fatalf("SC13 编译 order-domain 失败: %v", cErr)
	}

	paySchema, payOps, _, payErr := compileTestProtoSchema(t, db, "sc13-pay-domain", "GATEWAY", "", map[string]string{
		"pay/v1/pay.proto": `syntax = "proto3";
package pay.v1;
import "order/v1/order.proto";
message PayReq { order.v1.OrderReq order = 1; int64 amount = 2; }
message PayRsp { bool ok = 1; }
// 支付网关服务
service PayService {
  rpc PayOrder(PayReq) returns (PayRsp);
}
`,
	}, true, false, "")
	if payErr != nil {
		t.Fatalf("SC13 跨 Bundle 编译 pay-domain 失败: %v", payErr)
	}
	var compiledFiles, externalImports []string
	_ = json.Unmarshal(paySchema.ProtoFiles, &compiledFiles)
	_ = json.Unmarshal(paySchema.ExternalImports, &externalImports)
	if len(payOps) != 1 || payOps[0].ServiceName != "pay.v1.PayService" {
		t.Fatalf("外部 import 中的 order.v1.OrderService 必须跳过，仅抽取 pay.v1.PayService，实测: %+v", payOps)
	}
	if len(compiledFiles) != 1 || len(externalImports) == 0 {
		t.Fatalf("期望仅 1 个自有文件与非空 external_imports，实测 compiled=%v ext=%v", compiledFiles, externalImports)
	}

	// 验证 DB 中的 DescriptorBytes：自有 pay/v1/pay.proto 保留 SourceCodeInfo，外部 common/v1/base.proto 已剥离 SourceCodeInfo
	var payDBSchema models.ProtoSchema
	if err := db.First(&payDBSchema, paySchema.ID).Error; err != nil {
		t.Fatalf("查询 payDBSchema 失败: %v", err)
	}
	var payFDS descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(payDBSchema.DescriptorBytes, &payFDS); err != nil {
		t.Fatalf("反序列化 DescriptorBytes 失败: %v", err)
	}
	checkedOwn, checkedExt := false, false
	for _, fd := range payFDS.GetFile() {
		switch fd.GetName() {
		case "pay/v1/pay.proto":
			checkedOwn = true
			if fd.GetSourceCodeInfo() == nil || len(fd.GetSourceCodeInfo().GetLocation()) == 0 {
				t.Fatalf("自有文件 pay/v1/pay.proto 必须保留完整 SourceCodeInfo")
			}
		case "common/v1/base.proto", "order/v1/order.proto":
			checkedExt = true
			if fd.GetSourceCodeInfo() != nil {
				t.Fatalf("外部导入文件 %s 必须剥离 SourceCodeInfo", fd.GetName())
			}
		}
	}
	if !checkedOwn || !checkedExt {
		t.Fatalf("DescriptorBytes 校验未命中预期文件: checkedOwn=%v checkedExt=%v", checkedOwn, checkedExt)
	}

	// 3. 架构服务提供与 DISPATCH/DEPEND/REALIZATION 分层及成环拦截
	svcBizResp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modBizID, 10)+"/services", map[string]any{
		"service_name": "order.v1.OrderService",
	})
	svcGwResp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modGwID, 10)+"/services", map[string]any{
		"service_name": "pay.v1.PayService",
	})
	svcInfraResp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10)+"/services", map[string]any{
		"service_name": "infra.v1.AuditService",
	})
	var svcBiz, svcGw, svcInfra idLayerResp
	for _, pair := range []struct {
		r *httptest.ResponseRecorder
		d *idLayerResp
	}{{svcBizResp, &svcBiz}, {svcGwResp, &svcGw}, {svcInfraResp, &svcInfra}} {
		if pair.r.Code != http.StatusCreated {
			t.Fatalf("注册服务失败: %d %s", pair.r.Code, pair.r.Body.String())
		}
		if err := json.Unmarshal(pair.r.Body.Bytes(), pair.d); err != nil {
			t.Fatalf("解析服务失败: %v", err)
		}
	}

	// 合法向下依赖 GW(30)->BIZ(20) 放行，并构成后续 F-26 下游链路
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modGwID, 10)+"/dependencies", map[string]any{
		"service_id": svcBiz.ID,
	}); resp.Code != http.StatusCreated {
		t.Fatalf("上层 GW(30)->BIZ(20) 合法依赖应成功: %d %s", resp.Code, resp.Body.String())
	}

	// 越层 INFRA(10)->BIZ(20) 返回 409 ERR_LAYER_VIOLATION
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10)+"/dependencies", map[string]any{
		"service_id": svcBiz.ID,
	}); resp.Code != http.StatusConflict || !bytes.Contains(resp.Body.Bytes(), []byte("ERR_LAYER_VIOLATION")) {
		t.Fatalf("底层向上依赖应返回 409 ERR_LAYER_VIOLATION，实测 %d: %s", resp.Code, resp.Body.String())
	}

	// 多实现契约(eff=20)：INFRA(10) 实现应触发 ERR_REALIZATION_LAYER_INVERSION；BIZ(20) DISPATCH 消费后 GW(30) 再实现应触发 DISPATCH 反向 ERR_LAYER_VIOLATION
	contractResp := doJSON(http.MethodPost, "/api/arch/service-contracts", map[string]any{
		"service_name":      "contract.v1.PaymentRouter",
		"definer_module_id": modContractLibID,
	})
	if contractResp.Code != http.StatusCreated {
		t.Fatalf("创建多实现契约失败: %d %s", contractResp.Code, contractResp.Body.String())
	}
	var contract idLayerResp
	if err := json.Unmarshal(contractResp.Body.Bytes(), &contract); err != nil {
		t.Fatalf("解析契约响应失败: %v", err)
	}
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10)+"/services", map[string]any{
		"contract_id": contract.ID,
		"impl_key":    "infra_bad",
	}); resp.Code != http.StatusConflict || !bytes.Contains(resp.Body.Bytes(), []byte("ERR_REALIZATION_LAYER_INVERSION")) {
		t.Fatalf("INFRA(10) 实现 LIB(20) 契约应返回 ERR_REALIZATION_LAYER_INVERSION，实测 %d: %s", resp.Code, resp.Body.String())
	}
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modBizID, 10)+"/dependencies", map[string]any{
		"contract_id": contract.ID,
	}); resp.Code != http.StatusCreated {
		t.Fatalf("BIZ(20) DISPATCH 消费 LIB(20) 契约应成功: %d %s", resp.Code, resp.Body.String())
	}
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modGwID, 10)+"/services", map[string]any{
		"contract_id": contract.ID,
		"impl_key":    "gw_too_high",
	}); resp.Code != http.StatusConflict || !bytes.Contains(resp.Body.Bytes(), []byte("ERR_LAYER_VIOLATION")) {
		t.Fatalf("既有 DISPATCH 消费方 BIZ(20) < GW(30) 实现应触发 409 ERR_LAYER_VIOLATION，实测 %d: %s", resp.Code, resp.Body.String())
	}

	// 将 SUB_INFRA 层级重置为 0 后仍应拦截 BIZ -> INFRA -> GW -> BIZ 的内部有向环
	if resp := doJSON(http.MethodPut, "/api/arch/subsystems/"+strconv.FormatUint(subInfraID, 10), map[string]any{
		"code": "SC13_INFRA", "name": "SC13_INFRA-name", "layer_level": 0,
	}); resp.Code != http.StatusOK {
		t.Fatalf("重置子系统层级为 0 失败: %d %s", resp.Code, resp.Body.String())
	}
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modBizID, 10)+"/dependencies", map[string]any{
		"service_id": svcInfra.ID,
	}); resp.Code != http.StatusCreated {
		t.Fatalf("无层级约束 BIZ->INFRA 应成功: %d %s", resp.Code, resp.Body.String())
	}
	if resp := doJSON(http.MethodPost, "/api/arch/modules/"+strconv.FormatUint(modInfraID, 10)+"/dependencies", map[string]any{
		"service_id": svcGw.ID,
	}); resp.Code != http.StatusConflict || !bytes.Contains(resp.Body.Bytes(), []byte("ERR_CYCLIC_DEPENDENCY")) {
		t.Fatalf("成环依赖 INFRA->GW(->BIZ->INFRA) 即使在 layer=0 也必须返回 409 ERR_CYCLIC_DEPENDENCY，实测 %d: %s", resp.Code, resp.Body.String())
	}

	// 4. F-26 BASE_LIB 递推闭包雷达：破坏 common/v1/base.proto 触发 Breaking 拦截并透出 MOD_SC13_BIZ 与 MOD_SC13_GW
	_, _, _, baseBreakErr := compileTestProtoSchema(t, db, "sc13-common-base", "BASE_LIB", "", map[string]string{
		"common/v1/base.proto": "syntax = \"proto3\";\npackage common.v1;\nmessage TraceMeta {\n  int32 trace_id = 1;\n}\n",
	}, true, false, "")
	var baseBreakTyped *decoder.SchemaBreakingError
	if !errors.As(baseBreakErr, &baseBreakTyped) {
		t.Fatalf("BASE_LIB 类型破坏应返回 SchemaBreakingError，实测: %v", baseBreakErr)
	}
	hitModules := make(map[string]bool)
	for _, ad := range baseBreakTyped.AffectedDependents {
		hitModules[ad.ModuleCode] = true
	}
	if !hitModules["MOD_SC13_BIZ"] || !hitModules["MOD_SC13_GW"] {
		t.Fatalf("BASE_LIB 传递闭包应命中 MOD_SC13_BIZ 与 MOD_SC13_GW，实测: %+v", baseBreakTyped.AffectedDependents)
	}

	// 5. 验证 GET /api/proto-schemas/dependencies-graph 与 GET /api/arch/topology/graph 元数据
	depGraphResp := doJSON(http.MethodGet, "/api/proto-schemas/dependencies-graph", nil)
	if depGraphResp.Code != http.StatusOK {
		t.Fatalf("GET dependencies-graph 失败: %d %s", depGraphResp.Code, depGraphResp.Body.String())
	}
	var depGraph struct {
		Nodes []struct {
			Name       string `json:"name"`
			SourceType string `json:"source_type"`
		} `json:"nodes"`
		Edges []struct {
			FromSchemaID uint64 `json:"from_schema_id"`
			ToSchemaID   uint64 `json:"to_schema_id"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(depGraphResp.Body.Bytes(), &depGraph); err != nil {
		t.Fatalf("解析 depGraph 失败: %v", err)
	}
	if len(depGraph.Nodes) < 3 || len(depGraph.Edges) < 2 {
		t.Fatalf("期望至少 3 节点 2 有向边，实测 nodes=%d edges=%d", len(depGraph.Nodes), len(depGraph.Edges))
	}
	if depGraph.Nodes[0].SourceType != models.ProtoSourceGitSync {
		t.Errorf("dependencies-graph 节点应包含 source_type='GIT_SYNC'，实测 %+v", depGraph.Nodes[0])
	}

	for _, viewMode := range []string{"logical", "physical"} {
		topoResp := doJSON(http.MethodGet, "/api/arch/topology/graph?view="+viewMode, nil)
		if topoResp.Code != http.StatusOK {
			t.Fatalf("GET topology/graph?view=%s 失败: %d %s", viewMode, topoResp.Code, topoResp.Body.String())
		}
		var topoGraph struct {
			Nodes []struct {
				ID   string         `json:"id"`
				Type string         `json:"type"`
				Meta map[string]any `json:"meta"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(topoResp.Body.Bytes(), &topoGraph); err != nil {
			t.Fatalf("解析 topology/graph (%s) 失败: %v", viewMode, err)
		}
		sawBizMod := false
		for _, n := range topoGraph.Nodes {
			if n.ID == "mod_"+strconv.FormatUint(modBizID, 10) {
				sawBizMod = true
				if n.Meta["effective_layer_level"] != float64(20) {
					t.Fatalf("%s 视图 MOD_SC13_BIZ effective_layer_level 应为 20，实测 %+v", viewMode, n.Meta)
				}
			}
		}
		if !sawBizMod {
			t.Fatalf("%s 视图缺少 MOD_SC13_BIZ 节点", viewMode)
		}
	}
}
