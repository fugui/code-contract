package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code-traefik/demo/pkg/crosframe"
	"code-traefik/demo/pkg/proto/commonpb"
	"code-traefik/demo/pkg/proto/orderpb"
	"code-traefik/internal/server/decoder"
	"code-traefik/internal/server/linter"
	"code-traefik/internal/server/protodoc"
	"code-traefik/internal/server/security"
	"code-traefik/models"
	"code-traefik/pkg/crosrpc"

	"google.golang.org/protobuf/proto"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	seedCommitV1SHA = "7c31b09e4a2f1180d5e6c7b8a9f0123456789abc"
	seedCommitV2SHA = "a8f92e4b7c1d3058f1a29b4e6d8c0f1234567890"
)

type bundleSpec struct {
	BundleName       string
	TargetSchemaName string
	Category         string
	Description      string
	EntryFiles       []string
	FileGlobs        []string
	SchemaDesc       string
}

type protoFileSeedSpec struct {
	BundleKey    string
	FilePath     string
	PackageName  string
	LegacyV1Text string
}

// seedGovernanceAndLifecycleData 重建 Proto 全生命周期、文档符号、黄金样本、静态检查、MR 门禁与站内通知演示数据。
func seedGovernanceAndLifecycleData(
	db *gorm.DB,
	decoderReg *decoder.Registry,
	demoPbPath string,
	modMap map[string]models.Module,
	sutOrderArchProcID uint64,
) error {
	ctx := context.Background()
	adminUser := resolveSeedAdminUser(db)

	log.Println("[6/9] 重建静态检查规则与电商领域标准词汇库...")
	if err := seedRulesAndVocabTerms(db, adminUser.ID); err != nil {
		return err
	}

	log.Println("[7/9] 读取 demo/proto 源码树并编译激活 4 个 GitProtoBundle (BASE_LIB / BUSINESS / GATEWAY)...")
	sources, err := loadDemoProtoSources(demoPbPath)
	if err != nil {
		return err
	}

	gitRepo, _, fileIDs, activeSchemas, err := seedGitBundlesAndCompileSchemas(ctx, db, decoderReg, sources, adminUser.ID)
	if err != nil {
		return err
	}

	log.Println("[8/9] 构造订单核心黄金测试集 (GoldenSuite) 并提取全量 ProtoDocSymbol 文档树...")
	if err := seedDemoGoldenSuite(db, sutOrderArchProcID, adminUser.ID); err != nil {
		return err
	}
	for _, sc := range activeSchemas {
		cnt, docErr := protodoc.RebuildForSchema(db, sc)
		if docErr != nil {
			return fmt.Errorf("重建 Schema [%s] 文档符号失败: %w", sc.Name, docErr)
		}
		log.Printf("  • 已提取 [%s] (ID=%d, %s) 文档符号 %d 条", sc.Name, sc.ID, sc.Category, cnt)
	}

	log.Println("[9/9] 执行静态合规巡检 (含 AST 重复结构体探测) 并预置 MR 门禁战报与站内信通知...")
	report, err := linter.RunAndPersistScan(ctx, linter.ScanRequest{
		DB:                db,
		TriggerSource:     models.TriggerSourceManual,
		TriggeredBy:       adminUser.ID,
		GitRepoID:         &gitRepo.ID,
		IncludeDuplicates: true,
		IncludeLLM:        false,
	})
	if err != nil {
		return fmt.Errorf("执行静态合规巡检失败: %w", err)
	}
	log.Printf("  • 静态巡检报告 #%d 已落库: 状态=%s, Error=%d, Warning=%d, 重复结构体簇=%d",
		report.ID, report.Status, report.ErrorCount, report.WarningCount, report.DuplicateGroupCount)

	if err := seedMRCheckRunsAndNotifications(db, gitRepo, activeSchemas, fileIDs, report, modMap, adminUser); err != nil {
		return err
	}
	return nil
}

func resolveSeedAdminUser(db *gorm.DB) models.User {
	var admin models.User
	if err := db.Where("username = ?", "admin").First(&admin).Error; err == nil && admin.ID > 0 {
		return admin
	}
	if err := db.Order("id ASC").First(&admin).Error; err == nil && admin.ID > 0 {
		return admin
	}
	return models.User{
		ID:       1,
		Username: "admin",
		Email:    "admin@code-traefik.local",
	}
}

func seedRulesAndVocabTerms(db *gorm.DB, adminID uint) error {
	if err := models.EnsureBuiltinCheckRules(db); err != nil {
		return fmt.Errorf("重建内置静态检查规则失败: %w", err)
	}
	if err := models.EnsureBuiltinVocabularyTerms(db); err != nil {
		return fmt.Errorf("重建内置标准词汇库失败: %w", err)
	}

	domainTerms := []models.VocabularyTerm{
		{
			Term:          "sku",
			DescriptionEn: "Stock Keeping Unit",
			DescriptionZh: "商品标准库存单元编码",
			Aliases:       datatypes.JSON([]byte(`["item_code","goods_no"]`)),
			SimilarTerms:  datatypes.JSON([]byte(`["item","product"]`)),
			Domain:        "INVENTORY",
			Source:        models.VocabSourceManual,
			IsApproved:    true,
			CreatedBy:     adminID,
		},
		{
			Term:          "mch",
			DescriptionEn: "Merchant",
			DescriptionZh: "签约商户主体编码",
			Aliases:       datatypes.JSON([]byte(`["merch","merchant_id"]`)),
			SimilarTerms:  datatypes.JSON([]byte(`["merchant","seller"]`)),
			Domain:        "PAYMENT",
			Source:        models.VocabSourceManual,
			IsApproved:    true,
			CreatedBy:     adminID,
		},
		{
			Term:          "cny",
			DescriptionEn: "Chinese Yuan",
			DescriptionZh: "人民币结算币种标识",
			Aliases:       datatypes.JSON([]byte(`["rmb"]`)),
			SimilarTerms:  datatypes.JSON([]byte(`["currency"]`)),
			Domain:        "FINANCE",
			Source:        models.VocabSourceManual,
			IsApproved:    true,
			CreatedBy:     adminID,
		},
		{
			Term:          "tx_sn",
			DescriptionEn: "Transaction Serial Number",
			DescriptionZh: "待审批 AI 推荐：统一归并至标准词根 tx_serial_no / channel_tx_no",
			Aliases:       datatypes.JSON([]byte(`["trans_sn","serial_num"]`)),
			SimilarTerms:  datatypes.JSON([]byte(`["tx_serial_no","channel_tx_no"]`)),
			Domain:        "TRADE",
			Source:        models.VocabSourceLLMSuggested,
			IsApproved:    false,
			CreatedBy:     adminID,
		},
		{
			Term:          "refund_amt",
			DescriptionEn: "Refund Amount Cents",
			DescriptionZh: "待审批 AI 推荐：金额字段建议统一使用 amount_cents 避免单位歧义",
			Aliases:       datatypes.JSON([]byte(`["rf_amt","refund_money"]`)),
			SimilarTerms:  datatypes.JSON([]byte(`["refunded_amount_cents","amount_cents"]`)),
			Domain:        "FINANCE",
			Source:        models.VocabSourceLLMSuggested,
			IsApproved:    false,
			CreatedBy:     adminID,
		},
	}

	for _, term := range domainTerms {
		wantApproved := term.IsApproved
		if err := db.Create(&term).Error; err != nil {
			return fmt.Errorf("写入领域标准词汇 %s 失败: %w", term.Term, err)
		}
		if !wantApproved {
			// GORM 对带 default:true 标签的 bool 零值会回退为数据库默认值 true，需显式 UPDATE 为 false
			if err := db.Model(&term).Update("is_approved", false).Error; err != nil {
				return fmt.Errorf("更新领域标准词汇 %s 审批态失败: %w", term.Term, err)
			}
		}
	}
	return nil
}

func loadDemoProtoSources(demoPbPath string) (map[string]string, error) {
	fileNames := []string{
		"common.proto",
		"payment.proto",
		"account.proto",
		"inventory.proto",
		"order.proto",
	}

	candidateDirs := []string{
		filepath.Join(filepath.Dir(demoPbPath), "proto"),
		filepath.Join("demo", "proto"),
		filepath.Join("..", "..", "demo", "proto"),
	}

	var resolvedDir string
	for _, dir := range candidateDirs {
		if _, err := os.Stat(filepath.Join(dir, "common.proto")); err == nil {
			resolvedDir = dir
			break
		}
	}
	if resolvedDir == "" {
		return nil, fmt.Errorf("未找到 demo/proto 源码目录（已探测: %v）", candidateDirs)
	}

	sources := make(map[string]string, len(fileNames))
	for _, name := range fileNames {
		fullPath := filepath.Join(resolvedDir, name)
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", fullPath, err)
		}
		sources["demo/proto/"+name] = string(raw)
	}
	return sources, nil
}

func seedGitBundlesAndCompileSchemas(
	ctx context.Context,
	db *gorm.DB,
	decoderReg *decoder.Registry,
	sources map[string]string,
	adminID uint,
) (*models.GitRepo, map[string]models.GitProtoBundle, map[string]uint64, []*models.ProtoSchema, error) {
	now := time.Now().UTC()

	gitRepo := models.GitRepo{
		Name:            "dophi-ecommerce-contracts",
		Provider:        models.GitProviderGitLab,
		RepoURL:         "https://gitlab.internal.dophi.io/arch/dophi-ecommerce-contracts.git",
		APIBaseURL:      "https://gitlab.internal.dophi.io/api/v4",
		DefaultBranch:   "main",
		AuthType:        models.GitAuthNone,
		SyncMode:        models.GitSyncManual,
		PollIntervalSec: 300,
		AutoCompile:     true,
		AutoActivate:    true,
		LintOnSync:      true,
		LastSyncAt:      &now,
		LastSyncCommit:  seedCommitV2SHA,
		LastSyncStatus:  models.GitSyncStatusSuccess,
		Description:     "DOPHI 电商 CROS RPC 全链路 Protobuf 接口契约主仓（Monorepo 四包治理基线）",
		CreatedBy:       adminID,
	}
	if strings.TrimSpace(os.Getenv(security.EnvTraefikCryptoKey)) != "" {
		gitRepo.AuthType = models.GitAuthToken
		if err := gitRepo.SetCredentials("glpat-demo-ecommerce-readonly-token"); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("加密 Git 凭据失败: %w", err)
		}
		if err := gitRepo.SetWebhookSecret("whsec-demo-ecommerce-mr-gate"); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("加密 Webhook Secret 失败: %w", err)
		}
	}
	if err := db.Create(&gitRepo).Error; err != nil {
		return nil, nil, nil, nil, fmt.Errorf("创建演示 GitRepo 失败: %w", err)
	}

	specs := []bundleSpec{
		{
			BundleName:       "demo-common-base",
			TargetSchemaName: "demo-common-base",
			Category:         models.ProtoCategoryBaseLib,
			Description:      "公共基础契约库（OrderStatusEnum / PaymentChannelEnum / TraceAuditMeta）",
			EntryFiles:       []string{"demo/proto/common.proto"},
			FileGlobs:        []string{"demo/proto/common.proto"},
			SchemaDesc:       "电商公共基础枚举与全链路追踪审计扩展头基础库 (BASE_LIB)",
		},
		{
			BundleName:       "demo-payment-contract",
			TargetSchemaName: "demo-payment-contract",
			Category:         models.ProtoCategoryBaseLib,
			Description:      "统一支付通道多实现 LIB 契约库（定义 demo.payment.PaymentService，供 ALIPAY/WECHAT 双实例实现）",
			EntryFiles:       []string{"demo/proto/payment.proto"},
			FileGlobs:        []string{"demo/proto/payment.proto"},
			SchemaDesc:       "统一支付渠道 LIB 契约描述集（BASE_LIB，引用 demo/proto/common.proto）",
		},
		{
			BundleName:       "demo-domain-services",
			TargetSchemaName: "demo-domain-services",
			Category:         models.ProtoCategoryBusiness,
			Description:      "核心下游领域服务契约包（AccountService 资金账本 + InventoryService 库存履约）",
			EntryFiles:       []string{"demo/proto/account.proto", "demo/proto/inventory.proto"},
			FileGlobs:        []string{"demo/proto/account.proto", "demo/proto/inventory.proto"},
			SchemaDesc:       "资金账户与仓储履约领域服务描述集（BUSINESS）",
		},
		{
			BundleName:       "demo-order-gateway",
			TargetSchemaName: "demo-order-gateway",
			Category:         models.ProtoCategoryGateway,
			Description:      "订单交易中枢聚合网关契约包（OrderService，跨包依赖 demo.common 与 demo.payment）",
			EntryFiles:       []string{"demo/proto/order.proto"},
			FileGlobs:        []string{"demo/proto/order.proto"},
			SchemaDesc:       "订单交易中枢聚合网关描述集（GATEWAY）",
		},
	}

	bundles := make(map[string]models.GitProtoBundle, len(specs))
	for _, sp := range specs {
		entryJSON, err := json.Marshal(sp.EntryFiles)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		globsJSON, err := json.Marshal(sp.FileGlobs)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		b := models.GitProtoBundle{
			GitRepoID:        gitRepo.ID,
			BundleName:       sp.BundleName,
			TargetSchemaName: sp.TargetSchemaName,
			Category:         sp.Category,
			SubdirPath:       ".",
			IncludeDirsJSON:  datatypes.JSON([]byte(`["."]`)),
			EntryFilesJSON:   datatypes.JSON(entryJSON),
			FileGlobsJSON:    datatypes.JSON(globsJSON),
			ExcludeGlobsJSON: datatypes.JSON([]byte(`[]`)),
			AutoCompile:      true,
			AutoActivate:     true,
			MRAutoPublish:    models.MRAutoPublishAutoActivate,
			LintOnSync:       true,
			LastSyncCommit:   seedCommitV2SHA,
			LastSyncAt:       &now,
			Description:      sp.Description,
		}
		if err := db.Create(&b).Error; err != nil {
			return nil, nil, nil, nil, fmt.Errorf("创建 GitProtoBundle %s 失败: %w", sp.BundleName, err)
		}
		bundles[sp.BundleName] = b
	}

	fileSpecs := []protoFileSeedSpec{
		{
			BundleKey:   "demo-common-base",
			FilePath:    "demo/proto/common.proto",
			PackageName: "demo.common",
		},
		{
			BundleKey:    "demo-payment-contract",
			FilePath:     "demo/proto/payment.proto",
			PackageName:  "demo.payment",
			LegacyV1Text: legacyPaymentProtoV1,
		},
		{
			BundleKey:   "demo-domain-services",
			FilePath:    "demo/proto/account.proto",
			PackageName: "demo.account",
		},
		{
			BundleKey:   "demo-domain-services",
			FilePath:    "demo/proto/inventory.proto",
			PackageName: "demo.inventory",
		},
		{
			BundleKey:    "demo-order-gateway",
			FilePath:     "demo/proto/order.proto",
			PackageName:  "demo.order",
			LegacyV1Text: legacyOrderProtoV1,
		},
	}

	latestVersionIDsByFile := make(map[string]uint64, len(fileSpecs))
	fileIDs := make(map[string]uint64, len(fileSpecs))
	for _, fsp := range fileSpecs {
		content, ok := sources[fsp.FilePath]
		if !ok {
			return nil, nil, nil, nil, fmt.Errorf("缺少源文件 %s", fsp.FilePath)
		}
		bundle := bundles[fsp.BundleKey]
		pfID, verID, err := createProtoFileWithHistory(db, gitRepo.ID, bundle, fsp, content, now)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		fileIDs[fsp.FilePath] = pfID
		latestVersionIDsByFile[fsp.FilePath] = verID
	}

	// 按依赖拓扑顺序逐一执行纯 Go protocompile 在线编译与激活（确保 BASE_LIB 优先入池供下游解析）
	activeSchemas := make([]*models.ProtoSchema, 0, len(specs))
	sourceProvider := decoder.NewDBProtoSourceProvider(db)
	totalOps := 0
	for _, sp := range specs {
		b := bundles[sp.BundleName]
		filesMap := make(map[string]string, len(sp.EntryFiles))
		verIDs := make([]uint64, 0, len(sp.EntryFiles))
		for _, fp := range sp.EntryFiles {
			filesMap[fp] = sources[fp]
			if vid := latestVersionIDsByFile[fp]; vid > 0 {
				verIDs = append(verIDs, vid)
			}
		}

		repoID := gitRepo.ID
		bundleID := b.ID
		schema, ops, _, compErr := decoderReg.CompileAndSaveSchema(ctx, decoder.CompileProtoBundleInput{
			Name:             b.TargetSchemaName,
			Category:         b.Category,
			VersionTag:       "v2.0.0",
			Description:      sp.SchemaDesc,
			Files:            filesMap,
			ProtoIncludeDirs: b.IncludeDirs(),
			EntryPoints:      sp.EntryFiles,
			AutoActivate:     true,
			SourceType:       models.ProtoSourceGitSync,
			GitRepoID:        &repoID,
			SourceBundleID:   &bundleID,
			SourceCommitSHA:  seedCommitV2SHA,
			OperatorID:       adminID,
			SourceProvider:   sourceProvider,
		})
		if compErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("编译并激活 Bundle [%s] 失败: %w", sp.BundleName, compErr)
		}

		scID := schema.ID
		if err := db.Model(&models.GitProtoBundle{}).Where("id = ?", b.ID).Update("last_schema_id", scID).Error; err != nil {
			return nil, nil, nil, nil, fmt.Errorf("更新 Bundle [%s] last_schema_id 失败: %w", sp.BundleName, err)
		}
		b.LastSchemaID = &scID
		bundles[sp.BundleName] = b

		if len(verIDs) > 0 {
			if err := db.Model(&models.ProtoFileVersion{}).Where("id IN ?", verIDs).Update("schema_id", scID).Error; err != nil {
				return nil, nil, nil, nil, fmt.Errorf("绑定 ProtoFileVersion schema_id 失败: %w", err)
			}
		}

		activeSchemas = append(activeSchemas, schema)
		totalOps += len(ops)
		log.Printf("  • Bundle [%s] (%s) 编译激活成功 -> Schema #%d (%d 个 Operation，外引: %s)",
			b.BundleName, b.Category, schema.ID, len(ops), string(schema.ExternalImports))
	}
	log.Printf("  • 全部 4 个 Git Bundle 编译激活完毕，共计生成 %d 个活跃 ProtoOperation", totalOps)

	return &gitRepo, bundles, fileIDs, activeSchemas, nil
}

func createProtoFileWithHistory(
	db *gorm.DB,
	gitRepoID uint64,
	bundle models.GitProtoBundle,
	fsp protoFileSeedSpec,
	latestContent string,
	now time.Time,
) (uint64, uint64, error) {
	latestSHA := sha256Hex(latestContent)
	latestLines := countTextLines(latestContent)

	pf := models.ProtoFile{
		GitRepoID:        gitRepoID,
		BundleID:         bundle.ID,
		TargetSchemaName: bundle.TargetSchemaName,
		Category:         bundle.Category,
		FilePath:         fsp.FilePath,
		RepoFilePath:     fsp.FilePath,
		PackageName:      fsp.PackageName,
		ContentSHA256:    latestSHA,
		LineCount:        latestLines,
		LatestVersionNum: 1,
		LatestCommitSHA:  seedCommitV2SHA,
		IsDeleted:        false,
	}
	if err := db.Create(&pf).Error; err != nil {
		return 0, 0, fmt.Errorf("创建 ProtoFile %s 失败: %w", fsp.FilePath, err)
	}

	nextVerNum := 1
	if strings.TrimSpace(fsp.LegacyV1Text) != "" {
		v1 := models.ProtoFileVersion{
			ProtoFileID:   pf.ID,
			Version:       1,
			VersionTag:    "v1.0.0",
			SourceType:    models.ProtoSourceGitSync,
			CommitSHA:     seedCommitV1SHA,
			CommitMessage: "feat(proto): 初始化电商交易与支付网关基础契约 v1.0",
			AuthorName:    "zhangsan",
			AuthorEmail:   "zhangsan@code-traefik.local",
			ContentText:   fsp.LegacyV1Text,
			ContentSHA256: sha256Hex(fsp.LegacyV1Text),
			LineCount:     countTextLines(fsp.LegacyV1Text),
			CommittedAt:   now.Add(-48 * time.Hour),
		}
		if err := db.Create(&v1).Error; err != nil {
			return 0, 0, fmt.Errorf("创建 %s v1 历史快照失败: %w", fsp.FilePath, err)
		}
		nextVerNum = 2
	}

	latestVer := models.ProtoFileVersion{
		ProtoFileID:   pf.ID,
		Version:       nextVerNum,
		VersionTag:    "v2.0.0",
		SourceType:    models.ProtoSourceGitSync,
		CommitSHA:     seedCommitV2SHA,
		CommitMessage: "feat(proto): 补充 common 公共枚举审计头、@owner/@error_code 文档标签与对账快照契约",
		AuthorName:    "lisi",
		AuthorEmail:   "lisi@code-traefik.local",
		ContentText:   latestContent,
		ContentSHA256: latestSHA,
		LineCount:     latestLines,
		CommittedAt:   now.Add(-2 * time.Hour),
	}
	if err := db.Create(&latestVer).Error; err != nil {
		return 0, 0, fmt.Errorf("创建 %s 最新快照失败: %w", fsp.FilePath, err)
	}

	if err := db.Model(&pf).Updates(map[string]any{
		"latest_version_id":  latestVer.ID,
		"latest_version_num": nextVerNum,
	}).Error; err != nil {
		return 0, 0, fmt.Errorf("更新 ProtoFile %s 最新版本指针失败: %w", fsp.FilePath, err)
	}

	return pf.ID, latestVer.ID, nil
}

func seedDemoGoldenSuite(db *gorm.DB, archProcID uint64, adminID uint) error {
	suite := models.GoldenSuite{
		Code:             "GS_DEMO_ORDER_CORE",
		Name:             "订单交易中枢核心链路黄金回归套件",
		ArchProcessID:    archProcID,
		Version:          "v2.0.0",
		Status:           models.GoldenSuiteStatusActive,
		ExtraIgnorePaths: datatypes.JSON([]byte(`["created_at_unix","audit_meta.timestamp_unix"]`)),
		ItemCount:        3,
		Description:      "涵盖复合行情询价、支付宝下单成交及取消订单逆向三方补偿的标准 CROS RPC 黄金基线样本",
		CreatedBy:        adminID,
		UpdatedBy:        adminID,
	}
	if err := db.Create(&suite).Error; err != nil {
		return fmt.Errorf("创建演示黄金测试套件失败: %w", err)
	}

	trace := &crosrpc.TraceKeyExt{
		TraceID:      0x2026111500000001,
		SpanID:       0x0000000000001001,
		FatherSpanID: 0,
		RecordSwitch: 1,
	}

	type sampleCase struct {
		OrderNo   int
		Name      string
		Operation string
		SNO       uint64
		ReqMsg    proto.Message
		RspMsg    proto.Message
	}

	cases := []sampleCase{
		{
			OrderNo:   1,
			Name:      "复合行情询价正常返回用户余额与商品实时库存",
			Operation: "GetCompositeQuote",
			SNO:       10001,
			ReqMsg: &orderpb.CompositeQuoteReq{
				UserId: "U1001",
				SkuId:  "SKU-PHONE-01",
			},
			RspMsg: &orderpb.CompositeQuoteRsp{
				Code:           0,
				Message:        "ok",
				UserId:         "U1001",
				BalanceCents:   150000,
				SkuId:          "SKU-PHONE-01",
				SkuName:        "Dophi Flagship Phone X1",
				UnitPriceCents: 29900,
				AvailableStock: 42,
			},
		},
		{
			OrderNo:   2,
			Name:      "支付宝渠道下单并完成锁库扣款授权成交",
			Operation: "CreateOrder",
			SNO:       10002,
			ReqMsg: &orderpb.CreateOrderReq{
				UserId:         "U1001",
				SkuId:          "SKU-PHONE-01",
				Quantity:       1,
				ClientOrderRef: "CLI-ORD-20261115-0001",
				PaymentChannel: "ALIPAY",
			},
			RspMsg: &orderpb.CreateOrderRsp{
				Code:             0,
				Message:          "order created",
				OrderId:          "ORD-U1001-0001",
				TotalAmountCents: 29900,
				Status:           "SUCCESS",
				CreatedAtUnix:    1763164800,
				PaymentChannel:   "ALIPAY",
				PaymentTxNo:      "ALIPAY-TX-001-0001",
				StatusEnum:       commonpb.OrderStatusEnum_ORDER_STATUS_ENUM_SUCCESS,
				AuditMeta: &commonpb.TraceAuditMeta{
					TraceId:       "2026111500000001",
					CallerModule:  "CLIENT_TUI",
					TimestampUnix: 1763164800,
					RegionCode:    "cn-east-1a",
				},
			},
		},
		{
			OrderNo:   3,
			Name:      "取消已成交订单并触发支付退款/余额返还/库存释放三方补偿",
			Operation: "CancelOrder",
			SNO:       10003,
			ReqMsg: &orderpb.CancelOrderReq{
				OrderId: "ORD-U1001-0001",
				Reason:  "买家重新选择商品颜色规格",
			},
			RspMsg: &orderpb.CancelOrderRsp{
				Code:                0,
				Message:             "order cancelled and compensated",
				OrderId:             "ORD-U1001-0001",
				RefundedAmountCents: 29900,
				ReleasedStock:       1,
			},
		},
	}

	for _, sc := range cases {
		reqPayload, err := proto.Marshal(sc.ReqMsg)
		if err != nil {
			return fmt.Errorf("序列化黄金用例 %s 请求失败: %w", sc.Operation, err)
		}
		rspPayload, err := proto.Marshal(sc.RspMsg)
		if err != nil {
			return fmt.Errorf("序列化黄金用例 %s 响应失败: %w", sc.Operation, err)
		}

		reqPkt := crosframe.BuildReqPacket(sc.SNO, "demo.order.OrderService", sc.Operation, trace, reqPayload)
		rspPkt := crosframe.BuildRspPacket(sc.SNO, "demo.order.OrderService", sc.Operation, 0, trace, rspPayload)

		item := models.GoldenSuiteItem{
			SuiteID:         suite.ID,
			OrderNo:         sc.OrderNo,
			Name:            sc.Name,
			ServiceName:     "demo.order.OrderService",
			Operation:       sc.Operation,
			BaselineStatus:  models.U64Numeric(0),
			RspSegCount:     1,
			RawReqPacket:    reqPkt,
			RawRspPacket:    rspPkt,
			ScopedMocks:     datatypes.JSON([]byte(`[]`)),
			EgressEndpoints: datatypes.JSON([]byte(`[]`)),
			MaskRulesJSON:   datatypes.JSON([]byte(`[]`)),
			CreatedBy:       adminID,
		}
		if err := db.Create(&item).Error; err != nil {
			return fmt.Errorf("写入黄金用例 %s 失败: %w", sc.Operation, err)
		}
	}
	return nil
}

func seedMRCheckRunsAndNotifications(
	db *gorm.DB,
	gitRepo *models.GitRepo,
	activeSchemas []*models.ProtoSchema,
	fileIDs map[string]uint64,
	report *models.CheckReport,
	modMap map[string]models.Module,
	adminUser models.User,
) error {
	now := time.Now().UTC()
	schemaByName := make(map[string]*models.ProtoSchema, len(activeSchemas))
	for _, sc := range activeSchemas {
		schemaByName[sc.Name] = sc
	}

	var bundlePayment, bundleServices, bundleGateway *uint64
	if sc := schemaByName["demo-payment-contract"]; sc != nil {
		bundlePayment = sc.SourceBundleID
	}
	if sc := schemaByName["demo-domain-services"]; sc != nil {
		bundleServices = sc.SourceBundleID
	}
	if sc := schemaByName["demo-order-gateway"]; sc != nil {
		bundleGateway = sc.SourceBundleID
	}

	// 查询受影响模块 DTO 切片
	affectedPay := buildAffectedDependents(modMap, []string{"ORDER_CORE", "PAYMENT_ALIPAY", "PAYMENT_WECHAT"})
	affectedPayBytes, err := json.Marshal(affectedPay)
	if err != nil {
		return err
	}
	affectedDom := buildAffectedDependents(modMap, []string{"ACCOUNT_CORE", "INVENTORY_CORE"})
	affectedDomBytes, err := json.Marshal(affectedDom)
	if err != nil {
		return err
	}

	breakingList := []decoder.BreakingRuleViolation{
		{
			Rule:        decoder.RuleTypeIncompatible,
			ProtoFile:   "demo/proto/payment.proto",
			Symbol:      "demo.payment.AuthorizePayReq.amount_cents",
			Message:     "字段 AuthorizePayReq.amount_cents (#3) 类型从 int64 篡改为 string，破坏线上二进制 Wire 兼容性",
			ServiceName: "demo.payment.PaymentService",
		},
		{
			Rule:        decoder.RuleTagMutation,
			ProtoFile:   "demo/proto/payment.proto",
			Symbol:      "demo.payment.AuthorizePayReq.channel",
			Message:     "字段 AuthorizePayReq.channel Tag 编号发生变更（4 -> 6）且未 reserve 原 Tag #4",
			ServiceName: "demo.payment.PaymentService",
		},
	}
	breakingBytes, err := json.Marshal(breakingList)
	if err != nil {
		return err
	}

	t1Start := now.Add(-6 * time.Hour)
	t1End := t1Start.Add(380 * time.Millisecond)
	mrPassed := models.MRCheckRun{
		GitRepoID:             gitRepo.ID,
		BundleID:              bundleGateway,
		MRIID:                 101,
		EventPhase:            models.MREventPhaseGate,
		MRTitle:               "feat(order): 订单响应追加强类型 OrderStatusEnum 与全链路审计头",
		MRURL:                 "https://gitlab.internal.dophi.io/arch/dophi-ecommerce-contracts/-/merge_requests/101",
		SourceBranch:          "feat/order-audit-enum",
		TargetBranch:          "main",
		HeadCommitSHA:         seedCommitV2SHA,
		TargetHeadSHA:         seedCommitV1SHA,
		AuthorUsername:        "wangwu",
		AuthorEmail:           "wangwu@code-traefik.local",
		Status:                models.MRCheckStatusPassed,
		CompilePassed:         true,
		LintErrorCount:        0,
		LintWarningCount:      0,
		DuplicateGroupCount:   0,
		BreakingChangeCount:   0,
		AffectedModulesCount:  0,
		ChangedProtoFilesJSON: datatypes.JSON([]byte(`["demo/proto/order.proto"]`)),
		BreakingDetailsJSON:   datatypes.JSON([]byte(`[]`)),
		AffectedModulesJSON:   datatypes.JSON([]byte(`[]`)),
		RemoteCommentID:       "note-mr101-passed",
		AttemptCount:          1,
		DurationMs:            380,
		StartedAt:             &t1Start,
		CompletedAt:           &t1End,
	}

	t2Start := now.Add(-3 * time.Hour)
	t2End := t2Start.Add(520 * time.Millisecond)
	mrFailed := models.MRCheckRun{
		GitRepoID:             gitRepo.ID,
		BundleID:              bundlePayment,
		MRIID:                 102,
		EventPhase:            models.MREventPhaseGate,
		MRTitle:               "refactor(payment): 尝试调整 AuthorizePayReq 金额字段为 string 并重排 Tag",
		MRURL:                 "https://gitlab.internal.dophi.io/arch/dophi-ecommerce-contracts/-/merge_requests/102",
		SourceBranch:          "refactor/payment-wire-break",
		TargetBranch:          "main",
		HeadCommitSHA:         "b4d2e89c1f0a3b7d6e5c4b3a2f1e0d9c8b7a6f5e",
		TargetHeadSHA:         seedCommitV1SHA,
		AuthorUsername:        "zhaoliu",
		AuthorEmail:           "zhaoliu@code-traefik.local",
		Status:                models.MRCheckStatusFailed,
		CompilePassed:         true,
		LintErrorCount:        0,
		LintWarningCount:      1,
		DuplicateGroupCount:   0,
		BreakingChangeCount:   len(breakingList),
		AffectedModulesCount:  len(affectedPay),
		ChangedProtoFilesJSON: datatypes.JSON([]byte(`["demo/proto/payment.proto"]`)),
		BreakingDetailsJSON:   datatypes.JSON(breakingBytes),
		AffectedModulesJSON:   datatypes.JSON(affectedPayBytes),
		RemoteCommentID:       "note-mr102-failed",
		AttemptCount:          1,
		DurationMs:            520,
		StartedAt:             &t2Start,
		CompletedAt:           &t2End,
	}

	t3Start := now.Add(-90 * time.Minute)
	t3End := t3Start.Add(640 * time.Millisecond)
	t3Override := now.Add(-60 * time.Minute)
	overriderID := adminUser.ID
	var reportIDPtr *uint64
	var reportID uint64
	var reportDupCount int
	if report != nil {
		reportDupCount = report.DuplicateGroupCount
		if report.ID > 0 {
			reportID = report.ID
			reportIDPtr = &reportID
		}
	}
	mrOverridden := models.MRCheckRun{
		GitRepoID:             gitRepo.ID,
		BundleID:              bundleServices,
		MRIID:                 103,
		EventPhase:            models.MREventPhaseGate,
		MRTitle:               "feat(settlement): 双十一对账期在 account/inventory 新增结算快照结构",
		MRURL:                 "https://gitlab.internal.dophi.io/arch/dophi-ecommerce-contracts/-/merge_requests/103",
		SourceBranch:          "hotfix/settlement-snapshot",
		TargetBranch:          "main",
		HeadCommitSHA:         "c5e3f90d2a1b4c8e7f6a5b4c3d2e1f0a9b8c7d6e",
		TargetHeadSHA:         seedCommitV2SHA,
		AuthorUsername:        "lisi",
		AuthorEmail:           "lisi@code-traefik.local",
		Status:                models.MRCheckStatusOverridden,
		CompilePassed:         true,
		LintErrorCount:        0,
		LintWarningCount:      1,
		DuplicateGroupCount:   reportDupCount,
		BreakingChangeCount:   0,
		AffectedModulesCount:  len(affectedDom),
		ChangedProtoFilesJSON: datatypes.JSON([]byte(`["demo/proto/account.proto","demo/proto/inventory.proto"]`)),
		BreakingDetailsJSON:   datatypes.JSON([]byte(`[]`)),
		AffectedModulesJSON:   datatypes.JSON(affectedDomBytes),
		CheckReportID:         reportIDPtr,
		RemoteCommentID:       "note-mr103-overridden",
		AttemptCount:          1,
		OverriddenBy:          &overriderID,
		OverrideReason:        "双十一大促日终对账窗口紧急上线，AccountSettlementSnapshot 与 InventorySettlementSnapshot 跨包重复已登记技术债，下个迭代抽离至 demo/proto/common.proto",
		OverriddenAt:          &t3Override,
		DurationMs:            640,
		StartedAt:             &t3Start,
		CompletedAt:           &t3End,
	}

	runs := []*models.MRCheckRun{&mrPassed, &mrFailed, &mrOverridden}
	for _, r := range runs {
		if err := db.Create(r).Error; err != nil {
			return fmt.Errorf("创建演示 MRCheckRun !%d 失败: %w", r.MRIID, err)
		}
	}

	// 预置 3 条覆盖阻断、公共库波及与 AST 重复治理的站内信通知（全部指向 ADR-19 新版工作台直达链接）
	sourceAcctFileID := fileIDs["demo/proto/account.proto"]
	dupLink := "/proto-governance?gov_tab=report"
	if sourceAcctFileID > 0 {
		dupLink = fmt.Sprintf("/proto-governance?gov_tab=report&file_id=%d", sourceAcctFileID)
	}

	notifMetaFailed, err := json.Marshal(map[string]any{
		"mr_iid":                 mrFailed.MRIID,
		"repo_name":              gitRepo.Name,
		"bundle_name":            "demo-payment-contract",
		"breaking_change_count":  mrFailed.BreakingChangeCount,
		"affected_modules_count": mrFailed.AffectedModulesCount,
	})
	if err != nil {
		return err
	}
	notifMetaImpact, err := json.Marshal(map[string]any{
		"mr_iid":           mrFailed.MRIID,
		"base_lib_bundle":  "demo-payment-contract",
		"affected_modules": []string{"ORDER_CORE", "PAYMENT_ALIPAY", "PAYMENT_WECHAT"},
	})
	if err != nil {
		return err
	}
	notifMetaDup, err := json.Marshal(map[string]any{
		"report_id":             reportID,
		"duplicate_group_count": reportDupCount,
		"pair":                  []string{"demo.account.AccountSettlementSnapshot", "demo.inventory.InventorySettlementSnapshot"},
	})
	if err != nil {
		return err
	}

	readAt := now.Add(-30 * time.Minute)
	notifications := []models.Notification{
		{
			RecipientUserID: adminUser.ID,
			RecipientEmail:  adminUser.Email,
			Category:        models.NotifCategoryMRGateBlocked,
			Severity:        models.NotifSeverityCritical,
			Title:           "[MR !102 门禁阻断] payment.proto 检测到 2 处破坏性契约变更",
			BodyMarkdown:    "合并请求 `!102` (`refactor/payment-wire-break -> main`) 修改了 `AuthorizePayReq.amount_cents` 类型与 `channel` Tag 编号，触发 `TYPE_INCOMPATIBLE` 与 `TAG_MUTATION` 红线拦截，已回写 GitLab Commit Status 为 `failed`。",
			LinkURL:         fmt.Sprintf("/proto-governance?gov_tab=mr-gates&runId=%d", mrFailed.ID),
			SourceType:      models.NotifSourceMRCheckRun,
			SourceID:        mrFailed.ID,
			MetadataJSON:    datatypes.JSON(notifMetaFailed),
			IsRead:          false,
			ExternalChannel: models.ExternalChannelNone,
			EmailStatus:     models.EmailStatusSkipped,
		},
		{
			RecipientUserID: adminUser.ID,
			RecipientEmail:  adminUser.Email,
			Category:        models.NotifCategoryBaseLibImpact,
			Severity:        models.NotifSeverityWarning,
			Title:           "[公共库波及预警] demo-payment-contract 变更波及 3 个下游模块",
			BodyMarkdown:    "基础契约库 `demo/proto/payment.proto` (`demo.payment.PaymentService`) 若合入非兼容改动，将直接冲击 **ORDER_CORE**（DISPATCH 消费方）以及 **PAYMENT_ALIPAY / PAYMENT_WECHAT**（多实现服务实例）。",
			LinkURL:         fmt.Sprintf("/proto-governance?gov_tab=mr-gates&runId=%d", mrFailed.ID),
			SourceType:      models.NotifSourceMRCheckRun,
			SourceID:        mrFailed.ID,
			MetadataJSON:    datatypes.JSON(notifMetaImpact),
			IsRead:          false,
			ExternalChannel: models.ExternalChannelNone,
			EmailStatus:     models.EmailStatusSkipped,
		},
		{
			RecipientUserID: adminUser.ID,
			RecipientEmail:  adminUser.Email,
			Category:        models.NotifCategoryDuplicateWarn,
			Severity:        models.NotifSeverityInfo,
			Title:           "[重复定义治理] 发现跨模块 100% 相似对账快照结构体",
			BodyMarkdown:    "`demo.account.AccountSettlementSnapshot` 与 `demo.inventory.InventorySettlementSnapshot` 5 个核心字段 AST 词根指纹相似度达到 **100%**，建议下个迭代上浮抽离至 `demo/proto/common.proto`。",
			LinkURL:         dupLink,
			SourceType:      models.NotifSourceCheckReport,
			SourceID:        reportID,
			MetadataJSON:    datatypes.JSON(notifMetaDup),
			IsRead:          true,
			ReadAt:          &readAt,
			ExternalChannel: models.ExternalChannelNone,
			EmailStatus:     models.EmailStatusSkipped,
		},
	}

	for _, n := range notifications {
		if err := db.Create(&n).Error; err != nil {
			return fmt.Errorf("创建演示站内通知失败: %w", err)
		}
	}

	log.Printf("  • 已生成 %d 条 MR 门禁战报 (PASSED / FAILED / OVERRIDDEN) 与 %d 条站内通知",
		len(runs), len(notifications))
	return nil
}

func buildAffectedDependents(modMap map[string]models.Module, codes []string) []decoder.AffectedDependent {
	out := make([]decoder.AffectedDependent, 0, len(codes))
	procMap := map[string]string{
		"ORDER_CORE":     "PROC_SUT_ORDER",
		"ACCOUNT_CORE":   "PROC_BACKEND_ACCOUNT",
		"INVENTORY_CORE": "PROC_BACKEND_INVENTORY",
		"PAYMENT_ALIPAY": "PROC_BACKEND_PAYMENT_ALIPAY",
		"PAYMENT_WECHAT": "PROC_BACKEND_PAYMENT_WECHAT",
	}
	for _, code := range codes {
		m, ok := modMap[code]
		if !ok {
			continue
		}
		out = append(out, decoder.AffectedDependent{
			ModuleID:        m.ID,
			ModuleCode:      m.Code,
			ModuleName:      m.Name,
			ArchProcessCode: procMap[code],
			OwnerName:       "admin",
		})
	}
	return out
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func countTextLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

const legacyPaymentProtoV1 = `syntax = "proto3";

package demo.payment;
option go_package = "code-traefik/demo/pkg/proto/paymentpb";

// 统一支付通道接口契约 v1.0 (无 common.proto 强类型枚举，无结构化文档标签)
service PaymentService {
  // 渠道支付授权扣款
  rpc AuthorizePay(AuthorizePayReq) returns (AuthorizePayRsp);
  // 渠道原路退款补偿
  rpc RefundPay(RefundPayReq) returns (RefundPayRsp);
}

message AuthorizePayReq {
  string order_id = 1;
  string user_id = 2;
  int64 amount_cents = 3;
  string channel = 4; // ALIPAY, WECHAT
}

message AuthorizePayRsp {
  int32 code = 1;
  string message = 2;
  string order_id = 3;
  string channel = 4;
  string channel_tx_no = 5;
  int64 paid_at_unix = 6;
}

message RefundPayReq {
  string order_id = 1;
  string channel_tx_no = 2;
  int64 amount_cents = 3;
  string reason = 4;
}

message RefundPayRsp {
  int32 code = 1;
  string message = 2;
  string order_id = 3;
  string channel = 4;
  string refund_tx_no = 5;
}
`

const legacyOrderProtoV1 = `syntax = "proto3";

package demo.order;
option go_package = "code-traefik/demo/pkg/proto/orderpb";

// 订单综合服务契约 v1.0 (尚无 @owner / @error_code 与跨包状态枚举引用)
service OrderService {
  rpc GetCompositeQuote(CompositeQuoteReq) returns (CompositeQuoteRsp);
  rpc CreateOrder(CreateOrderReq) returns (CreateOrderRsp);
  rpc GetOrderDetail(OrderDetailReq) returns (OrderDetailRsp);
  rpc CancelOrder(CancelOrderReq) returns (CancelOrderRsp);
}

message CompositeQuoteReq {
  string user_id = 1;
  string sku_id = 2;
}

message CompositeQuoteRsp {
  int32 code = 1;
  string message = 2;
  string user_id = 3;
  int64 balance_cents = 4;
  string sku_id = 5;
  string sku_name = 6;
  int64 unit_price_cents = 7;
  int32 available_stock = 8;
}

message CreateOrderReq {
  string user_id = 1;
  string sku_id = 2;
  int32 quantity = 3;
  string client_order_ref = 4;
  string payment_channel = 5;
}

message CreateOrderRsp {
  int32 code = 1;
  string message = 2;
  string order_id = 3;
  int64 total_amount_cents = 4;
  string status = 5;
  int64 created_at_unix = 6;
  string payment_channel = 7;
  string payment_tx_no = 8;
}

message OrderDetailReq {
  string order_id = 1;
}

message OrderDetailRsp {
  int32 code = 1;
  string message = 2;
  string order_id = 3;
  string user_id = 4;
  string sku_id = 5;
  int32 quantity = 6;
  int64 total_amount_cents = 7;
  string status = 8;
  int64 created_at_unix = 9;
  string payment_channel = 10;
  string payment_tx_no = 11;
}

message CancelOrderReq {
  string order_id = 1;
  string reason = 2;
}

message CancelOrderRsp {
  int32 code = 1;
  string message = 2;
  string order_id = 3;
  int64 refunded_amount_cents = 4;
  int32 released_stock = 5;
}
`
