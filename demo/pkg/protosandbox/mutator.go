package protosandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"code-common/backend/gitsync"
	"code-contract/config"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/gitclient"
	"code-contract/models"

	"gorm.io/gorm"
)

// MutateResult 描述一次动态演进剧本的执行结果。
type MutateResult struct {
	Scenario      string `json:"scenario"`
	RepoName      string `json:"repo_name"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	MRCheckRunID  uint64 `json:"mr_check_run_id,omitempty"`
	MRStatus      string `json:"mr_status,omitempty"`
	BreakingCount int    `json:"breaking_count,omitempty"`
	AffectedCount int    `json:"affected_count,omitempty"`
	LintErrors    int    `json:"lint_errors,omitempty"`
	LintWarnings  int    `json:"lint_warnings,omitempty"`
	DupGroups     int    `json:"dup_groups,omitempty"`
	Message       string `json:"message"`
}

// Mutate 执行 Group C 动态契约演进剧本（breaking / lint-and-dup / promote-no-proto / doc-update / rollback）。
func Mutate(ctx context.Context, db *gorm.DB, cfg *config.Config, scenario string, runSync bool) (*MutateResult, error) {
	if db == nil {
		return nil, errors.New("db 不能为空")
	}
	if cfg == nil {
		cfg = &config.AppConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	codesDir := cfg.Storage.GetCodesDir()

	switch strings.ToLower(strings.TrimSpace(scenario)) {
	case "breaking":
		return mutateBreaking(ctx, db, cfg, codesDir)
	case "lint-and-dup":
		return mutateLintAndDup(ctx, db, cfg, codesDir)
	case "promote-no-proto":
		return mutatePromoteNoProto(ctx, db, cfg, codesDir, runSync)
	case "doc-update":
		return mutateDocUpdate(ctx, db, cfg, codesDir, runSync)
	case "rollback":
		return mutateRollback(ctx, db, cfg, codesDir, runSync)
	default:
		return nil, fmt.Errorf("未知演进剧本 %q（可选: breaking, lint-and-dup, promote-no-proto, doc-update, rollback）", scenario)
	}
}

func ensureOrderGatewayBaseline(ctx context.Context, db *gorm.DB, cfg *config.Config) (*models.GitRepo, *models.GitProtoBundle, *decoder.Registry, error) {
	var orderRepo models.GitRepo
	if err := db.WithContext(ctx).Where("name = ?", "sandbox-order-gateway").First(&orderRepo).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("未找到 sandbox-order-gateway，请先运行 proto-sandbox up --sync --activate: %w", err)
	}
	var bundle models.GitProtoBundle
	if err := db.WithContext(ctx).Where("git_repo_id = ?", orderRepo.ID).Order("id ASC").First(&bundle).Error; err != nil || bundle.LastSchemaID == nil {
		if _, _, err := syncAndActivateGroupABaseline(ctx, db, cfg); err != nil {
			return nil, nil, nil, err
		}
		if err := db.WithContext(ctx).Where("git_repo_id = ?", orderRepo.ID).Order("id ASC").First(&bundle).Error; err != nil {
			return nil, nil, nil, fmt.Errorf("sandbox-order-gateway 尚未生成编译 Bundle: %w", err)
		}
	}
	reg, err := decoder.NewRegistry(db)
	if err != nil {
		return nil, nil, nil, err
	}
	return &orderRepo, &bundle, reg, nil
}

func mutateBreaking(ctx context.Context, db *gorm.DB, cfg *config.Config, codesDir string) (*MutateResult, error) {
	orderRepo, bundle, reg, err := ensureOrderGatewayBaseline(ctx, db, cfg)
	if err != nil {
		return nil, err
	}

	localPath, err := gitsync.ResolveRepoLocalPath(codesDir, orderRepo.RepoURL)
	if err != nil {
		return nil, err
	}

	// 破坏性变更：删除 CreateOrderReq.payment_channel (field 5)，并将 quantity (field 3) 从 int32 改为 string
	brokenOrder := strings.Replace(protoEcomOrder,
		"  // 购买数量（需大于 0）\n  int32 quantity = 3;\n  // 客户端幂等防重流水号\n  string client_order_ref = 4;\n  // 指定外部支付路由渠道：ALIPAY（默认） / WECHAT\n  string payment_channel = 5;",
		"  // 购买数量（破坏性变更：int32 -> string，且移除 payment_channel=5）\n  string quantity = 3;\n  // 客户端幂等防重流水号\n  string client_order_ref = 4;",
		1)

	sha, err := CommitAndAdvanceOrigin(localPath, "main", map[string][]byte{
		"pg/demo/proto/order.proto": []byte(brokenOrder),
	}, "feat(breaking): change CreateOrderReq.quantity type and drop payment_channel")
	if err != nil {
		return nil, err
	}

	runner := gitclient.NewMRGateRunner(db, reg, nil)
	run, err := runner.CreateAndExecuteBundleRun(ctx, gitclient.MRExecuteInput{
		Repo:           orderRepo,
		Bundle:         bundle,
		MRIID:          101,
		EventPhase:     models.MREventPhaseGate,
		MRTitle:        "[Sandbox] Breaking change on CreateOrderReq",
		MRURL:          "https://git.sandbox.local/sandbox-demo/ecom/sandbox-order-gateway/-/merge_requests/101",
		SourceBranch:   "feat/breaking-order-v2",
		TargetBranch:   "main",
		HeadCommitSHA:  sha,
		TargetHeadSHA:  orderRepo.LastSyncCommit,
		AuthorUsername: "sandbox_bot",
		PatchFiles: []gitclient.MRPatchFile{
			{
				ChangeType: gitclient.MRChangeModified,
				NewPath:    "demo/proto/order.proto",
				Content:    brokenOrder,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return &MutateResult{
		Scenario:      "breaking",
		RepoName:      orderRepo.Name,
		CommitSHA:     sha,
		MRCheckRunID:  run.ID,
		MRStatus:      run.Status,
		BreakingCount: run.BreakingChangeCount,
		AffectedCount: run.AffectedModulesCount,
		Message:       fmt.Sprintf("已触发破坏性变更 MR 门禁流水 #%d (status=%s, breaking=%d, affected=%d)", run.ID, run.Status, run.BreakingChangeCount, run.AffectedModulesCount),
	}, nil
}

func mutateLintAndDup(ctx context.Context, db *gorm.DB, cfg *config.Config, codesDir string) (*MutateResult, error) {
	orderRepo, bundle, reg, err := ensureOrderGatewayBaseline(ctx, db, cfg)
	if err != nil {
		return nil, err
	}

	localPath, err := gitsync.ResolveRepoLocalPath(codesDir, orderRepo.RepoURL)
	if err != nil {
		return nil, err
	}

	// 在 order.proto 末尾追加与 AccountSettlementSnapshot 5/5 字段完全一致且命名不规范的消息，同时触发 Lint + AST 重复簇 (similarity=1.0 >= 0.85)
	lintDupOrder := protoEcomOrder + `
message order_settlement_audit_bad {
  string order_id = 1;
  string user_id = 2;
  int64 amount_cents = 3;
  string settlement_serial_no = 4;
  int64 settled_at_unix = 5;
}
`
	sha, err := CommitAndAdvanceOrigin(localPath, "main", map[string][]byte{
		"pg/demo/proto/order.proto": []byte(lintDupOrder),
	}, "feat(lint): add OrderSettlementAudit with style violations and AST duplicate")
	if err != nil {
		return nil, err
	}

	runner := gitclient.NewMRGateRunner(db, reg, nil)
	run, err := runner.CreateAndExecuteBundleRun(ctx, gitclient.MRExecuteInput{
		Repo:           orderRepo,
		Bundle:         bundle,
		MRIID:          102,
		EventPhase:     models.MREventPhaseGate,
		MRTitle:        "[Sandbox] Lint violations & AST duplicate cluster demo",
		MRURL:          "https://git.sandbox.local/sandbox-demo/ecom/sandbox-order-gateway/-/merge_requests/102",
		SourceBranch:   "feat/lint-and-dup-demo",
		TargetBranch:   "main",
		HeadCommitSHA:  sha,
		TargetHeadSHA:  orderRepo.LastSyncCommit,
		AuthorUsername: "sandbox_bot",
		PatchFiles: []gitclient.MRPatchFile{
			{
				ChangeType: gitclient.MRChangeModified,
				NewPath:    "demo/proto/order.proto",
				Content:    lintDupOrder,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return &MutateResult{
		Scenario:     "lint-and-dup",
		RepoName:     orderRepo.Name,
		CommitSHA:    sha,
		MRCheckRunID: run.ID,
		MRStatus:     run.Status,
		LintErrors:   run.LintErrorCount,
		LintWarnings: run.LintWarningCount,
		DupGroups:    run.DuplicateGroupCount,
		Message:      fmt.Sprintf("已触发静态规范与 AST 重复簇 MR 检查 #%d (status=%s, err=%d, warn=%d, dup_groups=%d)", run.ID, run.Status, run.LintErrorCount, run.LintWarningCount, run.DuplicateGroupCount),
	}, nil
}

func mutatePromoteNoProto(ctx context.Context, db *gorm.DB, cfg *config.Config, codesDir string, runSync bool) (*MutateResult, error) {
	var repo models.GitRepo
	if err := db.WithContext(ctx).Where("name = ?", "sandbox-no-proto-app").First(&repo).Error; err != nil {
		return nil, fmt.Errorf("未找到 sandbox-no-proto-app，请先运行 proto-sandbox up: %w", err)
	}
	localPath, err := gitsync.ResolveRepoLocalPath(codesDir, repo.RepoURL)
	if err != nil {
		return nil, err
	}

	notifyProto := `syntax = "proto3";

// 工具消息通知服务（新增小写 pg/ 目录，验证 NO_PROTO 自动晋升为 HAS_PROTO）
// @owner: tools-platform-team
package tools.notify;

service NotifyService {
  // 发送站内通知
  rpc SendNotice(SendNoticeReq) returns (SendNoticeRsp);
}

message SendNoticeReq {
  string receiver = 1;
  string title = 2;
}

message SendNoticeRsp {
  int32 code = 1;
}
`
	sha, err := CommitAndAdvanceOrigin(localPath, "main", map[string][]byte{
		"pg/notify_service.proto": []byte(notifyProto),
	}, "feat(proto): add lowercase pg/notify_service.proto")
	if err != nil {
		return nil, err
	}

	msg := "已向 sandbox-no-proto-app 写入 pg/notify_service.proto；在控制台点击「仅同步已有缓存」或传入 --sync 即可验证 NO_PROTO -> HAS_PROTO 晋升"
	if runSync {
		if _, sErr := syncSelectedSandboxRepos(ctx, db, cfg, []uint64{repo.ID}); sErr != nil {
			return nil, sErr
		}
		var updated models.GitRepo
		_ = db.WithContext(ctx).First(&updated, repo.ID).Error
		msg = fmt.Sprintf("已同步 sandbox-no-proto-app，当前 proto_status=%s", updated.ProtoStatus)
	}
	return &MutateResult{
		Scenario:  "promote-no-proto",
		RepoName:  repo.Name,
		CommitSHA: sha,
		Message:   msg,
	}, nil
}

func mutateDocUpdate(ctx context.Context, db *gorm.DB, cfg *config.Config, codesDir string, runSync bool) (*MutateResult, error) {
	var repo models.GitRepo
	if err := db.WithContext(ctx).Where("name = ?", "sandbox-order-gateway").First(&repo).Error; err != nil {
		return nil, fmt.Errorf("未找到 sandbox-order-gateway，请先运行 proto-sandbox up: %w", err)
	}
	localPath, err := gitsync.ResolveRepoLocalPath(codesDir, repo.RepoURL)
	if err != nil {
		return nil, err
	}

	updatedOrder := strings.Replace(protoEcomOrder,
		"  // @error_code 1007 ERR_ORDER_CANCEL_NOT_ALLOWED 当前订单状态不允许取消（仅 SUCCESS 状态可取消）\n  rpc CancelOrder(CancelOrderReq) returns (CancelOrderRsp);\n}",
		"  // @error_code 1007 ERR_ORDER_CANCEL_NOT_ALLOWED 当前订单状态不允许取消（仅 SUCCESS 状态可取消）\n  rpc CancelOrder(CancelOrderReq) returns (CancelOrderRsp);\n\n  // 批量查询订单状态摘要（活体文档增量刷新演示）\n  // @owner: trade-order-team\n  // @error_code 1008 ERR_ORDER_QUOTA_EXCEEDED 单用户并发查单频率超限\n  rpc BatchGetOrderStatus(BatchOrderStatusReq) returns (BatchOrderStatusRsp);\n}",
		1)
	updatedOrder += `
// BatchOrderStatusReq 批量查询订单状态请求
message BatchOrderStatusReq {
  // 待查询订单号清单（单批上限 50）
  repeated string order_ids = 1;
}

// BatchOrderStatusRsp 批量查询订单状态响应
message BatchOrderStatusRsp {
  // 业务状态码：0=成功
  int32 code = 1;
  // 订单状态映射（order_id -> status）
  map<string, string> status_map = 2;
}
`
	sha, err := CommitAndAdvanceOrigin(localPath, "main", map[string][]byte{
		"pg/demo/proto/order.proto": []byte(updatedOrder),
	}, "docs(order): add BatchGetOrderStatus RPC and error code 1008")
	if err != nil {
		return nil, err
	}

	msg := "已更新 sandbox-order-gateway 接口定义；在控制台点击同步并激活，或加 --sync 后即可在 /proto-docs 查看增量文档"
	if runSync {
		if _, sErr := syncSelectedSandboxRepos(ctx, db, cfg, []uint64{repo.ID}); sErr != nil {
			return nil, sErr
		}
		_, _ = activateGroupASchemas(ctx, db)
		msg = "已完成 sandbox-order-gateway 增量同步、编译并刷新 /proto-docs 活体文档索引"
	}
	return &MutateResult{
		Scenario:  "doc-update",
		RepoName:  repo.Name,
		CommitSHA: sha,
		Message:   msg,
	}, nil
}

func mutateRollback(ctx context.Context, db *gorm.DB, cfg *config.Config, codesDir string, runSync bool) (*MutateResult, error) {
	targets := []string{"sandbox-order-gateway", "sandbox-no-proto-app"}
	var rolled []string
	var repoIDs []uint64
	var lastSHA string
	for _, name := range targets {
		var repo models.GitRepo
		if err := db.WithContext(ctx).Where("name = ?", name).First(&repo).Error; err != nil {
			continue
		}
		localPath, err := gitsync.ResolveRepoLocalPath(codesDir, repo.RepoURL)
		if err != nil {
			continue
		}
		sha, rErr := RollbackRepoToTag(localPath, "main", SandboxTagV1)
		if rErr != nil {
			return nil, rErr
		}
		lastSHA = sha
		rolled = append(rolled, name)
		repoIDs = append(repoIDs, repo.ID)
	}
	if len(rolled) == 0 {
		return nil, errors.New("未找到可回滚的沙箱仓库，请先运行 proto-sandbox up")
	}
	if runSync && len(repoIDs) > 0 {
		_, _ = syncSelectedSandboxRepos(ctx, db, cfg, repoIDs)
	}
	return &MutateResult{
		Scenario:  "rollback",
		RepoName:  strings.Join(rolled, ","),
		CommitSHA: lastSHA,
		Message:   fmt.Sprintf("已将 %s 回滚至 %s (%s)", strings.Join(rolled, ", "), SandboxTagV1, lastSHA),
	}, nil
}
