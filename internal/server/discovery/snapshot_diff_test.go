package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"code-common/backend/testdb"
	"code-contract/config"
	"code-contract/models"
)

func TestSnapshot_ContentHashStableAcrossPIDAndDynamicPortChanges(t *testing.T) {
	base := &DiscoveryLiveRespDTO{
		AgentID:       1,
		CSGAddr:       "127.0.0.1:31806",
		DiscoveryMode: "CROSTCP_RPCMON",
		CapturedAt:    "2026-11-25T01:00:00Z",
		Processes: []DiscoveryProcessGroupDTO{
			{
				ProcessKey:         "fp:order_svr",
				ProcName:           "order_svr",
				ExeName:            "order_svr",
				Cmdline:            "/opt/bin/order_svr --port=21001 --pid=1111",
				PIDs:               []uint32{1111, 1112},
				ListenPorts:        []uint16{21001, 21002},
				ObservedRole:       "DUAL",
				ObservedTransports: []string{"TCP"},
				Services: []DiscoveryLiveServiceDTO{
					{
						ServiceName:    "OrderSvc-OrderSvc",
						RawServiceName: "OrderSvc-OrderSvc-1111",
						Endpoints:      []string{"tcp://ipv4:10.0.0.1:21001"},
						Transport:      "TCP",
						ServerPIDs:     []uint32{1111},
					},
				},
				Subscriptions: []DiscoveryLiveSubDTO{
					{ServiceName: "PaySvc-PaySvc", RawServiceName: "PaySvc-PaySvc-2222", ProviderKind: "LOCAL_UNENROLLED"},
				},
			},
		},
	}

	restarted := &DiscoveryLiveRespDTO{
		AgentID:       2,
		CSGAddr:       "10.0.8.8:31806",
		DiscoveryMode: "CROSTCP_RPCMON",
		CapturedAt:    "2026-11-25T09:30:00Z",
		Processes: []DiscoveryProcessGroupDTO{
			{
				ProcessKey:         "fp:order_svr",
				ProcName:           "order_svr",
				ExeName:            "order_svr",
				Cmdline:            "/opt/bin/order_svr --port=39001 --pid=8888",
				PIDs:               []uint32{8888},
				ListenPorts:        []uint16{39001},
				ObservedRole:       "DUAL",
				ObservedTransports: []string{"TCP"},
				Services: []DiscoveryLiveServiceDTO{
					{
						ServiceName:    "OrderSvc-OrderSvc",
						RawServiceName: "OrderSvc-OrderSvc-8888",
						Endpoints:      []string{"tcp://ipv4:10.0.8.8:39001"},
						Transport:      "TCP",
						ServerPIDs:     []uint32{8888},
					},
				},
				Subscriptions: []DiscoveryLiveSubDTO{
					{ServiceName: "PaySvc-PaySvc", RawServiceName: "PaySvc-PaySvc-9999", ProviderKind: "LOCAL_UNENROLLED"},
				},
			},
		},
	}

	h1 := ComputeSnapshotContentHash(base)
	h2 := ComputeSnapshotContentHash(restarted)
	if h1 == "" || h1 != h2 {
		t.Fatalf("expected identical content_hash across PID/port/timestamp changes, got h1=%q h2=%q", h1, h2)
	}
}

func TestSnapshot_FIFORetentionSkipsPinned_AndEnforcesPinLimit20(t *testing.T) {
	db := testdb.SetupIsolatedDB(t, config.ServiceName)
	if err := models.SetupWithDB(db); err != nil {
		t.Fatalf("SetupWithDB failed: %v", err)
	}

	agent := models.Agent{
		Name:     "fifo-agent",
		Endpoint: "http://127.0.0.1:19092",
		Scope:    models.ScopeShared,
		Status:   models.AgentStatusOnline,
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}

	makeLive := func(idx int) *DiscoveryLiveRespDTO {
		return &DiscoveryLiveRespDTO{
			AgentID:       agent.ID,
			AgentName:     agent.Name,
			CSGAddr:       "127.0.0.1:31806",
			DiscoveryMode: "CROSTCP_RPCMON",
			CapturedAt:    time.Now().Add(time.Duration(idx) * time.Second).UTC().Format(time.RFC3339),
			Processes: []DiscoveryProcessGroupDTO{
				{
					ProcessKey:         fmt.Sprintf("proc-%d", idx),
					ProcName:           fmt.Sprintf("proc-%d", idx),
					ObservedRole:       "SERVER",
					ObservedTransports: []string{"TCP"},
					Services: []DiscoveryLiveServiceDTO{
						{ServiceName: fmt.Sprintf("Svc%d", idx), Transport: "TCP"},
					},
				},
			},
		}
	}

	// 1. Create 20 pinned snapshots -> succeeds; 21st pinned snapshot -> ErrPinLimitExceeded (400)
	for i := 1; i <= 20; i++ {
		if _, err := SaveSnapshot(db, SaveSnapshotInput{
			AgentID:     agent.ID,
			CSGAddr:     "127.0.0.1:31806",
			TriggerMode: models.DiscoveryTriggerManual,
			Label:       fmt.Sprintf("pin-%d", i),
			IsPinned:    true,
			CreatedBy:   1,
			Live:        makeLive(i),
		}); err != nil {
			t.Fatalf("save pinned snapshot %d failed: %v", i, err)
		}
	}
	if _, err := SaveSnapshot(db, SaveSnapshotInput{
		AgentID:     agent.ID,
		CSGAddr:     "127.0.0.1:31806",
		TriggerMode: models.DiscoveryTriggerManual,
		Label:       "pin-21",
		IsPinned:    true,
		CreatedBy:   1,
		Live:        makeLive(21),
	}); !errors.Is(err, ErrPinLimitExceeded) {
		t.Fatalf("expected ErrPinLimitExceeded on 21st pinned snapshot, got %v", err)
	}

	// 2. AUTO snapshot deduplication: identical content_hash skips insert
	liveAuto := makeLive(100)
	firstAuto, err := SaveSnapshot(db, SaveSnapshotInput{
		AgentID:     agent.ID,
		CSGAddr:     "127.0.0.1:31806",
		TriggerMode: models.DiscoveryTriggerAuto,
		Live:        liveAuto,
	})
	if err != nil || firstAuto.Deduplicated {
		t.Fatalf("expected first AUTO snapshot saved, got err=%v dedup=%v", err, firstAuto)
	}
	dupAuto, err := SaveSnapshot(db, SaveSnapshotInput{
		AgentID:     agent.ID,
		CSGAddr:     "127.0.0.1:31806",
		TriggerMode: models.DiscoveryTriggerAuto,
		Live:        liveAuto,
	})
	if err != nil || !dupAuto.Deduplicated {
		t.Fatalf("expected duplicate AUTO snapshot skipped, got err=%v dedup=%v", err, dupAuto)
	}

	// 3. Write 55 unpinned snapshots -> retains latest 50 unpinned + all 20 pinned (total 70)
	for i := 101; i <= 155; i++ {
		if _, err := SaveSnapshot(db, SaveSnapshotInput{
			AgentID:     agent.ID,
			CSGAddr:     "127.0.0.1:31806",
			TriggerMode: models.DiscoveryTriggerManual,
			Label:       fmt.Sprintf("unpinned-%d", i),
			IsPinned:    false,
			CreatedBy:   1,
			Live:        makeLive(i),
		}); err != nil {
			t.Fatalf("save unpinned snapshot %d failed: %v", i, err)
		}
	}

	var pinnedCount, unpinnedCount int64
	db.Model(&models.DiscoverySnapshot{}).Where("agent_id = ? AND is_pinned = true", agent.ID).Count(&pinnedCount)
	db.Model(&models.DiscoverySnapshot{}).Where("agent_id = ? AND is_pinned = false", agent.ID).Count(&unpinnedCount)
	if pinnedCount != 20 {
		t.Fatalf("expected all 20 pinned snapshots preserved, got %d", pinnedCount)
	}
	if unpinnedCount != 50 {
		t.Fatalf("expected FIFO retention of 50 unpinned snapshots, got %d", unpinnedCount)
	}
}

func TestSnapshotDiff_AllDiffCategories_TimelineAndCrossEnv(t *testing.T) {
	left := &DiscoveryLiveRespDTO{
		AgentID:    1,
		AgentName:  "Env-A",
		CapturedAt: "2026-11-25T01:00:00Z",
		Processes: []DiscoveryProcessGroupDTO{
			{
				ProcessKey: "proc:gateway",
				ProcName:   "gateway_svr",
				PIDs:       []uint32{101},
				Services: []DiscoveryLiveServiceDTO{
					{ServiceName: "GatewaySvc", Transport: "TCP", Endpoints: []string{"tcp://ipv4:10.0.0.1:20001"}},
					{ServiceName: "DeprecatedSvc", Transport: "TCP", Endpoints: []string{"tcp://ipv4:10.0.0.1:20002"}},
				},
				Subscriptions: []DiscoveryLiveSubDTO{
					{ServiceName: "OrderSvc", ProviderKind: "LOCAL_ENROLLED"},
					{ServiceName: "OldDepSvc", ProviderKind: "LOCAL_UNENROLLED"},
				},
			},
			{
				ProcessKey: "proc:legacy_worker",
				ProcName:   "legacy_worker",
				PIDs:       []uint32{102},
			},
		},
	}

	right := &DiscoveryLiveRespDTO{
		AgentID:    2,
		AgentName:  "Env-B",
		CapturedAt: "2026-11-25T02:00:00Z",
		Processes: []DiscoveryProcessGroupDTO{
			{
				ProcessKey: "proc:gateway",
				ProcName:   "gateway_svr",
				PIDs:       []uint32{9901}, // different PID & IP/port should be ignored
				Services: []DiscoveryLiveServiceDTO{
					{ServiceName: "GatewaySvc", Transport: "MESH", Endpoints: []string{"dophi_mesh://ipv4:10.1.0.2:30001"}},
					{ServiceName: "NewFeatureSvc", Transport: "TCP", Endpoints: []string{"tcp://ipv4:10.1.0.2:30002"}},
				},
				Subscriptions: []DiscoveryLiveSubDTO{
					{ServiceName: "OrderSvc", ProviderKind: "REMOTE_LDC"},
					{ServiceName: "AuditSvc", ProviderKind: "LOCAL_ENROLLED"},
				},
			},
			{
				ProcessKey: "proc:new_billing",
				ProcName:   "new_billing",
				PIDs:       []uint32{9902},
			},
		},
	}

	diff := DiffSnapshots(left, right, "CROSS_ENV", "Env-A", "Env-B")
	if diff.Mode != "CROSS_ENV" {
		t.Fatalf("expected CROSS_ENV mode, got %s", diff.Mode)
	}

	seenKinds := make(map[string]int)
	for _, item := range diff.Items {
		seenKinds[item.Kind]++
	}

	requiredKinds := []string{
		DiffKindAddedProcess,
		DiffKindRemovedProcess,
		DiffKindAddedService,
		DiffKindRemovedService,
		DiffKindTransportChanged,
		DiffKindAddedSubscription,
		DiffKindRemovedSubscription,
		DiffKindProviderKindChanged,
	}
	for _, k := range requiredKinds {
		if seenKinds[k] == 0 {
			t.Fatalf("expected diff kind %s in diff items, got counts=%+v items=%+v", k, seenKinds, diff.Items)
		}
	}
	if diff.Summary.Total != len(diff.Items) || diff.Summary.Total != 8 {
		t.Fatalf("expected 8 total diff items, got summary=%+v items=%d", diff.Summary, len(diff.Items))
	}

	timelineDiff := DiffSnapshots(left, right, "TIMELINE", "T1", "T2")
	if timelineDiff.Mode != "TIMELINE" || timelineDiff.Summary.Total != 8 {
		t.Fatalf("expected TIMELINE diff to detect 8 changes, got %+v", timelineDiff)
	}
}

func TestRedactLiveResp_PreservesEmptySlicesAsJSONArrays(t *testing.T) {
	input := &DiscoveryLiveRespDTO{
		AgentID:       1,
		DiscoveryMode: "CROSTCP_RPCMON",
		Processes: []DiscoveryProcessGroupDTO{
			{
				ProcessKey:         "fp:empty_proc",
				ProcName:           "empty_proc",
				PIDs:               []uint32{},
				ListenPorts:        []uint16{},
				ObservedTransports: []string{},
				Services: []DiscoveryLiveServiceDTO{
					{
						ServiceName: "EmptySvc",
						Transport:   "TCP",
						Endpoints:   []string{},
						ServerPIDs:  []uint32{},
					},
				},
				Subscriptions: []DiscoveryLiveSubDTO{},
			},
		},
		RemoteLDCProcesses: []DiscoveryProcessGroupDTO{},
	}

	redacted := RedactLiveResp(input)
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("json.Marshal(redacted) failed: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		`"subscriptions":null`,
		`"services":null`,
		`"pids":null`,
		`"listen_ports":null`,
		`"observed_transports":null`,
		`"endpoints":null`,
		`"server_pids":null`,
		`"csg_endpoints":null`,
		`"ports":null`,
		`"remote_ldc_processes":null`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("expected JSON array instead of %s in %s", forbidden, body)
		}
	}
}
