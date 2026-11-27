package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtoPipelineConfigDefaultsAndOverride(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
server:
  port: ":8091"
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if AppConfig.ProtoPipeline.PublishMode != "AUTO" ||
		AppConfig.ProtoPipeline.CompileConcurrency != 4 ||
		AppConfig.ProtoPipeline.FanoutWarnThreshold != 100 ||
		AppConfig.ProtoPipeline.MRMergeDebounceSec != 300 ||
		AppConfig.ProtoPipeline.ArchiveReminderDays != 30 {
		t.Fatalf("ProtoPipeline defaults mismatch: %+v", AppConfig.ProtoPipeline)
	}

	cfgPreview := filepath.Join(tmpDir, "preview.yaml")
	if err := os.WriteFile(cfgPreview, []byte(`
proto_pipeline:
  publish_mode: "preview"
  compile_concurrency: 8
  fanout_warn_threshold: 50
  mr_merge_debounce_sec: 120
  archive_reminder_days: 15
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(cfgPreview); err != nil {
		t.Fatalf("LoadConfig preview failed: %v", err)
	}
	if AppConfig.ProtoPipeline.PublishMode != "PREVIEW" ||
		AppConfig.ProtoPipeline.CompileConcurrency != 8 ||
		AppConfig.ProtoPipeline.FanoutWarnThreshold != 50 ||
		AppConfig.ProtoPipeline.MRMergeDebounceSec != 120 ||
		AppConfig.ProtoPipeline.ArchiveReminderDays != 15 {
		t.Fatalf("ProtoPipeline override mismatch: %+v", AppConfig.ProtoPipeline)
	}
}
