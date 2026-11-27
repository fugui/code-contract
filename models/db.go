package models

import (
	"fmt"
	"log"
	"time"

	commonAuth "code-common/backend/auth"
	"code-common/backend/gormdb"
	"code-contract/config"

	"gorm.io/gorm"
)

// DB 模块全局 GORM 数据库连接句柄
var DB *gorm.DB

// BusinessTableNames 返回全部业务数据表名（按外键依赖拓扑倒序排列，排除 ModuleSetting 等全局配置表），
// 供 cmd/resetdb 与 scripts/seed-large-topology 等清表工具共用，作为单一真实源。
func BusinessTableNames() []string {
	return []string{
		MigBackup{}.TableName(),
		AttributionJob{}.TableName(),
		DependencyDecision{}.TableName(),
		ProcessDependency{}.TableName(),
		ServicePlacement{}.TableName(),
		DiscoverySnapshot{}.TableName(),
		Notification{}.TableName(),
		MRCheckRun{}.TableName(),
		CheckViolation{}.TableName(),
		CheckReport{}.TableName(),
		VocabularyTerm{}.TableName(),
		CheckRule{}.TableName(),
		ProtoDocSymbol{}.TableName(),
		ProtoPipelineRun{}.TableName(),
		ProtoPublishRun{}.TableName(),
		ProtoPoolReconcileRun{}.TableName(),
		ProtoGovernanceConfig{}.TableName(),
		ProtoSyncJob{}.TableName(),
		ProtoImportOwner{}.TableName(),
		ProtoFileVersion{}.TableName(),
		ProtoFile{}.TableName(),
		GitProtoBundle{}.TableName(),
		GitRepo{}.TableName(),
		GoldenSuiteItem{}.TableName(),
		GoldenSuite{}.TableName(),
		TopologyLayout{}.TableName(),
		TrafficRecord{}.TableName(),
		RecordingSession{}.TableName(),
		ReplayItem{}.TableName(),
		ReplayTask{}.TableName(),
		Process{}.TableName(),
		ModuleDependency{}.TableName(),
		ModuleService{}.TableName(),
		ServiceContract{}.TableName(),
		Module{}.TableName(),
		FunctionalGroup{}.TableName(),
		Subsystem{}.TableName(),
		ArchProcess{}.TableName(),
		ArchProcessGroup{}.TableName(),
		ProtoOperation{}.TableName(),
		ProtoSchema{}.TableName(),
		Agent{}.TableName(),
	}
}

// InitDB 连接 PostgreSQL 数据库并执行表结构迁移与种子数据初始化
func InitDB() error {
	db, err := gormdb.Connect(config.AppConfig.Database, gormdb.Options{
		ServiceName: config.ServiceName,
	})
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	return SetupWithDB(db)
}

// SetupWithDB 使用外部注入的 *gorm.DB 完成迁移与默认种子初始化（便于单元测试复用）
func SetupWithDB(db *gorm.DB) error {
	DB = db

	// 0. ADR-26 (r4): AutoMigrate 收口 platform_repo_id NOT NULL 前先归并存量手工仓并归档孤儿记录
	if err := PreMigrateADR26R4Orphans(DB); err != nil {
		return fmt.Errorf("预迁移存量 Git 仓库失败: %w", err)
	}

	// 1. 自动迁移标准实体表（核心 contract_* 表 + 平台公共审计/用户表，ADR-14 ~ ADR-27）
	if err := DB.AutoMigrate(
		&Department{},
		&User{},
		&Repository{},
		&SysAuditLog{},
		&ModuleSetting{},
		&Subsystem{},
		&FunctionalGroup{},
		&ArchProcessGroup{},
		&ArchProcess{},
		&Module{},
		&ServiceContract{},
		&ModuleService{},
		&ModuleDependency{},
		&Agent{},
		&Process{},
		&GitRepo{},
		&GitProtoBundle{},
		&ProtoFile{},
		&ProtoFileVersion{},
		&ProtoImportOwner{},
		&ProtoSyncJob{},
		&ProtoGovernanceConfig{},
		&ProtoPoolReconcileRun{},
		&ProtoPublishRun{},
		&ProtoPipelineRun{},
		&ProtoSchema{},
		&ProtoOperation{},
		&RecordingSession{},
		&ReplayTask{},
		&ReplayItem{},
		&GoldenSuite{},
		&GoldenSuiteItem{},
		&TopologyLayout{},
		&ProtoDocSymbol{},
		&CheckRule{},
		&VocabularyTerm{},
		&CheckReport{},
		&CheckViolation{},
		&MRCheckRun{},
		&Notification{},
		&DiscoverySnapshot{},
		&ServicePlacement{},
		&ProcessDependency{},
		&DependencyDecision{},
		&AttributionJob{},
		&MigBackup{},
	); err != nil {
		return fmt.Errorf("迁移数据表结构失败: %w", err)
	}

	// 2. 初始化 contract_traffic_records 物理分区母表与近期按日分区
	pm := &PartitionManager{}
	if err := pm.EnsureParentTable(DB); err != nil {
		return fmt.Errorf("初始化流量分区母表失败: %w", err)
	}
	now := time.Now()
	if _, err := pm.EnsurePartitionsRange(DB, now.AddDate(0, 0, -1), now.AddDate(0, 0, 7)); err != nil {
		return fmt.Errorf("预建日常流量分区子表失败: %w", err)
	}

	// 3. 显式幂等创建关键业务部分唯一索引与检查约束
	procConstraintSQL := `
DO $$
BEGIN
    -- arch_processes 默认端口检查约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_arch_proc_port') THEN
        ALTER TABLE contract_arch_processes ADD CONSTRAINT chk_contract_arch_proc_port CHECK (default_listen_port >= 0 AND default_listen_port <= 65535);
    END IF;

    -- processes 物化有效端口检查约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proc_eff_port') THEN
        ALTER TABLE contract_processes ADD CONSTRAINT chk_contract_proc_eff_port CHECK (effective_listen_port >= 0 AND effective_listen_port <= 65535);
    END IF;

    -- processes 覆盖端口检查约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proc_override_port') THEN
        ALTER TABLE contract_processes ADD CONSTRAINT chk_contract_proc_override_port CHECK (override_listen_port IS NULL OR (override_listen_port >= 0 AND override_listen_port <= 65535));
    END IF;

    -- processes 预挂期望状态检查约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proc_desired') THEN
        ALTER TABLE contract_processes ADD CONSTRAINT chk_contract_proc_desired CHECK (probe_desired_state IN ('ARMED','DISARMED'));
    END IF;
END $$;`
	if err := DB.Exec(procConstraintSQL).Error; err != nil {
		return fmt.Errorf("更新进程端口检查约束失败: %w", err)
	}

	protoConstraintSQL := `
DO $$
BEGIN
    -- contract_proto_schemas 大小检查约束 (16MB = 16777216 字节)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_size') THEN
        ALTER TABLE contract_proto_schemas ADD CONSTRAINT chk_contract_proto_size CHECK (file_size > 0 AND file_size <= 16777216);
    END IF;

    -- contract_proto_schemas 分类检查约束
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_proto_category') THEN
        ALTER TABLE contract_proto_schemas ADD CONSTRAINT chk_contract_proto_category CHECK (category IN ('BASE_LIB', 'BUSINESS', 'GATEWAY'));
    END IF;

    -- contract_proto_schemas 资产来源类型检查约束 (M22 ADR-18 / M43 ADR-29)
    ALTER TABLE contract_proto_schemas DROP CONSTRAINT IF EXISTS chk_contract_proto_source_type;
    ALTER TABLE contract_proto_schemas ADD CONSTRAINT chk_contract_proto_source_type CHECK (source_type IN ('UPLOAD_PB', 'ONLINE_COMPILE', 'GIT_SYNC', 'GIT_MR_AUTO', 'GIT_PIPELINE'));

    -- contract_subsystems / contract_modules 架构分层约束 (M22 ADR-18)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_subsystems_layer') THEN
        ALTER TABLE contract_subsystems ADD CONSTRAINT chk_contract_subsystems_layer CHECK (layer_level >= 0 AND layer_level <= 99);
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_modules_layer') THEN
        ALTER TABLE contract_modules ADD CONSTRAINT chk_contract_modules_layer CHECK (layer_level >= 0 AND layer_level <= 99);
    END IF;

    -- contract_golden_suites 状态检查约束 (F-25, ADR-14)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_golden_suite_status') THEN
        ALTER TABLE contract_golden_suites ADD CONSTRAINT chk_contract_golden_suite_status CHECK (status IN ('ACTIVE', 'ARCHIVED'));
    END IF;

    -- contract_topology_layouts 公私域、视图与归属组合检查约束 (F-04.3, ADR-15)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_topo_layout_scope') THEN
        ALTER TABLE contract_topology_layouts ADD CONSTRAINT chk_contract_topo_layout_scope CHECK (scope IN ('SHARED','PRIVATE'));
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_topo_layout_view') THEN
        ALTER TABLE contract_topology_layouts ADD CONSTRAINT chk_contract_topo_layout_view CHECK (view IN ('LOGICAL','PHYSICAL'));
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_topo_layout_owner') THEN
        ALTER TABLE contract_topology_layouts ADD CONSTRAINT chk_contract_topo_layout_owner
            CHECK ((scope = 'SHARED' AND owner_id IS NULL AND focus_scope = 'GLOBAL' AND is_default = false)
                OR (scope = 'PRIVATE' AND owner_id IS NOT NULL));
    END IF;

    -- contract_proto_doc_symbols 符号类型检查约束 (F-18, M23)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_pdoc_kind') THEN
        ALTER TABLE contract_proto_doc_symbols ADD CONSTRAINT chk_contract_pdoc_kind
            CHECK (symbol_kind IN ('SERVICE','METHOD','MESSAGE','ENUM'));
    END IF;
END $$;`
	if err := DB.Exec(protoConstraintSQL).Error; err != nil {
		return fmt.Errorf("更新描述集检查约束失败: %w", err)
	}

	contractConstraintSQL := `
DO $$
BEGIN
    -- 1. contract_modules 类型与进程绑定检查约束 (ADR-16 §3.1)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_modules_kind') THEN
        ALTER TABLE contract_modules ADD CONSTRAINT chk_contract_modules_kind CHECK (module_kind IN ('BUSINESS', 'CONTRACT_LIB'));
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_modules_lib_no_proc') THEN
        ALTER TABLE contract_modules ADD CONSTRAINT chk_contract_modules_lib_no_proc CHECK (module_kind = 'BUSINESS' OR arch_process_id IS NULL);
    END IF;

    -- 2. contract_service_contracts 提供模式检查约束 (ADR-16 §3.2)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_svc_contract_mode') THEN
        ALTER TABLE contract_service_contracts ADD CONSTRAINT chk_contract_svc_contract_mode CHECK (provide_mode IN ('MULTI_IMPL'));
    END IF;

    -- 3. contract_module_services 契约实现与自定义运行期服务名约束 (ADR-16 v2.0)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_mod_svc_contract_impl') THEN
        ALTER TABLE contract_module_services ADD CONSTRAINT chk_contract_mod_svc_contract_impl CHECK ((contract_id IS NULL AND impl_key = '') OR (contract_id IS NOT NULL AND impl_key <> ''));
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_mod_svc_custom_name') THEN
        ALTER TABLE contract_module_services ADD CONSTRAINT chk_contract_mod_svc_custom_name CHECK (contract_id IS NOT NULL OR is_custom_service_name = false);
    END IF;

    -- 4. contract_module_dependencies 内部实现/分发契约/外部三选一检查约束 (ADR-16 §3.5)
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_dep_target_xor') THEN
        ALTER TABLE contract_module_dependencies ADD CONSTRAINT chk_contract_dep_target_xor CHECK (num_nonnulls(service_id, contract_id) + (CASE WHEN is_external THEN 1 ELSE 0 END) = 1);
    END IF;

    -- 5. 数据一致性校准：依据与关联契约服务名是否一致修正 is_custom_service_name 标记
    UPDATE contract_module_services ms
    SET is_custom_service_name = (ms.service_name <> sc.service_name)
    FROM contract_service_contracts sc
    WHERE ms.contract_id = sc.id
      AND ms.is_custom_service_name IS DISTINCT FROM (ms.service_name <> sc.service_name);
END $$;`
	if err := DB.Exec(contractConstraintSQL).Error; err != nil {
		return fmt.Errorf("更新多实现服务契约检查约束失败: %w", err)
	}

	partialIndexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_proto_active_name ON contract_proto_schemas (name) WHERE is_active = true;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_active_proto_op ON contract_proto_operations (service_name, operation) WHERE is_active = true;`,
		`CREATE INDEX IF NOT EXISTS idx_contract_proto_schemas_cat_active ON contract_proto_schemas (category, is_active);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_proc_agent_port ON contract_processes (agent_id, effective_listen_port) WHERE effective_listen_port > 0;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_proc_agent_arch ON contract_processes (agent_id, arch_process_id);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_golden_suite_code ON contract_golden_suites (code);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_golden_item_order ON contract_golden_suite_items (suite_id, order_no);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_replay_task_seq ON contract_replay_items (task_id, seq_no);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_topo_layout_shared ON contract_topology_layouts (view) WHERE scope = 'SHARED';`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_topo_layout_private_name ON contract_topology_layouts (owner_id, view, name) WHERE scope = 'PRIVATE';`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_topo_layout_private_default ON contract_topology_layouts (owner_id, view, focus_scope) WHERE scope = 'PRIVATE' AND is_default;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_mod_svc_name_excl ON contract_module_services (service_name) WHERE contract_id IS NULL OR is_custom_service_name = true;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_mod_svc_contract_impl ON contract_module_services (contract_id, impl_key) WHERE contract_id IS NOT NULL;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_mod_svc_contract_mod ON contract_module_services (contract_id, module_id) WHERE contract_id IS NOT NULL;`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_dep_consumer_contract ON contract_module_dependencies (consumer_module_id, contract_id) WHERE contract_id IS NOT NULL;`,
		`CREATE INDEX IF NOT EXISTS idx_contract_pdoc_schema_kind ON contract_proto_doc_symbols (schema_id, symbol_kind);`,
		`CREATE INDEX IF NOT EXISTS idx_contract_pdoc_fts ON contract_proto_doc_symbols USING gin (to_tsvector('simple', coalesce(full_name,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(leading_comments,'') || ' ' || coalesce(trailing_comments,'') || ' ' || coalesce(owner_tag,'')));`,
		`CREATE INDEX IF NOT EXISTS idx_contract_pdoc_fields_gin ON contract_proto_doc_symbols USING gin (fields_meta_json);`,
		`CREATE INDEX IF NOT EXISTS idx_contract_pdoc_err_gin ON contract_proto_doc_symbols USING gin (error_codes_json);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uk_contract_pfile_bundle_path ON contract_proto_files (bundle_id, file_path) WHERE is_deleted = false;`,
		`CREATE INDEX IF NOT EXISTS idx_contract_pfile_active_cat_path ON contract_proto_files (file_path, category) WHERE is_deleted = false;`,
		`DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_provider') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_provider CHECK (provider IN ('GITLAB','GITHUB','GITEA','GENERIC'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_auth_type') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_auth_type CHECK (auth_type IN ('NONE','TOKEN','SSH_KEY'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_repo_sync_mode') THEN
		ALTER TABLE contract_git_repos ADD CONSTRAINT chk_contract_git_repo_sync_mode CHECK (sync_mode IN ('WEBHOOK','POLL','MANUAL'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_bundle_category') THEN
		ALTER TABLE contract_git_proto_bundles ADD CONSTRAINT chk_contract_git_bundle_category CHECK (category IN ('BASE_LIB','BUSINESS','GATEWAY'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_git_bundle_mr_publish') THEN
		ALTER TABLE contract_git_proto_bundles ADD CONSTRAINT chk_contract_git_bundle_mr_publish CHECK (mr_auto_publish IN ('DISABLED','DRAFT_ONLY','AUTO_ACTIVATE'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chk_contract_replay_csg_strategy') THEN
		ALTER TABLE contract_replay_tasks ADD CONSTRAINT chk_contract_replay_csg_strategy CHECK (csg_strategy IN ('PASSTHROUGH','CLIENT_INJECT','PUB_STUB','MOCK_SERVER'));
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_git_bundles_repo') THEN
		ALTER TABLE contract_git_proto_bundles ADD CONSTRAINT fk_contract_git_bundles_repo FOREIGN KEY (git_repo_id) REFERENCES contract_git_repos(id) ON DELETE CASCADE;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_files_repo') THEN
		ALTER TABLE contract_proto_files ADD CONSTRAINT fk_contract_proto_files_repo FOREIGN KEY (git_repo_id) REFERENCES contract_git_repos(id) ON DELETE CASCADE;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_files_bundle') THEN
		ALTER TABLE contract_proto_files ADD CONSTRAINT fk_contract_proto_files_bundle FOREIGN KEY (bundle_id) REFERENCES contract_git_proto_bundles(id) ON DELETE CASCADE;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_file_versions_file') THEN
		ALTER TABLE contract_proto_file_versions ADD CONSTRAINT fk_contract_proto_file_versions_file FOREIGN KEY (proto_file_id) REFERENCES contract_proto_files(id) ON DELETE CASCADE;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_files_latest_ver') THEN
		ALTER TABLE contract_proto_files ADD CONSTRAINT fk_contract_proto_files_latest_ver FOREIGN KEY (latest_version_id) REFERENCES contract_proto_file_versions(id) ON DELETE SET NULL;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_schemas_git_repo') THEN
		ALTER TABLE contract_proto_schemas ADD CONSTRAINT fk_contract_proto_schemas_git_repo FOREIGN KEY (git_repo_id) REFERENCES contract_git_repos(id) ON DELETE SET NULL;
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_schemas_source_bundle') THEN
		ALTER TABLE contract_proto_schemas ADD CONSTRAINT fk_contract_proto_schemas_source_bundle FOREIGN KEY (source_bundle_id) REFERENCES contract_git_proto_bundles(id) ON DELETE SET NULL;
	END IF;
END $$;`,
	}
	for _, idxSQL := range partialIndexes {
		if err := DB.Exec(idxSQL).Error; err != nil {
			return fmt.Errorf("创建部分唯一索引失败: %w", err)
		}
	}

	// 描述集 proto_file_count 与 service_count 幂等统计校准
	backfillSQL := `
UPDATE contract_proto_schemas 
SET proto_file_count = jsonb_array_length(proto_files) 
WHERE proto_file_count = 0 
  AND proto_files IS NOT NULL 
  AND jsonb_typeof(proto_files) = 'array' 
  AND jsonb_array_length(proto_files) > 0;

UPDATE contract_proto_schemas 
SET service_count = sub.cnt
FROM (
    SELECT schema_id, COUNT(DISTINCT service_name) AS cnt
    FROM contract_proto_operations
    GROUP BY schema_id
) sub
WHERE contract_proto_schemas.id = sub.schema_id 
  AND contract_proto_schemas.service_count = 0;
`
	if err := DB.Exec(backfillSQL).Error; err != nil {
		return fmt.Errorf("回填描述集统计计数失败: %w", err)
	}

	// 幂等补齐复合索引 idx_contract_tr_session_req_ns (ADR-12, M13)
	if err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_contract_tr_session_req_ns ON contract_traffic_records (session_id, req_captured_at_ns, id)`).Error; err != nil {
		log.Printf("[models] 提示: 创建复合索引 idx_contract_tr_session_req_ns: %v", err)
	}

	// 4. 显式创建外键约束（由于工作区基础库默认配置 DisableForeignKeyConstraintWhenMigrating，此处补充权威外键以保障数据防腐）
	fkSQL := `
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_fg_subsystem') THEN
        ALTER TABLE contract_functional_groups
        ADD CONSTRAINT fk_contract_fg_subsystem
        FOREIGN KEY (subsystem_id) REFERENCES contract_subsystems(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_fg_parent') THEN
        ALTER TABLE contract_functional_groups
        ADD CONSTRAINT fk_contract_fg_parent
        FOREIGN KEY (parent_id) REFERENCES contract_functional_groups(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_modules_subsystem') THEN
        ALTER TABLE contract_modules
        ADD CONSTRAINT fk_contract_modules_subsystem
        FOREIGN KEY (subsystem_id) REFERENCES contract_subsystems(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_modules_group') THEN
        ALTER TABLE contract_modules
        ADD CONSTRAINT fk_contract_modules_group
        FOREIGN KEY (group_id) REFERENCES contract_functional_groups(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_modules_arch_proc') THEN
        ALTER TABLE contract_modules
        ADD CONSTRAINT fk_contract_modules_arch_proc
        FOREIGN KEY (arch_process_id) REFERENCES contract_arch_processes(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_mod_svc_module') THEN
        ALTER TABLE contract_module_services
        ADD CONSTRAINT fk_contract_mod_svc_module
        FOREIGN KEY (module_id) REFERENCES contract_modules(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_dep_consumer') THEN
        ALTER TABLE contract_module_dependencies
        ADD CONSTRAINT fk_contract_dep_consumer
        FOREIGN KEY (consumer_module_id) REFERENCES contract_modules(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_dep_service') THEN
        ALTER TABLE contract_module_dependencies
        ADD CONSTRAINT fk_contract_dep_service
        FOREIGN KEY (service_id) REFERENCES contract_module_services(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_dep_provider') THEN
        ALTER TABLE contract_module_dependencies
        ADD CONSTRAINT fk_contract_dep_provider
        FOREIGN KEY (provider_module_id) REFERENCES contract_modules(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_arch_proc_group') THEN
        ALTER TABLE contract_arch_processes
        ADD CONSTRAINT fk_contract_arch_proc_group
        FOREIGN KEY (group_id) REFERENCES contract_arch_process_groups(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proc_agent') THEN
        ALTER TABLE contract_processes
        ADD CONSTRAINT fk_contract_proc_agent
        FOREIGN KEY (agent_id) REFERENCES contract_agents(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proc_arch') THEN
        ALTER TABLE contract_processes
        ADD CONSTRAINT fk_contract_proc_arch
        FOREIGN KEY (arch_process_id) REFERENCES contract_arch_processes(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_agents_fallback') THEN
        ALTER TABLE contract_agents
        ADD CONSTRAINT fk_contract_agents_fallback
        FOREIGN KEY (fallback_agent_id) REFERENCES contract_agents(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_proto_op_schema') THEN
        ALTER TABLE contract_proto_operations
        ADD CONSTRAINT fk_contract_proto_op_schema
        FOREIGN KEY (schema_id) REFERENCES contract_proto_schemas(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_pdoc_schema') THEN
        ALTER TABLE contract_proto_doc_symbols
        ADD CONSTRAINT fk_contract_pdoc_schema
        FOREIGN KEY (schema_id) REFERENCES contract_proto_schemas(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_task_session') THEN
        ALTER TABLE contract_replay_tasks
        ADD CONSTRAINT fk_contract_replay_task_session
        FOREIGN KEY (session_id) REFERENCES contract_recording_sessions(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_golden_suites_arch_proc') THEN
        ALTER TABLE contract_golden_suites
        ADD CONSTRAINT fk_contract_golden_suites_arch_proc
        FOREIGN KEY (arch_process_id) REFERENCES contract_arch_processes(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_golden_suite_items_suite') THEN
        ALTER TABLE contract_golden_suite_items
        ADD CONSTRAINT fk_contract_golden_suite_items_suite
        FOREIGN KEY (suite_id) REFERENCES contract_golden_suites(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_task_suite') THEN
        ALTER TABLE contract_replay_tasks
        ADD CONSTRAINT fk_contract_replay_task_suite
        FOREIGN KEY (golden_suite_id) REFERENCES contract_golden_suites(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_item_suite_item') THEN
        ALTER TABLE contract_replay_items
        ADD CONSTRAINT fk_contract_replay_item_suite_item
        FOREIGN KEY (suite_item_id) REFERENCES contract_golden_suite_items(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_task_arch_proc') THEN
        ALTER TABLE contract_replay_tasks
        ADD CONSTRAINT fk_contract_replay_task_arch_proc
        FOREIGN KEY (arch_process_id) REFERENCES contract_arch_processes(id)
        ON UPDATE CASCADE ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_task_src_proc') THEN
        ALTER TABLE contract_replay_tasks
        ADD CONSTRAINT fk_contract_replay_task_src_proc
        FOREIGN KEY (source_process_id) REFERENCES contract_processes(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_task_tgt_proc') THEN
        ALTER TABLE contract_replay_tasks
        ADD CONSTRAINT fk_contract_replay_task_tgt_proc
        FOREIGN KEY (target_process_id) REFERENCES contract_processes(id)
        ON UPDATE CASCADE ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_contract_replay_task_item') THEN
        ALTER TABLE contract_replay_items
        ADD CONSTRAINT fk_contract_replay_task_item
        FOREIGN KEY (task_id) REFERENCES contract_replay_tasks(id)
        ON UPDATE CASCADE ON DELETE CASCADE;
    END IF;
END $$;
`
	if err := DB.Exec(fkSQL).Error; err != nil {
		return fmt.Errorf("创建外键完整性约束失败: %w", err)
	}

	// 5. 确保独立运行模式下至少存在一个默认管理员账号
	if err := commonAuth.EnsureSeedAdmin(DB, config.AdminRole); err != nil {
		log.Printf("[%s] 提示: 初始化默认管理员账号未完成: %v", config.ServiceName, err)
	}

	// 6. 初始化治理参数种子数据
	if err := seedDefaultSettings(DB); err != nil {
		log.Printf("[%s] 提示: 初始化模块配置参数种子未完成: %v", config.ServiceName, err)
	}

	if err := ensureLinterConstraints(DB); err != nil {
		return fmt.Errorf("初始化静态检查约束失败: %w", err)
	}
	if err := ensureM26MRGateAndNotificationConstraints(DB); err != nil {
		return fmt.Errorf("初始化 M26 MR 门禁与通知约束失败: %w", err)
	}
	if err := ensureM30aDiscoveryConstraints(DB); err != nil {
		return fmt.Errorf("初始化 M30a 现网发现快照约束失败: %w", err)
	}
	if err := ensureM30bSchemaAndConstraints(DB); err != nil {
		return fmt.Errorf("初始化 M30b 物理/逻辑双态约束失败: %w", err)
	}
	if err := ensureADR26SchemaAndBackfill(DB); err != nil {
		return fmt.Errorf("初始化 ADR-26 跨仓流水线约束与存量回填失败: %w", err)
	}
	if err := pm.EnsureArchProcessPartitionIndexes(DB); err != nil {
		return fmt.Errorf("初始化 M30b 流量分区物理归因索引失败: %w", err)
	}
	if err := EnsureBuiltinCheckRules(DB); err != nil {
		return fmt.Errorf("初始化内置静态检查规则失败: %w", err)
	}
	if err := EnsureBuiltinVocabularyTerms(DB); err != nil {
		return fmt.Errorf("初始化基础标准词库失败: %w", err)
	}
	if err := ensureM24DefaultSettings(DB); err != nil {
		log.Printf("[%s] 提示: 初始化 M24 安全/LLM 默认参数未完成: %v", config.ServiceName, err)
	}
	if err := ensureM30aDefaultSettings(DB); err != nil {
		log.Printf("[%s] 提示: 初始化 M30a 现网发现默认参数未完成: %v", config.ServiceName, err)
	}
	if err := ensureM30bDefaultSettings(DB); err != nil {
		log.Printf("[%s] 提示: 初始化 M30b 物理/逻辑解耦默认参数未完成: %v", config.ServiceName, err)
	}

	return nil
}

// EnsureDefaultSettings 幂等恢复模块运行参数与 M24/M30a/M30b 默认配置项
func EnsureDefaultSettings(db *gorm.DB) error {
	if err := seedDefaultSettings(db); err != nil {
		return fmt.Errorf("初始化默认运行参数失败: %w", err)
	}
	if err := ensureM24DefaultSettings(db); err != nil {
		return fmt.Errorf("初始化 M24 安全/LLM 默认参数失败: %w", err)
	}
	if err := ensureM30aDefaultSettings(db); err != nil {
		return fmt.Errorf("初始化 M30a 现网发现默认参数未完成: %w", err)
	}
	if err := ensureM30bDefaultSettings(db); err != nil {
		return fmt.Errorf("初始化 M30b 物理/逻辑解耦默认参数未完成: %w", err)
	}
	return nil
}

func seedDefaultSettings(db *gorm.DB) error {
	var settingCount int64
	if err := db.Model(&ModuleSetting{}).Count(&settingCount).Error; err != nil {
		return err
	}
	if settingCount == 0 {
		defaultSettings := []ModuleSetting{
			{
				SettingKey:  "traffic_retention_days",
				SettingVal:  "30",
				Description: "流量往返记录物理分区保留天数（超出自动执行 DROP TABLE 销毁）",
				UpdatedBy:   "system",
			},
			{
				SettingKey:  "default_max_frames",
				SettingVal:  "100000",
				Description: "录制任务默认单批次最大捕获帧数上限",
				UpdatedBy:   "system",
			},
			{
				SettingKey:  "default_max_duration_sec",
				SettingVal:  "1800",
				Description: "录制任务默认单批次最大录制时长（秒）",
				UpdatedBy:   "system",
			},
			{
				SettingKey:  "default_max_frame_bytes",
				SettingVal:  "4194304",
				Description: "单帧报文截断排空阈值（字节，默认 4MB）",
				UpdatedBy:   "system",
			},
			{
				SettingKey:  "default_probe_desired_state_shared",
				SettingVal:  "ARMED",
				Description: "公共环境 Agent 挂载进程默认预挂探针期望状态 (ARMED/DISARMED)",
				UpdatedBy:   "system",
			},
			{
				SettingKey:  "default_probe_desired_state_private",
				SettingVal:  "ARMED",
				Description: "个人环境 Agent 挂载进程默认预挂探针期望状态 (ARMED/DISARMED)",
				UpdatedBy:   "system",
			},
		}
		if err := db.Create(&defaultSettings).Error; err != nil {
			return err
		}
	}
	return nil
}
