package diff

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code-contract/internal/server/decoder"
	"code-contract/internal/server/hub"
	"code-contract/models"
	"code-contract/pkg/agentproto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func buildOrderProtoDecoder(t *testing.T, db *gorm.DB) *decoder.Registry {
	t.Helper()
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("order.proto"),
				Package: proto.String("dophi.order"),
				Syntax:  proto.String("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("OrderReq"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("order_id"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
						},
					},
					{
						Name: proto.String("OrderRsp"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("order_id"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: proto.String("trace_ts"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
						},
					},
				},
				Service: []*descriptorpb.ServiceDescriptorProto{
					{
						Name: proto.String("OrderService"),
						Method: []*descriptorpb.MethodDescriptorProto{
							{
								Name:       proto.String("CreateOrder"),
								InputType:  proto.String(".dophi.order.OrderReq"),
								OutputType: proto.String(".dophi.order.OrderRsp"),
							},
						},
					},
				},
			},
		},
	}
	rawFDS, err := proto.Marshal(fds)
	if err != nil {
		t.Fatalf("marshal fds failed: %v", err)
	}
	reg, err := decoder.NewRegistry(db)
	if err != nil {
		t.Fatalf("decoder.NewRegistry failed: %v", err)
	}
	if _, _, _, err := reg.UploadSchema(decoder.UploadSchemaInput{
		Name:         "order_v1_adr31",
		FileName:     "order.pb",
		RawBytes:     rawFDS,
		AutoActivate: true,
		SourceType:   "GIT_SYNC",
	}); err != nil {
		t.Fatalf("UploadSchema failed: %v", err)
	}
	return reg
}

func marshalOrderRspPayload(t *testing.T, reg *decoder.Registry, orderID, traceTs string) []byte {
	t.Helper()
	op := reg.LookupOperation("dophi.order.OrderService", "CreateOrder")
	if op == nil || op.RspDesc == nil {
		t.Fatalf("OrderService.CreateOrder descriptor missing")
	}
	msg := dynamicpb.NewMessage(op.RspDesc)
	msg.Set(op.RspDesc.Fields().ByName("order_id"), protoreflect.ValueOfString(orderID))
	msg.Set(op.RspDesc.Fields().ByName("trace_ts"), protoreflect.ValueOfString(traceTs))
	b, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal dynamicpb failed: %v", err)
	}
	return b
}

func TestReplayScheduler_FailClosed_Rediff_CancelGuard_AndSSE_ADR31(t *testing.T) {
	db := setupIsolatedDiffDB(t)
	reg := buildOrderProtoDecoder(t, db)

	// 注册独立 hub SSE 订阅验证回放 SSE 事件推流
	sseBroker := hub.NewSSEBroker()
	hub.SetGlobalSSEBroker(sseBroker)
	subID, sseCh := sseBroker.SubscribeForUser(100)
	defer sseBroker.Unsubscribe(subID)

	var agentResp agentproto.ReplayTaskResultResp
	var executeStatus int = http.StatusOK
	var executeBody map[string]any
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/agent/v1/replays/execute":
			if executeStatus != http.StatusOK {
				w.WriteHeader(executeStatus)
				_ = json.NewEncoder(w).Encode(executeBody)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ARMED"})
		case r.Method == http.MethodGet && r.URL.Path[:17] == "/agent/v1/replays":
			_ = json.NewEncoder(w).Encode(agentResp)
		case r.Method == http.MethodPost && r.URL.Path[:17] == "/agent/v1/replays":
			_ = json.NewEncoder(w).Encode(map[string]any{"cancelled": true})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer agentSrv.Close()

	// 基础建模数据
	pgrp := models.ArchProcessGroup{Code: "PG_ADR31", Name: "ADR31组"}
	db.Create(&pgrp)
	archProc := models.ArchProcess{GroupID: pgrp.ID, Code: "ARCH_ADR31", Name: "订单进程", DefaultListenPort: 19001}
	db.Create(&archProc)
	sub := models.Subsystem{Code: "SUB_ADR31", Name: "ADR31子系统", CreatedBy: 100}
	db.Create(&sub)
	grp := models.FunctionalGroup{SubsystemID: sub.ID, Code: "GRP_ADR31", Name: "ADR31功能组", Level: 1, CreatedBy: 100}
	db.Create(&grp)
	mod := models.Module{SubsystemID: sub.ID, GroupID: grp.ID, ArchProcessID: &archProc.ID, Code: "MOD_ADR31", Name: "订单模块", CreatedBy: 100}
	db.Create(&mod)
	db.Create(&models.ModuleService{ModuleID: &mod.ID, ServiceName: "dophi.order.OrderService"})

	userA := uint(100)
	agt := models.Agent{
		Name:      "agt-adr31",
		Endpoint:  agentSrv.URL,
		Scope:     models.ScopeShared,
		Status:    models.AgentStatusOnline,
		CreatedBy: userA,
	}
	db.Create(&agt)
	proc := models.Process{
		AgentID:             agt.ID,
		ArchProcessID:       archProc.ID,
		EffectiveListenPort: 19001,
		ProbeDesiredState:   models.ProbeDesiredStateArmed,
		ProbeStatus:         models.ProbeStatusReady,
		CreatedBy:           userA,
	}
	db.Create(&proc)

	snapBytes, _ := json.Marshal([]models.ProcessSnapshotItem{{
		ProcessID: proc.ID, ArchProcessID: archProc.ID, Code: archProc.Code, Name: archProc.Name, ModuleIDs: []uint64{mod.ID}, ListenPort: 19001,
	}})
	sess := models.RecordingSession{
		SessionNo:          "SESS_ADR31",
		Name:               "ADR31 测试录制",
		Scope:              models.ScopeShared,
		OwnerID:            userA,
		Status:             models.SessionStatusCompleted,
		TargetProcessIDs:   datatypes.JSON([]byte("[]")),
		LeasedAgentIDs:     datatypes.JSON([]byte("[]")),
		ProcessSnapshots:   datatypes.JSON(snapBytes),
		EndpointSnapshots:  datatypes.JSON([]byte("[]")),
		CSGSnapshots:       datatypes.JSON([]byte("[]")),
		ProcessDiagnostics: datatypes.JSON([]byte("[]")),
		NoiseExcludeRules:  datatypes.JSON([]byte("[]")),
		CreatedBy:          userA,
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("create recording session failed: %v", err)
	}

	now := time.Now().UTC()
	_, _ = (&models.PartitionManager{}).EnsureDailyPartition(db, now)

	var trace1 [16]byte
	basePl := marshalOrderRspPayload(t, reg, "ORD-1", "100")
	mutPl := marshalOrderRspPayload(t, reg, "ORD-1", "999")
	baseRsp := buildRspPacketWithAllTiming(1001, 0, trace1, 1, 2, 3, 4, basePl)
	mutRsp := buildRspPacketWithAllTiming(1001, 0, trace1, 1, 2, 3, 4, mutPl)

	recPass := models.TrafficRecord{
		CreatedAt: now, SessionID: sess.ID, SeqNo: 1, AgentID: agt.ID, ProcessID: proc.ID,
		Direction: models.TrafficDirIngress, ExchangeType: models.ExchangeUnary,
		SNO: 1001, ServiceName: "dophi.order.OrderService", Operation: "CreateOrder",
		ReqCapturedAtNs: now.UnixNano(), RawReqPacket: []byte("REQ1"), RawRspPacket: baseRsp,
		IsReplayable: true, HeadersJSON: datatypes.JSON(`{}`), PayloadJSON: datatypes.JSON(`{}`),
	}
	recMissHex := models.TrafficRecord{
		CreatedAt: now, SessionID: sess.ID, SeqNo: 2, AgentID: agt.ID, ProcessID: proc.ID,
		Direction: models.TrafficDirIngress, ExchangeType: models.ExchangeUnary,
		SNO: 1002, ServiceName: "dophi.order.OrderService", Operation: "CreateOrder",
		ReqCapturedAtNs: now.UnixNano() + 100, RawReqPacket: []byte("REQ2"), RawRspPacket: baseRsp,
		IsReplayable: true, HeadersJSON: datatypes.JSON(`{}`), PayloadJSON: datatypes.JSON(`{}`),
	}
	recOneway := models.TrafficRecord{
		CreatedAt: now, SessionID: sess.ID, SeqNo: 3, AgentID: agt.ID, ProcessID: proc.ID,
		Direction: models.TrafficDirIngress, ExchangeType: models.ExchangeOneway,
		SNO: 1003, ServiceName: "dophi.order.OrderService", Operation: "CreateOrder",
		ReqCapturedAtNs: now.UnixNano() + 200, RawReqPacket: []byte("REQ3"), RawRspPacket: nil,
		IsReplayable: true, HeadersJSON: datatypes.JSON(`{}`), PayloadJSON: datatypes.JSON(`{}`),
	}
	db.Create(&recPass)
	db.Create(&recMissHex)
	db.Create(&recOneway)

	sched := NewReplayScheduler(db, reg)

	// 1. Fail-Closed: 空白 actual_rsp_hex 不得回退自比较为 PASS，必须判为 ERROR (FailedLayer=1)
	agentResp = agentproto.ReplayTaskResultResp{
		TaskID: 1,
		Status: models.ReplayResultPass,
		Items: []agentproto.ReplayItemDetail{
			{RecordID: recPass.ID, SeqNo: 1, Status: "PASS", ActualRspHex: hex.EncodeToString(mutRsp), LatencyUs: 320, MockTrace: []agentproto.MockTraceEvent{{RecordID: recPass.ID, ServiceName: "dophi.stock", Operation: "Lock", HitKind: "EXACT_HASH", SNO: "2001"}}},
			{RecordID: recMissHex.ID, SeqNo: 2, Status: "PASS", ActualRspHex: "", LatencyUs: 10},
			{RecordID: recOneway.ID, SeqNo: 3, Status: "PASS", ActualRspHex: "", LatencyUs: 5},
		},
	}

	task, err := sched.CreateAndExecuteTask(context.Background(), CreateReplayTaskReq{
		Name: "FailClosed验证", SessionID: sess.ID, SourceProcessID: proc.ID, TargetProcessID: proc.ID,
		SelectedRecordIDs: []uint64{recPass.ID, recMissHex.ID, recOneway.ID}, TimeoutMs: 3000,
	}, userA, false)
	if err != nil {
		t.Fatalf("CreateAndExecuteTask failed: %v", err)
	}

	// 轮询等待 COMPLETED
	var doneTask models.ReplayTask
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		_ = db.First(&doneTask, task.ID).Error
		if doneTask.LifecycleState == "COMPLETED" {
			break
		}
	}
	if doneTask.LifecycleState != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s", doneTask.LifecycleState)
	}
	if doneTask.ResultStatus != models.ReplayResultError {
		t.Fatalf("expected Fail-Closed ERROR overall status, got %s", doneTask.ResultStatus)
	}

	var items []models.ReplayItem
	db.Where("task_id = ?", task.ID).Order("seq_no ASC").Find(&items)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].ResultStatus != models.ReplayResultFailed || items[0].FailedLayer != 2 || len(items[0].ActualPayload) == 0 {
		t.Fatalf("expected item[0] FAILED at layer 2 with ActualPayload decoded, got status=%s layer=%d payload=%s", items[0].ResultStatus, items[0].FailedLayer, string(items[0].ActualPayload))
	}
	if items[1].ResultStatus != models.ReplayResultError || items[1].FailedLayer != 1 || items[1].ErrorMessage == "" {
		t.Fatalf("expected item[1] empty hex Fail-Closed ERROR at layer 1, got status=%s layer=%d err=%s", items[1].ResultStatus, items[1].FailedLayer, items[1].ErrorMessage)
	}
	if items[2].ResultStatus != models.ReplayResultPass {
		t.Fatalf("expected ONEWAY item[2] PASS, got %s", items[2].ResultStatus)
	}

	// 验证 SSE 事件推流 (replay.item_done & replay.completed)
	gotItemDone, gotCompleted := 0, false
	drainDeadline := time.After(500 * time.Millisecond)
drainLoop:
	for {
		select {
		case evt := <-sseCh:
			if evt.Event == "replay.item_done" && evt.TaskID == task.ID {
				gotItemDone++
			}
			if evt.Event == "replay.completed" && evt.TaskID == task.ID {
				gotCompleted = true
			}
		case <-drainDeadline:
			break drainLoop
		}
	}
	if gotItemDone != 3 || !gotCompleted {
		t.Fatalf("expected 3 replay.item_done and 1 replay.completed SSE events, got itemDone=%d completed=%v", gotItemDone, gotCompleted)
	}

	// 2. Anti-IDOR ListItems & AgentID ListTasks & RecordID filter
	if _, _, err := sched.ListItems(context.Background(), task.ID, ReplayItemListQuery{}, 999, false); err == nil {
		t.Fatalf("expected ListItems 403 for unrelated user")
	}
	filteredItems, fTotal, err := sched.ListItems(context.Background(), task.ID, ReplayItemListQuery{RecordID: recPass.ID}, userA, false)
	if err != nil || fTotal != 1 || len(filteredItems) != 1 || filteredItems[0].ID != items[0].ID {
		t.Fatalf("expected RecordID filter to return item[0], got err=%v total=%d", err, fTotal)
	}
	listedTasks, tTotal, err := sched.ListTasks(context.Background(), ReplayTaskListQuery{AgentID: agt.ID}, userA, false)
	if err != nil || tTotal == 0 || len(listedTasks) == 0 {
		t.Fatalf("expected ListTasks AgentID filter to return tasks, got err=%v total=%d", err, tTotal)
	}

	// 3. RediffTask: 忽略 trace_ts 使 item[0] 转 PASS，但严禁洗白 item[1] ERROR
	rediffed, err := sched.RediffTask(context.Background(), task.ID, []string{"trace_ts"}, userA, false)
	if err != nil {
		t.Fatalf("RediffTask failed: %v", err)
	}
	if rediffed.ResultStatus != models.ReplayResultError {
		t.Fatalf("expected RediffTask to preserve ERROR item[1] so task stays ERROR, got %s", rediffed.ResultStatus)
	}
	var afterItems []models.ReplayItem
	db.Where("task_id = ?", task.ID).Order("seq_no ASC").Find(&afterItems)
	if afterItems[0].ResultStatus != models.ReplayResultPass {
		t.Fatalf("expected item[0] to become PASS after ignoring trace_ts, got %s", afterItems[0].ResultStatus)
	}
	if afterItems[1].ResultStatus != models.ReplayResultError {
		t.Fatalf("expected item[1] ERROR preserved after RediffTask, got %s", afterItems[1].ResultStatus)
	}

	// 4. CANCELLED 防并发覆写守卫
	cancelTask := &models.ReplayTask{
		TaskNo: "REP_CANCEL_GUARD", Name: "取消守卫测试", OwnerID: userA, SessionID: &sess.ID,
		ArchProcessID: archProc.ID, AgentID: agt.ID, LifecycleState: "CANCELLED",
	}
	db.Create(cancelTask)
	sched.markTaskFailed(cancelTask.ID, "late failure")
	var reloadedCancel models.ReplayTask
	db.First(&reloadedCancel, cancelTask.ID)
	if reloadedCancel.LifecycleState != "CANCELLED" {
		t.Fatalf("expected CANCELLED state not overwritten by markTaskFailed, got %s", reloadedCancel.LifecycleState)
	}

	// 5. 409 结构化错误透传
	executeStatus = http.StatusConflict
	executeBody = map[string]any{"code": "PROCESS_NOT_ARMED", "status": "DISARMED", "error": "探针未布防"}
	if err := postExecuteReplayToAgent(context.Background(), http.DefaultClient, &agt, agentproto.ExecuteReplayReq{TaskID: 99}); err == nil {
		t.Fatalf("expected 409 error from postExecuteReplayToAgent")
	} else {
		var csgErr *CSGConflictError
		if !errors.As(err, &csgErr) || csgErr.Code != "PROCESS_NOT_ARMED" {
			t.Fatalf("expected CSGConflictError(PROCESS_NOT_ARMED), got %T %v", err, err)
		}
	}
}
