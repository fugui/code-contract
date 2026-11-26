package agentproto

import (
	"encoding/json"
	"testing"
	"time"
)

func TestComputeArmedDigest(t *testing.T) {
	// 空列表返回空串
	if digest := ComputeArmedDigest(nil); digest != "" {
		t.Fatalf("expected empty digest for nil, got %s", digest)
	}
	if digest := ComputeArmedDigest([]ProcessMonitorSpec{}); digest != "" {
		t.Fatalf("expected empty digest for empty slice, got %s", digest)
	}

	// 顺序无关测试：按 ProcessID 升序排序确定性
	specs1 := []ProcessMonitorSpec{
		{
			ProcessID:  2,
			ListenPort: 8081,
			CmdPattern: "order-svc",
			Downstreams: []DownstreamEndpoint{
				{IP: "127.0.0.1", Port: 9002},
				{IP: "192.168.1.1", Port: 9001},
			},
		},
		{
			ProcessID:  1,
			ListenPort: 8080,
			CmdPattern: "account-svc",
			Downstreams: []DownstreamEndpoint{
				{IP: "127.0.0.1", Port: 9000},
			},
		},
	}

	specs2 := []ProcessMonitorSpec{
		{
			ProcessID:  1,
			ListenPort: 8080,
			CmdPattern: "account-svc",
			Downstreams: []DownstreamEndpoint{
				{IP: "127.0.0.1", Port: 9000},
			},
		},
		{
			ProcessID:  2,
			ListenPort: 8081,
			CmdPattern: "order-svc",
			Downstreams: []DownstreamEndpoint{
				{IP: "192.168.1.1", Port: 9001},
				{IP: "127.0.0.1", Port: 9002},
			},
		},
	}

	d1 := ComputeArmedDigest(specs1)
	d2 := ComputeArmedDigest(specs2)

	if d1 == "" || d2 == "" {
		t.Fatalf("digest should not be empty")
	}
	if d1 != d2 {
		t.Fatalf("digest should be deterministic and order-independent: %s != %s", d1, d2)
	}

	// 修改任意字段哈希发生改变
	specs3 := []ProcessMonitorSpec{
		{
			ProcessID:  1,
			ListenPort: 8080,
			CmdPattern: "account-svc-modified",
		},
	}
	d3 := ComputeArmedDigest(specs3)
	if d3 == d1 {
		t.Fatalf("modified spec must yield different digest")
	}
}

func TestHandshakeResp_TransportDriversJSONRoundtrip(t *testing.T) {
	resp := HandshakeResp{
		AgentVersion:    "v1.10.0",
		ProtocolVersion: ProtocolVersion,
		BootID:          "boot-uuid-1234",
		StartedAt:       time.Now().UTC(),
		KernelVersion:   "6.8.0-generic",
		HasEbpfCap:      true,
		CapDetail:       "CAP_BPF+CAP_NET_RAW",
		TransportDrivers: []TransportDriverCapability{
			{
				Name:          "builtin-tcp",
				Version:       "1.0.0",
				Transports:    []string{"tcp"},
				TransportCode: 1,
				Capabilities:  CapDiscover | CapCapture | CapInject | CapStub,
				Available:     true,
			},
			{
				Name:          "builtin-drt-uprobe",
				Version:       "1.0.0",
				Transports:    []string{"dophi_mesh", "mesh"},
				TransportCode: 4,
				Capabilities:  CapDiscover | CapCapture,
				Available:     true,
				Detail:        "libdopra.so detected with DRT_WriteBulk",
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var decoded HandshakeResp
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if len(decoded.TransportDrivers) != 2 {
		t.Fatalf("expected 2 transport drivers, got %d", len(decoded.TransportDrivers))
	}
	if decoded.TransportDrivers[0].Name != "builtin-tcp" || decoded.TransportDrivers[0].TransportCode != 1 {
		t.Errorf("driver 0 mismatch: %+v", decoded.TransportDrivers[0])
	}
	if decoded.TransportDrivers[1].Name != "builtin-drt-uprobe" || decoded.TransportDrivers[1].TransportCode != 4 {
		t.Errorf("driver 1 mismatch: %+v", decoded.TransportDrivers[1])
	}
}

func TestIsPureMeshTransports(t *testing.T) {
	cases := []struct {
		name       string
		transports []string
		want       bool
	}{
		{name: "single MESH", transports: []string{"MESH"}, want: true},
		{name: "lowercase dophi_mesh", transports: []string{"dophi_mesh"}, want: true},
		{name: "mesh with blank", transports: []string{"", " MESH "}, want: true},
		{name: "single TCP", transports: []string{"TCP"}, want: false},
		{name: "dual TCP and MESH", transports: []string{"TCP", "MESH"}, want: false},
		{name: "MIXED", transports: []string{"MIXED"}, want: false},
		{name: "empty slice", transports: []string{}, want: false},
		{name: "nil", transports: nil, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPureMeshTransports(tc.transports); got != tc.want {
				t.Fatalf("IsPureMeshTransports(%v) = %v, want %v", tc.transports, got, tc.want)
			}
			spec := ProcessMonitorSpec{ProcessID: 1, Transports: tc.transports}
			if got := spec.IsPureMesh(); got != tc.want {
				t.Fatalf("ProcessMonitorSpec.IsPureMesh() = %v, want %v", got, tc.want)
			}
		})
	}

	// 验证 ComputeArmedDigest 不受 Transports 字段影响（保持与旧版 Agent 摘要兼容）
	dWithout := ComputeArmedDigest([]ProcessMonitorSpec{{
		ProcessID:  10,
		ListenPort: 0,
		CmdPattern: "mesh_svr",
	}})
	dWith := ComputeArmedDigest([]ProcessMonitorSpec{{
		ProcessID:  10,
		ListenPort: 0,
		CmdPattern: "mesh_svr",
		Transports: []string{"MESH"},
		TargetPIDs: []uint32{1001, 1002},
		CSGAddr:    "127.0.0.1:31806",
	}})
	if dWithout != dWith {
		t.Fatalf("ComputeArmedDigest should stay backward compatible: %s != %s", dWithout, dWith)
	}
}
