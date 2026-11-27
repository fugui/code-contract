package models

import (
	"os"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
)

func TestADR29ModelsConstraintsAndAllowedSources(t *testing.T) {
	if !IsAllowedGitProtoSource(ProtoSourceGitSync) ||
		!IsAllowedGitProtoSource(ProtoSourceGitMRAuto) ||
		!IsAllowedGitProtoSource(ProtoSourceGitPipeline) {
		t.Fatalf("Git 来源应被允许")
	}
	if IsAllowedGitProtoSource(ProtoSourceUploadPB) || IsAllowedGitProtoSource(ProtoSourceOnlineCompile) || IsAllowedGitProtoSource("") {
		t.Fatalf("外部来源与空来源禁止通过 IsAllowedGitProtoSource")
	}

	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过数据库隔离测试")
	}

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	// 验证 R3_AUTO 写入 contract_proto_import_owners
	owner := ProtoImportOwner{
		LogicalPath:    "trade/order.proto",
		OwnerGitRepoID: 1,
		OwnerBundleID:  10,
		Source:         ImportOwnerSourceR3Auto,
		UpdatedBy:      "pool_reconciler",
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("创建 R3_AUTO ImportOwner 失败: %v", err)
	}

	// 验证 GIT_PIPELINE 写入 contract_proto_schemas
	sc := ProtoSchema{
		Name:               "trade-order",
		Version:            1,
		Category:           ProtoCategoryBusiness,
		SourceType:         ProtoSourceGitPipeline,
		FileName:           "trade-order.pb",
		FileSHA256:         "abc123",
		FileSize:           128,
		DescriptorBytes:    []byte{0x0a, 0x00},
		CompileInputSHA256: "inputsha256",
	}
	if err := db.Create(&sc).Error; err != nil {
		t.Fatalf("创建 GIT_PIPELINE ProtoSchema 失败: %v", err)
	}

	// 验证 ProtoPublishRun 与 ProtoPipelineRun 表与约束
	pubRun := ProtoPublishRun{
		PoolReconcileRunID: 1,
		PublishMode:        PublishModeAuto,
		Status:             PublishRunStatusCompleted,
		TotalBundles:       2,
		PublishedCount:     2,
		StartedAt:          time.Now().UTC(),
	}
	if err := db.Create(&pubRun).Error; err != nil {
		t.Fatalf("创建 ProtoPublishRun 失败: %v", err)
	}
	if err := db.Create(&ProtoPublishRun{
		PoolReconcileRunID: 1,
		PublishMode:        "BAD_MODE",
		Status:             PublishRunStatusCompleted,
		StartedAt:          time.Now().UTC(),
	}).Error; err == nil {
		t.Fatalf("非法 publish_mode 应被拦截")
	}

	pipeRun := ProtoPipelineRun{
		FromStage:     PipelineStageCollectNormalize,
		CurrentStage:  PipelineStagePublish,
		Status:        PipelineRunStatusCompleted,
		TriggerSource: PipelineTriggerManual,
		PublishRunID:  &pubRun.ID,
		StartedAt:     time.Now().UTC(),
	}
	if err := db.Create(&pipeRun).Error; err != nil {
		t.Fatalf("创建 ProtoPipelineRun 失败: %v", err)
	}
	if err := db.Create(&ProtoPipelineRun{
		FromStage:     "UNKNOWN",
		CurrentStage:  "UNKNOWN",
		Status:        PipelineRunStatusRunning,
		TriggerSource: PipelineTriggerManual,
		StartedAt:     time.Now().UTC(),
	}).Error; err == nil {
		t.Fatalf("非法 from_stage 应被拦截")
	}
}
