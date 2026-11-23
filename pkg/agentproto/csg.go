package agentproto

import "encoding/json"

// CSGReplayStrategy enumerates CSG control-plane simulation modes during replay.
type CSGReplayStrategy string

const (
	CSGStrategyPassthrough  CSGReplayStrategy = "PASSTHROUGH"
	CSGStrategyClientInject CSGReplayStrategy = "CLIENT_INJECT"
	CSGStrategyPubStub      CSGReplayStrategy = "PUB_STUB"
	CSGStrategyMockServer   CSGReplayStrategy = "MOCK_SERVER"
)

// ParseCSGReplayStrategy normalizes an input strategy string and returns whether it is valid.
func ParseCSGReplayStrategy(raw string) (CSGReplayStrategy, bool) {
	switch CSGReplayStrategy(raw) {
	case "", CSGStrategyPassthrough:
		return CSGStrategyPassthrough, true
	case CSGStrategyClientInject:
		return CSGStrategyClientInject, true
	case CSGStrategyPubStub:
		return CSGStrategyPubStub, true
	case CSGStrategyMockServer:
		return CSGStrategyMockServer, true
	default:
		return "", false
	}
}

// CSGEndpointInfo describes a local/configured CSG dlink control-plane listener.
type CSGEndpointInfo struct {
	PID        uint32 `json:"pid"`
	ExeName    string `json:"exe_name,omitempty"`
	Cmdline    string `json:"cmdline,omitempty"`
	ListenIP   string `json:"listen_ip"`
	ListenPort uint16 `json:"listen_port"`
	Source     string `json:"source"` // e.g. "PROC_SOCKET", "CLI_FLAG", "ENV"
}

// Addr returns ip:port for CSG control-plane connections.
func (e CSGEndpointInfo) Addr() string {
	if e.ListenIP == "" || e.ListenPort == 0 {
		return ""
	}
	return formatHostPort(e.ListenIP, e.ListenPort)
}

func formatHostPort(ip string, port uint16) string {
	return ip + ":" + itoaUint16(port)
}

func itoaUint16(v uint16) string {
	if v == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// CSGPortUsage reports RPC port pool utilization on a CSG node.
type CSGPortUsage struct {
	IPAddr string `json:"ip_addr"`
	Used   uint32 `json:"used"`
	Total  uint32 `json:"total"`
}

// CSGServiceInfo merges rpcmon instance metadata with dlink Sub enrichment.
type CSGServiceInfo struct {
	ServiceName   string `json:"service_name"`
	Addr          string `json:"addr"`
	Transport     string `json:"transport"` // "TCP", "MESH", "DMS", "UC", "DOMAIN"
	HostIP        string `json:"host_ip"`
	Port          uint16 `json:"port"`
	ServerPID     uint32 `json:"server_pid"`
	ServerProc    string `json:"server_proc,omitempty"`
	ClientID      uint64 `json:"client_id,omitempty"`
	Version       uint64 `json:"version,omitempty"`
	OsNodeID      uint32 `json:"os_node_id,omitempty"`
	CompatName32  string `json:"compat_name32,omitempty"`
	IsLocal       bool   `json:"is_local"`
	IsRemoteLDC   bool   `json:"is_remote_ldc,omitempty"`
	ResolvedBySub bool   `json:"resolved_by_sub,omitempty"`
}

// CSGClientEntry represents an observed subscriber entry from RPCMonitorService.
type CSGClientEntry struct {
	ProcName    string `json:"proc_name"`
	PID         uint32 `json:"pid"`
	ClientAddr  string `json:"client_addr"`
	ServiceName string `json:"service_name"`
	IsLocal     bool   `json:"is_local"`
	IsRemoteLDC bool   `json:"is_remote_ldc,omitempty"`
}

// CSGSubscription summarizes fused dependency edges (subscriber process -> service).
type CSGSubscription struct {
	ClientPID   uint32 `json:"client_pid"`
	ClientProc  string `json:"client_proc"`
	ClientAddr  string `json:"client_addr"`
	ServiceName string `json:"service_name"`
	IsLocal     bool   `json:"is_local"`
}

// CSGTopologyReport is the fused dual-layer Agent CSG discovery output (L2 rpcmon + dlink + L1 /proc).
type CSGTopologyReport struct {
	CSGEndpoints         []CSGEndpointInfo `json:"csg_endpoints"`
	DiscoveryMode        string            `json:"discovery_mode"` // "CROSTCP_RPCMON", "CROSMESH_RPCMON", "PROC_ONLY"
	PortUsages           []CSGPortUsage    `json:"port_usages,omitempty"`
	Services             []CSGServiceInfo  `json:"services"`
	Subscriptions        []CSGSubscription `json:"subscriptions"`
	RawClients           []CSGClientEntry  `json:"raw_clients,omitempty"`
	FilteredRemoteCount  int               `json:"filtered_remote_count"`
	FilteredTransientCnt int               `json:"filtered_transient_count"`
	CapturedAt           string            `json:"captured_at"`
	Errors               []string          `json:"errors,omitempty"`
}

// CSGSnapshotRecord is persisted in RecordingSession.CSGSnapshots / GoldenSuiteItem.CSGSnapshots.
type CSGSnapshotRecord struct {
	ServiceName string `json:"service_name"`
	Addr        string `json:"addr"`
	Transport   string `json:"transport"`
	HostIP      string `json:"host_ip"`
	Port        uint16 `json:"port"`
	Source      string `json:"source"` // "START_SESSION", "DLINK_OBSERVER"
	ObservedAt  string `json:"observed_at,omitempty"`
}

// CSGObservation is pushed from Agent EgressProxy dlink Observer via WS event 0x08.
type CSGObservation struct {
	SessionID   uint64              `json:"session_id"`
	Direction   string              `json:"direction"`
	SubTypeName string              `json:"sub_type_name"`
	ServiceName string              `json:"service_name,omitempty"`
	Snapshots   []CSGSnapshotRecord `json:"snapshots,omitempty"`
	ObservedAt  string              `json:"observed_at"`
}

// CSGResolution records per-service resolution or stubbing details during replay.
type CSGResolution struct {
	ServiceName   string `json:"service_name"`
	Strategy      string `json:"strategy"`
	OriginalAddr  string `json:"original_addr,omitempty"`
	EffectiveAddr string `json:"effective_addr,omitempty"`
	ResolvedBySub bool   `json:"resolved_by_sub,omitempty"`
	PubStubActive bool   `json:"pub_stub_active,omitempty"`
	MeshRewritten bool   `json:"mesh_rewritten,omitempty"`
	Note          string `json:"note,omitempty"`
}

// CSGSandboxReport summarizes Agent CSG simulation, sandbox subscriptions, and 5-exit hygiene.
type CSGSandboxReport struct {
	Strategy              CSGReplayStrategy `json:"strategy"`
	CSGAddr               string            `json:"csg_addr,omitempty"`
	MockCSGAddr           string            `json:"mock_csg_addr,omitempty"`
	Resolutions           []CSGResolution   `json:"resolutions,omitempty"`
	SandboxSubscribedSvcs []string          `json:"sandbox_subscribed_svcs,omitempty"`
	UnpublishedStubs      []string          `json:"unpublished_stubs,omitempty"`
	MeshRewrites          int               `json:"mesh_rewrites,omitempty"`
	RealCSGLeakCount      int               `json:"real_csg_leak_count"`
	CleanupCompleted      bool              `json:"cleanup_completed"`
	ErrorCode             string            `json:"error_code,omitempty"`
	ErrorMessage          string            `json:"error_message,omitempty"`
	ExtraJSON             json.RawMessage   `json:"extra,omitempty"`
}
