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
		ModuleID:        modOrder.ID,
		ServiceName:     "OrderService-0",
		DiffIgnorePaths: datatypes.JSON("[]"),
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

	if reconciled.Summary.MatchedCount != 1 || reconciled.Summary.UndeclaredCount != 1 || reconciled.Summary.MissingInLiveCount != 1 {
		t.Fatalf("unexpected service summary counts: %+v", reconciled.Summary)
	}

	statesBySvc := make(map[string]topology.ReconciledCSGService)
	for _, s := range reconciled.Services {
		statesBySvc[s.ServiceName] = s
	}
	if item, ok := statesBySvc["OrderService-0-12001"]; !ok || item.State != topology.CSGReconcileMatched || item.SSOTServiceID == nil || *item.SSOTServiceID != svcOrder.ID {
		t.Fatalf("expected OrderService MATCHED, got %+v", item)
	}
	if item, ok := statesBySvc["PayService-PayService"]; !ok || item.State != topology.CSGReconcileUndeclared || item.SSOTServiceID != nil {
		t.Fatalf("expected PayService UNDECLARED_IN_SSOT, got %+v", item)
	}
	if item, ok := statesBySvc["GhostService-GhostService"]; !ok || item.State != topology.CSGReconcileMissingInLive || item.SSOTServiceID == nil || *item.SSOTServiceID != svcGhost.ID {
		t.Fatalf("expected GhostService MISSING_IN_LIVE, got %+v", item)
	}

	if len(reconciled.Subscriptions) != 2 {
		t.Fatalf("expected 2 reconciled subscriptions (1 UNDECLARED + 1 MISSING_IN_LIVE), got %+v", reconciled.Subscriptions)
	}
}
