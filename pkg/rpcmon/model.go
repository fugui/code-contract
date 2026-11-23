// Package rpcmon implements read-only RPCMonitorService querying over both
// standard CROS RPC TCP ("CSE.TCP", ucProtocol=1) and pure-Go dophi_mesh
// (128B HELLO + 40B MESH_MSG_DATA_HEAD_S + CROS ucProtocol=4) with zero-reflection
// hand-coded Protobuf wire codecs.
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
