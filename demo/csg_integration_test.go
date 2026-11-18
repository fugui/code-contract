package demo_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code-contract/demo/pkg/crosframe"
	"code-contract/demo/pkg/payment"
	"code-contract/demo/pkg/proto/accountpb"
	"code-contract/demo/pkg/proto/inventorypb"
	"code-contract/demo/pkg/proto/orderpb"
	"code-contract/demo/pkg/proto/paymentpb"
	"code-contract/internal/agent/csg"
	"code-contract/pkg/agentproto"
	"code-contract/pkg/crosrpc"
	"code-contract/pkg/csgmock"
	"code-contract/pkg/ebpf"
	"code-contract/pkg/replay"
	"code-contract/pkg/rpcmon"
	"google.golang.org/protobuf/proto"
)

func newOrderCSGTestOrchestrator(csgAddr string, paySvcNames map[string]string) *orderTestOrchestrator {
	pClis := make(map[string]*crosframe.Client, len(paySvcNames))
	pSvcs := make(map[string]string, len(paySvcNames))
	for ch, svc := range paySvcNames {
		pClis[ch] = crosframe.NewCSGClient(csgAddr, svc, "", 2*time.Second)
		pSvcs[ch] = svc
	}
	return &orderTestOrchestrator{
		accountCli:     crosframe.NewCSGClient(csgAddr, "demo.account.AccountService", "", 2*time.Second),
		inventoryCli:   crosframe.NewCSGClient(csgAddr, "demo.inventory.InventoryService", "", 2*time.Second),
		paymentClis:    pClis,
		paymentSvcName: pSvcs,
		orders:         make(map[string]*orderTestRecord),
	}
}

func TestDemoCSGCluster_PubSubRpcmonAndDiscoverAndFuse(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()
	if err := mockCSG.StartMonitor("127.0.0.1:0"); err != nil {
		t.Fatalf("mockCSG.StartMonitor failed: %v", err)
	}

	// 1. 启动 4 个下游服务端并自动向 CSG PUB
	accStore := newAccountTestStore()
	accSrv := crosframe.NewServer("127.0.0.1:0", "CSGAccount")
	accSrv.SetCSGAddr(mockCSG.Addr())
	accStore.Register(accSrv)
	if err := accSrv.Start(); err != nil {
		t.Fatalf("accSrv.Start failed: %v", err)
	}
	defer func() { _ = accSrv.Stop() }()

	invStore := newInventoryTestStore()
	invSrv := crosframe.NewServer("127.0.0.1:0", "CSGInventory")
	invSrv.SetCSGAddr(mockCSG.Addr())
	invStore.Register(invSrv)
	if err := invSrv.Start(); err != nil {
		t.Fatalf("invSrv.Start failed: %v", err)
	}
	defer func() { _ = invSrv.Stop() }()

	payAlipayGW := payment.NewPaymentGateway(payment.ChannelAlipay, "")
	payAlipaySrv := crosframe.NewServer("127.0.0.1:0", "CSGPayAlipay")
	payAlipaySrv.SetCSGAddr(mockCSG.Addr())
	payAlipayGW.RegisterHandlers(payAlipaySrv)
	if err := payAlipaySrv.Start(); err != nil {
		t.Fatalf("payAlipaySrv.Start failed: %v", err)
	}
	defer func() { _ = payAlipaySrv.Stop() }()

	payWechatGW := payment.NewPaymentGateway(payment.ChannelWechat, "")
	payWechatSrv := crosframe.NewServer("127.0.0.1:0", "CSGPayWechat")
	payWechatSrv.SetCSGAddr(mockCSG.Addr())
	payWechatGW.RegisterHandlers(payWechatSrv)
	if err := payWechatSrv.Start(); err != nil {
		t.Fatalf("payWechatSrv.Start failed: %v", err)
	}
	defer func() { _ = payWechatSrv.Stop() }()

	// 2. 启动 SUT (OrderService) 并通过 CSG SUB 连接 4 个下游
	sutOrchestrator := newOrderCSGTestOrchestrator(mockCSG.Addr(), map[string]string{
		payment.ChannelAlipay: payAlipayGW.ServiceName(),
		payment.ChannelWechat: payWechatGW.ServiceName(),
	})
	sutSrv := crosframe.NewServer("127.0.0.1:0", "CSGSUT")
	sutSrv.SetCSGAddr(mockCSG.Addr())
	sutOrchestrator.Register(sutSrv)
	if err := sutSrv.Start(); err != nil {
		t.Fatalf("sutSrv.Start failed: %v", err)
	}
	defer func() {
		_ = sutSrv.Stop()
		_ = sutOrchestrator.accountCli.Close()
		_ = sutOrchestrator.inventoryCli.Close()
		for _, cli := range sutOrchestrator.paymentClis {
			_ = cli.Close()
		}
	}()

	// 3. 启动纯客户端 Client 并经 CSG SUB 发现 OrderService
	client := crosframe.NewCSGClient(mockCSG.Addr(), "demo.order.OrderService", "", 3*time.Second)
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	trace := &crosrpc.TraceKeyExt{TraceID: 0xAA01, SpanID: 1, RecordSwitch: 1}

	// 验证 RPCMonitorService 可完整观测到 5 个已发布服务与 5 条订阅关系
	monBackend, err := rpcmon.NewCrosTCP(mockCSG.MonitorAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewCrosTCP failed: %v", err)
	}
	defer monBackend.Close()

	monRep, err := rpcmon.Fetch(ctx, monBackend, "all")
	if err != nil {
		t.Fatalf("rpcmon.Fetch failed: %v", err)
	}
	if monRep.ServerNum != 5 || monRep.ClientNum != 5 {
		t.Fatalf("expected 5 servers & 5 client subscriptions, got servers=%d clients=%d (%+v)",
			monRep.ServerNum, monRep.ClientNum, monRep)
	}

	// 执行正向下单与补偿流，验证全链路通过 CSG 动态服务发现完成
	quoteReq, _ := proto.Marshal(&orderpb.CompositeQuoteReq{UserId: "Alice", SkuId: "SKU-101"})
	_, quoteRspB, err := client.Invoke(ctx, "demo.order.OrderService", "GetCompositeQuote", trace, quoteReq)
	if err != nil {
		t.Fatalf("GetCompositeQuote over CSG failed: %v", err)
	}
	var quoteRsp orderpb.CompositeQuoteRsp
	_ = proto.Unmarshal(quoteRspB, &quoteRsp)
	if quoteRsp.Code != 0 || quoteRsp.BalanceCents != 100000 || quoteRsp.AvailableStock != 10 {
		t.Fatalf("unexpected GetCompositeQuote rsp: %+v", &quoteRsp)
	}

	ordReq, _ := proto.Marshal(&orderpb.CreateOrderReq{UserId: "Alice", SkuId: "SKU-101", Quantity: 1, PaymentChannel: "WECHAT"})
	_, ordRspB, err := client.Invoke(ctx, "demo.order.OrderService", "CreateOrder", trace, ordReq)
	if err != nil {
		t.Fatalf("CreateOrder (WECHAT) over CSG failed: %v", err)
	}
	var ordRsp orderpb.CreateOrderRsp
	_ = proto.Unmarshal(ordRspB, &ordRsp)
	if ordRsp.Code != 0 || ordRsp.Status != "SUCCESS" || ordRsp.PaymentChannel != "WECHAT" {
		t.Fatalf("unexpected CreateOrder rsp: %+v", &ordRsp)
	}

	// 验证 Agent DiscoverAndFuse 无需手动配置 MonitorAddr 即可通过 dlink Sub 自动解析 RPCMonitorService 并完成双层融合
	discRep, fusedProcs, err := csg.DiscoverAndFuse(ctx, csg.DiscovererConfig{
		CSGAddr:   mockCSG.Addr(),
		LocalIPs:  []string{"127.0.0.1"},
		SubEnrich: true,
	})
	if err != nil {
		t.Fatalf("DiscoverAndFuse failed: %v", err)
	}
	if discRep.DiscoveryMode != "CROSTCP_RPCMON" {
		t.Fatalf("expected DiscoveryMode CROSTCP_RPCMON via dlink auto-resolve, got %q (errors=%v)", discRep.DiscoveryMode, discRep.Errors)
	}
	if len(discRep.Services) != 5 || len(discRep.Subscriptions) != 5 {
		t.Fatalf("expected 5 services and 5 subscriptions in fused report, got %d/%d", len(discRep.Services), len(discRep.Subscriptions))
	}
	for _, s := range discRep.Services {
		if !s.ResolvedBySub {
			t.Fatalf("expected service %s to have ResolvedBySub=true", s.ServiceName)
		}
	}
	if len(fusedProcs) == 0 {
		t.Fatalf("expected non-empty fused processes")
	}
}

func TestDemoCSG_ThreeModeSimulation_ClientInject_PubStub_MockServer(t *testing.T) {
	mockCSG := csgmock.NewMockCSGServer("127.0.0.1:0")
	if err := mockCSG.Start(); err != nil {
		t.Fatalf("mockCSG.Start failed: %v", err)
	}
	defer mockCSG.Close()
	if err := mockCSG.StartMonitor("127.0.0.1:0"); err != nil {
		t.Fatalf("mockCSG.StartMonitor failed: %v", err)
	}

	// 启动真实下游
	accStore := newAccountTestStore()
	accSrv := crosframe.NewServer("127.0.0.1:0", "SimAccount")
	accSrv.SetCSGAddr(mockCSG.Addr())
	accStore.Register(accSrv)
	if err := accSrv.Start(); err != nil {
		t.Fatalf("accSrv.Start failed: %v", err)
	}
	accStopped := false
	defer func() {
		if !accStopped {
			_ = accSrv.Stop()
		}
	}()

	invStore := newInventoryTestStore()
	invSrv := crosframe.NewServer("127.0.0.1:0", "SimInventory")
	invSrv.SetCSGAddr(mockCSG.Addr())
	invStore.Register(invSrv)
	if err := invSrv.Start(); err != nil {
		t.Fatalf("invSrv.Start failed: %v", err)
	}
	invStopped := false
	defer func() {
		if !invStopped {
			_ = invSrv.Stop()
		}
	}()

	payAlipayGW := payment.NewPaymentGateway(payment.ChannelAlipay, "")
	payAlipaySrv := crosframe.NewServer("127.0.0.1:0", "SimPayAlipay")
	payAlipaySrv.SetCSGAddr(mockCSG.Addr())
	payAlipayGW.RegisterHandlers(payAlipaySrv)
	if err := payAlipaySrv.Start(); err != nil {
		t.Fatalf("payAlipaySrv.Start failed: %v", err)
	}
	payStopped := false
	defer func() {
		if !payStopped {
			_ = payAlipaySrv.Stop()
		}
	}()

	// 启动真实 SUT (OrderService)
	sutOrch := newOrderCSGTestOrchestrator(mockCSG.Addr(), map[string]string{
		payment.ChannelAlipay: payAlipayGW.ServiceName(),
	})
	sutSrv := crosframe.NewServer("127.0.0.1:0", "SimSUT")
	sutSrv.SetCSGAddr(mockCSG.Addr())
	sutOrch.Register(sutSrv)
	if err := sutSrv.Start(); err != nil {
		t.Fatalf("sutSrv.Start failed: %v", err)
	}
	defer func() {
		_ = sutSrv.Stop()
		_ = sutOrch.accountCli.Close()
		_ = sutOrch.inventoryCli.Close()
		for _, cli := range sutOrch.paymentClis {
			_ = cli.Close()
		}
	}()

	ctx := context.Background()
	trace := &crosrpc.TraceKeyExt{TraceID: 0xBB01, SpanID: 1, RecordSwitch: 1}
	createOrdPayload, _ := proto.Marshal(&orderpb.CreateOrderReq{
		UserId:         "Alice",
		SkuId:          "SKU-101",
		Quantity:       1,
		PaymentChannel: "ALIPAY",
	})
	ingressCreateOrder := crosframe.BuildReqPacket(5001, "demo.order.OrderService", "CreateOrder", trace, createOrdPayload)

	// === 模式 1: CLIENT_INJECT（ProcessSpec.ListenPort 故意为过期端口 19999，依靠 CSG 动态解析 SUT 实际端口） ===
	t.Run("Mode1_CLIENT_INJECT_DynamicPortResolution", func(t *testing.T) {
		simCtrl := csg.NewSimController()
		simOut, err := simCtrl.Prepare(ctx, csg.SimPrepareInput{
			Strategy:       agentproto.CSGStrategyClientInject,
			CSGAddr:        mockCSG.Addr(),
			ListenPort:     19999,
			TargetServices: []string{"demo.order.OrderService"},
			Timeout:        2 * time.Second,
		})
		if err != nil {
			t.Fatalf("Prepare CLIENT_INJECT failed: %v", err)
		}
		defer func() { _ = simCtrl.Cleanup() }()

		expectedAddr := sutSrv.Addr().String()
		if simOut.TargetDialAddr != expectedAddr {
			t.Fatalf("TargetDialAddr = %q, want %q", simOut.TargetDialAddr, expectedAddr)
		}

		exec := replay.NewReplayExecutor()
		exec.SetTargetDialOverride(simOut.TargetDialAddr)
		res, err := exec.Execute(ctx, agentproto.ExecuteReplayReq{
			TaskID: 1001,
			ProcessSpec: agentproto.ProcessMonitorSpec{
				ProcessID:  1,
				ListenPort: 19999,
			},
			TimeoutMs:       3000,
			CSGStrategy:     agentproto.CSGStrategyClientInject,
			IngressRequests: [][]byte{ingressCreateOrder},
		})
		if err != nil {
			t.Fatalf("ReplayExecutor CLIENT_INJECT failed: %v", err)
		}
		if res.Status != "PASS" || res.PassedRequests != 1 {
			t.Fatalf("expected CLIENT_INJECT PASS 1/1, got %+v", res)
		}
	})

	// === 模式 2: PUB_STUB（先在线冲突返 409，下线真实后端后由 Agent 发布 Stub 并执行 ullSNO 自愈，结束后 0 残留） ===
	t.Run("Mode2_PUB_STUB_Conflict409_ThenOfflineMockHealAndCleanup", func(t *testing.T) {
		exec := replay.NewReplayExecutor()
		mockPort, err := exec.EnsureMockListener()
		if err != nil {
			t.Fatalf("EnsureMockListener failed: %v", err)
		}

		downstreamSvcs := []string{
			"demo.inventory.InventoryService",
			"demo.account.AccountService",
			payAlipayGW.ServiceName(),
		}

		// 2.1 下游在线时必须触发 409 CSG_SERVICE_ALREADY_PUBLISHED
		conflictCtrl := csg.NewSimController()
		_, err = conflictCtrl.Prepare(ctx, csg.SimPrepareInput{
			Strategy:       agentproto.CSGStrategyPubStub,
			CSGAddr:        mockCSG.Addr(),
			MockDataPort:   mockPort,
			TargetServices: downstreamSvcs,
			Timeout:        2 * time.Second,
		})
		_ = conflictCtrl.Cleanup()
		if !errors.Is(err, csg.ErrServiceAlreadyPublished) {
			t.Fatalf("expected ErrServiceAlreadyPublished (409) when backends online, got %v", err)
		}

		// 2.2 关闭所有真实后端（模拟离线），验证 CSG 注册表已清空
		_ = accSrv.Stop()
		accStopped = true
		_ = invSrv.Stop()
		invStopped = true
		_ = payAlipaySrv.Stop()
		payStopped = true

		// 2.3 重新发起 PUB_STUB 筹备并下发带 DstTag 的录制 Mock 规则
		pubCtrl := csg.NewSimController()
		_, err = pubCtrl.Prepare(ctx, csg.SimPrepareInput{
			Strategy:       agentproto.CSGStrategyPubStub,
			CSGAddr:        mockCSG.Addr(),
			MockDataPort:   mockPort,
			TargetServices: downstreamSvcs,
			Timeout:        2 * time.Second,
		})
		if err != nil {
			exec.CloseMockListener()
			t.Fatalf("Prepare PUB_STUB after backend offline failed: %v", err)
		}

		// 构造一次完整 CreateOrder 所需的 5 对出站录制规则（包含不同 SNO 与 DstTag）
		invStockReq, _ := proto.Marshal(&inventorypb.GetItemStockReq{SkuId: "SKU-101"})
		invStockRsp, _ := proto.Marshal(&inventorypb.GetItemStockRsp{Code: 0, SkuId: "SKU-101", SkuName: "机械键盘", UnitPriceCents: 29900, TotalStock: 10, AvailableStock: 10})
		invLockReq, _ := proto.Marshal(&inventorypb.LockStockReq{SkuId: "SKU-101", Quantity: 1, BizOrderId: "ORD-REC-1"})
		invLockRsp, _ := proto.Marshal(&inventorypb.LockStockRsp{Code: 0, LockId: "LCK-REC-1", LockedQuantity: 1})
		accDeductReq, _ := proto.Marshal(&accountpb.DeductBalanceReq{UserId: "Alice", AmountCents: 29900, BizOrderId: "ORD-REC-1"})
		accDeductRsp, _ := proto.Marshal(&accountpb.DeductBalanceRsp{Code: 0, UserId: "Alice", RemainingBalance: 70100, TxSerialNo: "TX-REC-1"})
		payAuthReq, _ := proto.Marshal(&paymentpb.AuthorizePayReq{OrderId: "ORD-REC-1", UserId: "Alice", AmountCents: 29900, Channel: "ALIPAY"})
		payAuthRsp, _ := proto.Marshal(&paymentpb.AuthorizePayRsp{Code: 0, OrderId: "ORD-REC-1", Channel: "ALIPAY", ChannelTxNo: "ALIPAY-TX-REC-1"})
		invConfReq, _ := proto.Marshal(&inventorypb.ConfirmStockReq{LockId: "LCK-REC-1", BizOrderId: "ORD-REC-1"})
		invConfRsp, _ := proto.Marshal(&inventorypb.ConfirmStockRsp{Code: 0, RemainingStock: 9})

		scopedRules := []agentproto.MockRuleDownstream{
			{
				DstTag:     "backend-inventory",
				ReqPacket:  crosframe.BuildReqPacket(9101, "demo.inventory.InventoryService", "GetItemStock", trace, invStockReq),
				RspPackets: [][]byte{crosframe.BuildRspPacket(9101, "demo.inventory.InventoryService", "GetItemStock", 0, trace, invStockRsp)},
			},
			{
				DstTag:     "backend-inventory",
				ReqPacket:  crosframe.BuildReqPacket(9102, "demo.inventory.InventoryService", "LockStock", trace, invLockReq),
				RspPackets: [][]byte{crosframe.BuildRspPacket(9102, "demo.inventory.InventoryService", "LockStock", 0, trace, invLockRsp)},
			},
			{
				DstTag:     "backend-account",
				ReqPacket:  crosframe.BuildReqPacket(9103, "demo.account.AccountService", "DeductBalance", trace, accDeductReq),
				RspPackets: [][]byte{crosframe.BuildRspPacket(9103, "demo.account.AccountService", "DeductBalance", 0, trace, accDeductRsp)},
			},
			{
				DstTag:     "backend-payment-alipay",
				ReqPacket:  crosframe.BuildReqPacket(9104, payAlipayGW.ServiceName(), "AuthorizePay", trace, payAuthReq),
				RspPackets: [][]byte{crosframe.BuildRspPacket(9104, payAlipayGW.ServiceName(), "AuthorizePay", 0, trace, payAuthRsp)},
			},
			{
				DstTag:     "backend-inventory",
				ReqPacket:  crosframe.BuildReqPacket(9105, "demo.inventory.InventoryService", "ConfirmStock", trace, invConfReq),
				RspPackets: [][]byte{crosframe.BuildRspPacket(9105, "demo.inventory.InventoryService", "ConfirmStock", 0, trace, invConfRsp)},
			},
		}

		// 以全新 SNO 注入 CreateOrder 重放，迫使 SUT 断线重连至 CSG PUB_STUB MockPort
		freshReq := crosframe.BuildReqPacket(88888, "demo.order.OrderService", "CreateOrder", trace, createOrdPayload)
		sutPort := uint16(sutSrv.Addr().(*net.TCPAddr).Port)
		res, err := exec.Execute(ctx, agentproto.ExecuteReplayReq{
			TaskID: 2002,
			ProcessSpec: agentproto.ProcessMonitorSpec{
				ProcessID:  2,
				ListenPort: sutPort,
			},
			TimeoutMs:   4000,
			CSGStrategy: agentproto.CSGStrategyPubStub,
			IngressItems: []agentproto.ReplayIngressItem{
				{RecordID: 1, RawReq: freshReq},
			},
			ScopedEgressMocks: map[uint64][]agentproto.MockRuleDownstream{
				1: scopedRules,
			},
		})
		_ = pubCtrl.Cleanup()
		if err != nil {
			t.Fatalf("PUB_STUB replay failed: %v", err)
		}
		if res.Status != "PASS" || res.PassedRequests != 1 || res.MockHitCount < 5 || res.MockMissCount != 0 {
			t.Fatalf("expected PUB_STUB PASS with >=5 healed hits and 0 misses, got %+v", res)
		}
		// 校验退出清理后 CSG 无任何 Stub 残留
		for _, s := range mockCSG.Snapshot().Servers {
			if s.ServerName != "demo.order.OrderService" {
				t.Fatalf("expected 0 leftover downstream stubs in CSG after cleanup, found: %+v", s)
			}
		}
	})

	// === 模式 3: MOCK_SERVER（通过 EgressProxy 全隔离接管 dlink 控制面与数据面，真实 CSG 零连接泄漏） ===
	t.Run("Mode3_MOCK_SERVER_FullSandboxZeroRealCSGLeak", func(t *testing.T) {
		sentinelRealCSG, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen sentinel real CSG failed: %v", err)
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

		sentinelPort := uint16(sentinelRealCSG.Addr().(*net.TCPAddr).Port)
		simCtrl := csg.NewSimController()
		simOut, err := simCtrl.Prepare(ctx, csg.SimPrepareInput{
			Strategy: agentproto.CSGStrategyMockServer,
			CSGAddr:  sentinelRealCSG.Addr().String(),
			Snapshots: []agentproto.CSGSnapshotRecord{
				{
					ServiceName: "demo.account.AccountService",
					Addr:        "dophi_mesh://ipv4:127.0.0.1:39202",
					Transport:   "MESH",
					HostIP:      "127.0.0.1",
					Port:        39202,
				},
			},
			Timeout: 2 * time.Second,
		})
		if err != nil {
			t.Fatalf("Prepare MOCK_SERVER failed: %v", err)
		}
		defer func() { _ = simCtrl.Cleanup() }()

		// 搭建带 MockMatcher + SetCSGHandler 的 EgressProxy 模拟 eBPF 数据面分流
		matcher := replay.NewScopedMockMatcher()
		accBalReq, _ := proto.Marshal(&accountpb.GetBalanceReq{UserId: "Alice"})
		accBalRsp, _ := proto.Marshal(&accountpb.GetBalanceRsp{Code: 0, UserId: "Alice", UserName: "Alice", BalanceCents: 99900})
		matcher.AddGlobalFallback("demo.account.AccountService", "GetBalance", accBalReq, [][]byte{
			crosframe.BuildRspPacket(777, "demo.account.AccountService", "GetBalance", 0, trace, accBalRsp),
		})

		var useDataPlane atomic.Bool
		proxy, err := ebpf.NewEgressProxy(
			func(_ uint16) (uint64, uint32, net.IP, uint16, bool) {
				if useDataPlane.Load() {
					return 1, uint32(os.Getpid()), net.IPv4(127, 0, 0, 1), 39202, true
				}
				return 1, uint32(os.Getpid()), net.IPv4(127, 0, 0, 1), sentinelPort, true
			},
			nil,
		)
		if err != nil {
			t.Fatalf("NewEgressProxy failed: %v", err)
		}
		defer proxy.Close()

		proxy.SetCSGHandler(
			func(_ net.IP, port uint16) bool { return port == sentinelPort },
			func(conn net.Conn) { simOut.MockCSGServer.ServeConn(conn) },
		)
		proxy.SetMockMatcher(matcher, []agentproto.DownstreamEndpoint{{IP: "127.0.0.1", Port: 39202}})
		proxy.SetMode(ebpf.ModeReplayMock)

		proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxy.ProxyPort())

		// 1) 通过代理发起 CSG 服务发现（模拟 SUT 发起 dlink SUB）-> 命中沙箱 MockCSGServer 并改写 dophi_mesh -> tcp://
		cli := crosframe.NewCSGClient(proxyAddr, "demo.account.AccountService", "", 2*time.Second)
		if !strings.HasPrefix(cli.ResolvedAddr(), "127.0.0.1:39202") {
			_ = cli.Close()
			t.Fatalf("expected Mesh->TCP rewritten addr 127.0.0.1:39202 from sandbox MockCSGServer, got %q", cli.ResolvedAddr())
		}
		_ = cli.Close()

		// 2) 通过代理发起 CROS RPC 出站调用 -> 命中 ScopedMockMatcher 并完成 SNO 自愈
		useDataPlane.Store(true)
		dataCli := crosframe.NewClient(proxyAddr, 2*time.Second)
		defer func() { _ = dataCli.Close() }()
		pkt, rspBytes, err := dataCli.Invoke(ctx, "demo.account.AccountService", "GetBalance", trace, accBalReq)
		if err != nil {
			t.Fatalf("MOCK_SERVER data-plane Invoke failed: %v", err)
		}
		if pkt.Rsp.StatusCode != 0 {
			t.Fatalf("unexpected status code %d", pkt.Rsp.StatusCode)
		}
		var gotRsp accountpb.GetBalanceRsp
		_ = proto.Unmarshal(rspBytes, &gotRsp)
		if gotRsp.BalanceCents != 99900 {
			t.Fatalf("expected healed mock balance 99900, got %d", gotRsp.BalanceCents)
		}

		if err := simCtrl.Cleanup(); err != nil {
			t.Fatalf("MOCK_SERVER Cleanup failed: %v", err)
		}
		if sentinelAccepted.Load() != 0 {
			t.Fatalf("LEAK: real CSG sentinel received %d connections in MOCK_SERVER mode", sentinelAccepted.Load())
		}
		rep := simCtrl.Report()
		if !rep.CleanupCompleted || rep.RealCSGLeakCount != 0 || rep.MeshRewrites == 0 || len(rep.SandboxSubscribedSvcs) == 0 {
			t.Fatalf("unexpected MOCK_SERVER sandbox report: %+v", rep)
		}
	})
}

func TestDemoCSG_LocateEndpointAndDiagnoseExcludesCSGDlinkPort(t *testing.T) {
	procRoot := t.TempDir()
	netDir := filepath.Join(procRoot, "net")
	fdDir := filepath.Join(procRoot, "202", "fd")
	_ = os.MkdirAll(netDir, 0o755)
	_ = os.MkdirAll(fdDir, 0o755)
	_ = os.MkdirAll(filepath.Join(procRoot, "202", "net"), 0o755)

	// 模拟 sut-order(pid=202) 监听 :9001 (2329)，持有对 csgserver :31806 (7C3E) 的 dlink 长连接，无任何直连业务下游
	tcpTable := strings.Join([]string{
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: 0100007F:2329 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 20001 1 00000000 100 0 0 10 0",
		"   1: 0100007F:C111 0100007F:7C3E 01 00000000:00000000 00:00000000 00000000  1000        0 20002 1 00000000 100 0 0 10 0",
	}, "\n") + "\n"
	_ = os.WriteFile(filepath.Join(netDir, "tcp"), []byte(tcpTable), 0o644)
	_ = os.WriteFile(filepath.Join(procRoot, "202", "net", "tcp"), []byte(tcpTable), 0o644)
	_ = os.Symlink("socket:[20001]", filepath.Join(fdDir, "3"))
	_ = os.Symlink("socket:[20002]", filepath.Join(fdDir, "4"))

	scanner := ebpf.NewProcScanner(procRoot)
	estConns, needRec, err := scanner.DiagnoseEstablishedConns(202, nil, 9001, 0, 31806)
	if err != nil {
		t.Fatalf("DiagnoseEstablishedConns failed: %v", err)
	}
	if estConns != 0 || needRec {
		t.Fatalf("expected CSG dlink port 31806 to be excluded from established downstream conns, got estConns=%d needRec=%v", estConns, needRec)
	}
}
