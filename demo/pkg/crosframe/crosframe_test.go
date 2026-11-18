package crosframe

import (
	"context"
	"net"
	"testing"
	"time"

	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgmock"
)

func TestBuildPacketValidation(t *testing.T) {
	trace := &crosrpc.TraceKeyExt{
		TraceID:      0x1122334455667788,
		SpanID:       0x0102030405060708,
		FatherSpanID: 0,
		RecordSwitch: 1,
	}
	payload := []byte("hello cros rpc payload")

	// 1. 测试请求包构建
	reqPkt := BuildReqPacket(2001, "demo.order.OrderService", "CreateOrder", trace, payload)
	parsedReq, err := crosrpc.ParsePacket(reqPkt)
	if err != nil {
		t.Fatalf("ParsePacket for Req failed: %v", err)
	}
	if parsedReq.Comm.SNO != 2001 {
		t.Fatalf("SNO expected 2001, got %d", parsedReq.Comm.SNO)
	}
	if parsedReq.Req == nil || parsedReq.Req.ServiceName != "demo.order.OrderService" || parsedReq.Req.Operation != "CreateOrder" {
		t.Fatalf("Req header mismatch: %+v", parsedReq.Req)
	}
	if parsedReq.Trace == nil || parsedReq.Trace.TraceID != trace.TraceID || parsedReq.Trace.SpanID != trace.SpanID {
		t.Fatalf("TraceKey header mismatch: %+v", parsedReq.Trace)
	}
	extractedReqPayload := reqPkt[parsedReq.PayloadOffset:]
	if string(extractedReqPayload) != string(payload) {
		t.Fatalf("Payload mismatch: expected %s, got %s", payload, extractedReqPayload)
	}

	// 2. 测试应答包构建
	rspPkt := BuildRspPacket(2001, "demo.order.OrderService", "CreateOrder", 0, trace, payload)
	parsedRsp, err := crosrpc.ParsePacket(rspPkt)
	if err != nil {
		t.Fatalf("ParsePacket for Rsp failed: %v", err)
	}
	if parsedRsp.Comm.SNO != 2001 {
		t.Fatalf("SNO expected 2001, got %d", parsedRsp.Comm.SNO)
	}
	if parsedRsp.Rsp == nil || parsedRsp.Rsp.ServiceName != "demo.order.OrderService" || parsedRsp.Rsp.StatusCode != 0 {
		t.Fatalf("Rsp header mismatch: %+v", parsedRsp.Rsp)
	}
	if parsedRsp.Trace == nil || parsedRsp.Trace.TraceID != trace.TraceID {
		t.Fatalf("TraceKey in Rsp mismatch: %+v", parsedRsp.Trace)
	}
	extractedRspPayload := rspPkt[parsedRsp.PayloadOffset:]
	if string(extractedRspPayload) != string(payload) {
		t.Fatalf("Payload mismatch: expected %s, got %s", payload, extractedRspPayload)
	}
}

func TestServerAndClientEndToEnd(t *testing.T) {
	// 动态申请本地空闲端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := NewServer(addr, "TestEchoServer")
	srv.Register("demo.test-EchoService", "Echo", func(ctx context.Context, packet *crosrpc.ParsedPacket, reqPayload []byte) (uint32, []byte, error) {
		// 返回前缀 + reqPayload
		rsp := append([]byte("ECHO:"), reqPayload...)
		return 0, rsp, nil
	})

	if err := srv.Start(); err != nil {
		t.Fatalf("Server start failed: %v", err)
	}
	defer func() { _ = srv.Stop() }()

	client := NewClient(addr, 2*time.Second)
	defer func() { _ = client.Close() }()

	trace := &crosrpc.TraceKeyExt{
		TraceID: 0x9999,
		SpanID:  0x1,
	}

	rspPkt, rspPayload, err := client.Invoke(context.Background(), "demo.test-EchoService", "Echo", trace, []byte("Ping"))
	if err != nil {
		t.Fatalf("Client.Invoke failed: %v", err)
	}

	if rspPkt.Rsp.StatusCode != 0 {
		t.Fatalf("Expected status code 0, got %d", rspPkt.Rsp.StatusCode)
	}
	if string(rspPayload) != "ECHO:Ping" {
		t.Fatalf("Expected ECHO:Ping, got %s", string(rspPayload))
	}
	if rspPkt.Trace == nil || rspPkt.Trace.TraceID != 0x9999 {
		t.Fatalf("Expected trace 0x9999, got %+v", rspPkt.Trace)
	}
}

func TestCSGServerAndClient_PubSubFailoverAndCleanup(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()

	svcName := "demo.test.EchoCSGService"

	srvA := NewServer("127.0.0.1:0", "CSGEchoA")
	srvA.SetCSGAddr(mockCSG.Addr())
	srvA.Register(svcName, "Echo", func(ctx context.Context, packet *crosrpc.ParsedPacket, reqPayload []byte) (uint32, []byte, error) {
		return 0, append([]byte("A:"), reqPayload...), nil
	})
	if err := srvA.Start(); err != nil {
		t.Fatalf("srvA.Start failed: %v", err)
	}

	cli := NewCSGClient(mockCSG.Addr(), svcName, "", 2*time.Second)

	snap1 := mockCSG.Snapshot()
	if snap1.ServerNum() != 1 || snap1.ClientNum() != 1 {
		t.Fatalf("expected 1 server and 1 client subscription in CSG, got servers=%d clients=%d", snap1.ServerNum(), snap1.ClientNum())
	}

	_, rspA, err := cli.Invoke(context.Background(), svcName, "Echo", nil, []byte("hello"))
	if err != nil {
		t.Fatalf("cli.Invoke srvA failed: %v", err)
	}
	if string(rspA) != "A:hello" {
		t.Fatalf("expected A:hello, got %q", string(rspA))
	}

	// Simulate backend A going offline and backend B taking over on a new port
	_ = srvA.Stop()
	srvB := NewServer("127.0.0.1:0", "CSGEchoB")
	srvB.SetCSGAddr(mockCSG.Addr())
	srvB.Register(svcName, "Echo", func(ctx context.Context, packet *crosrpc.ParsedPacket, reqPayload []byte) (uint32, []byte, error) {
		return 0, append([]byte("B:"), reqPayload...), nil
	})
	if err := srvB.Start(); err != nil {
		t.Fatalf("srvB.Start failed: %v", err)
	}
	defer func() { _ = srvB.Stop() }()

	_, rspB, err := cli.Invoke(context.Background(), svcName, "Echo", nil, []byte("failover"))
	if err != nil {
		t.Fatalf("cli.Invoke failover to srvB failed: %v", err)
	}
	if string(rspB) != "B:failover" {
		t.Fatalf("expected B:failover after CSG re-resolve, got %q", string(rspB))
	}

	_ = cli.Close()
	_ = srvB.Stop()

	snap2 := mockCSG.Snapshot()
	if snap2.ServerNum() != 0 || snap2.ClientNum() != 0 {
		t.Fatalf("expected 0 servers and 0 clients after Stop/Close, got servers=%d clients=%d", snap2.ServerNum(), snap2.ClientNum())
	}
}
