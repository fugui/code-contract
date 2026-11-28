package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// buildAccountV1DescriptorBytes 构造 account v1 描述集 (AccountService.GetAccount)
func buildAccountV1DescriptorBytes(t *testing.T) []byte {
	t.Helper()
	file := &descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto3"),
		Name:    proto.String("account/v1/account.proto"),
		Package: proto.String("account.v1"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("AccountReq"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("user_id"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(),
					},
					{
						Name:   proto.String("user_name"),
						Number: proto.Int32(2),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
				},
			},
			{
				Name: proto.String("AccountRsp"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("success"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("AccountService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("GetAccount"),
						InputType:  proto.String(".account.v1.AccountReq"),
						OutputType: proto.String(".account.v1.AccountRsp"),
					},
				},
			},
		},
	}
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}}
	data, err := proto.Marshal(fds)
	if err != nil {
		t.Fatalf("marshal account v1 fds failed: %v", err)
	}
	return data
}

// buildAccountV2BreakingDescriptorBytes 构造包含破坏性变更 (TagMutation) 的 account v2 描述集
func buildAccountV2BreakingDescriptorBytes(t *testing.T) []byte {
	t.Helper()
	file := &descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto3"),
		Name:    proto.String("account/v1/account.proto"),
		Package: proto.String("account.v1"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("AccountReq"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("user_id"),
						Number: proto.Int32(2), // Tag 从 1 改为 2 (TAG_MUTATION)
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(),
					},
					{
						Name:   proto.String("user_name"),
						Number: proto.Int32(3),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					},
				},
			},
			{
				Name: proto.String("AccountRsp"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   proto.String("success"),
						Number: proto.Int32(1),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("AccountService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("GetAccount"),
						InputType:  proto.String(".account.v1.AccountReq"),
						OutputType: proto.String(".account.v1.AccountRsp"),
					},
				},
			},
		},
	}
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}}
	data, err := proto.Marshal(fds)
	if err != nil {
		t.Fatalf("marshal account v2 fds failed: %v", err)
	}
	return data
}

// TestSC09_BreakingChangeDetection_DownstreamAlert_AndAllowBreakingRollbackAudit
// 验证 SC-09：破坏性变更阻断雷达、下游消费者血缘推导、管理员带因审计逃生舱全链路 (M16 / ADR-14)
func TestSC09_BreakingChangeDetection_DownstreamAlert_AndAllowBreakingRollbackAudit(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)
	adminUser := createTestUserInDB(t, db, "admin_breaking", config.AdminRole)
	adminToken := generateTestJWT(t, adminUser)

	// 1. 初始化下游消费者血缘架构数据：
	//    - Provider Module: "ACCOUNT_SERVICE"
	//    - ModuleService: "account.v1.AccountService"
	//    - ArchProcess: "sut-order"
	//    - Consumer Module: "ORDER_CORE" (Owner: adminUser)
	//    - ModuleDependency: ORDER_CORE 依赖 account.v1.AccountService
	subsys := models.Subsystem{Code: "SUB_BIZ", Name: "业务子系统", CreatedBy: adminUser.ID}
	_ = db.Create(&subsys).Error
	group := models.FunctionalGroup{SubsystemID: subsys.ID, Code: "GRP_CORE", Name: "核心组", CreatedBy: adminUser.ID}
	_ = db.Create(&group).Error
	procGroup := models.ArchProcessGroup{Code: "PG_APP", Name: "应用分组", CreatedBy: adminUser.ID}
	_ = db.Create(&procGroup).Error
	archProc := models.ArchProcess{GroupID: procGroup.ID, Code: "sut-order", Name: "订单标准进程", CreatedBy: adminUser.ID}
	_ = db.Create(&archProc).Error

	provMod := models.Module{SubsystemID: subsys.ID, GroupID: group.ID, Code: "ACCOUNT_CORE", Name: "账户服务模块", CreatedBy: adminUser.ID}
	_ = db.Create(&provMod).Error
	consumerMod := models.Module{
		SubsystemID:   subsys.ID,
		GroupID:       group.ID,
		ArchProcessID: &archProc.ID,
		Code:          "ORDER_CORE",
		Name:          "订单核心模块",
		OwnerID:       &adminUser.ID,
		CreatedBy:     adminUser.ID,
	}
	_ = db.Create(&consumerMod).Error

	modSvc := models.ModuleService{
		ModuleID:    models.Uint64Ptr(provMod.ID),
		ServiceName: "account.v1.AccountService",
		CreatedBy:   adminUser.ID,
	}
	_ = db.Create(&modSvc).Error

	dep := models.ModuleDependency{
		ConsumerModuleID: consumerMod.ID,
		ServiceID:        &modSvc.ID,
		CreatedBy:        adminUser.ID,
	}
	_ = db.Create(&dep).Error

	// 2. 通过注册表写入并激活 account v1 (基线版本)
	v1Bytes := buildAccountV1DescriptorBytes(t)
	s1, _, _, err1 := uploadTestProtoBytes(t, db, "account-schema", "account.pb", "BUSINESS", v1Bytes, true, false, "")
	if err1 != nil {
		t.Fatalf("导入并激活 account v1 失败: %v", err1)
	}
	v1ID := s1.ID

	// 3. 写入 account v2 草稿 (auto_activate=false, 带破坏性改动) + 破坏性告警检测
	v2Bytes := buildAccountV2BreakingDescriptorBytes(t)
	s2, _, _, err2 := uploadTestProtoBytes(t, db, "account-schema", "account.pb", "BUSINESS", v2Bytes, false, false, "")
	if err2 != nil {
		t.Fatalf("导入 account v2 草稿失败: %v", err2)
	}
	v2ID := s2.ID
	reg, _ := handlers.GetDecoderRegistry()
	breakingWarnings, _ := reg.DetectBreakingChangesAgainstActive("account-schema", v2Bytes)
	if len(breakingWarnings) == 0 {
		t.Fatalf("v2 草稿应检测到 breaking_warnings 提示，但为空")
	}

	viewerUser := createTestUserInDB(t, db, "viewer_breaking", "developer")
	viewerToken := generateTestJWT(t, viewerUser)

	var req *http.Request
	var resp *httptest.ResponseRecorder

	// 4. 直接激活 account v2 -> 不再阻断，返回 200 OK，同时返回 breaking_warnings 并自动写入破坏性变更审计事件
	req = httptest.NewRequest(http.MethodPost, "/api/proto-schemas/"+strconvFormat(v2ID)+"/activate", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("激活描述集应直接成功 200 OK: code=%d, body=%s", resp.Code, resp.Body.String())
	}

	var activateResp struct {
		BreakingEventCount int                             `json:"breaking_event_count"`
		BreakingWarnings   []decoder.BreakingRuleViolation `json:"breaking_warnings"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &activateResp)
	if activateResp.BreakingEventCount != 1 || len(activateResp.BreakingWarnings) == 0 {
		t.Fatalf("期望返回 breaking_event_count=1 及 breaking_warnings，实测: %s", resp.Body.String())
	}

	// 刷新审计日志入库
	_ = commonAudit.Close(context.Background())

	// 验证写入了 P1 级操作审计日志
	var auditLog commonModels.SysAuditLog
	err := db.Where("module = ? AND action = ? AND level = ?", config.ModuleKey, "activate", "P1").Order("id DESC").First(&auditLog).Error
	if err != nil {
		t.Fatalf("未找到对应的 P1 级审计日志: %v", err)
	}
	if !strings.Contains(auditLog.Summary, "破坏性变更审计") {
		t.Errorf("审计日志 Summary 未注明破坏性变更审计: %s", auditLog.Summary)
	}

	// 5. 查询破坏性变更审计列表与详情，校验下游受波及模块推导 (ORDER_CORE)
	req = httptest.NewRequest(http.MethodGet, "/api/breaking-change-events?status=OPEN", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/breaking-change-events 失败 (%d): %s", resp.Code, resp.Body.String())
	}
	var listResp struct {
		Items     []handlers.BreakingChangeEventDTO `json:"items"`
		Total     int64                             `json:"total"`
		OpenCount int64                             `json:"open_count"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &listResp)
	if listResp.Total != 1 || listResp.OpenCount != 1 || len(listResp.Items) != 1 {
		t.Fatalf("期望 1 条 OPEN 审计事件，实测: %+v", listResp)
	}
	ev := listResp.Items[0]
	if ev.SchemaName != "account-schema" || ev.Source != models.BreakingSourceManualActivate || len(ev.AffectedDependents) == 0 {
		t.Fatalf("审计事件属性异常: %+v", ev)
	}
	if ev.AffectedDependents[0].ModuleCode != "ORDER_CORE" || ev.AffectedDependents[0].ArchProcessCode != "sut-order" {
		t.Errorf("推导下游血缘属性不符: %+v", ev.AffectedDependents[0])
	}

	// 6. 普通用户无权确认 -> 403；管理员确认 -> 200 且 open_count 归零
	req = httptest.NewRequest(http.MethodPost, "/api/breaking-change-events/"+strconvFormat(ev.ID)+"/acknowledge", bytes.NewReader([]byte(`{"note":"viewer"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("非管理员调用 acknowledge 必须返回 403，实测 %d: %s", resp.Code, resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/breaking-change-events/"+strconvFormat(ev.ID)+"/acknowledge", bytes.NewReader([]byte(`{"note":"已通知 ORDER_CORE 同步适配"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("管理员 acknowledge 失败 (%d): %s", resp.Code, resp.Body.String())
	}
	var acked handlers.BreakingChangeEventDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &acked)
	if acked.Status != models.BreakingAuditStatusAcknowledged || acked.AcknowledgeNote != "已通知 ORDER_CORE 同步适配" {
		t.Fatalf("acknowledge 后状态或备注异常: %+v", acked)
	}

	// 7. 批量激活切回 v1（同样触发破坏性变更审计）并验证 batch-acknowledge
	batchReq, _ := json.Marshal(map[string]interface{}{
		"schema_ids": []uint64{v1ID},
	})
	req = httptest.NewRequest(http.MethodPost, "/api/proto-schemas/batch-activate", bytes.NewReader(batchReq))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("批量激活应直接成功 200: code=%d, body=%s", resp.Code, resp.Body.String())
	}
	var batchActResp struct {
		BreakingEventCount int `json:"breaking_event_count"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &batchActResp)
	if batchActResp.BreakingEventCount != 1 {
		t.Fatalf("批量激活期望生成 1 条审计事件, got %s", resp.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/breaking-change-events?status=OPEN", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	_ = json.Unmarshal(resp.Body.Bytes(), &listResp)
	if listResp.OpenCount != 1 || len(listResp.Items) != 1 || listResp.Items[0].Source != models.BreakingSourceBatchActivate {
		t.Fatalf("期望 1 条 BATCH_ACTIVATE OPEN 事件, got %+v", listResp)
	}

	batchAckBody, _ := json.Marshal(map[string]any{
		"ids":  []uint64{listResp.Items[0].ID},
		"note": "批量确认回滚基线变更",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/breaking-change-events/batch-acknowledge", bytes.NewReader(batchAckBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !bytes.Contains(resp.Body.Bytes(), []byte(`"acknowledged_count":1`)) {
		t.Fatalf("batch-acknowledge 失败 (%d): %s", resp.Code, resp.Body.String())
	}
}

func strconvFormat(n uint64) string {
	return strconvFormatUint(n)
}

func strconvFormatUint(n uint64) string {
	var buf [20]byte
	i := len(buf)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + (n % 10))
		n /= 10
	}
	return string(buf[i:])
}
