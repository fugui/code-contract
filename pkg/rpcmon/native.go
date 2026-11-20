package rpcmon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var validCSGAddrPattern = regexp.MustCompile(`^ipv4:([0-9]{1,3}\.){3}[0-9]{1,3}:[0-9]{1,5}$`)

// NativeConfig configures execution of the external read-only DOPRA probe binary.
// Command invocations use direct exec.CommandContext without any shell expansion.
type NativeConfig struct {
	ProbeBin    string
	CSGAddr     string
	AllowedDirs []string
	Timeout     time.Duration
}

type nativePayload struct {
	PortInfo  []PortUsage   `json:"portInfo"`
	Servers   []ServerEntry `json:"servers"`
	Clients   []ClientEntry `json:"clients"`
	ServerNum uint32        `json:"serverNum"`
	ClientNum uint32        `json:"clientNum"`
	Timestamp int64         `json:"timestamp"`
}

type nativeBackend struct {
	cfg  NativeConfig
	mu   sync.Mutex
	data *nativePayload
}

// NewNative constructs a Native probe Backend with strict path and argument validation.
func NewNative(cfg NativeConfig) Backend {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &nativeBackend{cfg: cfg}
}

// ValidateNativeConfig verifies probe binary safety (regular non-symlink executable within AllowedDirs if set)
// and validates CSGAddr strictly matches ipv4:<ip>:<port>.
func ValidateNativeConfig(cfg NativeConfig) (string, error) {
	probePath := strings.TrimSpace(cfg.ProbeBin)
	if probePath == "" {
		return "", fmt.Errorf("rpcmon: probe binary path is empty")
	}
	if strings.ContainsRune(probePath, '\x00') || strings.Contains(probePath, "..") {
		return "", fmt.Errorf("rpcmon: invalid probe path %q", probePath)
	}
	cleanPath := filepath.Clean(probePath)
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("rpcmon: resolve probe path %q: %w", cleanPath, err)
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return "", fmt.Errorf("rpcmon: lstat probe binary %q: %w", absPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("rpcmon: symlink probe binary %q is forbidden", absPath)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("rpcmon: probe binary %q must be a regular executable file", absPath)
	}
	if len(cfg.AllowedDirs) > 0 {
		allowed := false
		for _, dir := range cfg.AllowedDirs {
			absDir, err := filepath.Abs(filepath.Clean(dir))
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(absDir, absPath)
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("rpcmon: probe binary %q is outside allowlisted directories", absPath)
		}
	}

	if cfg.CSGAddr != "" {
		if err := ValidateCSGArg(cfg.CSGAddr); err != nil {
			return "", err
		}
	}
	return absPath, nil
}

// DefaultProbeBinaryName is the fixed filename of the read-only DOPRA probe executable.
const DefaultProbeBinaryName = "rpcmon_probe"

// DefaultProbeSearchDirs returns the standard controlled directories searched when
// auto-discovering a prebuilt read-only rpcmon_probe binary.
func DefaultProbeSearchDirs() []string {
	dirs := make([]string, 0, 4)
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		if dir := filepath.Dir(exePath); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	dirs = append(dirs, "/opt/code-contract/bin", "/usr/local/bin")
	return dirs
}

// AutoDiscoverProbeBin searches controlled directories for a valid, non-symlink executable
// named "rpcmon_probe". When allowedDirs is non-empty, discovery is strictly confined to
// allowedDirs; otherwise extraSearchDirs (or DefaultProbeSearchDirs when omitted) are scanned.
func AutoDiscoverProbeBin(allowedDirs []string, extraSearchDirs ...string) string {
	var searchDirs []string
	if len(allowedDirs) > 0 {
		searchDirs = append(searchDirs, allowedDirs...)
	} else if len(extraSearchDirs) > 0 {
		searchDirs = append(searchDirs, extraSearchDirs...)
	} else {
		searchDirs = DefaultProbeSearchDirs()
	}

	seen := make(map[string]struct{}, len(searchDirs))
	for _, dir := range searchDirs {
		trimmed := strings.TrimSpace(dir)
		if trimmed == "" {
			continue
		}
		absDir, err := filepath.Abs(filepath.Clean(trimmed))
		if err != nil {
			continue
		}
		if _, dup := seen[absDir]; dup {
			continue
		}
		seen[absDir] = struct{}{}

		candidate := filepath.Join(absDir, DefaultProbeBinaryName)
		if validated, err := ValidateNativeConfig(NativeConfig{
			ProbeBin:    candidate,
			AllowedDirs: allowedDirs,
		}); err == nil {
			return validated
		}
	}
	return ""
}

// ValidateCSGArg strictly validates a CSG address passed to rpcmon_probe as ipv4:<ip>:<port>.
func ValidateCSGArg(csgAddr string) error {
	if !validCSGAddrPattern.MatchString(csgAddr) {
		return fmt.Errorf("rpcmon: invalid CSG address %q (want ipv4:<ip>:<port>)", csgAddr)
	}
	trimmed := strings.TrimPrefix(csgAddr, "ipv4:")
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return fmt.Errorf("rpcmon: invalid CSG host:port %q: %w", csgAddr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("rpcmon: invalid IPv4 address in %q", csgAddr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("rpcmon: invalid TCP port in %q", csgAddr)
	}
	return nil
}

func (b *nativeBackend) ensure(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data != nil {
		return nil
	}

	probePath, err := ValidateNativeConfig(b.cfg)
	if err != nil {
		return err
	}

	cctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()

	var args []string
	if b.cfg.CSGAddr != "" {
		args = append(args, b.cfg.CSGAddr)
	}
	cmd := exec.CommandContext(cctx, probePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rpcmon: native probe failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	raw := stdout.Bytes()
	idx := bytes.IndexByte(raw, '{')
	if idx < 0 {
		return fmt.Errorf("rpcmon: native probe output lacks JSON object (output: %s)", truncateString(string(raw), 512))
	}
	raw = raw[idx:]

	var payload nativePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("rpcmon: decode probe JSON: %w", err)
	}
	b.data = &payload
	return nil
}

func (b *nativeBackend) Close() error { return nil }

func (b *nativeBackend) stamp() int64 {
	if b.data != nil && b.data.Timestamp != 0 {
		return b.data.Timestamp
	}
	return time.Now().Unix()
}

func (b *nativeBackend) PortInfo(ctx context.Context) ([]PortUsage, int64, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, 0, err
	}
	return append([]PortUsage(nil), b.data.PortInfo...), b.stamp(), nil
}

func (b *nativeBackend) ServerNum(ctx context.Context) (uint32, error) {
	if err := b.ensure(ctx); err != nil {
		return 0, err
	}
	return b.data.ServerNum, nil
}

func (b *nativeBackend) ServerDetail(ctx context.Context) ([]ServerEntry, int64, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, 0, err
	}
	return append([]ServerEntry(nil), b.data.Servers...), b.stamp(), nil
}

func (b *nativeBackend) ClientNum(ctx context.Context) (uint32, error) {
	if err := b.ensure(ctx); err != nil {
		return 0, err
	}
	return b.data.ClientNum, nil
}

func (b *nativeBackend) ClientDetail(ctx context.Context) ([]ClientEntry, int64, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, 0, err
	}
	return append([]ClientEntry(nil), b.data.Clients...), b.stamp(), nil
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
