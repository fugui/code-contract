package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"code-contract/config"
	"code-contract/models"
)

func TestBatchMoveArchProcesses_SuccessPartialInvalidRBAC(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)
	adminUser := createTestUserInDB(t, db, "bm_admin", config.AdminRole)
	normalUser := createTestUserInDB(t, db, "bm_user", "developer")
	adminToken := generateTestJWT(t, adminUser)
	normalToken := generateTestJWT(t, normalUser)

	grpAuto := models.ArchProcessGroup{Code: "PGRP_CSG_AUTO", Name: "现网发现自动纳管进程组", SortOrder: 999}
	grpPay := models.ArchProcessGroup{Code: "CORE_PAY", Name: "支付核心组", SortOrder: 1}
	if err := db.Create(&grpAuto).Error; err != nil {
		t.Fatalf("创建暂存组失败: %v", err)
	}
	if err := db.Create(&grpPay).Error; err != nil {
		t.Fatalf("创建目标组失败: %v", err)
	}

	proc1 := models.ArchProcess{
		GroupID:           grpAuto.ID,
		Code:              "bm-proc-1",
		Name:              "待归类进程1",
		DefaultListenPort: 9001,
		DefaultCmdPattern: "bm-proc-1",
	}
	proc2 := models.ArchProcess{
		GroupID:           grpAuto.ID,
		Code:              "bm-proc-2",
		Name:              "待归类进程2",
		DefaultListenPort: 9002,
		DefaultCmdPattern: "bm-proc-2",
	}
	proc3 := models.ArchProcess{
		GroupID:           grpPay.ID,
		Code:              "bm-proc-3",
		Name:              "已在目标组进程3",
		DefaultListenPort: 9003,
		DefaultCmdPattern: "bm-proc-3",
	}
	for _, p := range []*models.ArchProcess{&proc1, &proc2, &proc3} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("创建进程 %s 失败: %v", p.Code, err)
		}
	}

	doPost := func(tok string, body interface{}) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/arch/processes/batch-move", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. 非管理员越权 -> 403
	w403 := doPost(normalToken, map[string]interface{}{
		"process_ids":     []uint64{proc1.ID},
		"target_group_id": grpPay.ID,
	})
	if w403.Code != http.StatusForbidden {
		t.Fatalf("非管理员调用 batch-move 期望 403，实际 %d: %s", w403.Code, w403.Body.String())
	}

	// 2. 非法入参（空 process_ids / 缺失 target_group_id）-> 400
	wEmptyIDs := doPost(adminToken, map[string]interface{}{
		"process_ids":     []uint64{},
		"target_group_id": grpPay.ID,
	})
	if wEmptyIDs.Code != http.StatusBadRequest {
		t.Fatalf("空 process_ids 期望 400，实际 %d", wEmptyIDs.Code)
	}
	wNoTarget := doPost(adminToken, map[string]interface{}{
		"process_ids":     []uint64{proc1.ID},
		"target_group_id": 0,
	})
	if wNoTarget.Code != http.StatusBadRequest {
		t.Fatalf("target_group_id=0 期望 400，实际 %d", wNoTarget.Code)
	}

	// 3. 目标组不存在 -> 404
	wNoGroup := doPost(adminToken, map[string]interface{}{
		"process_ids":     []uint64{proc1.ID},
		"target_group_id": 999999,
	})
	if wNoGroup.Code != http.StatusNotFound {
		t.Fatalf("目标组不存在期望 404，实际 %d", wNoGroup.Code)
	}

	// 4. 包含不存在的进程 ID -> 404 且不更新合法进程
	wMissingProc := doPost(adminToken, map[string]interface{}{
		"process_ids":     []uint64{proc1.ID, 888888},
		"target_group_id": grpPay.ID,
	})
	if wMissingProc.Code != http.StatusNotFound {
		t.Fatalf("包含无效进程 ID 期望 404，实际 %d", wMissingProc.Code)
	}
	var checkProc1 models.ArchProcess
	_ = db.First(&checkProc1, proc1.ID).Error
	if checkProc1.GroupID != grpAuto.ID {
		t.Fatalf("事务应回滚，proc1 不应被移动，当前 GroupID=%d", checkProc1.GroupID)
	}

	// 5. 正常批量移动（含 1 个已在目标组的跳过项与重复 ID 去重）-> 200
	wOK := doPost(adminToken, map[string]interface{}{
		"process_ids":     []uint64{proc1.ID, proc2.ID, proc3.ID, proc1.ID},
		"target_group_id": grpPay.ID,
	})
	if wOK.Code != http.StatusOK {
		t.Fatalf("batch-move 期望 200，实际 %d: %s", wOK.Code, wOK.Body.String())
	}

	var resp struct {
		TargetGroupID   uint64   `json:"target_group_id"`
		TargetGroupName string   `json:"target_group_name"`
		MovedCount      int      `json:"moved_count"`
		SkippedCount    int      `json:"skipped_count"`
		MovedIDs        []uint64 `json:"moved_ids"`
	}
	if err := json.Unmarshal(wOK.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.TargetGroupID != grpPay.ID || resp.MovedCount != 2 || resp.SkippedCount != 1 || len(resp.MovedIDs) != 2 {
		t.Fatalf("batch-move 返回统计异常: %+v", resp)
	}

	for _, pid := range []uint64{proc1.ID, proc2.ID, proc3.ID} {
		var p models.ArchProcess
		if err := db.First(&p, pid).Error; err != nil {
			t.Fatalf("查询进程 %d 失败: %v", pid, err)
		}
		if p.GroupID != grpPay.ID {
			t.Fatalf("进程 %d 期望已归入组 %d，实际 %d", pid, grpPay.ID, p.GroupID)
		}
	}
}
