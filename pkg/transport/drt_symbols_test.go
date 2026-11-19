package transport_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"code-contract/pkg/transport"
)

func TestFindMappedLibraryPath_MatchesVersionedAndPrivateCopy(t *testing.T) {
	tempDir := t.TempDir()
	pid := uint32(9999)
	mapsDir := filepath.Join(tempDir, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(mapsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	mapsContent := `
55a6d3600000-55a6d3620000 r--p 00000000 08:01 1001 /usr/bin/my_service
55a6d3620000-55a6d3650000 r-xp 00020000 08:01 1001 /usr/bin/my_service
7f3b89000000-7f3b89100000 r--p 00000000 08:01 2001 /usr/lib/libc.so.6
7f3b89100000-7f3b89280000 r-xp 00100000 08:01 2001 /usr/lib/libc.so.6
7f3b89300000-7f3b89350000 rw-p 00000000 08:01 3001 /opt/dopra/lib/libdopra.so.2.1
7f3b89350000-7f3b89400000 r-xp 00050000 08:01 3001 /opt/dopra/lib/libdopra.so.2.1
`
	if err := os.WriteFile(filepath.Join(mapsDir, "maps"), []byte(mapsContent), 0644); err != nil {
		t.Fatalf("write maps failed: %v", err)
	}

	path, err := transport.FindMappedLibraryPath(tempDir, pid, "libdopra.so")
	if err != nil {
		t.Fatalf("FindMappedLibraryPath failed: %v", err)
	}
	expected := "/opt/dopra/lib/libdopra.so.2.1"
	if path != expected {
		t.Errorf("expected path %s, got %s", expected, path)
	}

	// Non-existent PID
	_, err = transport.FindMappedLibraryPath(tempDir, 1111, "libdopra.so")
	if err == nil {
		t.Fatalf("expected error for non-existent PID")
	}

	// Missing library in maps
	missingMapsDir := filepath.Join(tempDir, "2222")
	_ = os.MkdirAll(missingMapsDir, 0755)
	_ = os.WriteFile(filepath.Join(missingMapsDir, "maps"), []byte("55a6d3620000-55a6d3650000 r-xp 00000000 08:01 1 /bin/bash\n"), 0644)
	_, err = transport.FindMappedLibraryPath(tempDir, 2222, "libdopra.so")
	if !errors.Is(err, transport.ErrLibraryNotFound) {
		t.Errorf("expected ErrLibraryNotFound, got %v", err)
	}
}

func TestResolveDRTSymbolOffsets_DynamicELFFixture(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not available on host, skipping fixture build test")
	}

	tempDir := t.TempDir()
	cSource := filepath.Join(tempDir, "drt_fixture.c")
	soPath := filepath.Join(tempDir, "libdopra_test.so")

	cCode := `
__attribute__((visibility("default"))) int DRT_WriteBulk(void* handle, int count, void* entries) {
    return 42;
}
__attribute__((visibility("default"))) int DRT_ReadLen(void* handle, int len) {
    return 10;
}
`
	if err := os.WriteFile(cSource, []byte(cCode), 0644); err != nil {
		t.Fatalf("write c source failed: %v", err)
	}

	// Build shared library
	buildCmd := exec.Command(clang, "-shared", "-fPIC", "-O2", "-o", soPath, cSource)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("clang build shared library failed: %v, out: %s", err, string(out))
	}

	// Strip static symbols if llvm-strip available
	if strip, sErr := exec.LookPath("llvm-strip"); sErr == nil {
		_ = exec.Command(strip, "--strip-all", soPath).Run()
	}

	offsets, err := transport.ResolveDRTSymbolOffsets(soPath)
	if err != nil {
		t.Fatalf("ResolveDRTSymbolOffsets failed on fixture .so: %v", err)
	}

	if offsets.WriteBulkOffset == 0 {
		t.Errorf("expected non-zero WriteBulkOffset")
	}
	if offsets.ReadLenOffset == 0 {
		t.Errorf("expected non-zero ReadLenOffset")
	}
	t.Logf("Fixture offsets: DRT_WriteBulk=0x%x, DRT_ReadLen=0x%x", offsets.WriteBulkOffset, offsets.ReadLenOffset)
}
