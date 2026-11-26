package noise

import (
	"context"
	"strings"
	"testing"

	"code-common/backend/testdb"
	"code-contract/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func setupNoiseTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.SetupIsolatedDB(t, "noise_rules")
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}
	return db
}

func TestLoadAndAppendNoiseConfig_DualSource(t *testing.T) {
	db := setupNoiseTestDB(t)
	ctx := context.Background()

	// 1. 默认初始态：全局规则和台账标记均为空
	cfg, err := LoadEffectiveNoiseConfig(ctx, db)
	if err != nil {
		t.Fatalf("LoadEffectiveNoiseConfig failed: %v", err)
	}
	if len(cfg.EffectiveRules) != 0 {
		t.Fatalf("expected empty default effective rules, got %v", cfg.EffectiveRules)
	}
	if len(cfg.PresetTemplates) < 3 {
		t.Fatalf("expected preset templates, got %d", len(cfg.PresetTemplates))
	}

	// 2. 创建一条标记为噪音的 ModuleService（带 ProtoServiceName 别名）
	sub := models.Subsystem{Code: "INFRA", Name: "基础设施"}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create subsystem failed: %v", err)
	}
	fg := models.FunctionalGroup{SubsystemID: sub.ID, Code: "LOG_GRP", Name: "日志组", Level: 1, Path: "/1/"}
	if err := db.Create(&fg).Error; err != nil {
		t.Fatalf("create functional group failed: %v", err)
	}
	mod := models.Module{SubsystemID: sub.ID, GroupID: fg.ID, Code: "LOG_MOD", Name: "日志模块"}
	if err := db.Create(&mod).Error; err != nil {
		t.Fatalf("create module failed: %v", err)
	}
	svc := models.ModuleService{
		ModuleID:         models.Uint64Ptr(mod.ID),
		ServiceName:      "CLS_Log-1",
		ProtoServiceName: "dophi.v1.LogService",
		IsNoiseService:   true,
		DiffIgnorePaths:  datatypes.JSON("[]"),
	}
	if err := db.Create(&svc).Error; err != nil {
		t.Fatalf("create module service failed: %v", err)
	}

	// 3. 追加全局规则并验证大小写去重
	cfg, err = AppendGlobalNoiseRule(ctx, db, "*MonitorService*", "admin")
	if err != nil {
		t.Fatalf("AppendGlobalNoiseRule failed: %v", err)
	}
	cfg, err = AppendGlobalNoiseRule(ctx, db, "*monitorservice*", "admin")
	if err != nil {
		t.Fatalf("AppendGlobalNoiseRule duplicate failed: %v", err)
	}
	if len(cfg.GlobalRules) != 1 || cfg.GlobalRules[0] != "*MonitorService*" {
		t.Fatalf("unexpected GlobalRules: %v", cfg.GlobalRules)
	}
	if len(cfg.MarkedServices) != 1 || cfg.MarkedServices[0].ModuleName != "日志模块" {
		t.Fatalf("unexpected MarkedServices: %+v", cfg.MarkedServices)
	}
	if len(cfg.EffectiveRules) != 3 {
		t.Fatalf("expected 3 merged effective rules, got %v", cfg.EffectiveRules)
	}
}

func TestBuildNoiseSQLFilter_EscapesWildcards(t *testing.T) {
	clause, args := BuildNoiseSQLFilter([]string{"DH_Log*", "MonitorSvc:Ping"})
	if clause == "" {
		t.Fatalf("expected non-empty SQL clause")
	}
	if len(args) != 5 {
		t.Fatalf("expected 5 args, got %d (%v)", len(args), args)
	}
	if !strings.Contains(clause, `ESCAPE '\'`) {
		t.Fatalf("expected ESCAPE clause, got %s", clause)
	}
	if args[0] != `DH\_Log%` {
		t.Fatalf("expected escaped underscore and wildcard, got %v", args[0])
	}
}
