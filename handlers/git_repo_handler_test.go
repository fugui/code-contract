package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	commonModels "code-common/backend/models"
	"code-contract/config"
	"code-contract/handlers"
	"code-contract/models"

	"gorm.io/gorm/clause"
)

type handlerMockCommit struct {
	SHA     string
	Message string
	Author  string
	Email   string
	Files   map[string]string
}

type handlerMockGitLab struct {
	mu      sync.RWMutex
	token   string
	commits map[string]*handlerMockCommit
}

func newHandlerMockGitLab(token string) *handlerMockGitLab {
	return &handlerMockGitLab{
		token:   token,
		commits: make(map[string]*handlerMockCommit),
	}
}

func (m *handlerMockGitLab) setBranch(branch string, snap *handlerMockCommit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits[branch] = snap
	m.commits[snap.SHA] = snap
}

func (m *handlerMockGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.token != "" {
		if r.Header.Get("PRIVATE-TOKEN") != m.token && r.Header.Get("Authorization") != "Bearer "+m.token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
			return
		}
	}
	rawPath := r.URL.RawPath
	if rawPath == "" {
		rawPath = r.URL.Path
	}
	w.Header().Set("Content-Type", "application/json")

	if strings.HasSuffix(rawPath, "/repository/branches") {
		m.mu.RLock()
		defer m.mu.RUnlock()
		type branchResp struct {
			Name string `json:"name"`
		}
		var out []branchResp
		for k := range m.commits {
			if len(k) < 40 {
				out = append(out, branchResp{Name: k})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}

	if idx := strings.Index(rawPath, "/repository/commits/"); idx >= 0 {
		ref := rawPath[idx+len("/repository/commits/"):]
		if unesc, err := url.PathUnescape(ref); err == nil {
			ref = unesc
		}
		m.mu.RLock()
		snap := m.commits[ref]
		m.mu.RUnlock()
		if snap == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             snap.SHA,
			"message":        snap.Message,
			"author_name":    snap.Author,
			"author_email":   snap.Email,
			"committed_date": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if strings.HasSuffix(rawPath, "/repository/tree") {
		ref := r.URL.Query().Get("ref")
		subPath := strings.Trim(r.URL.Query().Get("path"), "/")
		m.mu.RLock()
		snap := m.commits[ref]
		m.mu.RUnlock()
		if snap == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		type treeItem struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
			Path string `json:"path"`
		}
		var items []treeItem
		for p, content := range snap.Files {
			if subPath != "" && !strings.HasPrefix(p, subPath+"/") && p != subPath {
				continue
			}
			sum := sha256.Sum256([]byte(content))
			items = append(items, treeItem{
				ID:   hex.EncodeToString(sum[:])[:40],
				Name: p,
				Type: "blob",
				Path: p,
			})
		}
		_ = json.NewEncoder(w).Encode(items)
		return
	}

	if idx := strings.Index(rawPath, "/repository/files/"); idx >= 0 && strings.HasSuffix(rawPath, "/raw") {
		escapedFile := rawPath[idx+len("/repository/files/") : len(rawPath)-len("/raw")]
		filePath, _ := url.PathUnescape(escapedFile)
		ref := r.URL.Query().Get("ref")
		m.mu.RLock()
		snap := m.commits[ref]
		var body string
		var ok bool
		if snap != nil {
			body, ok = snap.Files[filePath]
		}
		m.mu.RUnlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func TestGitRepoAndBundles_AESEncryptionRedaction_AuditAndSync(t *testing.T) {
	t.Setenv("CONTRACT_CRYPTO_KEY", "test-contract-crypto-key-m25-32bytes!")
	r, db := setupProtoDocRouterAndDB(t)

	const secretToken = "glpat-secret-token-987654"
	const webhookSecret = "whsec-very-secret-4321"
	mockGit := newHandlerMockGitLab(secretToken)
	mockGit.setBranch("main", &handlerMockCommit{
		SHA:     "1111111111111111111111111111111111111111",
		Message: "feat: init common and order contracts",
		Author:  "Alice",
		Email:   "alice@example.com",
		Files: map[string]string{
			"common/proto/common.proto": `syntax = "proto3";
package dophi.common.v1;

// 通用请求头
message RequestMeta {
  string trace_id = 1;
}
`,
			"services/order/order.proto": `syntax = "proto3";
package dophi.order.v1;
import "common.proto";

// 订单请求
message CreateOrderReq {
  dophi.common.v1.RequestMeta meta = 1;
  string order_no = 2;
}

// 订单响应
message CreateOrderRsp {
  int32 code = 1;
}

// 订单契约服务
service OrderService {
  // 创建订单
  rpc CreateOrder (CreateOrderReq) returns (CreateOrderRsp);
}
`,
		},
	})
	mockGit.setBranch("release/v1", mockGit.commits["main"])

	gitSrv := httptest.NewServer(mockGit)
	defer gitSrv.Close()

	adminUser := createTestUserInDB(t, db, "git_admin", config.AdminRole)
	normalUser := createTestUserInDB(t, db, "git_viewer", "viewer")
	adminJWT := generateTestJWT(t, adminUser)
	viewerJWT := generateTestJWT(t, normalUser)

	// 1. M37 已下线手工创建/删除/导入入口，均应返回 404
	for _, tc := range []struct {
		method string
		path   string
		token  string
	}{
		{http.MethodPost, "/api/git-repos", viewerJWT},
		{http.MethodPost, "/api/git-repos", adminJWT},
		{http.MethodDelete, "/api/git-repos/1", adminJWT},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("已下线接口 %s %s 期望 404，实际 %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	// 为测试 Mock GitLab Server 写入 127.0.0.0/8 白名单
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"setting_val"}),
	}).Create(&models.ModuleSetting{
		SettingKey:  "outbound_trusted_cidrs",
		SettingVal:  "127.0.0.0/8",
		Description: "test trusted git server loopback",
	}).Error; err != nil {
		t.Fatalf("写入 outbound_trusted_cidrs 失败: %v", err)
	}

	// 2. 从平台唯一真值源 repositories 对齐生成 contract_git_repos，并验证凭据加密落库与 GET 严格脱敏
	dep := commonModels.Department{Name: "订单平台部"}
	if err := db.Create(&dep).Error; err != nil {
		t.Fatalf("创建部门失败: %v", err)
	}
	repoURL := gitSrv.URL + "/dophi/contracts.git"
	platRepo := commonModels.Repository{
		DepartmentID: dep.ID,
		Name:         "dophi-monorepo",
		URL:          repoURL,
		HTTPURL:      repoURL,
		OwnerID:      adminUser.ID,
		Branch:       "main",
		ServiceGroup: "trade-core",
		IsActive:     true,
	}
	if err := db.Create(&platRepo).Error; err != nil {
		t.Fatalf("创建平台 Repository 失败: %v", err)
	}
	storedRepo := models.GitRepo{
		PlatformRepoID:  uint64(platRepo.ID),
		SourceMode:      models.GitSourceModePlatformShared,
		SyncStrategy:    models.GitSyncStrategyFetchAndRead,
		ProtoStatus:     models.GitRepoProtoStatusPending,
		SyncEnabled:     true,
		SubsystemSlug:   "trade-core",
		Name:            "dophi-monorepo",
		Provider:        models.GitProviderGitLab,
		RepoURL:         repoURL,
		APIBaseURL:      gitSrv.URL,
		DefaultBranch:   "main",
		AuthType:        models.GitAuthToken,
		SyncMode:        models.GitSyncManual,
		AutoCompile:     true,
		LintOnSync:      true,
		PollIntervalSec: 1800,
	}
	if err := storedRepo.SetCredentials(secretToken); err != nil {
		t.Fatalf("SetCredentials 失败: %v", err)
	}
	if err := storedRepo.SetWebhookSecret(webhookSecret); err != nil {
		t.Fatalf("SetWebhookSecret 失败: %v", err)
	}
	if err := db.Create(&storedRepo).Error; err != nil {
		t.Fatalf("创建对齐 GitRepo 失败: %v", err)
	}
	repoID := storedRepo.ID
	if storedRepo.CredentialsEnc == "" || storedRepo.CredentialsEnc == secretToken || storedRepo.WebhookSecretEnc == webhookSecret {
		t.Fatalf("数据库凭据未经过 AES-256-GCM 加密")
	}

	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos?page=1&page_size=25", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/git-repos 期望 200，实际 %d: %s", w.Code, w.Body.String())
		}
		listRaw := w.Body.String()
		if strings.Contains(listRaw, secretToken) || strings.Contains(listRaw, webhookSecret) || strings.Contains(listRaw, storedRepo.CredentialsEnc) {
			t.Fatalf("GET /api/git-repos 泄漏凭据/密文: %s", listRaw)
		}
		if !strings.Contains(listRaw, `"service_group":"trade-core"`) {
			t.Fatalf("GET /api/git-repos 缺少平台只读元字段 service_group: %s", listRaw)
		}
	}

	// 3. Bundle 目录穿越防御、创建 common-base & order-svc Bundle 与重复 target_schema_name 409
	{
		badPathBody, _ := json.Marshal(map[string]any{
			"bundle_name":        "bad-bundle",
			"subdir_path":        "../secrets",
			"target_schema_name": "bad-schema",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/bundles", bytes.NewReader(badPathBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("路径穿越 subdir_path 期望返回 400，实际 %d: %s", w.Code, w.Body.String())
		}
	}
	var commonBundleID uint64
	{
		bBody, _ := json.Marshal(map[string]any{
			"bundle_name":        "common-base",
			"subdir_path":        "common/proto",
			"include_dirs":       []string{"."},
			"file_globs":         []string{"*.proto"},
			"target_schema_name": "common-proto",
			"category":           "BASE_LIB",
			"auto_compile":       true,
			"auto_activate":      true,
			"lint_on_sync":       true,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/bundles", bytes.NewReader(bBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("创建 common-base Bundle 期望 201，实际 %d: %s", w.Code, w.Body.String())
		}
		var bResp struct {
			ID uint64 `json:"id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &bResp)
		commonBundleID = bResp.ID
		if commonBundleID == 0 {
			t.Fatalf("commonBundleID 为 0: %s", w.Body.String())
		}
	}

	// 4. 添加第二个 Monorepo Bundle (order-proto) 并验证重复 target_schema_name 返回 409
	var orderBundleID uint64
	{
		bBody, _ := json.Marshal(map[string]any{
			"bundle_name":        "order-svc",
			"subdir_path":        "services/order",
			"include_dirs":       []string{"."},
			"target_schema_name": "order-proto",
			"category":           "BUSINESS",
			"auto_compile":       true,
			"auto_activate":      true,
			"lint_on_sync":       true,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/bundles", bytes.NewReader(bBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("添加第 2 个 Bundle 期望 201，实际 %d: %s", w.Code, w.Body.String())
		}
		var bResp struct {
			ID uint64 `json:"id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &bResp)
		orderBundleID = bResp.ID
		if orderBundleID == 0 {
			t.Fatalf("orderBundleID 为 0: %s", w.Body.String())
		}
	}

	{
		dupBody, _ := json.Marshal(map[string]any{
			"bundle_name":        "order-dup-target",
			"subdir_path":        "services/other",
			"target_schema_name": "common-proto",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/bundles", bytes.NewReader(dupBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("重复 target_schema_name 期望返回 409 Conflict，实际 %d: %s", w.Code, w.Body.String())
		}
	}

	// 5. PUT 权限校验 + 禁止篡改平台 SSOT 字段 + 更新描述保留原有加密凭据不变
	{
		updBody, _ := json.Marshal(map[string]any{"description": "viewer forbidden"})
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoID, 10), bytes.NewReader(updBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("普通用户 PUT /api/git-repos/:id 期望 403，实际 %d", w.Code)
		}
	}
	for _, forbiddenPayload := range []map[string]any{
		{"name": "hijack-name"},
		{"repo_url": "https://evil.example.com/hijack.git"},
		{"credential_plain": "new-secret"},
	} {
		badBody, _ := json.Marshal(forbiddenPayload)
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoID, 10), bytes.NewReader(badBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("修改平台 SSOT/凭据字段 %v 期望 400，实际 %d: %s", forbiddenPayload, w.Code, w.Body.String())
		}
	}
	{
		updBody, _ := json.Marshal(map[string]any{
			"description": "updated monorepo desc",
		})
		req := httptest.NewRequest(http.MethodPut, "/api/git-repos/"+strconv.FormatUint(repoID, 10), bytes.NewReader(updBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT /api/git-repos/:id 期望 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var reloaded models.GitRepo
		_ = db.First(&reloaded, repoID).Error
		dec, decErr := reloaded.DecryptCredentials()
		if decErr != nil || dec != secretToken {
			t.Fatalf("留空 credential_plain 更新后凭据应当保留原值，实际 dec=%q err=%v", dec, decErr)
		}
	}

	// 6. 订阅 SSE 并按 Bundle 触发 POST /api/git-repos/:id/sync，断言 202 与 SSE git_sync.completed 事件
	subID, sseCh := handlers.GetGlobalSSEBroker().Subscribe()
	defer handlers.GetGlobalSSEBroker().Unsubscribe(subID)

	for _, bID := range []uint64{commonBundleID, orderBundleID} {
		syncBody, _ := json.Marshal(map[string]any{
			"branch":    "main",
			"bundle_id": bID,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/sync", bytes.NewReader(syncBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("POST /api/git-repos/:id/sync 期望 202 Accepted，实际 %d: %s", w.Code, w.Body.String())
		}
		var syncAck struct {
			RepoID uint64 `json:"repo_id"`
			Branch string `json:"branch"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &syncAck)
		if syncAck.RepoID != repoID || syncAck.Status != models.GitSyncStatusRunning {
			t.Fatalf("同步 202 响应异常: %+v", syncAck)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		gotCompleted := false
		for !gotCompleted {
			select {
			case evt := <-sseCh:
				if evt.Event == "git_sync.completed" {
					gotCompleted = true
				}
			case <-ctx.Done():
				cancel()
				t.Fatalf("超时未收到 Bundle %d 的 SSE git_sync.completed 推送事件", bID)
			}
		}
		cancel()
	}

	// 验证 2 个 Bundle 各自编译生成了 GIT_SYNC ProtoSchema
	var schemas []models.ProtoSchema
	if err := db.Where("git_repo_id = ?", repoID).Order("id ASC").Find(&schemas).Error; err != nil {
		t.Fatalf("查询编译后的 ProtoSchema 失败: %v", err)
	}
	if len(schemas) != 2 {
		t.Fatalf("期望生成 2 条 GIT_SYNC ProtoSchema，实际 %d", len(schemas))
	}
	for _, sc := range schemas {
		if sc.SourceType != models.ProtoSourceGitSync || sc.SourceBundleID == nil {
			t.Fatalf("ProtoSchema 来源元数据不完整: %+v", sc)
		}
	}

	// 7. 分支查询与 SHA256 只读对账接口验证
	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/branches", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /branches 期望 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var brResp struct {
			DefaultBranch string   `json:"default_branch"`
			Branches      []string `json:"branches"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &brResp)
		if brResp.DefaultBranch != "main" || len(brResp.Branches) < 2 {
			t.Fatalf("分支列表返回值异常: %+v", brResp)
		}
	}

	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/consistency?branch=main", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /consistency 期望 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var consResp struct {
			InSync  bool `json:"in_sync"`
			Summary struct {
				InSync int `json:"in_sync"`
			} `json:"summary"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &consResp)
		if !consResp.InSync || consResp.Summary.InSync != 2 {
			t.Fatalf("同步后一致性对账应为 2 条 in_sync，实际 %+v", consResp)
		}
	}

	// 推进远端 Commit 修改 order.proto -> 验证 consistency 检测出 modified_files
	mockGit.setBranch("main", &handlerMockCommit{
		SHA:     "2222222222222222222222222222222222222222",
		Message: "feat: modify order",
		Author:  "Alice",
		Email:   "alice@example.com",
		Files: map[string]string{
			"common/proto/common.proto":  mockGit.commits["main"].Files["common/proto/common.proto"],
			"services/order/order.proto": mockGit.commits["main"].Files["services/order/order.proto"] + "\n// changed\n",
		},
	})
	{
		req := httptest.NewRequest(http.MethodGet, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/consistency?branch=main", nil)
		req.Header.Set("Authorization", "Bearer "+viewerJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("二次 GET /consistency 期望 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var consResp struct {
			InSync        bool     `json:"in_sync"`
			ModifiedFiles []string `json:"modified_files"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &consResp)
		if consResp.InSync || len(consResp.ModifiedFiles) != 1 {
			t.Fatalf("修改远端文件后一致性对账应检出 1 条 modified，实际 %+v", consResp)
		}
	}

	// 验证审计日志写入且脱敏（不包含 secretToken / webhookSecret）
	var auditLogs []commonModels.SysAuditLog
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = db.Where("action LIKE ?", "contract_git:%").Find(&auditLogs).Error
		if len(auditLogs) >= 3 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(auditLogs) == 0 {
		t.Fatalf("期望写入 contract_git:* 审计日志，实际为 0 条")
	}
	auditRaw, _ := json.Marshal(auditLogs)
	if strings.Contains(string(auditRaw), secretToken) || strings.Contains(string(auditRaw), webhookSecret) {
		t.Fatalf("SysAuditLog 绝对不能记录明文凭据: %s", string(auditRaw))
	}

	// 8. 删除单个 Bundle 与整个仓库，验证历史 ProtoSchema 安全保留并 ON DELETE SET NULL
	{
		req := httptest.NewRequest(http.MethodDelete, "/api/git-repos/"+strconv.FormatUint(repoID, 10)+"/bundles/"+strconv.FormatUint(orderBundleID, 10), nil)
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("DELETE bundle 期望 200，实际 %d: %s", w.Code, w.Body.String())
		}
		var orderSchema models.ProtoSchema
		if err := db.Where("name = ?", "order-proto").First(&orderSchema).Error; err != nil {
			t.Fatalf("删除 Bundle 后历史 ProtoSchema 应当保留，查询失败: %v", err)
		}
		if orderSchema.SourceBundleID != nil {
			t.Fatalf("删除 Bundle 后 ProtoSchema.SourceBundleID 应当置 NULL (ON DELETE SET NULL)，实际 %v", *orderSchema.SourceBundleID)
		}
	}
	_ = commonBundleID
	// 确认已下线 DELETE /api/git-repos/:id 返回 404 且不删除 ProtoFile / ProtoSchema
	{
		req := httptest.NewRequest(http.MethodDelete, "/api/git-repos/"+strconv.FormatUint(repoID, 10), nil)
		req.Header.Set("Authorization", "Bearer "+adminJWT)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("已下线 DELETE /api/git-repos/:id 期望 404，实际 %d: %s", w.Code, w.Body.String())
		}
	}
}
