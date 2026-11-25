package models

import (
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
)

var partitionNameRegex = regexp.MustCompile(`^contract_traffic_records_y(\d{4})m(\d{2})d(\d{2})$`)

// PartitionManager 负责 contract_traffic_records 物理分区母表与按日分区的自动生命周期管理
type PartitionManager struct{}

// EnsureParentTable 幂等创建物理分区母表及自增序列
func (pm *PartitionManager) EnsureParentTable(db *gorm.DB) error {
	ddl := `
CREATE SEQUENCE IF NOT EXISTS contract_traffic_records_id_seq;

CREATE TABLE IF NOT EXISTS contract_traffic_records (
    id BIGINT NOT NULL DEFAULT nextval('contract_traffic_records_id_seq'),
    created_at TIMESTAMPTZ NOT NULL,
    session_id BIGINT NOT NULL,
    seq_no BIGINT NOT NULL,
    agent_id BIGINT NOT NULL,
    process_id BIGINT NOT NULL,
    captured_pid INTEGER NOT NULL,
    conn_key VARCHAR(128) NOT NULL,
    direction VARCHAR(16) NOT NULL,
    exchange_type VARCHAR(24) NOT NULL DEFAULT 'UNARY',
    src_addr VARCHAR(64) NOT NULL,
    dst_addr VARCHAR(64) NOT NULL,
    req_captured_at_ns BIGINT NOT NULL,
    rsp_captured_at_ns BIGINT,
    latency_us BIGINT,
    transport_proto SMALLINT NOT NULL DEFAULT 1,
    sno NUMERIC(20,0) NOT NULL,
    req_type SMALLINT NOT NULL DEFAULT 0,
    rsp_seg_count INTEGER NOT NULL DEFAULT 0,
    is_packed SMALLINT NOT NULL DEFAULT 1,
    service_name VARCHAR(128) NOT NULL DEFAULT '',
    operation VARCHAR(128) NOT NULL DEFAULT '',
    status_code NUMERIC(20,0) NOT NULL DEFAULT 0,
    trace_id VARCHAR(32) NOT NULL DEFAULT '',
    span_id VARCHAR(32) NOT NULL DEFAULT '',
    father_span_id VARCHAR(32) NOT NULL DEFAULT '',
    subsystem_id BIGINT,
    src_arch_process_id BIGINT,
    dst_arch_process_id BIGINT,
    src_module_id BIGINT,
    src_attribution_mode VARCHAR(32) NOT NULL DEFAULT '',
    src_candidate_module_ids JSONB,
    dst_module_id BIGINT,
    dst_contract_id BIGINT,
    headers_json JSONB NOT NULL DEFAULT '{}',
    schema_id BIGINT,
    schema_version INTEGER,
    decode_status VARCHAR(32) NOT NULL DEFAULT '',
    payload_json JSONB NOT NULL DEFAULT '{}',
    req_payload_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    raw_req_packet BYTEA,
    raw_rsp_packet BYTEA,
    is_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    is_replayable BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS idx_contract_tr_payload_gin ON contract_traffic_records USING GIN (payload_json jsonb_path_ops);
CREATE INDEX IF NOT EXISTS idx_contract_tr_session_seq ON contract_traffic_records (session_id, seq_no);
CREATE INDEX IF NOT EXISTS idx_contract_tr_session_svc ON contract_traffic_records (session_id, service_name, operation);
CREATE INDEX IF NOT EXISTS idx_contract_tr_conn_sno ON contract_traffic_records (conn_key, sno);
CREATE INDEX IF NOT EXISTS idx_contract_tr_session_req_ns ON contract_traffic_records (session_id, req_captured_at_ns, id);
ALTER TABLE contract_traffic_records ADD COLUMN IF NOT EXISTS dst_contract_id BIGINT;
ALTER TABLE contract_traffic_records ADD COLUMN IF NOT EXISTS src_arch_process_id BIGINT;
ALTER TABLE contract_traffic_records ADD COLUMN IF NOT EXISTS dst_arch_process_id BIGINT;
CREATE INDEX IF NOT EXISTS idx_contract_tr_dst_contract ON contract_traffic_records (dst_contract_id) WHERE dst_contract_id IS NOT NULL;
`
	return db.Exec(ddl).Error
}

// EnsureArchProcessPartitionIndexes 幂等在母表建立 ON ONLY 索引并逐分区并发挂接物理进程归因索引 (M30b, ADR-24 §4.6)
func (pm *PartitionManager) EnsureArchProcessPartitionIndexes(db *gorm.DB) error {
	parentDDL := `
CREATE INDEX IF NOT EXISTS idx_contract_tr_src_arch_proc ON ONLY contract_traffic_records (src_arch_process_id);
CREATE INDEX IF NOT EXISTS idx_contract_tr_dst_arch_proc ON ONLY contract_traffic_records (dst_arch_process_id);
`
	if err := db.Exec(parentDDL).Error; err != nil {
		return fmt.Errorf("创建母表物理进程归因索引失败: %w", err)
	}

	var partitionNames []string
	if err := db.Raw(`
SELECT c.relname
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = 'contract_traffic_records'
ORDER BY c.relname ASC
`).Scan(&partitionNames).Error; err != nil {
		return fmt.Errorf("查询流量分区子表失败: %w", err)
	}

	type archColIdx struct {
		parentIdx string
		col       string
		suffix    string
	}
	specs := []archColIdx{
		{parentIdx: "idx_contract_tr_src_arch_proc", col: "src_arch_process_id", suffix: "src_arch_proc"},
		{parentIdx: "idx_contract_tr_dst_arch_proc", col: "dst_arch_process_id", suffix: "dst_arch_proc"},
	}

	for _, partName := range partitionNames {
		if !partitionNameRegex.MatchString(partName) {
			continue
		}
		for _, sp := range specs {
			partIdxName := fmt.Sprintf("idx_%s_%s", partName, sp.suffix)
			var attached int64
			if err := db.Raw(`
SELECT COUNT(*)
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = ? AND c.relname = ?
`, sp.parentIdx, partIdxName).Scan(&attached).Error; err != nil {
				return err
			}
			if attached > 0 {
				continue
			}

			createSQL := fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s (%s)", partIdxName, partName, sp.col)
			if err := db.Exec(createSQL).Error; err != nil {
				// 若调用方处于事务内导致 CONCURRENTLY 不可用，则退化为普通索引创建以保证测试/迁移健壮性
				fallbackSQL := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", partIdxName, partName, sp.col)
				if fbErr := db.Exec(fallbackSQL).Error; fbErr != nil {
					return fmt.Errorf("创建分区索引 [%s] 失败: %w", partIdxName, fbErr)
				}
			}
			attachSQL := fmt.Sprintf("ALTER INDEX %s ATTACH PARTITION %s", sp.parentIdx, partIdxName)
			if err := db.Exec(attachSQL).Error; err != nil {
				return fmt.Errorf("挂接分区索引 [%s -> %s] 失败: %w", partIdxName, sp.parentIdx, err)
			}
		}
	}
	return nil
}

// FormatPartitionName 计算按日分区表名
func (pm *PartitionManager) FormatPartitionName(date time.Time) string {
	d := date.UTC()
	return fmt.Sprintf("contract_traffic_records_y%04dm%02dd%02d", d.Year(), d.Month(), d.Day())
}

// EnsureDailyPartition 幂等预建单日物理子分区及其局部 GIN 索引
func (pm *PartitionManager) EnsureDailyPartition(db *gorm.DB, date time.Time) (string, error) {
	d := date.UTC()
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)

	tableName := pm.FormatPartitionName(from)
	fromStr := from.Format("2006-01-02 15:04:05+00")
	toStr := to.Format("2006-01-02 15:04:05+00")

	createPartitionSQL := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s PARTITION OF contract_traffic_records
FOR VALUES FROM ('%s') TO ('%s');

CREATE INDEX IF NOT EXISTS idx_%s_payload_gin ON %s USING GIN (payload_json jsonb_path_ops);
`, tableName, fromStr, toStr, tableName, tableName)

	if err := db.Exec(createPartitionSQL).Error; err != nil {
		return "", fmt.Errorf("创建子分区表 [%s] 失败: %w", tableName, err)
	}

	return tableName, nil
}

// EnsurePartitionsRange 批量预建时间范围内的日常子分区
func (pm *PartitionManager) EnsurePartitionsRange(db *gorm.DB, from, to time.Time) ([]string, error) {
	var created []string
	curr := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)

	for !curr.After(end) {
		name, err := pm.EnsureDailyPartition(db, curr)
		if err != nil {
			return created, err
		}
		created = append(created, name)
		curr = curr.AddDate(0, 0, 1)
	}
	return created, nil
}

// PurgeExpiredPartitions 通过轻量级 DROP TABLE 物理清理超过保留期 (retentionDays) 的历史子分区表
func (pm *PartitionManager) PurgeExpiredPartitions(db *gorm.DB, retentionDays int, now time.Time) ([]string, error) {
	cutoffDate := now.UTC().AddDate(0, 0, -retentionDays)
	cutoffDay := time.Date(cutoffDate.Year(), cutoffDate.Month(), cutoffDate.Day(), 0, 0, 0, 0, time.UTC)

	query := `
SELECT c.relname
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class p ON p.oid = i.inhparent
WHERE p.relname = 'contract_traffic_records';
`
	var partitionNames []string
	if err := db.Raw(query).Scan(&partitionNames).Error; err != nil {
		return nil, fmt.Errorf("查询分区子表列表失败: %w", err)
	}

	var dropped []string
	for _, name := range partitionNames {
		matches := partitionNameRegex.FindStringSubmatch(name)
		if len(matches) != 4 {
			continue
		}

		var year, month, day int
		_, _ = fmt.Sscanf(matches[1], "%d", &year)
		_, _ = fmt.Sscanf(matches[2], "%d", &month)
		_, _ = fmt.Sscanf(matches[3], "%d", &day)
		partDay := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)

		// 若分区归属日期严格早于安全清理阈值日
		if partDay.Before(cutoffDay) {
			dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS %s;", name)
			if err := db.Exec(dropSQL).Error; err != nil {
				return dropped, fmt.Errorf("DROP 分区表 [%s] 失败: %w", name, err)
			}
			dropped = append(dropped, name)
		}
	}

	return dropped, nil
}
