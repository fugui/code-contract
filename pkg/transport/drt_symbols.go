package transport

import (
	"bufio"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrLibraryNotFound 未找到目标共享库映射
	ErrLibraryNotFound = errors.New("transport: mapped library not found in proc maps")
	// ErrSymbolNotFound 必需符号在共享库符号表中未找到
	ErrSymbolNotFound = errors.New("transport: required symbol not found in library symbols")
)

// DRTSymbolOffsets 记录 libdopra.so 内部 DRT 函数在 ELF 文件中的精确文件偏移
type DRTSymbolOffsets struct {
	WriteBulkOffset      uint64 `json:"write_bulk_offset"`
	ReadLenOffset        uint64 `json:"read_len_offset,omitempty"`
	ReadNoCheckLenOffset uint64 `json:"read_nocheck_len_offset,omitempty"`
}

// FindMappedLibraryPath 扫描 /proc/<pid>/maps 定位目标进程映射的可执行 libdopra.so 物理路径
func FindMappedLibraryPath(procRoot string, pid uint32, libPrefix string) (string, error) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	if libPrefix == "" {
		libPrefix = "libdopra.so"
	}

	mapsPath := filepath.Join(procRoot, fmt.Sprintf("%d", pid), "maps")
	file, err := os.Open(mapsPath)
	if err != nil {
		return "", fmt.Errorf("open %s failed: %w", mapsPath, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// 格式: address perms offset dev inode pathname
		// e.g.: 7f3b89000000-7f3b89200000 r-xp 00000000 08:01 123456 /opt/dopra/lib/libdopra.so
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		perms := fields[1]
		pathname := fields[5]

		// 必须是可执行段 (r-xp)
		if !strings.Contains(perms, "x") {
			continue
		}

		base := filepath.Base(pathname)
		if base == libPrefix || strings.HasPrefix(base, libPrefix+".") || strings.HasPrefix(base, libPrefix) {
			return pathname, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan %s failed: %w", mapsPath, err)
	}

	return "", fmt.Errorf("%w: %s for PID %d", ErrLibraryNotFound, libPrefix, pid)
}

// ResolveDRTSymbolOffsets 使用纯 Go debug/elf 解析 .so 的 .dynsym（及回退 .symtab）换算精确文件偏移
func ResolveDRTSymbolOffsets(soPath string) (*DRTSymbolOffsets, error) {
	f, err := elf.Open(soPath)
	if err != nil {
		return nil, fmt.Errorf("open elf %s failed: %w", soPath, err)
	}
	defer f.Close()

	// 优先读取 .dynsym 动态符号表（strip 后通常依然完整保留）
	symbols, err := f.DynamicSymbols()
	if err != nil || len(symbols) == 0 {
		symbols, _ = f.Symbols()
	}

	if len(symbols) == 0 {
		return nil, fmt.Errorf("%w: dynamic and static symbol tables are empty in %s", ErrSymbolNotFound, soPath)
	}

	symMap := make(map[string]elf.Symbol, len(symbols))
	for _, s := range symbols {
		if s.Name != "" {
			symMap[s.Name] = s
		}
	}

	// 辅助函数：根据虚拟地址 Vaddr 换算 ELF 文件的物理偏移
	vaddrToFileOffset := func(vaddr uint64) uint64 {
		for _, prog := range f.Progs {
			if prog.Type == elf.PT_LOAD && (prog.Flags&elf.PF_X) != 0 {
				if vaddr >= prog.Vaddr && vaddr < prog.Vaddr+prog.Filesz {
					return vaddr - prog.Vaddr + prog.Off
				}
			}
		}
		// 若未找到执行段，降级遍历所有 PT_LOAD
		for _, prog := range f.Progs {
			if prog.Type == elf.PT_LOAD {
				if vaddr >= prog.Vaddr && vaddr < prog.Vaddr+prog.Filesz {
					return vaddr - prog.Vaddr + prog.Off
				}
			}
		}
		return vaddr
	}

	wbSym, ok := symMap["DRT_WriteBulk"]
	if !ok || wbSym.Value == 0 {
		return nil, fmt.Errorf("%w: DRT_WriteBulk in %s", ErrSymbolNotFound, soPath)
	}

	offsets := &DRTSymbolOffsets{
		WriteBulkOffset: vaddrToFileOffset(wbSym.Value),
	}

	if rlSym, ok := symMap["DRT_ReadLen"]; ok && rlSym.Value != 0 {
		offsets.ReadLenOffset = vaddrToFileOffset(rlSym.Value)
	}
	if rncSym, ok := symMap["DRT_ReadNoCheckLen"]; ok && rncSym.Value != 0 {
		offsets.ReadNoCheckLenOffset = vaddrToFileOffset(rncSym.Value)
	}

	return offsets, nil
}
