package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

func TestUpdateArchProcess_ChangeGroupID(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	r, db := setupRouterAndDB(t)
	adminUser := createTestUserInDB(t, db, "chg_grp_admin", config.AdminRole)
	adminToken := generateTestJWT(t, adminUser)

	grpAuto := models.ArchProcessGroup{Code: "PGRP_CSG_AUTO", Name: "现网发现自动纳管进程组", SortOrder: 999}
	grpBackend := models.ArchProcessGroup{Code: "BACKEND_SVC", Name: "后端服务组", SortOrder: 1}
	if err := db.Create(&grpAuto).Error; err != nil {
		t.Fatalf("创建原组失败: %v", err)
	}
	if err := db.Create(&grpBackend).Error; err != nil {
		t.Fatalf("创建目标组失败: %v", err)
	}

	sub := models.Subsystem{Code: "SUB_CHG", Name: "测试子系统"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("创建子系统失败: %v", err)
	}
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "FG_CHG", Name: "测试功能组"}
	if err := db.Create(&fg).Error; err != nil {
		t.Fatalf("创建功能组失败: %v", err)
	}

	proc := models.ArchProcess{
		GroupID:           grpAuto.ID,
		Code:              "backend-account",
		Name:              "backend-account",
		DefaultListenPort: 0,
		DefaultCmdPattern: "./bin/backend-account",
	}
	if err := db.Create(&proc).Error; err != nil {
		t.Fatalf("创建测试进程失败: %v", err)
	}
	mod := models.Module{
		SubsystemID:   sub.ID,
		GroupID:       fg.ID,
		ArchProcessID: &proc.ID,
		Code:          "MOD_ACC",
		Name:          "账户模块",
	}
	if err := db.Create(&mod).Error; err != nil {
		t.Fatalf("创建测试模块失败: %v", err)
	}

	doPut := func(body interface{}) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPut, "/api/arch/processes/"+strconv.FormatUint(proc.ID, 10), bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. 目标组不存在 -> 404
	wNotFound := doPut(map[string]interface{}{"group_id": 999999})
	if wNotFound.Code != http.StatusNotFound {
		t.Fatalf("目标组不存在期望 404，实际 %d: %s", wNotFound.Code, wNotFound.Body.String())
	}

	// 2. 仅传 group_id 将进程从 grpAuto 移至 grpBackend -> 200 且响应与库内均已更新，同时不丢失已绑定模块
	wOK := doPut(map[string]interface{}{"group_id": grpBackend.ID})
	if wOK.Code != http.StatusOK {
		t.Fatalf("更新所属组期望 200，实际 %d: %s", wOK.Code, wOK.Body.String())
	}
	var putResp struct {
		ArchProcess models.ArchProcess `json:"arch_process"`
	}
	if err := json.Unmarshal(wOK.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("解析 PUT 响应失败: %v", err)
	}
	if putResp.ArchProcess.GroupID != grpBackend.ID {
		t.Fatalf("PUT 响应 group_id 期望 %d，实际 %d", grpBackend.ID, putResp.ArchProcess.GroupID)
	}
	if putResp.ArchProcess.Group == nil || putResp.ArchProcess.Group.ID != grpBackend.ID || putResp.ArchProcess.Group.Name != grpBackend.Name {
		t.Fatalf("PUT 响应 group 关联未同步更新: %+v", putResp.ArchProcess.Group)
	}
	if len(putResp.ArchProcess.Modules) != 1 || putResp.ArchProcess.Modules[0].ID != mod.ID {
		t.Fatalf("改组不应丢失已绑定模块，实际: %+v", putResp.ArchProcess.Modules)
	}

	var dbProc models.ArchProcess
	if err := db.First(&dbProc, proc.ID).Error; err != nil {
		t.Fatalf("查询数据库失败: %v", err)
	}
	if dbProc.GroupID != grpBackend.ID {
		t.Fatalf("数据库 group_id 期望 %d，实际 %d", grpBackend.ID, dbProc.GroupID)
	}

	// 3. 查询物理进程树，确认旧组清空、目标组包含该进程
	treeReq := httptest.NewRequest(http.MethodGet, "/api/arch/processes/tree", nil)
	treeReq.Header.Set("Authorization", "Bearer "+adminToken)
	treeW := httptest.NewRecorder()
	r.ServeHTTP(treeW, treeReq)
	if treeW.Code != http.StatusOK {
		t.Fatalf("查询物理进程树失败: %d: %s", treeW.Code, treeW.Body.String())
	}
	var treeResp struct {
		Tree []struct {
			ID        uint64               `json:"id"`
			Processes []models.ArchProcess `json:"processes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(treeW.Body.Bytes(), &treeResp); err != nil {
		t.Fatalf("解析树响应失败: %v", err)
	}
	groupCounts := make(map[uint64]int)
	for _, g := range treeResp.Tree {
		groupCounts[g.ID] = len(g.Processes)
	}
	if groupCounts[grpAuto.ID] != 0 || groupCounts[grpBackend.ID] != 1 {
		t.Fatalf("树分组统计异常，期望 auto=0, backend=1，实际: %+v", groupCounts)
	}

	// 4. 反向撤销改回原组同样生效
	wUndo := doPut(map[string]interface{}{"group_id": grpAuto.ID})
	if wUndo.Code != http.StatusOK {
		t.Fatalf("撤销改回原组期望 200，实际 %d: %s", wUndo.Code, wUndo.Body.String())
	}
	var dbProcUndo models.ArchProcess
	_ = db.First(&dbProcUndo, proc.ID).Error
	if dbProcUndo.GroupID != grpAuto.ID {
		t.Fatalf("撤销后数据库 group_id 期望 %d，实际 %d", grpAuto.ID, dbProcUndo.GroupID)
	}
}
