package main

import (
	"testing"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/models"

	"gorm.io/datatypes"
)

func TestValidateResetTarget_LocalAndRemoteGuard(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		force    bool
		wantHost string
		wantErr  bool
	}{
		{
			name:     "libpq 127.0.0.1 允许",
			dsn:      "host=127.0.0.1 port=5432 user=postgres dbname=code_shield sslmode=disable",
			wantHost: "127.0.0.1",
		},
		{
			name:     "libpq localhost 与单引号值允许",
			dsn:      "host='localhost' port=5432 dbname='code_shield'",
			wantHost: "localhost",
		},
		{
			name:     "URL ipv6 ::1 允许",
			dsn:      "postgres://postgres:secret@[::1]:5432/code_shield?sslmode=disable",
			wantHost: "::1",
		},
		{
			name:     "URL query unix socket 允许",
			dsn:      "postgresql://postgres@/code_shield?host=/var/run/postgresql",
			wantHost: "/var/run/postgresql",
		},
		{
			name:    "远程内网 IP 默认拦截",
			dsn:     "host=192.168.56.18 port=5432 dbname=code_shield",
			wantErr: true,
		},
		{
			name:    "远程域名默认拦截",
			dsn:     "postgres://postgres:secret@prod-pg.internal:5432/code_shield",
			wantErr: true,
		},
		{
			name:    "多主机混含远程地址默认拦截",
			dsn:     "host=127.0.0.1,10.0.0.12 port=5432 dbname=code_shield",
			wantErr: true,
		},
		{
			name:    "空 host 默认拦截",
			dsn:     "dbname=code_shield sslmode=disable",
			wantErr: true,
		},
		{
			name:     "force 模式放行远程地址",
			dsn:      "host=192.168.56.18 port=5432 dbname=code_shield",
			force:    true,
			wantHost: "192.168.56.18",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host, _, _, err := validateResetTarget(tc.dsn, tc.force)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望被安全熔断拦截，实际放行 (host=%q)", host)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望放行，实际报错: %v", err)
			}
			if host != tc.wantHost {
				t.Fatalf("host=%q，期望 %q", host, tc.wantHost)
			}
		})
	}
}

func TestResetLocalData_TruncatesBusinessPreservesSettingsAndRestoresBuiltins(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}

	// 1. 准备待保留的平台公共用户、自定义 ModuleSetting，以及各类待清空业务数据
	user := models.User{
		Username: "tester_keep",
		Email:    "tester_keep@code-contract.local",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	customSetting := models.ModuleSetting{
		SettingKey:  "custom_keep_key",
		SettingVal:  "keep_me",
		Description: "验证默认保留 contract_settings",
		UpdatedBy:   "tester",
	}
	if err := db.Create(&customSetting).Error; err != nil {
		t.Fatalf("创建自定义配置失败: %v", err)
	}

	agent := models.Agent{
		Name:     "old-demo-agent",
		Endpoint: "http://127.0.0.1:9091",
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}

	subsys := models.Subsystem{
		Code:       "TEMP_SUB",
		Name:       "临时子系统",
		LayerLevel: 2,
	}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatalf("创建子系统失败: %v", err)
	}

	fg := models.FunctionalGroup{
		SubsystemID: subsys.ID,
		Code:        "TEMP_FG",
		Name:        "临时功能组",
		Level:       1,
	}
	if err := db.Create(&fg).Error; err != nil {
		t.Fatalf("创建功能组失败: %v", err)
	}

	pgrp := models.ArchProcessGroup{
		Code: "TEMP_PGRP",
		Name: "临时进程组",
	}
	if err := db.Create(&pgrp).Error; err != nil {
		t.Fatalf("创建进程组失败: %v", err)
	}

	ap := models.ArchProcess{
		GroupID:                 pgrp.ID,
		Code:                    "TEMP_AP",
		Name:                    "临时架构进程",
		DefaultListenPort:       19001,
		DefaultExtraDownstreams: datatypes.JSON([]byte("[]")),
	}
	if err := db.Create(&ap).Error; err != nil {
		t.Fatalf("创建架构进程失败: %v", err)
	}

	mod := models.Module{
		SubsystemID:   subsys.ID,
		GroupID:       fg.ID,
		ArchProcessID: &ap.ID,
		Code:          "TEMP_MOD",
		Name:          "临时模块",
		LayerLevel:    2,
	}
	if err := db.Create(&mod).Error; err != nil {
		t.Fatalf("创建模块失败: %v", err)
	}

	svc := models.ModuleService{
		ModuleID:        mod.ID,
		ServiceName:     "demo.temp.TempService",
		DiffIgnorePaths: datatypes.JSON([]byte("[]")),
	}
	if err := db.Create(&svc).Error; err != nil {
		t.Fatalf("创建服务契约失败: %v", err)
	}

	proc := models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       ap.ID,
		EffectiveListenPort: 19001,
		ExtraDownstreams:    datatypes.JSON([]byte("[]")),
		ProbeDesiredState:   models.ProbeDesiredStateArmed,
		ProbeStatus:         models.ProbeStatusDisarmed,
	}
	if err := db.Create(&proc).Error; err != nil {
		t.Fatalf("创建挂载进程失败: %v", err)
	}

	manualTerm := models.VocabularyTerm{
		Term:          "temp_manual_term",
		DescriptionZh: "临时手工词根（应被清空）",
		Source:        models.VocabSourceManual,
		IsApproved:    true,
	}
	if err := db.Create(&manualTerm).Error; err != nil {
		t.Fatalf("创建手工词根失败: %v", err)
	}

	notif := models.Notification{
		RecipientUserID: user.ID,
		Category:        models.NotifCategoryMRGateBlocked,
		Title:           "临时通知",
		BodyMarkdown:    "临时通知内容",
		MetadataJSON:    datatypes.JSON([]byte("{}")),
	}
	if err := db.Create(&notif).Error; err != nil {
		t.Fatalf("创建通知失败: %v", err)
	}

	// 2. 连续执行两次验证清库 + 恢复内置基线的幂等性
	var lastRes *ResetResult
	for run := 1; run <= 2; run++ {
		res, err := resetLocalData(db, ResetOptions{ResetSettings: false})
		if err != nil {
			t.Fatalf("第 %d 次 resetLocalData 失败: %v", run, err)
		}
		lastRes = res
	}

	if lastRes.TruncatedTables != len(models.BusinessTableNames()) {
		t.Fatalf("TruncatedTables=%d，期望 %d", lastRes.TruncatedTables, len(models.BusinessTableNames()))
	}
	if lastRes.SettingsReset {
		t.Fatalf("默认模式不应重置 contract_settings")
	}
	if lastRes.RestoredCheckRules == 0 || lastRes.RestoredVocabTerms == 0 {
		t.Fatalf("内置静态检查规则或基础词根未恢复: rules=%d, vocab=%d", lastRes.RestoredCheckRules, lastRes.RestoredVocabTerms)
	}

	// 3. 断言除内置规则与内置词根表外，其余全部业务表均已归零（不预置任何 Agent/子系统）
	builtinTables := map[string]struct{}{
		(models.CheckRule{}).TableName():      {},
		(models.VocabularyTerm{}).TableName(): {},
	}
	for _, tbl := range models.BusinessTableNames() {
		var cnt int64
		if err := db.Table(tbl).Count(&cnt).Error; err != nil {
			t.Fatalf("统计表 %s 失败: %v", tbl, err)
		}
		if _, isBuiltin := builtinTables[tbl]; isBuiltin {
			if cnt == 0 {
				t.Fatalf("内置基线表 %s 未恢复数据", tbl)
			}
			continue
		}
		if cnt != 0 {
			t.Fatalf("业务表 %s 期望已清空 (0 行)，实际仍有 %d 行", tbl, cnt)
		}
	}

	// 4. 断言手工词根已清除、仅保留 BUILTIN_SEED 词根与内置规则
	var manualCnt int64
	if err := db.Model(&models.VocabularyTerm{}).Where("source <> ?", models.VocabSourceBuiltinSeed).Count(&manualCnt).Error; err != nil {
		t.Fatalf("统计非内置词根失败: %v", err)
	}
	if manualCnt != 0 {
		t.Fatalf("期望手工/LLM 词根全部被清除，实际残留 %d 条", manualCnt)
	}

	var nonBuiltinRuleCnt int64
	if err := db.Model(&models.CheckRule{}).Where("is_builtin = ?", false).Count(&nonBuiltinRuleCnt).Error; err != nil {
		t.Fatalf("统计非内置规则失败: %v", err)
	}
	if nonBuiltinRuleCnt != 0 {
		t.Fatalf("期望自定义规则全部被清除，实际残留 %d 条", nonBuiltinRuleCnt)
	}

	// 5. 断言 users 与自定义 contract_settings 保留未动
	var keptUser models.User
	if err := db.Where("username = ?", "tester_keep").First(&keptUser).Error; err != nil {
		t.Fatalf("共享表 users 记录被误删: %v", err)
	}
	var keptSetting models.ModuleSetting
	if err := db.Where("setting_key = ?", "custom_keep_key").First(&keptSetting).Error; err != nil {
		t.Fatalf("配置表 contract_settings 记录被误删: %v", err)
	}
	if keptSetting.SettingVal != "keep_me" {
		t.Fatalf("配置值被篡改: got %q", keptSetting.SettingVal)
	}

	// 6. 断言自增主键序列已重置 (RESTART IDENTITY)
	newAgent := models.Agent{
		Name:     "fresh-discovered-agent",
		Endpoint: "http://127.0.0.1:9091",
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&newAgent).Error; err != nil {
		t.Fatalf("重建 Agent 验证自增序列失败: %v", err)
	}
	if newAgent.ID != 1 {
		t.Fatalf("Agent ID=%d，期望 RESTART IDENTITY 重置为 1", newAgent.ID)
	}
}

func TestResetLocalData_ResetSettingsFlag(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}

	if err := db.Model(&models.ModuleSetting{}).
		Where("setting_key = ?", "traffic_retention_days").
		Update("setting_val", "999").Error; err != nil {
		t.Fatalf("篡改默认配置失败: %v", err)
	}
	if err := db.Create(&models.ModuleSetting{
		SettingKey: "temp_custom_setting",
		SettingVal: "should_disappear",
	}).Error; err != nil {
		t.Fatalf("写入临时配置失败: %v", err)
	}

	res, err := resetLocalData(db, ResetOptions{ResetSettings: true})
	if err != nil {
		t.Fatalf("resetLocalData(-reset-settings) 失败: %v", err)
	}
	if !res.SettingsReset {
		t.Fatalf("期望 SettingsReset=true")
	}

	var customCnt int64
	if err := db.Model(&models.ModuleSetting{}).Where("setting_key = ?", "temp_custom_setting").Count(&customCnt).Error; err != nil {
		t.Fatalf("查询临时配置失败: %v", err)
	}
	if customCnt != 0 {
		t.Fatalf("期望临时配置已清空，实际残留 %d 条", customCnt)
	}

	var retention models.ModuleSetting
	if err := db.Where("setting_key = ?", "traffic_retention_days").First(&retention).Error; err != nil {
		t.Fatalf("查询恢复后的默认配置失败: %v", err)
	}
	if retention.SettingVal != "30" {
		t.Fatalf("traffic_retention_days=%q，期望恢复为 \"30\"", retention.SettingVal)
	}
}
