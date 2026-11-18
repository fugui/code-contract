// Package csg implements dual-layer CSG endpoint discovery, L1+L2 process/topology
// fusion, and three-mode CSG simulation for replay on the Agent data plane.
package csg

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"code-contract/pkg/agentproto"
)

const (
	DefaultCSGPortMin uint16 = 31800
	DefaultCSGPortMax uint16 = 31900
)

// LocateOptions controls multi-tenant CSG endpoint discovery priority.
type LocateOptions struct {
	CLICSGAddrs []string
	EnvCSGAddr  string
	ProcRoot    string
	PortMin     uint16
	PortMax     uint16
}

// ParseCSGAddr extracts a normalized IPv4 address and TCP port from <ip>:<port>,
// ipv4:<ip>:<port>, or tcp://ipv4:<ip>:<port>.
func ParseCSGAddr(raw string) (string, uint16, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", 0, false
	}
	s = strings.TrimPrefix(s, "tcp://")
	s = strings.TrimPrefix(s, "ipv4:")
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, false
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, false
	}
	return ip.To4().String(), uint16(p), true
}

// LocateCSGEndpoints returns candidate CSG dlink endpoints ordered by authority:
// CLI flag > CONTRACT_AGENT_CSG_ADDR env > /proc/net/tcp ESTABLISHED [31800,31900] > LISTEN [31800,31900].
func LocateCSGEndpoints(opts LocateOptions) ([]agentproto.CSGEndpointInfo, error) {
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.PortMin == 0 {
		opts.PortMin = DefaultCSGPortMin
	}
	if opts.PortMax == 0 {
		opts.PortMax = DefaultCSGPortMax
	}
	if opts.EnvCSGAddr == "" {
		opts.EnvCSGAddr = os.Getenv("CONTRACT_AGENT_CSG_ADDR")
	}

	var result []agentproto.CSGEndpointInfo
	seen := make(map[string]struct{})
	addUnique := func(ep agentproto.CSGEndpointInfo) {
		key := ep.Addr()
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, ep)
	}

	for _, raw := range opts.CLICSGAddrs {
		for _, part := range strings.Split(raw, ",") {
			if ip, port, ok := ParseCSGAddr(part); ok {
				addUnique(agentproto.CSGEndpointInfo{
					ListenIP:   ip,
					ListenPort: port,
					Source:     "CLI_FLAG",
				})
			}
		}
	}

	if opts.EnvCSGAddr != "" {
		for _, part := range strings.Split(opts.EnvCSGAddr, ",") {
			if ip, port, ok := ParseCSGAddr(part); ok {
				addUnique(agentproto.CSGEndpointInfo{
					ListenIP:   ip,
					ListenPort: port,
					Source:     "ENV",
				})
			}
		}
	}

	estab, listen, err := scanProcNetTCPForCSG(opts.ProcRoot, opts.PortMin, opts.PortMax)
	if err != nil {
		// /proc may be unavailable on non-Linux hosts if explicit flags/env were supplied;
		// only surface error when no explicit endpoints exist.
		if len(result) == 0 {
			return nil, err
		}
		return result, nil
	}

	sort.Slice(estab, func(i, j int) bool {
		if estab[i].ListenPort != estab[j].ListenPort {
			return estab[i].ListenPort < estab[j].ListenPort
		}
		return estab[i].ListenIP < estab[j].ListenIP
	})
	for _, ep := range estab {
		addUnique(ep)
	}

	sort.Slice(listen, func(i, j int) bool {
		if listen[i].ListenPort != listen[j].ListenPort {
			return listen[i].ListenPort < listen[j].ListenPort
		}
		return listen[i].ListenIP < listen[j].ListenIP
	})
	for _, ep := range listen {
		addUnique(ep)
	}

	return result, nil
}

type procMeta struct {
	pid     uint32
	exeName string
	cmdline string
}

func scanProcNetTCPForCSG(procRoot string, portMin, portMax uint16) ([]agentproto.CSGEndpointInfo, []agentproto.CSGEndpointInfo, error) {
	tcpPath := filepath.Join(procRoot, "net", "tcp")
	f, err := os.Open(tcpPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", tcpPath, err)
	}
	defer func() { _ = f.Close() }()

	type pendingSock struct {
		ip     string
		port   uint16
		inode  string
		listen bool
	}

	var pending []pendingSock
	neededInodes := make(map[string]struct{})

	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if first {
			first = false
			continue
		}
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		localAddr, remAddr, state, inode := fields[1], fields[2], fields[3], fields[9]
		switch state {
		case "01": // TCP_ESTABLISHED -> remote address in [portMin, portMax] indicates CSG server
			ip, port, ok := parseProcHexAddr(remAddr)
			if ok && port >= portMin && port <= portMax {
				if ip == "0.0.0.0" {
					ip = "127.0.0.1"
				}
				pending = append(pending, pendingSock{ip: ip, port: port, inode: inode, listen: false})
				if inode != "" && inode != "0" {
					neededInodes[inode] = struct{}{}
				}
			}
		case "0A": // TCP_LISTEN -> local CSG port in [portMin, portMax]
			ip, port, ok := parseProcHexAddr(localAddr)
			if ok && port >= portMin && port <= portMax {
				if ip == "0.0.0.0" {
					ip = "127.0.0.1"
				}
				pending = append(pending, pendingSock{ip: ip, port: port, inode: inode, listen: true})
				if inode != "" && inode != "0" {
					neededInodes[inode] = struct{}{}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan %s: %w", tcpPath, err)
	}

	inodeOwners := mapInodesToProcs(procRoot, neededInodes)
	var estab, listen []agentproto.CSGEndpointInfo
	for _, item := range pending {
		ep := agentproto.CSGEndpointInfo{
			ListenIP:   item.ip,
			ListenPort: item.port,
			Source:     "PROC_ESTABLISHED",
		}
		if item.listen {
			ep.Source = "PROC_LISTEN"
		}
		if owner, ok := inodeOwners[item.inode]; ok {
			ep.PID = owner.pid
			ep.ExeName = owner.exeName
			ep.Cmdline = owner.cmdline
		}
		if item.listen {
			listen = append(listen, ep)
		} else {
			estab = append(estab, ep)
		}
	}
	return estab, listen, nil
}

func mapInodesToProcs(procRoot string, targetInodes map[string]struct{}) map[string]procMeta {
	out := make(map[string]procMeta)
	if len(targetInodes) == 0 {
		return out
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid64, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil {
			continue
		}
		pid := uint32(pid64)
		pidDir := filepath.Join(procRoot, entry.Name())
		fdDir := filepath.Join(pidDir, "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		var meta *procMeta
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
				continue
			}
			inode := link[len("socket:[") : len(link)-1]
			if _, ok := targetInodes[inode]; !ok {
				continue
			}
			if meta == nil {
				loaded := readProcMeta(pidDir, pid)
				meta = &loaded
			}
			out[inode] = *meta
		}
	}
	return out
}

func readProcMeta(pidDir string, pid uint32) procMeta {
	meta := procMeta{pid: pid}
	if b, err := os.ReadFile(filepath.Join(pidDir, "comm")); err == nil {
		meta.exeName = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(pidDir, "cmdline")); err == nil {
		parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		meta.cmdline = strings.TrimSpace(strings.Join(parts, " "))
	}
	return meta
}

func parseProcHexAddr(hexAddr string) (string, uint16, bool) {
	parts := strings.Split(hexAddr, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	ipRaw, err := hex.DecodeString(parts[0])
	if err != nil || len(ipRaw) != 4 {
		return "", 0, false
	}
	port64, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil || port64 == 0 {
		return "", 0, false
	}
	// /proc/net/tcp encodes IPv4 as little-endian 32-bit word.
	ip := net.IPv4(ipRaw[3], ipRaw[2], ipRaw[1], ipRaw[0]).String()
	return ip, uint16(port64), true
}
