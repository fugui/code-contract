// Package rpcmon implements read-only RPCMonitorService querying over standard
// CROS RPC TCP ("CSE.TCP") with zero-reflection hand-coded Protobuf wire codecs
// as well as the allowlisted native probe backend.
package rpcmon

// PortUsage mirrors swp.rpc.RpcPortInfo.
type PortUsage struct {
	IPAddr string `json:"ipAddr"`
	Used   uint32 `json:"uiPortUsed"`
	Total  uint32 `json:"uiPortTotal"`
}

// ServerEntry mirrors swp.rpc.RpcServerDetailInfo.
type ServerEntry struct {
	ProcName   string `json:"procName"`
	Pid        uint32 `json:"pid"`
	ServerName string `json:"serverName"`
	ServerAddr string `json:"serverAddr"`
}

// ClientEntry mirrors swp.rpc.RpcClientDetailInfo.
type ClientEntry struct {
	ProcName   string `json:"procName"`
	Pid        uint32 `json:"pid"`
	ClientAddr string `json:"clientAddr"`
	ServerName string `json:"serverName"`
}

// Snapshot is an immutable point-in-time view provided by MockCSGServer or another source.
type Snapshot struct {
	Ports   []PortUsage
	Servers []ServerEntry
	Clients []ClientEntry
}

// ServerNum returns the number of published server instances.
func (s Snapshot) ServerNum() uint32 { return uint32(len(s.Servers)) }

// ClientNum returns the number of active client subscriptions.
func (s Snapshot) ClientNum() uint32 { return uint32(len(s.Clients)) }

// Provider supplies current CSG registration & subscription snapshots for rpcmon.Server.
type Provider interface {
	Snapshot() Snapshot
}
