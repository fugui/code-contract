package csg

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/rpcmon"
)

const defaultSubEnrichBudget = 8 * time.Second

// DiscovererConfig configures L1 (/proc) + L2 (rpcmon & dlink Sub->Unsub) fusion.
type DiscovererConfig struct {
	Locate     LocateOptions
	MonitorAddr string
	CSGAddr    string
	LocalIPs   []string
	LocalPIDs  []agentproto.DiscoveredProcess
	SubEnrich  bool
	SubTimeout time.Duration
}

// DiscoverAndFuse executes dual-layer CSG topology discovery and returns both
// the fused CSGTopologyReport and enriched DiscoveredProcess list (backfilling
// pure client processes with listen_port==0 and dophi_mesh processes).
func DiscoverAndFuse(ctx context.Context, cfg DiscovererConfig) (*agentproto.CSGTopologyReport, []agentproto.DiscoveredProcess, error) {
	if cfg.SubTimeout <= 0 {
		cfg.SubTimeout = 2 * time.Second
	}

	endpoints := locateForConfig(cfg)
	csgAddr := strings.TrimSpace(cfg.CSGAddr)
	if csgAddr == "" && len(endpoints) > 0 {
		csgAddr = endpoints[0].Addr()
	}
	localSet := buildLocalIPSet(cfg.LocalIPs, csgAddr)

	report := &agentproto.CSGTopologyReport{
		CSGEndpoints:  endpoints,
		DiscoveryMode: "PROC_ONLY",
		Services:      []agentproto.CSGServiceInfo{},
		Subscriptions: []agentproto.CSGSubscription{},
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	var monRep *rpcmon.Report
	var monErr error
	explicitMonitor := strings.TrimSpace(cfg.MonitorAddr)

	fetchWithBackend := func(backend rpcmon.Backend, mode string) (*rpcmon.Report, error) {
		defer func() { _ = backend.Close() }()
		rep, err := rpcmon.Fetch(ctx, backend, "all")
		if err == nil {
			report.DiscoveryMode = mode
		}
		return rep, err
	}

	fetchTCP := func(addr string) (*rpcmon.Report, error) {
		backend, err := rpcmon.NewCrosTCP(addr, cfg.SubTimeout)
		if err != nil {
			return nil, err
		}
		return fetchWithBackend(backend, "CROSTCP_RPCMON")
	}

	fetchMesh := func(addr string) (*rpcmon.Report, error) {
		backend, err := rpcmon.NewCrosMesh(addr, cfg.SubTimeout)
		if err != nil {
			return nil, err
		}
		return fetchWithBackend(backend, "CROSMESH_RPCMON")
	}

	switch {
	case explicitMonitor != "":
		if csgdlink.IsMeshAddr(explicitMonitor) {
			monRep, monErr = fetchMesh(explicitMonitor)
			if monErr != nil {
				monErr = fmt.Errorf("dophi_mesh rpcmon(%s) 失败: %w", explicitMonitor, monErr)
			}
		} else {
			monRep, monErr = fetchTCP(explicitMonitor)
			if monErr != nil {
				monErr = fmt.Errorf("CrosTCP rpcmon(%s) 失败: %w", explicitMonitor, monErr)
			}
		}
	case csgAddr != "":
		tcpAddr, meshAddr, dlinkResolveErr := resolveMonitorAddrViaDlink(ctx, csgAddr, cfg.SubTimeout)
		switch {
		case tcpAddr != "":
			monRep, monErr = fetchTCP(tcpAddr)
			if monErr != nil && meshAddr != "" {
				report.Errors = append(report.Errors, fmt.Sprintf("CrosTCP rpcmon(%s) 失败，自动回退 dophi_mesh(%s): %v", tcpAddr, meshAddr, monErr))
				monRep, monErr = fetchMesh(meshAddr)
				if monErr != nil {
					monErr = fmt.Errorf("dophi_mesh rpcmon(%s) 经 CSG(%s) 失败: %w", meshAddr, csgAddr, monErr)
				}
			} else if monErr != nil {
				monErr = fmt.Errorf("CrosTCP rpcmon(%s) 经 CSG(%s) 失败: %w", tcpAddr, csgAddr, monErr)
			}
		case meshAddr != "":
			monRep, monErr = fetchMesh(meshAddr)
			if monErr != nil {
				monErr = fmt.Errorf("L2 发现降级为 PROC_ONLY：经 CSG(%s) 解析 %s 为 %s，纯 Go dophi_mesh 查询失败: %w", csgAddr, rpcmon.ServiceName, meshAddr, monErr)
			}
		case dlinkResolveErr != nil:
			report.Errors = append(report.Errors, fmt.Sprintf(
				"L2 发现降级为 PROC_ONLY：经 CSG(%s) 自动解析 %s 失败: %v",
				csgAddr, rpcmon.ServiceName, dlinkResolveErr,
			))
		}
	default:
		report.Errors = append(report.Errors, "L2 发现降级为 PROC_ONLY：未在本机发现可用 CSG 控制面端点，请在 Agent 启动参数指定 --csg-addr")
	}
	if monErr != nil {
		report.Errors = append(report.Errors, monErr.Error())
	}

	procByPID := make(map[uint32]*agentproto.DiscoveredProcess, len(cfg.LocalPIDs))
	procOrder := make([]uint32, 0, len(cfg.LocalPIDs))
	for _, p := range cfg.LocalPIDs {
		cp := p
		if csgAddr != "" && cp.CSGEndpoint == "" {
			cp.CSGEndpoint = csgAddr
		}
		if existing, ok := procByPID[cp.PID]; ok {
			if existing.ExeName == "" && cp.ExeName != "" {
				existing.ExeName = cp.ExeName
			}
			if existing.ListenPort == 0 && cp.ListenPort > 0 {
				existing.ListenPort = cp.ListenPort
			}
			if existing.CSGEndpoint == "" && cp.CSGEndpoint != "" {
				existing.CSGEndpoint = cp.CSGEndpoint
			}
			for _, sn := range cp.ServiceNames {
				existing.ServiceNames = appendUniqueString(existing.ServiceNames, sn)
			}
			for _, tr := range cp.Transports {
				existing.Transports = appendUniqueString(existing.Transports, tr)
			}
			continue
		}
		procByPID[cp.PID] = &cp
		procOrder = append(procOrder, cp.PID)
	}

	if monRep != nil {
		for _, p := range monRep.Ports {
			report.PortUsages = append(report.PortUsages, agentproto.CSGPortUsage{
				IPAddr: p.IPAddr,
				Used:   p.Used,
				Total:  p.Total,
			})
		}

		for _, s := range monRep.Servers {
			hostIP, port := extractHostIPAndPort(s.ServerAddr)
			if !isHostLocal(hostIP, localSet) {
				report.FilteredRemoteCount++
				continue
			}
			transport := detectTransport(s.ServerAddr)
			if transport == "" && port > 0 {
				transport = "TCP"
			}
			svc := agentproto.CSGServiceInfo{
				ServiceName: s.ServerName,
				Addr:        s.ServerAddr,
				Transport:   transport,
				HostIP:      hostIP,
				Port:        port,
				ServerPID:   s.Pid,
				ServerProc:  s.ProcName,
				IsLocal:     true,
			}
			report.Services = append(report.Services, svc)
		}

		for _, c := range monRep.Clients {
			if isTransientProbeProc(c.ProcName) {
				report.FilteredTransientCnt++
				continue
			}
			clientIP, _ := extractHostIPAndPort(c.ClientAddr)
			if !isHostLocal(clientIP, localSet) {
				report.FilteredRemoteCount++
				continue
			}
			report.RawClients = append(report.RawClients, agentproto.CSGClientEntry{
				ProcName:    c.ProcName,
				PID:         c.Pid,
				ClientAddr:  c.ClientAddr,
				ServiceName: c.ServerName,
				IsLocal:     true,
			})
			report.Subscriptions = append(report.Subscriptions, agentproto.CSGSubscription{
				ClientPID:   c.Pid,
				ClientProc:  c.ProcName,
				ClientAddr:  c.ClientAddr,
				ServiceName: c.ServerName,
				IsLocal:     true,
			})
		}
	}

	if cfg.SubEnrich && csgAddr != "" && len(report.Services) > 0 {
		if err := enrichServicesByDlinkSub(ctx, csgAddr, cfg.SubTimeout, report.Services); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("dlink Sub enrich degraded: %v", err))
		}
	}

	// Fuse L2 local servers and local clients into L1 discovered processes.
	procRoot := strings.TrimSpace(cfg.Locate.ProcRoot)
	if procRoot == "" {
		procRoot = "/proc"
	}
	ensureProc := func(pid uint32, procName string) *agentproto.DiscoveredProcess {
		if existing, ok := procByPID[pid]; ok {
			if existing.ExeName == "" && procName != "" {
				existing.ExeName = procName
			}
			if existing.Cmdline == "" {
				meta := readProcMeta(filepath.Join(procRoot, strconv.FormatUint(uint64(pid), 10)), pid)
				if meta.cmdline != "" {
					existing.Cmdline = meta.cmdline
				}
				if existing.ExeName == "" && meta.exeName != "" {
					existing.ExeName = meta.exeName
				}
			}
			return existing
		}
		meta := readProcMeta(filepath.Join(procRoot, strconv.FormatUint(uint64(pid), 10)), pid)
		exeName := procName
		if exeName == "" {
			exeName = meta.exeName
		}
		np := &agentproto.DiscoveredProcess{
			PID:         pid,
			ExeName:     exeName,
			Cmdline:     meta.cmdline,
			CSGEndpoint: csgAddr,
		}
		procByPID[pid] = np
		procOrder = append(procOrder, pid)
		return np
	}

	for _, svc := range report.Services {
		if svc.ServerPID == 0 {
			continue
		}
		p := ensureProc(svc.ServerPID, svc.ServerProc)
		if p.ListenPort == 0 && svc.Port > 0 && svc.Transport != "MESH" {
			p.ListenPort = svc.Port
		}
		p.IsPureClient = false
		p.ServiceNames = appendUniqueString(p.ServiceNames, svc.ServiceName)
		p.Transports = appendUniqueString(p.Transports, svc.Transport)
		if csgAddr != "" {
			p.CSGEndpoint = csgAddr
		}
	}

	for _, sub := range report.Subscriptions {
		if sub.ClientPID == 0 {
			continue
		}
		p := ensureProc(sub.ClientPID, sub.ClientProc)
		if p.ListenPort == 0 && len(p.ServiceNames) == 0 {
			p.IsPureClient = true
		}
		if csgAddr != "" {
			p.CSGEndpoint = csgAddr
		}
	}

	sort.Slice(procOrder, func(i, j int) bool { return procOrder[i] < procOrder[j] })
	fused := make([]agentproto.DiscoveredProcess, 0, len(procOrder))
	for _, pid := range procOrder {
		fused = append(fused, *procByPID[pid])
	}
	return report, fused, nil
}

func resolveMonitorAddrViaDlink(ctx context.Context, csgAddr string, timeout time.Duration) (tcpAddr string, meshAddr string, err error) {
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if strings.TrimSpace(csgAddr) == "" {
		return "", "", fmt.Errorf("empty CSG address")
	}
	if timeout <= 0 || timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	insts, err := csgdlink.ResolveServiceInstances(csgAddr, rpcmon.ServiceName, timeout)
	if err != nil {
		return "", "", err
	}
	for _, inst := range insts {
		if csgdlink.IsTCPAddr(inst.Addr) {
			if hp, ok := csgdlink.ParseTCPTarget(inst.Addr); ok {
				return hp, "", nil
			}
		}
		if meshAddr == "" && csgdlink.IsMeshAddr(inst.Addr) {
			meshAddr = inst.Addr
		}
	}
	if meshAddr != "" {
		return "", meshAddr, nil
	}
	if len(insts) > 0 {
		return "", "", fmt.Errorf("service %s has no tcp:// instance (%s)", rpcmon.ServiceName, insts[0].Addr)
	}
	return "", "", fmt.Errorf("service %s not published on %s", rpcmon.ServiceName, csgAddr)
}

func locateForConfig(cfg DiscovererConfig) []agentproto.CSGEndpointInfo {
	opts := cfg.Locate
	if cfg.CSGAddr != "" {
		opts.CLICSGAddrs = append([]string{cfg.CSGAddr}, opts.CLICSGAddrs...)
	}
	eps, err := LocateCSGEndpoints(opts)
	if err != nil {
		return nil
	}
	return eps
}

func enrichServicesByDlinkSub(ctx context.Context, csgAddr string, timeout time.Duration, services []agentproto.CSGServiceInfo) error {
	if len(services) == 0 {
		return nil
	}
	enrichCtx, cancel := context.WithTimeout(ctx, defaultSubEnrichBudget)
	defer cancel()

	client, err := csgdlink.Dial(csgAddr, cappedOpTimeout(enrichCtx, timeout))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Register(cappedOpTimeout(enrichCtx, timeout)); err != nil {
		return err
	}
	if _, err := client.ClientID(cappedOpTimeout(enrichCtx, timeout)); err != nil {
		return err
	}

	byName := make(map[string][]int)
	names := make([]string, 0)
	for i := range services {
		name := services[i].ServiceName
		if _, exists := byName[name]; !exists {
			names = append(names, name)
		}
		byName[name] = append(byName[name], i)
	}

	enrichedNames := 0
	for _, name := range names {
		if err := enrichCtx.Err(); err != nil {
			return fmt.Errorf("enriched %d/%d services before timeout: %w", enrichedNames, len(names), err)
		}
		insts, err := client.Sub(name, cappedOpTimeout(enrichCtx, timeout))
		unsubTimeout := timeout
		if unsubTimeout <= 0 || unsubTimeout > time.Second {
			unsubTimeout = time.Second
		}
		if unsubErr := client.Unsub(name, unsubTimeout); unsubErr != nil {
			return fmt.Errorf("unsub %s (enriched %d/%d): %w", name, enrichedNames, len(names), unsubErr)
		}
		if err != nil || len(insts) == 0 {
			continue
		}
		enrichedNames++
		for _, idx := range byName[name] {
			match := matchBestInstance(services[idx], insts)
			services[idx].ClientID = match.ClientID
			services[idx].Version = match.Version
			services[idx].OsNodeID = match.OsNodeID
			services[idx].CompatName32 = match.Name32
			services[idx].ResolvedBySub = true
			if match.Addr != "" && (services[idx].Addr == "" || !strings.Contains(services[idx].Addr, "://") || services[idx].Port == 0) {
				services[idx].Addr = match.Addr
				if tr := detectTransport(match.Addr); tr != "" {
					services[idx].Transport = tr
				}
				if hp, p := extractHostIPAndPort(match.Addr); hp != "" {
					services[idx].HostIP = hp
					if p > 0 {
						services[idx].Port = p
					}
				}
			}
		}
	}
	return nil
}

func cappedOpTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = 2 * time.Second
	}
	if dl, ok := ctx.Deadline(); ok {
		rem := time.Until(dl)
		if rem <= 0 {
			return 10 * time.Millisecond
		}
		if rem < fallback {
			return rem
		}
	}
	return fallback
}

func extractHostIPAndPort(raw string) (string, uint16) {
	if host, port, ok := csgdlink.ParseAddrIPPort(raw); ok {
		return host, port
	}
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "ipv4:")
	if trimmed == "" {
		return "", 0
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), 0
		}
	}
	if host, portStr, err := net.SplitHostPort(trimmed); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				if p, pErr := strconv.Atoi(portStr); pErr == nil && p > 0 && p <= 65535 {
					return v4.String(), uint16(p)
				}
				return v4.String(), 0
			}
		}
	}
	return "", 0
}

func matchBestInstance(svc agentproto.CSGServiceInfo, insts []csgdlink.Instance) csgdlink.Instance {
	for _, inst := range insts {
		if inst.Addr == svc.Addr {
			return inst
		}
	}
	for _, inst := range insts {
		if svc.ServerPID != 0 && inst.ProcID == svc.ServerPID {
			return inst
		}
	}
	if svc.HostIP != "" {
		for _, inst := range insts {
			if hp, _ := extractHostIPAndPort(inst.Addr); hp == svc.HostIP {
				return inst
			}
		}
	}
	return insts[0]
}

func detectTransport(addr string) string {
	trimmed := strings.TrimSpace(addr)
	switch {
	case csgdlink.IsMeshAddr(trimmed):
		return "MESH"
	case strings.Contains(trimmed, "dms://"):
		return "DMS"
	case strings.Contains(trimmed, "dpuc://"):
		return "UC"
	case strings.Contains(trimmed, "domain://"):
		return "DOMAIN"
	case csgdlink.IsTCPAddr(trimmed):
		return "TCP"
	default:
		return ""
	}
}

func isTransientProbeProc(procName string) bool {
	lower := strings.ToLower(strings.TrimSpace(procName))
	return lower == "rpcmon_probe" || strings.Contains(lower, "rpcmon_probe")
}

func buildLocalIPSet(explicit []string, csgAddr string) map[string]struct{} {
	set := map[string]struct{}{
		"127.0.0.1": {},
		"0.0.0.0":   {},
	}
	for _, ip := range explicit {
		if trimmed := strings.TrimSpace(ip); trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	if ip, _, ok := ParseCSGAddr(csgAddr); ok {
		set[ip] = struct{}{}
	}
	if len(explicit) == 0 {
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if ipNet, ok := a.(*net.IPNet); ok {
					if v4 := ipNet.IP.To4(); v4 != nil {
						set[v4.String()] = struct{}{}
					}
				}
			}
		}
	}
	return set
}

func isHostLocal(ip string, localSet map[string]struct{}) bool {
	if ip == "" {
		return true
	}
	_, ok := localSet[ip]
	return ok
}

func appendUniqueString(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, item := range list {
		if item == v {
			return list
		}
	}
	return append(list, v)
}
