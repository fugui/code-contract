package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageAndProtoCollectConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
server:
  port: ":8091"
storage:
  root: "/var/lib/code-contract"
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if AppConfig.Storage.SharedWriteEnabled {
		t.Fatalf("shared_write_enabled 默认必须为 false")
	}
	if got := AppConfig.Storage.GetCodesDir(); got != "/var/lib/code-contract/codes" {
		t.Fatalf("GetCodesDir unexpected: %q", got)
	}
	if got := AppConfig.Storage.GetProtosDir(); got != "/var/lib/code-contract/protos" {
		t.Fatalf("GetProtosDir unexpected: %q", got)
	}
	if AppConfig.ProtoCollect.MaxFileBytes != 2*1024*1024 {
		t.Fatalf("MaxFileBytes unexpected: %d", AppConfig.ProtoCollect.MaxFileBytes)
	}
	if AppConfig.ProtoCollect.MaxFilesPerRepo != 5000 {
		t.Fatalf("MaxFilesPerRepo unexpected: %d", AppConfig.ProtoCollect.MaxFilesPerRepo)
	}
	if AppConfig.ProtoCollect.SyncConcurrency != 4 {
		t.Fatalf("SyncConcurrency unexpected: %d", AppConfig.ProtoCollect.SyncConcurrency)
	}
	if AppConfig.ProtoCollect.StaleThresholdDuration() != 24*time.Hour {
		t.Fatalf("StaleThresholdDuration unexpected: %v", AppConfig.ProtoCollect.StaleThresholdDuration())
	}
	if len(AppConfig.ProtoCollect.CollectGlobs) != 2 ||
		AppConfig.ProtoCollect.CollectGlobs[0] != "pg/**/*.proto" ||
		AppConfig.ProtoCollect.CollectGlobs[1] != "*/pg/**/*.proto" {
		t.Fatalf("CollectGlobs unexpected: %v", AppConfig.ProtoCollect.CollectGlobs)
	}

	// 验证显式 codes_dir / protos_dir 覆盖
	st := StorageSection{
		Root:      "/var/lib/code-contract",
		CodesDir:  "/srv/shared/codes",
		ProtosDir: "/srv/shared/protos",
	}
	if st.GetCodesDir() != "/srv/shared/codes" || st.GetProtosDir() != "/srv/shared/protos" {
		t.Fatalf("显式覆盖目录未生效: %+v", st)
	}
}
