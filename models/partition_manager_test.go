package models

import (
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
)

func TestTrafficRecord_ArchProcessPartitionIndexesAttachedConcurrently(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	pm := &PartitionManager{}
	day1 := time.Now().UTC()
	day2 := day1.AddDate(0, 0, 1)

	part1, err := pm.EnsureDailyPartition(db, day1)
	if err != nil {
		t.Fatalf("EnsureDailyPartition day1 failed: %v", err)
	}
	part2, err := pm.EnsureDailyPartition(db, day2)
	if err != nil {
		t.Fatalf("EnsureDailyPartition day2 failed: %v", err)
	}

	// 验证连续两次调用幂等且全量分区索引均存在、有效且已挂接母表索引
	if err := pm.EnsureArchProcessPartitionIndexes(db); err != nil {
		t.Fatalf("first EnsureArchProcessPartitionIndexes failed: %v", err)
	}
	if err := pm.EnsureArchProcessPartitionIndexes(db); err != nil {
		t.Fatalf("second EnsureArchProcessPartitionIndexes failed: %v", err)
	}

	parentIndexes := []string{"idx_contract_tr_src_arch_proc", "idx_contract_tr_dst_arch_proc"}
	for _, parentIdx := range parentIndexes {
		var parentExists int64
		if err := db.Raw(`
SELECT COUNT(*)
FROM pg_class c
JOIN pg_index i ON i.indexrelid = c.oid
WHERE c.relname = ? AND i.indisvalid
`, parentIdx).Scan(&parentExists).Error; err != nil {
			t.Fatalf("query parent index %s failed: %v", parentIdx, err)
		}
		if parentExists != 1 {
			t.Fatalf("expected parent index %s to exist and be valid, count=%d", parentIdx, parentExists)
		}

		for _, partName := range []string{part1, part2} {
			var attachedValid int64
			if err := db.Raw(`
SELECT COUNT(*)
FROM pg_inherits inh
JOIN pg_class p ON p.oid = inh.inhparent
JOIN pg_class c ON c.oid = inh.inhrelid
JOIN pg_index ci ON ci.indexrelid = c.oid
JOIN pg_class tbl ON tbl.oid = ci.indrelid
WHERE p.relname = ? AND tbl.relname = ? AND ci.indisvalid
`, parentIdx, partName).Scan(&attachedValid).Error; err != nil {
				t.Fatalf("query attached partition index (%s on %s) failed: %v", parentIdx, partName, err)
			}
			if attachedValid != 1 {
				t.Fatalf("expected valid attached index for parent=%s on partition=%s, count=%d", parentIdx, partName, attachedValid)
			}
		}
	}
}
