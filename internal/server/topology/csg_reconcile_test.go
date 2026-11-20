package topology_test

import (
	"os"
	"testing"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/internal/server/topology"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"gorm.io/datatypes"
)

func TestReconcileLiveCSGWithSSOT_ThreeStates(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离数据库测试")
	}

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	agent := models.Agent{
		Name:     "csg-test-agent",
		Endpoint: "http://127.0.0.1:9991",
		Status:   models.AgentStatusOnline,
		Scope:    models.ScopeShared,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	subsys := models.Subsystem{Code: "core", Name: "Core"}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatalf("create subsystem failed: %v", err)
	}
	group := models.FunctionalGroup{SubsystemID: subsys.ID, Code: "g1", Name: "G1"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group failed: %v", err)
	}
	pgrp := models.ArchProcessGroup{Code: "PGRP_CSG", Name: "CSG进程组"}
	if err := db.Create(&pgrp).Error; err != nil {
		t.Fatalf("create process group failed: %v", err)
	}

	apOrder := models.ArchProcess{
		GroupID:           pgrp.ID,
		Code:              "order_svr",
		Name:              "OrderSvr",
		DefaultListenPort: 20001,
		DefaultCmdPattern: "order_svr",
	}
	apGhost := models.ArchProcess{
		GroupID:           pgrp.ID,
		Code:              "ghost_svr",
		Name:              "GhostSvr",
		DefaultListenPort: 20002,
		DefaultCmdPattern: "ghost_svr",
	}
	if err := db.Create(&apOrder).Error; err != nil {
		t.Fatalf("create apOrder failed: %v", err)
	}
	if err := db.Create(&apGhost).Error; err != nil {
		t.Fatalf("create apGhost failed: %v", err)
	}

	if err := db.Create(&models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       apOrder.ID,
		EffectiveListenPort: 20001,
		ExtraDownstreams:    datatypes.JSON("[]"),
	}).Error; err != nil {
		t.Fatalf("create proc order failed: %v", err)
	}
	if err := db.Create(&models.Process{
		AgentID:             agent.ID,
		ArchProcessID:       apGhost.ID,
		EffectiveListenPort: 20002,
		ExtraDownstreams:    datatypes.JSON("[]"),
	}).Error; err != nil {
		t.Fatalf("create proc ghost failed: %v", err)
	}

	modOrder := models.Module{SubsystemID: subsys.ID, GroupID: group.ID, Code: "m_order", Name: "OrderMod", ArchProcessID: &apOrder.ID}
	modGhost := models.Module{SubsystemID: subsys.ID, GroupID: group.ID, Code: "m_ghost", Name: "GhostMod", ArchProcessID: &apGhost.ID}
	if err := db.Create(&modOrder).Error; err != nil {
		t.Fatalf("create modOrder failed: %v", err)
	}
	if err := db.Create(&modGhost).Error; err != nil {
		t.Fatalf("create modGhost failed: %v", err)
	}

	svcOrder := models.ModuleService{
		ModuleID:         modOrder.ID,
		ServiceName:      "OrderService-0",
		ProtoServiceName: "order.v1.OrderService",
		DiffIgnorePaths:  datatypes.JSON("[]"),
	}
	svcGhost := models.ModuleService{
		ModuleID:        modGhost.ID,
		ServiceName:     "GhostService-GhostService",
		DiffIgnorePaths: datatypes.JSON("[]"),
	}
	if err := db.Create(&svcOrder).Error; err != nil {
		t.Fatalf("create svcOrder failed: %v", err)
	}
	if err := db.Create(&svcGhost).Error; err != nil {
		t.Fatalf("create svcGhost failed: %v", err)
	}

	if err := db.Create(&models.ModuleDependency{
		ConsumerModuleID: modGhost.ID,
		ServiceID:        &svcOrder.ID,
	}).Error; err != nil {
		t.Fatalf("create declared dep failed: %v", err)
	}

	liveReport := &agentproto.CSGTopologyReport{
		DiscoveryMode: "CROSTCP_RPCMON",
		Services: []agentproto.CSGServiceInfo{
			{
				ServiceName: "OrderService-0-12001",
				Addr:        "tcp://ipv4:127.0.0.1:20001",
				Transport:   "TCP",
				HostIP:      "127.0.0.1",
				Port:        20001,
				ServerPID:   12001,
				ServerProc:  "order_svr",
				IsLocal:     true,
			},
			{
				ServiceName: "PayService-PayService",
				Addr:        "tcp://ipv4:127.0.0.1:20003",
				Transport:   "TCP",
				HostIP:      "127.0.0.1",
				Port:        20003,
				ServerPID:   12003,
				ServerProc:  "pay_svr",
				IsLocal:     true,
			},
		},
		Subscriptions: []agentproto.CSGSubscription{
			{
				ClientPID:   12099,
				ClientProc:  "gateway_cli",
				ClientAddr:  "127.0.0.1",
				ServiceName: "OrderService-0",
				IsLocal:     true,
			},
		},
	}

	reconciled, err := topology.ReconcileLiveCSGWithSSOT(db, agent.ID, liveReport)
	if err != nil {
		t.Fatalf("ReconcileLiveCSGWithSSOT failed: %v", err)
	}

	if reconciled.Summary.LiveServiceCount != 2 || reconciled.Summary.MatchedCount != 1 || reconciled.Summary.UndeclaredCount != 1 || reconciled.Summary.MissingInLiveCount != 1 || reconciled.Summary.LiveSubCount != 1 {
		t.Fatalf("unexpected service summary counts: %+v", reconciled.Summary)
	}

	statesBySvc := make(map[string]topology.ReconciledCSGService)
	for _, s := range reconciled.Services {
		statesBySvc[s.ServiceName] = s
	}
	if item, ok := statesBySvc["OrderService-0-12001"]; !ok || item.State != topology.CSGReconcileMatched || item.SSOTServiceID == nil || *item.SSOTServiceID != svcOrder.ID || item.SSOTProtoSvc != "order.v1.OrderService" || item.SSOTModuleName != "OrderMod" {
		t.Fatalf("expected OrderService MATCHED with proto/module meta, got %+v", item)
	}
	if item, ok := statesBySvc["PayService-PayService"]; !ok || item.State != topology.CSGReconcileUndeclared || item.SSOTServiceID != nil {
		t.Fatalf("expected PayService UNDECLARED_IN_SSOT, got %+v", item)
	}
	if item, ok := statesBySvc["GhostService-GhostService"]; !ok || item.State != topology.CSGReconcileMissingInLive || item.SSOTServiceID == nil || *item.SSOTServiceID != svcGhost.ID || item.SSOTModuleName != "GhostMod" || item.ExpectedPort != 20002 {
		t.Fatalf("expected GhostService MISSING_IN_LIVE with module/expected_port, got %+v", item)
	}

	if len(reconciled.Subscriptions) != 2 {
		t.Fatalf("expected 2 reconciled subscriptions (1 UNDECLARED + 1 MISSING_IN_LIVE), got %+v", reconciled.Subscriptions)
	}
}

func TestImportCSGBlueprint_MeshServiceKeepsZeroTCPListenPort(t *testing.T) {
	if os.Getenv("TEST_DB_DSN") == "" {
		t.Skip("TEST_DB_DSN 未配置，跳过隔离数据库测试")
	}

	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	agent := models.Agent{
		Name:     "csg-mesh-agent",
		Endpoint: "http://127.0.0.1:9992",
		Status:   models.AgentStatusOnline,
		Scope:    models.ScopeShared,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}
	subsys := models.Subsystem{Code: "mesh_sub", Name: "MeshSub"}
	if err := db.Create(&subsys).Error; err != nil {
		t.Fatalf("create subsystem failed: %v", err)
	}

	res, err := topology.ImportCSGBlueprint(db, topology.CSGBlueprintImportInput{
		AgentID:            agent.ID,
		SubsystemID:        subsys.ID,
		ImportDependencies: true,
		OperatorID:         1,
		Live: &agentproto.CSGTopologyReport{
			DiscoveryMode: "NATIVE_PROBE",
			Services: []agentproto.CSGServiceInfo{
				{
					ServiceName: "MeshOnlySvc-MeshOnlySvc",
					Addr:        "dophi_mesh://ipv4:10.29.92.80:16000",
					Transport:   "MESH",
					HostIP:      "10.29.92.80",
					Port:        16000,
					ServerPID:   7701,
					ServerProc:  "mesh_only_svr",
					IsLocal:     true,
				},
				{
					ServiceName: "TcpSvc-TcpSvc",
					Addr:        "tcp://ipv4:10.29.92.80:18080",
					Transport:   "TCP",
					HostIP:      "10.29.92.80",
					Port:        18080,
					ServerPID:   7702,
					ServerProc:  "tcp_svr",
					IsLocal:     true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ImportCSGBlueprint failed: %v", err)
	}
	if res.CreatedServices != 2 || res.CreatedProcesses != 2 {
		t.Fatalf("unexpected import result: %+v", res)
	}

	var apMesh, apTCP models.ArchProcess
	if err := db.Where("code = ?", "mesh_only_svr").First(&apMesh).Error; err != nil {
		t.Fatalf("find apMesh failed: %v", err)
	}
	if err := db.Where("code = ?", "tcp_svr").First(&apTCP).Error; err != nil {
		t.Fatalf("find apTCP failed: %v", err)
	}
	if apMesh.DefaultListenPort != 0 {
		t.Fatalf("expected MESH ArchProcess DefaultListenPort=0, got %d", apMesh.DefaultListenPort)
	}
	if apTCP.DefaultListenPort != 18080 {
		t.Fatalf("expected TCP ArchProcess DefaultListenPort=18080, got %d", apTCP.DefaultListenPort)
	}

	var mountMesh models.Process
	if err := db.Where("agent_id = ? AND arch_process_id = ?", agent.ID, apMesh.ID).First(&mountMesh).Error; err != nil {
		t.Fatalf("find mountMesh failed: %v", err)
	}
	if mountMesh.EffectiveListenPort != 0 {
		t.Fatalf("expected MESH Process EffectiveListenPort=0, got %d", mountMesh.EffectiveListenPort)
	}
}
