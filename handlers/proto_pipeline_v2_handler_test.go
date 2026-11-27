package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/gitclient"
	"code-contract/models"
	"gorm.io/gorm"
)

func testSHA256HexHandler(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func seedHandlerPipelineFixture(t *testing.T, db *gorm.DB) (models.GitProtoBundle, models.GitProtoBundle, models.ProtoFile) {
	t.Helper()
	baseProto := `syntax = "proto3";
package pipe.base.v1;
message PipeMeta { string trace_id = 1; }
`
	bizProto := `syntax = "proto3";
package pipe.biz.v1;
import "pipe/base.proto";
// 管道测试订单服务
service PipeOrderService {
  // 创建订单
  rpc Create(CreatePipeReq) returns (CreatePipeRsp);
}
message CreatePipeReq {
  pipe.base.v1.PipeMeta meta = 1;
  string order_no = 2;
}
message CreatePipeRsp {
  string status = 1;
}
`
	repoBase := models.GitRepo{
		Name:           "pipe-base-repo",
		PlatformRepoID: 801,
		SourceMode:     models.GitSourceModePlatformShared,
		SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
		Provider:       models.GitProviderGitLab,
		RepoURL:        "http://mock/pipe-base.git",
		DefaultBranch:  "main",
		SubsystemSlug:  "pipebase",
		SyncEnabled:    true,
		ProtoStatus:    models.GitRepoProtoStatusHasProto,
	}
	db.Create(&repoBase)
	bBase := models.GitProtoBundle{
		GitRepoID:        repoBase.ID,
		BundleName:       "pipe-base",
		TargetSchemaName: "pipe-base-schema",
		Category:         models.ProtoCategoryBaseLib,
		SubdirPath:       ".",
		AutoCompile:      true,
	}
	db.Create(&bBase)
	shaBase := testSHA256HexHandler(baseProto)
	pfBase := models.ProtoFile{
		GitRepoID:         repoBase.ID,
		BundleID:          bBase.ID,
		TargetSchemaName:  bBase.TargetSchemaName,
		Category:          models.ProtoCategoryBaseLib,
		FilePath:          "pipe/base.proto",
		RepoFilePath:      "pipe/base.proto",
		CleanRelPath:      "pipe/base.proto",
		PackageName:       "pipe.base.v1",
		NormalizerVersion: "1",
		PipelineStatus:    models.ProtoPipelineNormalized,
		ImportsJSON:       []byte(`[]`),
		SymbolsJSON:       []byte(`[]`),
		ContentSHA256:     shaBase,
		LineCount:         3,
		LatestVersionNum:  1,
		LatestCommitSHA:   "c1",
	}
	db.Create(&pfBase)
	vBase := models.ProtoFileVersion{
		ProtoFileID:   pfBase.ID,
		Version:       1,
		SourceType:    models.ProtoSourceGitSync,
		ContentText:   baseProto,
		ContentSHA256: shaBase,
		LineCount:     3,
	}
	db.Create(&vBase)
	_ = db.Model(&pfBase).Update("latest_version_id", vBase.ID).Error

	repoBiz := models.GitRepo{
		Name:           "pipe-biz-repo",
		PlatformRepoID: 802,
		SourceMode:     models.GitSourceModePlatformShared,
		SyncStrategy:   models.GitSyncStrategyLocalCacheOnly,
		Provider:       models.GitProviderGitLab,
		RepoURL:        "http://mock/pipe-biz.git",
		DefaultBranch:  "main",
		SubsystemSlug:  "pipebiz",
		SyncEnabled:    true,
		ProtoStatus:    models.GitRepoProtoStatusHasProto,
	}
	db.Create(&repoBiz)
	bBiz := models.GitProtoBundle{
		GitRepoID:        repoBiz.ID,
		BundleName:       "pipe-biz",
		TargetSchemaName: "pipe-biz-schema",
		Category:         models.ProtoCategoryBusiness,
		SubdirPath:       ".",
		AutoCompile:      true,
	}
	db.Create(&bBiz)
	shaBiz := testSHA256HexHandler(bizProto)
	pfBiz := models.ProtoFile{
		GitRepoID:         repoBiz.ID,
		BundleID:          bBiz.ID,
		TargetSchemaName:  bBiz.TargetSchemaName,
		Category:          models.ProtoCategoryBusiness,
		FilePath:          "pipe/biz.proto",
		RepoFilePath:      "pipe/biz.proto",
		CleanRelPath:      "pipe/biz.proto",
		PackageName:       "pipe.biz.v1",
		NormalizerVersion: "1",
		PipelineStatus:    models.ProtoPipelineNormalized,
		IsUServiceSource:  true,
		ImportsJSON:       []byte(`["pipe/base.proto"]`),
		SymbolsJSON:       []byte(`[]`),
		ContentSHA256:     shaBiz,
		LineCount:         15,
		LatestVersionNum:  1,
		LatestCommitSHA:   "c1",
	}
	db.Create(&pfBiz)
	vBiz := models.ProtoFileVersion{
		ProtoFileID:   pfBiz.ID,
		Version:       1,
		SourceType:    models.ProtoSourceGitSync,
		ContentText:   bizProto,
		ContentSHA256: shaBiz,
		LineCount:     15,
	}
	db.Create(&vBiz)
	_ = db.Model(&pfBiz).Update("latest_version_id", vBiz.ID).Error
	return bBase, bBiz, pfBase
}

func TestProtoPipelineV2API_ClosureRBACStaleLockAndArchivedGuard(t *testing.T) {
	r, db := setupRouterAndDB(t)
	oldCodes := config.AppConfig.Storage.CodesDir
	oldProtos := config.AppConfig.Storage.ProtosDir
	oldSharedWrite := config.AppConfig.Storage.SharedWriteEnabled
	config.AppConfig.Storage.CodesDir = t.TempDir()
	config.AppConfig.Storage.ProtosDir = t.TempDir()
	config.AppConfig.Storage.SharedWriteEnabled = true
	config.ApplyProtoPipelineDefaults(&config.AppConfig.ProtoPipeline)
	defer func() {
		config.AppConfig.Storage.CodesDir = oldCodes
		config.AppConfig.Storage.ProtosDir = oldProtos
		config.AppConfig.Storage.SharedWriteEnabled = oldSharedWrite
	}()

	admin := createTestUserInDB(t, db, "pipe_admin", config.AdminRole)
	normal := createTestUserInDB(t, db, "pipe_viewer", "user")
	adminToken := generateTestJWT(t, admin)
	viewerToken := generateTestJWT(t, normal)

	doReq := func(method, path, token string, payload any) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 1. 已废止的 UPLOAD_PB / ONLINE_COMPILE 路由必须返回 404
	if w := doReq(http.MethodPost, "/api/proto-schemas", adminToken, map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("POST /api/proto-schemas 应返回 404，实际 %d", w.Code)
	}
	if w := doReq(http.MethodPost, "/api/proto-schemas/compile", adminToken, map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("POST /api/proto-schemas/compile 应返回 404，实际 %d", w.Code)
	}

	// 2. 历史归档来源（UPLOAD_PB / ONLINE_COMPILE）激活 / 批量激活必须返回 422 SCHEMA_SOURCE_ARCHIVED
	reg, err := decoder.NewRegistry(db)
	if err != nil {
		t.Fatalf("NewRegistry 失败: %v", err)
	}
	archSchema, _, _, err := reg.CompileAndSaveSchema(context.Background(), decoder.CompileProtoBundleInput{
		Name:         "legacy-archived",
		Category:     models.ProtoCategoryBusiness,
		Files:        map[string]string{"legacy/arch.proto": "syntax = \"proto3\"; package legacy.v1; message M { string id = 1; }"},
		AutoActivate: false,
		SourceType:   models.ProtoSourceUploadPB,
		OperatorID:   admin.ID,
	})
	if err != nil {
		t.Fatalf("构造归档 Schema 失败: %v", err)
	}
	if w := doReq(http.MethodPost, fmt.Sprintf("/api/proto-schemas/%d/activate", archSchema.ID), adminToken, map[string]any{}); w.Code != http.StatusUnprocessableEntity || !bytes.Contains(w.Body.Bytes(), []byte("SCHEMA_SOURCE_ARCHIVED")) {
		t.Fatalf("归档 UPLOAD_PB 激活期望 422 SCHEMA_SOURCE_ARCHIVED，实测 %d: %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodPost, "/api/proto-schemas/batch-activate", adminToken, map[string]any{"schema_ids": []uint64{archSchema.ID}}); w.Code != http.StatusUnprocessableEntity || !bytes.Contains(w.Body.Bytes(), []byte("SCHEMA_SOURCE_ARCHIVED")) {
		t.Fatalf("归档 UPLOAD_PB 批量激活期望 422 SCHEMA_SOURCE_ARCHIVED，实测 %d: %s", w.Code, w.Body.String())
	}

	// 3. 普通用户无权触发发布与流水线写接口 -> 403；只读 overview 允许 -> 200
	if w := doReq(http.MethodPost, "/api/proto-pipeline/run", viewerToken, map[string]any{"from_stage": "RECONCILE"}); w.Code != http.StatusForbidden {
		t.Fatalf("普通用户触发流水线期望 403，实际 %d", w.Code)
	}
	if w := doReq(http.MethodPost, "/api/proto-pipeline/publish", viewerToken, map[string]any{}); w.Code != http.StatusForbidden {
		t.Fatalf("普通用户触发发布期望 403，实际 %d", w.Code)
	}
	if w := doReq(http.MethodGet, "/api/proto-pipeline/overview", viewerToken, nil); w.Code != http.StatusOK {
		t.Fatalf("普通用户查看流水线概览期望 200，实际 %d: %s", w.Code, w.Body.String())
	}

	// 4. 构造双仓拓扑，验证阶段 ③ 未协调直接 publish -> 409 RECONCILE_STALE
	_, _, pfBase := seedHandlerPipelineFixture(t, db)
	if w := doReq(http.MethodPost, "/api/proto-pipeline/publish", adminToken, map[string]any{}); w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("RECONCILE_STALE")) {
		t.Fatalf("未先协调直接发布期望 409 RECONCILE_STALE，实测 %d: %s", w.Code, w.Body.String())
	}

	// 5. 全局互斥锁占用测试 -> 409 PIPELINE_LOCKED + lock_holder
	lk, lErr := gitclient.TryAcquirePipelineLock(context.Background(), db, gitclient.PipelineLockHolder{
		JobType: "PIPELINE_RUN",
		JobID:   777,
		Holder:  "other-admin",
	})
	if lErr != nil {
		t.Fatalf("获取测试流水线锁失败: %v", lErr)
	}
	wLock := doReq(http.MethodPost, "/api/proto-pipeline/run", adminToken, map[string]any{"from_stage": "RECONCILE", "sync": true})
	if wLock.Code != http.StatusConflict || !bytes.Contains(wLock.Body.Bytes(), []byte("PIPELINE_LOCKED")) || !bytes.Contains(wLock.Body.Bytes(), []byte("other-admin")) {
		lk.Release()
		t.Fatalf("并发触发期望 409 PIPELINE_LOCKED 并携带 lock_holder，实测 %d: %s", wLock.Code, wLock.Body.String())
	}
	lk.Release()

	// 6. 一键四阶段流水线同步执行 (from_stage=RECONCILE) -> 阶段 ③ 收敛 + 阶段 ④ 拓扑激活 + 文档重建
	wRun := doReq(http.MethodPost, "/api/proto-pipeline/run", adminToken, map[string]any{
		"from_stage": "RECONCILE",
		"sync":       true,
	})
	if wRun.Code != http.StatusOK {
		t.Fatalf("执行 from_stage=RECONCILE 同步流水线失败 (%d): %s", wRun.Code, wRun.Body.String())
	}
	var runResp struct {
		PipelineRun models.ProtoPipelineRun `json:"pipeline_run"`
	}
	if err := json.Unmarshal(wRun.Body.Bytes(), &runResp); err != nil {
		t.Fatalf("解析 run 响应失败: %v", err)
	}
	if runResp.PipelineRun.Status != models.PipelineRunStatusCompleted || runResp.PipelineRun.ReconcileRunID == nil || runResp.PipelineRun.PublishRunID == nil {
		t.Fatalf("流水线运行状态或关联 ID 异常: %+v", runResp.PipelineRun)
	}

	// 查询 GET /runs/:runId、/publish-runs/latest、/archived-external-schemas、/overview
	if w := doReq(http.MethodGet, fmt.Sprintf("/api/proto-pipeline/runs/%d", runResp.PipelineRun.ID), viewerToken, nil); w.Code != http.StatusOK {
		t.Fatalf("GET /runs/:runId 失败 (%d): %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodGet, "/api/proto-pipeline/publish-runs/latest", viewerToken, nil); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("COMPLETED")) {
		t.Fatalf("GET /publish-runs/latest 失败 (%d): %s", w.Code, w.Body.String())
	}
	if w := doReq(http.MethodGet, "/api/proto-pipeline/archived-external-schemas", viewerToken, nil); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("legacy-archived")) {
		t.Fatalf("GET /archived-external-schemas 未返回遗留归档资产 (%d): %s", w.Code, w.Body.String())
	}

	// 7. 变更源文件并验证 overview 返回 normalize/reconcile stale + PREVIEW 确认生效 + breaking approve
	breakBase := `syntax = "proto3";
package pipe.base.v1;
message PipeMeta { int64 trace_id = 1; }
`
	breakSHA := testSHA256HexHandler(breakBase)
	v2Break := models.ProtoFileVersion{
		ProtoFileID:   pfBase.ID,
		Version:       2,
		SourceType:    models.ProtoSourceGitSync,
		ContentText:   breakBase,
		ContentSHA256: breakSHA,
		LineCount:     3,
	}
	_ = db.Create(&v2Break).Error
	_ = db.Model(&pfBase).Updates(map[string]any{"content_sha256": breakSHA, "latest_version_num": 2, "latest_version_id": v2Break.ID}).Error

	wOv := doReq(http.MethodGet, "/api/proto-pipeline/overview", viewerToken, nil)
	if wOv.Code != http.StatusOK || !bytes.Contains(wOv.Body.Bytes(), []byte(`"reconcile_stale":true`)) {
		t.Fatalf("文件变更后 overview 应标记 reconcile_stale=true，实测 %d: %s", wOv.Code, wOv.Body.String())
	}

	// 运行 PREVIEW 模式：先协调再发布
	_, _, err = gitclient.RunPoolReconcile(context.Background(), db, gitclient.PoolReconcileOptions{
		CodesDir:    config.AppConfig.Storage.CodesDir,
		ProtosDir:   config.AppConfig.Storage.ProtosDir,
		TriggeredBy: "pipe_admin",
	})
	if err != nil {
		t.Fatalf("RunPoolReconcile 失败: %v", err)
	}
	wPrev := doReq(http.MethodPost, "/api/proto-pipeline/publish", adminToken, map[string]any{
		"publish_mode": "PREVIEW",
	})
	if wPrev.Code != http.StatusOK {
		t.Fatalf("PREVIEW publish 失败 (%d): %s", wPrev.Code, wPrev.Body.String())
	}
	var prevOut struct {
		Run     models.ProtoPublishRun      `json:"run"`
		Details gitclient.PublishRunDetails `json:"details"`
	}
	_ = json.Unmarshal(wPrev.Body.Bytes(), &prevOut)
	if prevOut.Run.Status != models.PublishRunStatusPartialBlocked || len(prevOut.Details.BreakingItems) != 1 {
		t.Fatalf("破坏性变更期望阻塞 pipe-base 并生成 1 条 BreakingItems, got %+v details=%+v", prevOut.Run, prevOut.Details)
	}

	// approve-breaking 放行破坏性变更
	wApprove := doReq(http.MethodPost, fmt.Sprintf("/api/proto-pipeline/publish-runs/%d/approve-breaking", prevOut.Run.ID), adminToken, map[string]any{
		"schema_ids":      []uint64{prevOut.Details.BreakingItems[0].SchemaID},
		"breaking_reason": "测试环境管理员评审通过类型修改并重建下游依赖",
	})
	if wApprove.Code != http.StatusOK {
		t.Fatalf("approve-breaking 失败 (%d): %s", wApprove.Code, wApprove.Body.String())
	}

	// 再测一次无破坏性变更下的 PREVIEW_READY -> confirm
	wPrev2 := doReq(http.MethodPost, "/api/proto-pipeline/publish", adminToken, map[string]any{
		"publish_mode":    "PREVIEW",
		"force_recompile": true,
	})
	if wPrev2.Code != http.StatusOK {
		t.Fatalf("PREVIEW publish #2 失败 (%d): %s", wPrev2.Code, wPrev2.Body.String())
	}
	var prev2Out struct {
		Run models.ProtoPublishRun `json:"run"`
	}
	_ = json.Unmarshal(wPrev2.Body.Bytes(), &prev2Out)
	if prev2Out.Run.Status != models.PublishRunStatusPreviewReady {
		t.Fatalf("无破坏性 force_recompile 预览发布期望 PREVIEW_READY, got %s", prev2Out.Run.Status)
	}
	wConf := doReq(http.MethodPost, fmt.Sprintf("/api/proto-pipeline/publish-runs/%d/confirm", prev2Out.Run.ID), adminToken, map[string]any{})
	if wConf.Code != http.StatusOK || !bytes.Contains(wConf.Body.Bytes(), []byte("COMPLETED")) {
		t.Fatalf("PREVIEW confirm 期望 200 COMPLETED，实测 %d: %s", wConf.Code, wConf.Body.String())
	}
}

// TestSC33_EndToEnd_SandboxPublishToDocsAndSources 验证 SC-33 端到端全链路：
// 阶段 ③+④ 一键发布成功后，消费侧 /api/proto-docs/tree、服务详情、/api/catalog/services、
// 跨包依赖图、/api/proto-files 均可立即检出活跃符号与文件正文，同时废止的外部上传接口维持 404。
func TestSC33_EndToEnd_SandboxPublishToDocsAndSources(t *testing.T) {
	r, db := setupRouterAndDB(t)
	oldCodes := config.AppConfig.Storage.CodesDir
	oldProtos := config.AppConfig.Storage.ProtosDir
	oldSharedWrite := config.AppConfig.Storage.SharedWriteEnabled
	config.AppConfig.Storage.CodesDir = t.TempDir()
	config.AppConfig.Storage.ProtosDir = t.TempDir()
	config.AppConfig.Storage.SharedWriteEnabled = true
	config.ApplyProtoPipelineDefaults(&config.AppConfig.ProtoPipeline)
	defer func() {
		config.AppConfig.Storage.CodesDir = oldCodes
		config.AppConfig.Storage.ProtosDir = oldProtos
		config.AppConfig.Storage.SharedWriteEnabled = oldSharedWrite
	}()

	admin := createTestUserInDB(t, db, "sc33_admin", config.AdminRole)
	viewer := createTestUserInDB(t, db, "sc33_viewer", "user")
	adminToken := generateTestJWT(t, admin)
	viewerToken := generateTestJWT(t, viewer)

	doReq := func(method, path, token string, payload any) *httptest.ResponseRecorder {
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	seedHandlerPipelineFixture(t, db)

	// 1. 触发端到端阶段 ③+④ 流水线同步执行
	wRun := doReq(http.MethodPost, "/api/proto-pipeline/run", adminToken, map[string]any{
		"from_stage": "RECONCILE",
		"sync":       true,
	})
	if wRun.Code != http.StatusOK || !bytes.Contains(wRun.Body.Bytes(), []byte(`"COMPLETED"`)) {
		t.Fatalf("SC-33 一键流水线执行期望 200 COMPLETED，实测 %d: %s", wRun.Code, wRun.Body.String())
	}

	// 2. 消费侧文档树与服务参考详情立即可见
	wTree := doReq(http.MethodGet, "/api/proto-docs/tree", viewerToken, nil)
	if wTree.Code != http.StatusOK || !bytes.Contains(wTree.Body.Bytes(), []byte("pipe.biz.v1.PipeOrderService")) {
		t.Fatalf("GET /api/proto-docs/tree 期望包含 pipe.biz.v1.PipeOrderService，实测 %d: %s", wTree.Code, wTree.Body.String())
	}
	wDetail := doReq(http.MethodGet, "/api/proto-docs/services/pipe.biz.v1.PipeOrderService", viewerToken, nil)
	if wDetail.Code != http.StatusOK || !bytes.Contains(wDetail.Body.Bytes(), []byte("CreatePipeReq")) {
		t.Fatalf("GET /api/proto-docs/services/... 期望返回 200 且含 CreatePipeReq，实测 %d: %s", wDetail.Code, wDetail.Body.String())
	}

	// 3. 统一契约目录与跨包依赖关系图立即可见
	wCat := doReq(http.MethodGet, "/api/catalog/services", viewerToken, nil)
	if wCat.Code != http.StatusOK {
		t.Fatalf("GET /api/catalog/services 期望 200，实测 %d: %s", wCat.Code, wCat.Body.String())
	}
	wGraph := doReq(http.MethodGet, "/api/proto-schemas/dependencies-graph", viewerToken, nil)
	if wGraph.Code != http.StatusOK {
		t.Fatalf("GET /api/proto-schemas/dependencies-graph 期望 200，实测 %d: %s", wGraph.Code, wGraph.Body.String())
	}

	// 4. 源码定位可检出对应 .proto 文件及内容
	wFiles := doReq(http.MethodGet, "/api/proto-files?page=1&page_size=20", viewerToken, nil)
	if wFiles.Code != http.StatusOK || !bytes.Contains(wFiles.Body.Bytes(), []byte("pipe/biz.proto")) {
		t.Fatalf("GET /api/proto-files 期望检出 pipe/biz.proto，实测 %d: %s", wFiles.Code, wFiles.Body.String())
	}

	// 5. 全站自封闭复核：已下线的外部上传/在线直编入口保持 404
	if w := doReq(http.MethodPost, "/api/proto-schemas", adminToken, map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("POST /api/proto-schemas 应保持 404，实测 %d", w.Code)
	}
	if w := doReq(http.MethodPost, "/api/proto-schemas/compile", adminToken, map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("POST /api/proto-schemas/compile 应保持 404，实测 %d", w.Code)
	}
}
