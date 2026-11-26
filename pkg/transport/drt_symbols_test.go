package transport_test

import (
	"debug/elf"
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

	// 验证偏移确实落在可执行 PT_LOAD 段文件区间内，且未受外部符号或非函数符号污染
	ef, err := elf.Open(soPath)
	if err != nil {
		t.Fatalf("elf.Open failed: %v", err)
	}
	defer ef.Close()

	inExecSegment := false
	for _, prog := range ef.Progs {
		if prog.Type == elf.PT_LOAD && (prog.Flags&elf.PF_X) != 0 {
			if offsets.WriteBulkOffset >= prog.Off && offsets.WriteBulkOffset < prog.Off+prog.Filesz {
				inExecSegment = true
				break
			}
		}
	}
	if !inExecSegment {
		t.Errorf("expected WriteBulkOffset=0x%x to fall within an executable PT_LOAD file range", offsets.WriteBulkOffset)
	}
}

func TestResolveDRTSymbolOffsets_IgnoresUndefinedDynamicRefsAndUsesSymtab(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not available on host, skipping fixture build test")
	}

	tempDir := t.TempDir()
	cSource := filepath.Join(tempDir, "drt_symtab_fixture.c")
	soPath := filepath.Join(tempDir, "libdopra_symtab.so")

	// DRT_WriteBulk 为隐藏符号（仅在 .symtab 中，不在 .dynsym 中）；同时声明一个外部未定义函数引用
	cCode := `
extern int external_helper(int x);
__attribute__((visibility("hidden"))) int DRT_WriteBulk(void* handle, int count, void* entries) {
    return external_helper(count);
}
__attribute__((visibility("default"))) int exported_entry(void* handle, int count, void* entries) {
    return DRT_WriteBulk(handle, count, entries);
}
`
	if err := os.WriteFile(cSource, []byte(cCode), 0644); err != nil {
		t.Fatalf("write c source failed: %v", err)
	}
	if out, err := exec.Command(clang, "-shared", "-fPIC", "-O2", "-o", soPath, cSource).CombinedOutput(); err != nil {
		t.Fatalf("clang build shared library failed: %v, out: %s", err, string(out))
	}

	offsets, err := transport.ResolveDRTSymbolOffsets(soPath)
	if err != nil {
		t.Fatalf("ResolveDRTSymbolOffsets failed on hidden symtab fixture: %v", err)
	}
	if offsets.WriteBulkOffset == 0 {
		t.Fatalf("expected non-zero WriteBulkOffset from .symtab fallback/merge")
	}
}
