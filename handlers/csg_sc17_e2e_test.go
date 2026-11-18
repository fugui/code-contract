package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/handlers"
	agentserver "code-contract/internal/agent/server"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/hub"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"gorm.io/datatypes"
)

func buildSC17OrderPBDescriptor(t *testing.T) []byte {
	t.Helper()
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("order_v1.proto"),
				Package: proto.String("order.v1"),
				Syntax:  proto.String("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("OrderReq"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     proto.String("id"),
								JsonName: proto.String("id"),
								Number:   proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
						},
					},
					{
						Name: proto.String("OrderRsp"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     proto.String("ok"),
								JsonName: proto.String("ok"),
								Number:   proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
						},
					},
				},
				Service: []*descriptorpb.ServiceDescriptorProto{
					{
						Name: proto.String("OrderService"),
						Method: []*descriptorpb.MethodDescriptorProto{
							{
								Name:       proto.String("CreateOrder"),
								InputType:  proto.String(".order.v1.OrderReq"),
								OutputType: proto.String(".order.v1.OrderRsp"),
							},
						},
					},
				},
			},
		},
	}
	raw, err := proto.Marshal(fds)
	if err != nil {
		t.Fatalf("marshal FileDescriptorSet failed: %v", err)
	}
	return raw
}

func startSC17LoopbackSUTServer(t *testing.T, rspPayload []byte) (net.Listener, string, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen sut failed: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, aErr := ln.Accept()
			if aErr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				framer := crosrpc.NewStreamFramer(0)
				buf := make([]byte, 8192)
				for {
					n, rErr := c.Read(buf)
					if n > 0 {
						frames, _ := framer.Feed(buf[:n])
						for _, f := range frames {
							parsed, pErr := crosrpc.ParsePacket(f.Data)
							if pErr != nil || parsed.Req == nil {
								continue
							}
							rspPkt := buildTestCrosRspFrame(
								parsed.Comm.SNO,
								crosrpc.MsgTypeRsp,
								1,
								0,
								parsed.Req.ServiceName,
								parsed.Req.Operation,
								rspPayload,
							)
							_, _ = c.Write(rspPkt)
						}
					}
					if rErr != nil {
						if rErr != io.EOF {
							return
						}
						return
					}
				}
			}(conn)
		}
	}()

	tcpAddr := ln.Addr().(*net.TCPAddr)
	return ln, ln.Addr().String(), uint16(tcpAddr.Port)
}

func TestGateEvaluate_MockServerSandbox_SubAuditAndZeroLeak(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB 失败: %v", err)
	}
	r, _ := setupGoldenSuiteTestServer(t, db)
	_, adminToken := createTestUser(t, db, "admin_mock_gate", config.AdminRole)

	// 1. Rotate CI token
	reqRotate := httptest.NewRequest(http.MethodPost, "/api/admin/ci-token/rotate", bytes.NewReader([]byte("{}")))
	reqRotate.Header.Set("Authorization", "Bearer "+adminToken)
	wRotate := httptest.NewRecorder()
	r.ServeHTTP(wRotate, reqRotate)
	var rot struct {
		CIToken string `json:"ci_token"`
	}
	_ = json.Unmarshal(wRotate.Body.Bytes(), &rot)
	ciToken := rot.CIToken

	// 2. Architecture fixture (Order process + module, initially zero downstream deps declared)
	subsys := models.Subsystem{Code: "SUB_SB_GATE", Name: "SandboxGateSubsys"}
	db.Create(&subsys)
	fg := models.FunctionalGroup{SubsystemID: subsys.ID, Code: "FG_SB_GATE", Name: "SandboxGateFG"}
	db.Create(&fg)
	grp := models.ArchProcessGroup{Code: "GRP_SB_GATE", Name: "SandboxGateGrp"}
	db.Create(&grp)
	archProc := models.ArchProcess{
		Code:              "sut-sb-gate",
		Name:              "沙箱审计被测进程",
		GroupID:           grp.ID,
		DefaultListenPort: 18195,
	}
	db.Create(&archProc)
	mod := models.Module{
		SubsystemID:   subsys.ID,
		GroupID:       fg.ID,
		Code:          "MOD_SB_ORDER",
		Name:          "沙箱订单模块",
		ArchProcessID: &archProc.ID,
	}
	db.Create(&mod)

	suite := models.GoldenSuite{
		Code:          "SUITE_SB_GATE",
		Name:          "沙箱门禁套件",
		ArchProcessID: archProc.ID,
		Status:        "ACTIVE",
	}
	db.Create(&suite)
	reqPkt := buildTestCrosReqFrame(9001, crosrpc.ReqTypeSync, "order.v1.OrderService", "Pay", []byte("pay"))
	rspPkt := buildTestCrosRspFrame(9001, crosrpc.MsgTypeRsp, 1, 0, "order.v1.OrderService", "Pay", []byte("ok"))
	item := models.GoldenSuiteItem{
		SuiteID:        suite.ID,
		OrderNo:        1,
		Name:           "PayCase",
		ServiceName:    "order.v1.OrderService",
		Operation:      "Pay",
		RawReqPacket:   reqPkt,
		RawRspPacket:   rspPkt,
		BaselineStatus: 0,
	}
	db.Create(&item)

	// 3. Sentinel real CSG listener: MUST receive 0 connections during MOCK_SERVER!
	sentinelRealCSG, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen sentinel realCSG failed: %v", err)
	}
	defer sentinelRealCSG.Close()
	var sentinelAccepted atomic.Int32
	go func() {
		for {
			c, aErr := sentinelRealCSG.Accept()
			if aErr != nil {
				return
			}
			sentinelAccepted.Add(1)
			_ = c.Close()
		}
	}()

	var capturedStrategy atomic.Value
	var capturedCSGAddr atomic.Value
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/agent/v1/replays/execute":
			var execReq agentproto.ExecuteReplayReq
			_ = json.NewDecoder(req.Body).Decode(&execReq)
			capturedStrategy.Store(string(execReq.CSGStrategy))
			capturedCSGAddr.Store(execReq.CSGAddr)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "RUNNING"})
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(agentproto.ReplayTaskResultResp{
				TaskID:         1,
				Status:         models.ReplayResultPass,
				TotalRequests:  1,
				PassedRequests: 1,
				CSGSandboxReport: &agentproto.CSGSandboxReport{
					Strategy:              agentproto.CSGStrategyMockServer,
					CSGAddr:               sentinelRealCSG.Addr().String(),
					MockCSGAddr:           "in-memory://mock-csg",
					SandboxSubscribedSvcs: []string{"UndeclaredSvc-0"},
					RealCSGLeakCount:      0,
					CleanupCompleted:      true,
				},
				Details: fmt.Sprintf(`[{"record_id":%d,"status":"%s","actual_rsp_hex":"%x"}]`,
					item.ID, models.ReplayResultPass, rspPkt),
			})
		}
	}))
	defer agentServer.Close()

	agent := models.Agent{
		Name:      "AGENT_SB_MOCK",
		Endpoint:  agentServer.URL,
		Scope:     models.ScopeShared,
		Status:    models.AgentStatusOnline,
		ClaimCode: "sb-claim",
	}
	db.Create(&agent)
	proc := models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       archProc.ID,
		EffectiveListenPort: 18195,
		ProbeStatus:         models.ProbeStatusArmed,
	}
	db.Create(&proc)

	// 4. First evaluate with MOCK_SERVER -> detects undeclared UndeclaredSvc-0 in sandbox Sub -> 422 GATED_VIOLATION
	evalBody1, _ := json.Marshal(handlers.GateEvaluateReq{
		TargetArchProcessID: archProc.ID,
		EnvAgentID:          agent.ID,
		GoldenSuiteID:       &suite.ID,
		TimeoutSeconds:      10,
		CSGStrategy:         "MOCK_SERVER",
		CSGAddr:             sentinelRealCSG.Addr().String(),
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/gates/evaluate", bytes.NewReader(evalBody1))
	req1.Header.Set("X-Contract-CI-Token", ciToken)
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	if w1.Code != http.StatusUnprocessableEntity {
		t.Fatalf("期望沙箱 Sub 越界阻断返回 422，实际 %d: %s", w1.Code, w1.Body.String())
	}
	var resp1 handlers.GateEvaluateResp
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)
	if resp1.GateStatus != "GATED_VIOLATION" || resp1.ViolationCount != 1 {
		t.Fatalf("期望 GATED_VIOLATION 且 ViolationCount=1，实际 %+v", resp1)
	}
	if capturedStrategy.Load() != "MOCK_SERVER" {
		t.Fatalf("期望调度器透传 csg_strategy=MOCK_SERVER，实际 %v", capturedStrategy.Load())
	}
	if sentinelAccepted.Load() != 0 {
		t.Fatalf("哨兵真实 CSG 收到 %d 次连接泄漏，必须为 0", sentinelAccepted.Load())
	}

	// 5. Declare UndeclaredSvc-0 in SSOT ModuleDependency -> second evaluate returns 200 PASSED
	depMod := models.Module{
		SubsystemID: subsys.ID,
		GroupID:     fg.ID,
		Code:        "MOD_SB_UNDECLARED",
		Name:        "沙箱补录下游模块",
	}
	db.Create(&depMod)
	depSvc := models.ModuleService{
		ModuleID:    depMod.ID,
		ServiceName: "UndeclaredSvc-0",
	}
	db.Create(&depSvc)
	db.Create(&models.ModuleDependency{
		ConsumerModuleID: mod.ID,
		ServiceID:        &depSvc.ID,
		ProviderModuleID: &depMod.ID,
	})

	req2 := httptest.NewRequest(http.MethodPost, "/api/gates/evaluate", bytes.NewReader(evalBody1))
	req2.Header.Set("X-Contract-CI-Token", ciToken)
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("补录依赖后门禁期望返回 200，实际 %d: %s", w2.Code, w2.Body.String())
	}
	var resp2 handlers.GateEvaluateResp
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.GateStatus != "PASSED" || resp2.ViolationCount != 0 {
		t.Fatalf("期望 PASSED 且 0 violation，实际 %+v", resp2)
	}
	if sentinelAccepted.Load() != 0 {
		t.Fatalf("哨兵真实 CSG 在第二次评估后仍必须保持 0 连接，实际 %d", sentinelAccepted.Load())
	}
}

func TestSC17_EndToEnd_DiscoveryBlueprintDecodeAndThreeModeSimulation(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离集成测试")
	}

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	// Real loopback SUT server answering valid CROS RPC + proto3 payload OrderRsp{ok: "true"}
	reqPayload := []byte{0x0a, 0x05, 'o', 'r', 'd', '-', '1'}
	rspPayload := []byte{0x0a, 0x04, 't', 'r', 'u', 'e'}
	_, sutAddr, sutPort := startSC17LoopbackSUTServer(t, rspPayload)

	// Real MockCSGServer (control plane + rpcmon CrosTCP monitor)
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()
	if err := mockCSG.StartMonitor("127.0.0.1:0"); err != nil {
		t.Fatalf("mockCSG.StartMonitor failed: %v", err)
	}

	// Publish OrderServiceImpl-OrderService (TCP SUT) and MeshDownstream-MeshDownstream (pure Mesh)
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 101,
		ProcID:   9101,
		OsNodeID: 1,
		Addr:     fmt.Sprintf("tcp://ipv4:%s", sutAddr),
		Name64:   "OrderServiceImpl-OrderService",
	})
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 102,
		ProcID:   9102,
		OsNodeID: 1,
		Addr:     "dophi_mesh://ipv4:127.0.0.1:39099",
		Name64:   "MeshDownstream-MeshDownstream",
	})

	// Seed active subscription: order_svr (9101) -> MeshDownstream-MeshDownstream
	subCli, err := csgdlink.Dial(mockCSG.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("subCli Dial failed: %v", err)
	}
	_, _ = subCli.Register(2 * time.Second)
	_, _ = subCli.ClientID(2 * time.Second)
	if _, err := subCli.Sub("MeshDownstream-MeshDownstream", 2*time.Second); err != nil {
		t.Fatalf("subCli Sub failed: %v", err)
	}
	defer subCli.Close()

	// Real AgentServer wired to mockCSG (seed fake /proc/<pid> entries so FindPIDByPortAndCmd resolves order_svr & mesh_svr)
	procRoot := t.TempDir()
	_ = os.MkdirAll(filepath.Join(procRoot, "net"), 0o755)
	_ = os.MkdirAll(filepath.Join(procRoot, "9101", "fd"), 0o755)
	_ = os.MkdirAll(filepath.Join(procRoot, "9102", "fd"), 0o755)
	_ = os.WriteFile(filepath.Join(procRoot, "9101", "comm"), []byte("order_svr\n"), 0o644)
	_ = os.WriteFile(filepath.Join(procRoot, "9101", "cmdline"), []byte("/opt/app/order_svr\x00"), 0o644)
	_ = os.Symlink("socket:[10001]", filepath.Join(procRoot, "9101", "fd", "5"))
	_ = os.Symlink("socket:[10002]", filepath.Join(procRoot, "9101", "fd", "6"))
	_ = os.WriteFile(filepath.Join(procRoot, "9102", "comm"), []byte("mesh_svr\n"), 0o644)
	_ = os.WriteFile(filepath.Join(procRoot, "9102", "cmdline"), []byte("/opt/app/mesh_svr\x00"), 0o644)
	_ = os.Symlink("socket:[10003]", filepath.Join(procRoot, "9102", "fd", "5"))
	fakeProcNetTCP := strings.Join([]string{
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		fmt.Sprintf("   0: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10001 1 00000000 100 0 0 10 0", sutPort),
		fmt.Sprintf("   1: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 10003 1 00000000 100 0 0 10 0", 39099),
	}, "\n") + "\n"
	_ = os.WriteFile(filepath.Join(procRoot, "net", "tcp"), []byte(fakeProcNetTCP), 0o644)

	agSrv, err := agentserver.NewAgentServer(agentserver.AgentServerConfig{
		ListenAddr:     "127.0.0.1:0",
		AllowIPs:       []string{"127.0.0.1/32"},
		ProcRoot:       procRoot,
		CSGAddrs:       []string{mockCSG.Addr()},
		CSGMonitorAddr: mockCSG.MonitorAddr(),
	})
	if err != nil {
		t.Fatalf("NewAgentServer failed: %v", err)
	}
	if err := agSrv.Start(); err != nil {
		t.Fatalf("agSrv.Start failed: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = agSrv.Shutdown(shutdownCtx)
	}()

	// Wire control plane router + hub + decoder + scheduler
	config.AppConfig = config.Config{
		Auth: config.AuthSection{
			JWTSecret: "test-jwt-secret-very-secure-key-123456",
		},
		AgentHub: config.AgentHubSection{
			DevAllowLoopback:     true,
			HeartbeatIntervalSec: 2,
		},
	}
	r, _ := setupGoldenSuiteTestServer(t, db)
	handlers.ResetGlobalHubManager(hub.NewManager(db, &config.AppConfig))

	adminUser, adminToken := createTestUser(t, db, "sc17_admin", config.AdminRole)
	normalUser, normalToken := createTestUser(t, db, "sc17_op", "operator")

	agent := models.Agent{
		Name:      "SC17_REAL_AGENT",
		Endpoint:  "http://" + agSrv.Addr().String(),
		ClaimCode: agSrv.ClaimCode(),
		Status:    models.AgentStatusOnline,
		Scope:     models.ScopeShared,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	// Unsub & close the extra subCli after initial topology snapshot? Wait keep it open during discovery & import, then close cleanly later before checking 0 clients!
	// Step A: GET /api/agents/:id/csg/topology -> UNDECLARED_IN_SSOT
	reqTopo1 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/agents/%d/csg/topology", agent.ID), nil)
	reqTopo1.Header.Set("Authorization", "Bearer "+normalToken)
	wTopo1 := httptest.NewRecorder()
	r.ServeHTTP(wTopo1, reqTopo1)
	if wTopo1.Code != http.StatusOK {
		t.Fatalf("GET /csg/topology failed %d: %s", wTopo1.Code, wTopo1.Body.String())
	}
	var topo1 topology.CSGTopologyReconcileResp
	if err := json.Unmarshal(wTopo1.Body.Bytes(), &topo1); err != nil {
		t.Fatalf("decode topo1 failed: %v", err)
	}
	if topo1.Summary.UndeclaredCount < 2 {
		t.Fatalf("expected >= 2 UNDECLARED_IN_SSOT services, got %+v", topo1.Summary)
	}

	// Step B: POST /api/agents/:id/csg/import-blueprint (403 for non-admin, 200 for admin -> MATCHED)
	subsys := models.Subsystem{Code: "SUB_SC17", Name: "SC17Subsys", LayerLevel: 20}
	db.Create(&subsys)
	importJSON, _ := json.Marshal(map[string]interface{}{
		"subsystem_id":        subsys.ID,
		"import_dependencies": true,
	})
	reqImpForbidden := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(importJSON))
	reqImpForbidden.Header.Set("Authorization", "Bearer "+normalToken)
	reqImpForbidden.Header.Set("Content-Type", "application/json")
	wImpForbidden := httptest.NewRecorder()
	r.ServeHTTP(wImpForbidden, reqImpForbidden)
	if wImpForbidden.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin blueprint import, got %d", wImpForbidden.Code)
	}

	reqImpAdmin := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agents/%d/csg/import-blueprint", agent.ID), bytes.NewReader(importJSON))
	reqImpAdmin.Header.Set("Authorization", "Bearer "+adminToken)
	reqImpAdmin.Header.Set("Content-Type", "application/json")
	wImpAdmin := httptest.NewRecorder()
	r.ServeHTTP(wImpAdmin, reqImpAdmin)
	if wImpAdmin.Code != http.StatusOK {
		t.Fatalf("expected 200 admin blueprint import, got %d: %s", wImpAdmin.Code, wImpAdmin.Body.String())
	}

	reqTopo2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/agents/%d/csg/topology", agent.ID), nil)
	reqTopo2.Header.Set("Authorization", "Bearer "+normalToken)
	wTopo2 := httptest.NewRecorder()
	r.ServeHTTP(wTopo2, reqTopo2)
	var topo2 topology.CSGTopologyReconcileResp
	_ = json.Unmarshal(wTopo2.Body.Bytes(), &topo2)
	if topo2.Summary.UndeclaredCount != 0 || topo2.Summary.MatchedCount < 2 {
		t.Fatalf("expected all services MATCHED after import, got %+v", topo2.Summary)
	}

	// Close discovery subscriber helper so MockCSGServer client list stays clean before simulation checks
	_ = subCli.Unsub("MeshDownstream-MeshDownstream", 2*time.Second)
	_ = subCli.Close()

	// Step C: Bind proto_service_name via HTTP PUT + activate .pb schema -> verify 64B alias & PID suffix decode lookup
	var orderSvc models.ModuleService
	if err := db.Where("service_name = ?", "OrderServiceImpl-OrderService").First(&orderSvc).Error; err != nil {
		t.Fatalf("find imported OrderServiceImpl-OrderService failed: %v", err)
	}
	var meshSvc models.ModuleService
	if err := db.Where("service_name = ?", "MeshDownstream-MeshDownstream").First(&meshSvc).Error; err != nil {
		t.Fatalf("find imported MeshDownstream-MeshDownstream failed: %v", err)
	}

	reg, regErr := handlers.GetDecoderRegistry()
	if regErr != nil {
		t.Fatalf("GetDecoderRegistry failed: %v", regErr)
	}
	if _, _, _, err := reg.UploadSchema(decoder.UploadSchemaInput{
		Name:         "order_sc17",
		VersionTag:   "v1.0.0",
		FileName:     "order_v1.pb",
		RawBytes:     buildSC17OrderPBDescriptor(t),
		AutoActivate: true,
		OperatorID:   adminUser.ID,
	}); err != nil {
		t.Fatalf("UploadSchema failed: %v", err)
	}

	updBody, _ := json.Marshal(map[string]interface{}{
		"service_name":       orderSvc.ServiceName,
		"proto_service_name": "order.v1.OrderService",
	})
	reqUpdSvc := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/arch/module-services/%d", orderSvc.ID), bytes.NewReader(updBody))
	reqUpdSvc.Header.Set("Authorization", "Bearer "+adminToken)
	reqUpdSvc.Header.Set("Content-Type", "application/json")
	wUpdSvc := httptest.NewRecorder()
	r.ServeHTTP(wUpdSvc, reqUpdSvc)
	if wUpdSvc.Code != http.StatusOK {
		t.Fatalf("PUT /api/arch/module-services failed %d: %s", wUpdSvc.Code, wUpdSvc.Body.String())
	}
	if op := reg.LookupOperation("OrderServiceImpl-OrderService", "CreateOrder"); op == nil {
		t.Fatalf("expected LookupOperation to resolve 64B alias OrderServiceImpl-OrderService via order.v1.OrderService")
	}

	// Step D: Wire imported modules to ArchProcesses + Agent Processes + Session & TrafficRecords
	var orderMod models.Module
	db.First(&orderMod, orderSvc.ModuleID)
	var meshMod models.Module
	db.First(&meshMod, meshSvc.ModuleID)
	if orderMod.ArchProcessID == nil || meshMod.ArchProcessID == nil {
		t.Fatalf("expected imported modules to have ArchProcessID, got order=%v mesh=%v", orderMod.ArchProcessID, meshMod.ArchProcessID)
	}

	// Add second dep StubDownstream-StubDownstream (NOT published in mockCSG) or toggle ConflictSvc via dlink Pub/Unpub
	stubDepMod := models.Module{
		SubsystemID: subsys.ID,
		GroupID:     orderMod.GroupID,
		Code:        "MOD_SC17_CONFLICT",
		Name:        "SC17PubConflictMod",
	}
	db.Create(&stubDepMod)
	stubDepSvc := models.ModuleService{
		ModuleID:    stubDepMod.ID,
		ServiceName: "ConflictDownstream-ConflictDownstream",
	}
	db.Create(&stubDepSvc)
	db.Create(&models.ModuleDependency{
		ConsumerModuleID: orderMod.ID,
		ServiceID:        &stubDepSvc.ID,
		ProviderModuleID: &stubDepMod.ID,
	})

	// ImportCSGBlueprint already auto-mounted Process rows for (agent.ID, ArchProcessID);
	// reuse them and intentionally set stale listen port (19999) on orderAgentProc so CLIENT_INJECT proves live CSG resolution!
	var orderAgentProc models.Process
	if err := db.Where("agent_id = ? AND arch_process_id = ?", agent.ID, *orderMod.ArchProcessID).First(&orderAgentProc).Error; err != nil {
		t.Fatalf("find imported orderAgentProc failed: %v", err)
	}
	if err := db.Model(&orderAgentProc).Updates(map[string]interface{}{
		"override_cmd_pattern":  "order_svr",
		"effective_listen_port": 19999,
		"probe_status":          models.ProbeStatusArmed,
	}).Error; err != nil {
		t.Fatalf("update orderAgentProc failed: %v", err)
	}
	var meshAgentProc models.Process
	if err := db.Where("agent_id = ? AND arch_process_id = ?", agent.ID, *meshMod.ArchProcessID).First(&meshAgentProc).Error; err != nil {
		t.Fatalf("find imported meshAgentProc failed: %v", err)
	}
	if err := db.Model(&meshAgentProc).Updates(map[string]interface{}{
		"override_cmd_pattern": "mesh_svr",
		"probe_status":         models.ProbeStatusArmed,
	}).Error; err != nil {
		t.Fatalf("update meshAgentProc failed: %v", err)
	}

	now := time.Now()
	csgSnapsJSON, _ := json.Marshal([]agentproto.CSGSnapshotRecord{
		{
			ServiceName: "ConflictDownstream-ConflictDownstream",
			Addr:        "dophi_mesh://ipv4:127.0.0.1:39111",
			Transport:   "MESH",
			HostIP:      "127.0.0.1",
			Port:        39111,
			Source:      "START_SESSION",
		},
	})
	recSess := models.RecordingSession{
		SessionNo:          "REC_SC17_E2E",
		Name:               "SC17 全链路录制",
		Scope:              models.ScopeShared,
		OwnerID:            normalUser.ID,
		Status:             models.SessionStatusCompleted,
		LeasedAgentIDs:     datatypes.JSON(fmt.Sprintf("[%d]", agent.ID)),
		TargetProcessIDs:   datatypes.JSON(fmt.Sprintf("[%d,%d]", orderAgentProc.ID, meshAgentProc.ID)),
		ProcessSnapshots:   datatypes.JSON("[]"),
		EndpointSnapshots:  datatypes.JSON("[]"),
		CSGSnapshots:       datatypes.JSON(csgSnapsJSON),
		ProcessDiagnostics: datatypes.JSON("[]"),
		CreatedBy:          normalUser.ID,
	}
	if err := db.Create(&recSess).Error; err != nil {
		t.Fatalf("create RecordingSession failed: %v", err)
	}

	orderReqPkt := buildTestCrosReqFrame(7001, crosrpc.ReqTypeSync, "OrderServiceImpl-OrderService", "CreateOrder", reqPayload)
	orderRspPkt := buildTestCrosRspFrame(7001, crosrpc.MsgTypeRsp, 1, 0, "OrderServiceImpl-OrderService", "CreateOrder", rspPayload)
	orderRec := models.TrafficRecord{
		CreatedAt:       now,
		SessionID:       recSess.ID,
		SeqNo:           1,
		AgentID:         agent.ID,
		ProcessID:       orderAgentProc.ID,
		Direction:       models.TrafficDirIngress,
		ExchangeType:    models.ExchangeUnary,
		SrcAddr:         "127.0.0.1:41001",
		DstAddr:         sutAddr,
		ReqCapturedAtNs: now.UnixNano(),
		SNO:             7001,
		ServiceName:     "OrderServiceImpl-OrderService",
		Operation:       "CreateOrder",
		StatusCode:      0,
		RawReqPacket:    orderReqPkt,
		RawRspPacket:    orderRspPkt,
		PayloadJSON:     datatypes.JSON(`{"req":{"id":"ord-1"},"rsp":{"ok":"true"}}`),
		IsReplayable:    true,
	}
	meshReqPkt := buildTestCrosReqFrame(7002, crosrpc.ReqTypeSync, "MeshDownstream-MeshDownstream", "Check", reqPayload)
	meshRspPkt := buildTestCrosRspFrame(7002, crosrpc.MsgTypeRsp, 1, 0, "MeshDownstream-MeshDownstream", "Check", rspPayload)
	meshRec := models.TrafficRecord{
		CreatedAt:       now,
		SessionID:       recSess.ID,
		SeqNo:           2,
		AgentID:         agent.ID,
		ProcessID:       meshAgentProc.ID,
		Direction:       models.TrafficDirIngress,
		ExchangeType:    models.ExchangeUnary,
		SrcAddr:         "127.0.0.1:41002",
		DstAddr:         "127.0.0.1:39099",
		ReqCapturedAtNs: now.UnixNano() + 1000,
		SNO:             7002,
		ServiceName:     "MeshDownstream-MeshDownstream",
		Operation:       "Check",
		StatusCode:      0,
		RawReqPacket:    meshReqPkt,
		RawRspPacket:    meshRspPkt,
		PayloadJSON:     datatypes.JSON(`{"req":{"id":"ord-1"},"rsp":{"ok":"true"}}`),
		IsReplayable:    true,
	}
	if err := db.Create(&orderRec).Error; err != nil {
		t.Fatalf("create orderRec failed: %v", err)
	}
	if err := db.Create(&meshRec).Error; err != nil {
		t.Fatalf("create meshRec failed: %v", err)
	}

	// Publish ConflictDownstream-ConflictDownstream live in mockCSG so PUB_STUB preflight hits 409!
	pubCli, err := csgdlink.Dial(mockCSG.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("pubCli Dial failed: %v", err)
	}
	_, _ = pubCli.Register(2 * time.Second)
	_, _ = pubCli.ClientID(2 * time.Second)
	if err := pubCli.Pub("ConflictDownstream-ConflictDownstream", "tcp://ipv4:127.0.0.1:39111", 2*time.Second); err != nil {
		t.Fatalf("pubCli.Pub failed: %v", err)
	}
	defer pubCli.Close()

	// D.1: PUB_STUB conflict -> immediate 409 CSG_SERVICE_ALREADY_PUBLISHED
	postReplay := func(name string, srcProc, dstProc, recID uint64, strategy string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]interface{}{
			"name":                name,
			"session_id":          recSess.ID,
			"source_process_id":   srcProc,
			"target_process_id":   dstProc,
			"selected_record_ids": []uint64{recID},
			"timeout_ms":          4000,
			"csg_strategy":        strategy,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/replay-tasks", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	wPubConflict := postReplay("SC17-PUB-CONFLICT", orderAgentProc.ID, orderAgentProc.ID, orderRec.ID, "PUB_STUB")
	if wPubConflict.Code != http.StatusConflict || !strings.Contains(wPubConflict.Body.String(), "CSG_SERVICE_ALREADY_PUBLISHED") {
		t.Fatalf("expected 409 CSG_SERVICE_ALREADY_PUBLISHED, got %d body=%s", wPubConflict.Code, wPubConflict.Body.String())
	}

	// D.2: CLIENT_INJECT on pure dophi_mesh service -> immediate 422 CSG_DATAPLANE_MESH_UNSUPPORTED
	wMesh422 := postReplay("SC17-MESH-422", meshAgentProc.ID, meshAgentProc.ID, meshRec.ID, "CLIENT_INJECT")
	if wMesh422.Code != http.StatusUnprocessableEntity || !strings.Contains(wMesh422.Body.String(), "CSG_DATAPLANE_MESH_UNSUPPORTED") {
		t.Fatalf("expected 422 CSG_DATAPLANE_MESH_UNSUPPORTED, got %d body=%s", wMesh422.Code, wMesh422.Body.String())
	}

	pollTaskDone := func(resp *httptest.ResponseRecorder, label string) models.ReplayTask {
		if resp.Code != http.StatusCreated {
			t.Fatalf("%s create failed %d: %s", label, resp.Code, resp.Body.String())
		}
		var created models.ReplayTask
		if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
			t.Fatalf("%s decode created failed: %v", label, err)
		}
		var finalTask models.ReplayTask
		var ag models.Agent
		for i := 0; i < 60; i++ {
			time.Sleep(100 * time.Millisecond)
			if err := db.First(&finalTask, created.ID).Error; err == nil && finalTask.LifecycleState == "COMPLETED" {
				if err := db.First(&ag, agent.ID).Error; err == nil && ag.LeaseType == "" {
					return finalTask
				}
			}
		}
		t.Fatalf("%s timed out waiting COMPLETED, last=%+v", label, finalTask)
		return finalTask
	}

	// D.3: CLIENT_INJECT on OrderServiceImpl-OrderService with stale EffectiveListenPort (19999) -> resolves sutAddr -> PASS!
	wClientInject := postReplay("SC17-CLIENT-INJECT", orderAgentProc.ID, orderAgentProc.ID, orderRec.ID, "CLIENT_INJECT")
	doneCI := pollTaskDone(wClientInject, "CLIENT_INJECT")
	if doneCI.ResultStatus != models.ReplayResultPass {
		t.Fatalf("expected CLIENT_INJECT PASS despite stale port 19999, got %+v", doneCI)
	}
	if len(mockCSG.Snapshot().Clients) != 0 {
		t.Fatalf("expected 0 dangling subscriptions after CLIENT_INJECT, got %+v", mockCSG.Snapshot().Clients)
	}

	// Unpub ConflictDownstream-ConflictDownstream & MeshDownstream before testing clean PUB_STUB / MOCK_SERVER; align effective listen port with sutPort
	_ = pubCli.Unpub("ConflictDownstream-ConflictDownstream", "tcp://ipv4:127.0.0.1:39111", 2*time.Second)
	_ = pubCli.Close()
	// Remove MeshDownstream-MeshDownstream from seeded instances by resetting via Unpub? No, wait: target deps of orderMod are MeshDownstream-MeshDownstream AND ConflictDownstream-ConflictDownstream;
	// remove MeshDownstream dependency so PUB_STUB targets solely ConflictDownstream-ConflictDownstream (now absent in mockCSG!).
	_ = db.Where("consumer_module_id = ? AND service_id = ?", orderMod.ID, meshSvc.ID).Delete(&models.ModuleDependency{}).Error
	_ = db.Model(&orderAgentProc).Update("effective_listen_port", sutPort).Error

	// D.4a: Clean PUB_STUB (absent service -> Pub mock listener -> Unpub on cleanup)
	wPubStub := postReplay("SC17-PUB-STUB", orderAgentProc.ID, orderAgentProc.ID, orderRec.ID, "PUB_STUB")
	donePS := pollTaskDone(wPubStub, "PUB_STUB")
	if donePS.ResultStatus != models.ReplayResultPass {
		t.Fatalf("expected PUB_STUB PASS, got %+v", donePS)
	}
	var psRep agentproto.CSGSandboxReport
	if err := json.Unmarshal(donePS.CSGSandboxReport, &psRep); err != nil {
		t.Fatalf("decode PUB_STUB sandbox report failed: %v (raw=%s)", err, string(donePS.CSGSandboxReport))
	}
	if !psRep.CleanupCompleted || len(psRep.UnpublishedStubs) == 0 {
		t.Fatalf("expected PUB_STUB cleanup_completed & unpublished_stubs, got %+v", psRep)
	}
	for _, s := range mockCSG.Snapshot().Servers {
		if s.ServerName == "ConflictDownstream-ConflictDownstream" {
			t.Fatalf("expected PUB_STUB to clean up ConflictDownstream-ConflictDownstream, still registered: %+v", s)
		}
	}

	// D.4b: MOCK_SERVER zero-leak sandbox simulation + CleanupCompleted + zero leftover Agent lease
	wMockSrv := postReplay("SC17-MOCK-SERVER", orderAgentProc.ID, orderAgentProc.ID, orderRec.ID, "MOCK_SERVER")
	doneMS := pollTaskDone(wMockSrv, "MOCK_SERVER")
	if doneMS.ResultStatus != models.ReplayResultPass {
		t.Fatalf("expected MOCK_SERVER PASS, got %+v", doneMS)
	}
	var msRep agentproto.CSGSandboxReport
	if err := json.Unmarshal(doneMS.CSGSandboxReport, &msRep); err != nil {
		t.Fatalf("decode MOCK_SERVER sandbox report failed: %v", err)
	}
	if !msRep.CleanupCompleted || msRep.RealCSGLeakCount != 0 {
		t.Fatalf("expected MOCK_SERVER CleanupCompleted=true & RealCSGLeakCount=0, got %+v", msRep)
	}

	var checkAgent models.Agent
	_ = db.First(&checkAgent, agent.ID).Error
	if checkAgent.LeaseType != "" {
		t.Fatalf("expected empty Agent LeaseType after SC-17 simulations, got %q", checkAgent.LeaseType)
	}
	if leaseType, _ := agSrv.ActiveLease(); leaseType != "" {
		t.Fatalf("expected empty AgentServer active lease after SC-17 simulations, got %q", leaseType)
	}

	// Keep strconv used if needed
	_ = strconv.FormatUint(agent.ID, 10)
}
