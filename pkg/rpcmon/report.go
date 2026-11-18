package rpcmon

import (
	"context"
	"encoding/json"
	"fmt"
)

// Backend defines the 5 read-only RPCMonitorService operations.
type Backend interface {
	PortInfo(ctx context.Context) ([]PortUsage, int64, error)
	ServerNum(ctx context.Context) (uint32, error)
	ServerDetail(ctx context.Context) ([]ServerEntry, int64, error)
	ClientNum(ctx context.Context) (uint32, error)
	ClientDetail(ctx context.Context) ([]ClientEntry, int64, error)
	Close() error
}

// Report is the unified JSON-serializable topology snapshot produced by both CrosTCP and Native backends.
type Report struct {
	Query     string        `json:"query"`
	Timestamp int64         `json:"timestamp"`
	ServerNum uint32        `json:"serverNum"`
	ClientNum uint32        `json:"clientNum"`
	Ports     []PortUsage   `json:"ports,omitempty"`
	Servers   []ServerEntry `json:"servers,omitempty"`
	Clients   []ClientEntry `json:"clients,omitempty"`
}

// Fetch queries one or all RPCMonitorService endpoints. Passing op=="" defaults to "all".
func Fetch(ctx context.Context, b Backend, op string) (*Report, error) {
	if op == "" {
		op = "all"
	}
	switch op {
	case "all", "port-info", "server-num", "server-detail", "client-num", "client-detail":
	default:
		return nil, fmt.Errorf("rpcmon: unknown op %q (want port-info|server-num|server-detail|client-num|client-detail|all)", op)
	}

	rep := &Report{Query: op}

	if op == "all" || op == "port-info" {
		ports, ts, err := b.PortInfo(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetRpcPortInfo: %w", err)
		}
		rep.Ports = ports
		if ts != 0 {
			rep.Timestamp = ts
		}
	}

	if op == "all" || op == "server-num" || op == "server-detail" {
		num, err := b.ServerNum(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetRpcServerNumInfo: %w", err)
		}
		rep.ServerNum = num
	}

	if op == "all" || op == "server-detail" {
		srvs, ts, err := b.ServerDetail(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetRpcServerDetailInfo: %w", err)
		}
		rep.Servers = srvs
		if ts != 0 {
			rep.Timestamp = ts
		}
	}

	if op == "all" || op == "client-num" || op == "client-detail" {
		num, err := b.ClientNum(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetRpcClientNumInfo: %w", err)
		}
		rep.ClientNum = num
	}

	if op == "all" || op == "client-detail" {
		clis, ts, err := b.ClientDetail(ctx)
		if err != nil {
			return nil, fmt.Errorf("GetRpcClientDetailInfo: %w", err)
		}
		rep.Clients = clis
		if ts != 0 {
			rep.Timestamp = ts
		}
	}

	return rep, nil
}

// JSON formats Report with readable indentation.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
