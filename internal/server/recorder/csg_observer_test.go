package recorder

import (
	"encoding/json"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"code-contract/models"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/ebpf"
	"gorm.io/datatypes"
)

func TestRecordingSession_CSGObserver0x08_NoStreamFramerFalseFrame(t *testing.T) {
	// Part 1: Verify EgressProxy ModeCapturing passively observes "DLNK" traffic into 0x08
	// without feeding bytes to crosrpc.StreamFramer or polluting 0x01 traffic frames.
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()
	mockCSG.SeedInstance(csgdlink.Instance{
		ClientID: 7,
		ProcID:   3001,
		OsNodeID: 2,
		Addr:     "tcp://ipv4:127.0.0.1:28888",
		Name64:   "TradeSvc-TradeSvc",
	})

	csgHost, csgPortStr, err := net.SplitHostPort(mockCSG.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort failed: %v", err)
	}
	var csgPort uint16
	if _, err := net.LookupPort("tcp", csgPortStr); err == nil {
		_ = csgPort
	}
	csgTCPAddr, _ := net.ResolveTCPAddr("tcp", mockCSG.Addr())
	csgIP := net.ParseIP(csgHost).To4()
	csgPort = uint16(csgTCPAddr.Port)

	var mu sync.Mutex
	var envelopes [][]byte
	proxy, err := ebpf.NewEgressProxy(
		func(clientPort uint16) (uint64, uint32, net.IP, uint16, bool) {
			return 101, 4444, csgIP, csgPort, true
		},
		func(env []byte) {
			mu.Lock()
			defer mu.Unlock()
			cp := make([]byte, len(env))
			copy(cp, env)
			envelopes = append(envelopes, cp)
		},
	)
	if err != nil {
		t.Fatalf("NewEgressProxy failed: %v", err)
	}
	defer func() { _ = proxy.Close() }()

	const sessionID uint64 = 9001
	proxy.StartCapture(sessionID)

	proxyAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(proxy.ProxyPort())))
	dlinkCli, err := csgdlink.Dial(proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy failed: %v", err)
	}
	if _, err := dlinkCli.Register(2 * time.Second); err != nil {
		t.Fatalf("dlink Register failed: %v", err)
	}
	if _, err := dlinkCli.ClientID(2 * time.Second); err != nil {
		t.Fatalf("dlink ClientID failed: %v", err)
	}
	insts, err := dlinkCli.Sub("TradeSvc-TradeSvc", 2*time.Second)
	if err != nil || len(insts) != 1 {
		t.Fatalf("dlink Sub through EgressProxy failed: insts=%+v err=%v", insts, err)
	}
	_ = dlinkCli.Close()

	// Wait briefly for relay goroutines to emit 0x08 observation envelopes
	deadline := time.Now().Add(2 * time.Second)
	var obsEnvs [][]byte
	for time.Now().Before(deadline) {
		mu.Lock()
		copied := append([][]byte(nil), envelopes...)
		mu.Unlock()
		obsEnvs = obsEnvs[:0]
		for _, raw := range copied {
			hdr, _, uErr := agentproto.UnmarshalEnvelope(raw)
			if uErr != nil {
				continue
			}
			if hdr.EventType == agentproto.EventTrafficFrame {
				t.Fatalf("unexpected 0x01 EventTrafficFrame emitted for DLNK stream!")
			}
			if hdr.EventType == agentproto.EventCSGObservation {
				obsEnvs = append(obsEnvs, raw)
			}
		}
		if len(obsEnvs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(obsEnvs) == 0 {
		t.Fatalf("expected >=1 EventCSGObservation (0x08) envelope from EgressProxy")
	}

	// Verify Part 2 against PostgreSQL Orchestrator when TEST_DB_DSN is set.
	if os.Getenv("TEST_DB_DSN") == "" {
		return
	}

	db := setupIsolatedRecorderDB(t)
	agent := models.Agent{Name: "obs-agt", Endpoint: "http://127.0.0.1:19991", Status: models.AgentStatusOnline, Scope: models.ScopeShared}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent failed: %v", err)
	}
	sess := models.RecordingSession{
		SessionNo:          "REC_OBS_TEST",
		Name:               "obs-sess",
		Scope:              models.ScopeShared,
		Status:             models.SessionStatusRecording,
		LeasedAgentIDs:     datatypes.JSON("[]"),
		TargetProcessIDs:   datatypes.JSON("[]"),
		ProcessSnapshots:   datatypes.JSON("[]"),
		EndpointSnapshots:  datatypes.JSON("[]"),
		CSGSnapshots:       datatypes.JSON("[]"),
		ProcessDiagnostics: datatypes.JSON("[]"),
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("create session failed: %v", err)
	}

	orch := &Orchestrator{db: db}
	for _, raw := range obsEnvs {
		_, payload, err := agentproto.UnmarshalEnvelope(raw)
		if err != nil {
			t.Fatalf("unmarshal obs envelope: %v", err)
		}
		orch.handleCSGObservationPayload(sess.ID, payload)
	}

	var refreshed models.RecordingSession
	if err := db.First(&refreshed, sess.ID).Error; err != nil {
		t.Fatalf("reload session failed: %v", err)
	}
	var snaps []agentproto.CSGSnapshotRecord
	if err := json.Unmarshal(refreshed.CSGSnapshots, &snaps); err != nil {
		t.Fatalf("unmarshal CSGSnapshots failed: %v", err)
	}
	var found bool
	for _, s := range snaps {
		if s.ServiceName == "TradeSvc-TradeSvc" && s.Addr == "tcp://ipv4:127.0.0.1:28888" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected TradeSvc-TradeSvc in session CSGSnapshots, got %+v", snaps)
	}
}
