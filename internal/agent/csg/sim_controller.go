package csg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code-contract/pkg/agentproto"
	"code-contract/pkg/csgdlink"
	"code-contract/pkg/csgmock"
)

const (
	ErrCodeServiceAlreadyPublished  = "CSG_SERVICE_ALREADY_PUBLISHED"
	ErrCodeDataPlaneMeshUnsupported = "CSG_DATAPLANE_MESH_UNSUPPORTED"
)

var (
	// ErrServiceAlreadyPublished maps to HTTP 409 when PUB_STUB preflight Sub > 0.
	ErrServiceAlreadyPublished = errors.New("目标 CSG 服务已在线注册，拒绝 PUB_STUB 抢占发布")
	// ErrDataPlaneMeshUnsupported maps to HTTP 422 when data plane requires pure dophi_mesh.
	ErrDataPlaneMeshUnsupported = errors.New("目标服务仅支持 dophi_mesh:// 传输协议，当前数据面重放仅支持 TCP")
)

// SimPrepareInput configures CSG control-plane simulation for a replay execution.
type SimPrepareInput struct {
	Strategy       agentproto.CSGReplayStrategy
	CSGAddr        string
	MockDataPort   uint16
	ListenPort     uint16
	TargetServices []string
	Snapshots      []agentproto.CSGSnapshotRecord
	Timeout        time.Duration
}

// SimPrepareOutput exposes side effects needed by ReplayExecutor and EgressProxy.
type SimPrepareOutput struct {
	TargetDialAddr string
	MockCSGServer  *csgmock.MockCSGServer
	SandboxReport  *agentproto.CSGSandboxReport
}

type publishedStub struct {
	serviceName string
	addr        string
}

// SimController manages the lifecycle of a single replay task's CSG strategy
// (PASSTHROUGH / CLIENT_INJECT / PUB_STUB / MOCK_SERVER) and guarantees idempotent
// cleanup across all 5 Agent exit paths.
type SimController struct {
	mu           sync.Mutex
	strategy     agentproto.CSGReplayStrategy
	csgAddr      string
	timeout      time.Duration
	prepared     bool
	cleaned      bool
	stubClient   *csgdlink.Client
	activeStubs  []publishedStub
	mockCSG      *csgmock.MockCSGServer
	subSet       map[string]struct{}
	meshOnlySvcs []string
	resolutions  []agentproto.CSGResolution
	unpubSvcs    []string
	errCode      string
	errMsg       string
}

// NewSimController constructs a fresh simulation lifecycle controller.
func NewSimController() *SimController {
	return &SimController{
		subSet: make(map[string]struct{}),
	}
}

// Prepare executes preflight validation and sets up the requested CSG strategy.
func (c *SimController) Prepare(ctx context.Context, in SimPrepareInput) (*SimPrepareOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	timeout := in.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	strat, ok := agentproto.ParseCSGReplayStrategy(string(in.Strategy))
	if !ok {
		return nil, fmt.Errorf("未知的 CSG 重放策略: %q", in.Strategy)
	}
	c.strategy = strat
	c.csgAddr = strings.TrimSpace(in.CSGAddr)
	c.timeout = timeout
	c.prepared = true

	if c.csgAddr == "" && strat != agentproto.CSGStrategyPassthrough && strat != agentproto.CSGStrategyMockServer {
		if eps, err := LocateCSGEndpoints(LocateOptions{}); err == nil && len(eps) > 0 {
			c.csgAddr = eps[0].Addr()
		}
	}

	switch strat {
	case agentproto.CSGStrategyPassthrough:
		return &SimPrepareOutput{SandboxReport: c.buildReportLocked()}, nil

	case agentproto.CSGStrategyClientInject:
		return c.prepareClientInjectLocked(ctx, in)

	case agentproto.CSGStrategyPubStub:
		return c.preparePubStubLocked(ctx, in)

	case agentproto.CSGStrategyMockServer:
		return c.prepareMockServerLocked(ctx, in)

	default:
		return &SimPrepareOutput{SandboxReport: c.buildReportLocked()}, nil
	}
}

func (c *SimController) prepareClientInjectLocked(ctx context.Context, in SimPrepareInput) (*SimPrepareOutput, error) {
	if c.csgAddr == "" {
		return nil, fmt.Errorf("CLIENT_INJECT 模式缺少可用 CSG 控制面端点 csg_addr")
	}
	targets := collectTargetServices(in.TargetServices, in.Snapshots)
	if len(targets) == 0 {
		return &SimPrepareOutput{SandboxReport: c.buildReportLocked()}, nil
	}

	client, err := csgdlink.Dial(c.csgAddr, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("连接 CSG %s 失败: %w", c.csgAddr, err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Register(c.timeout); err != nil {
		return nil, fmt.Errorf("dlink REGISTER 失败: %w", err)
	}
	if _, err := client.ClientID(c.timeout); err != nil {
		return nil, fmt.Errorf("dlink CLIENT_ID 失败: %w", err)
	}

	var dialAddr string
	for _, svc := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		insts, err := client.Sub(svc, c.timeout)
		_ = client.Unsub(svc, c.timeout)
		if err != nil {
			return nil, fmt.Errorf("dlink SUB %s 失败: %w", svc, err)
		}
		if len(insts) == 0 {
			fallback := ""
			if in.ListenPort > 0 {
				fallback = net.JoinHostPort("127.0.0.1", strconv.Itoa(int(in.ListenPort)))
			}
			c.resolutions = append(c.resolutions, agentproto.CSGResolution{
				ServiceName:   svc,
				Strategy:      string(agentproto.CSGStrategyClientInject),
				EffectiveAddr: fallback,
				Note:          "CSG 无在线实例，回退到静态监听端口",
			})
			continue
		}

		var tcpTarget string
		var meshOnly bool
		var origAddr string
		for _, inst := range insts {
			origAddr = inst.Addr
			if csgdlink.IsTCPAddr(inst.Addr) {
				if hp, ok := csgdlink.ParseTCPTarget(inst.Addr); ok {
					tcpTarget = hp
					break
				}
			} else if csgdlink.IsMeshAddr(inst.Addr) {
				meshOnly = true
			}
		}
		if tcpTarget == "" && meshOnly {
			c.errCode = ErrCodeDataPlaneMeshUnsupported
			c.errMsg = fmt.Sprintf("服务 %s 仅提供 dophi_mesh:// 实例 (%s)，不支持 CLIENT_INJECT", svc, origAddr)
			return nil, fmt.Errorf("%w: %s", ErrDataPlaneMeshUnsupported, c.errMsg)
		}
		if tcpTarget != "" && dialAddr == "" {
			dialAddr = tcpTarget
		}
		c.resolutions = append(c.resolutions, agentproto.CSGResolution{
			ServiceName:   svc,
			Strategy:      string(agentproto.CSGStrategyClientInject),
			OriginalAddr:  origAddr,
			EffectiveAddr: tcpTarget,
			ResolvedBySub: true,
		})
	}

	return &SimPrepareOutput{
		TargetDialAddr: dialAddr,
		SandboxReport:  c.buildReportLocked(),
	}, nil
}

func (c *SimController) preparePubStubLocked(ctx context.Context, in SimPrepareInput) (*SimPrepareOutput, error) {
	if c.csgAddr == "" {
		return nil, fmt.Errorf("PUB_STUB 模式缺少可用 CSG 控制面端点 csg_addr")
	}
	if in.MockDataPort == 0 {
		return nil, fmt.Errorf("PUB_STUB 模式缺少本地 Mock 监听端口")
	}
	targets := collectTargetServices(in.TargetServices, in.Snapshots)
	if len(targets) == 0 {
		return &SimPrepareOutput{SandboxReport: c.buildReportLocked()}, nil
	}

	client, err := csgdlink.Dial(c.csgAddr, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("连接 CSG %s 失败: %w", c.csgAddr, err)
	}
	cleanupOnErr := true
	defer func() {
		if cleanupOnErr {
			_ = client.Close()
		}
	}()

	if _, err := client.Register(c.timeout); err != nil {
		return nil, fmt.Errorf("dlink REGISTER 失败: %w", err)
	}
	if _, err := client.ClientID(c.timeout); err != nil {
		return nil, fmt.Errorf("dlink CLIENT_ID 失败: %w", err)
	}

	// Preflight Sub == 0 gate (NEVER call GET_BIND_ADDR or FREE_BIND_ADDR).
	for _, svc := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		insts, err := client.Sub(svc, c.timeout)
		_ = client.Unsub(svc, c.timeout)
		if err != nil {
			return nil, fmt.Errorf("PUB_STUB 前置 Sub 检测 %s 失败: %w", svc, err)
		}
		if len(insts) > 0 {
			c.errCode = ErrCodeServiceAlreadyPublished
			c.errMsg = fmt.Sprintf("服务 %s 当前在线存在 %d 个真实实例，禁止 PUB_STUB 抢占", svc, len(insts))
			return nil, fmt.Errorf("%w: %s", ErrServiceAlreadyPublished, c.errMsg)
		}
	}

	stubURI := fmt.Sprintf("tcp://ipv4:127.0.0.1:%d", in.MockDataPort)
	for _, svc := range targets {
		if err := client.Pub(svc, stubURI, c.timeout); err != nil {
			// Roll back any already-published stubs on partial failure
			for _, prev := range c.activeStubs {
				_ = client.Unpub(prev.serviceName, prev.addr, c.timeout)
			}
			c.activeStubs = nil
			return nil, fmt.Errorf("dlink PUB %s 失败: %w", svc, err)
		}
		c.activeStubs = append(c.activeStubs, publishedStub{serviceName: svc, addr: stubURI})
		c.resolutions = append(c.resolutions, agentproto.CSGResolution{
			ServiceName:   svc,
			Strategy:      string(agentproto.CSGStrategyPubStub),
			EffectiveAddr: stubURI,
			PubStubActive: true,
		})
	}

	c.stubClient = client
	cleanupOnErr = false
	return &SimPrepareOutput{SandboxReport: c.buildReportLocked()}, nil
}

func (c *SimController) prepareMockServerLocked(_ context.Context, in SimPrepareInput) (*SimPrepareOutput, error) {
	mock := csgmock.NewMockCSGServer("127.0.0.1:0")
	mock.EnableMeshToTCPRewrite(true)

	mock.SetSubObservedHook(func(_ uint32, serviceName string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.subSet[serviceName] = struct{}{}
	})
	mock.SetMeshOnlySubHook(func(serviceName string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.meshOnlySvcs = appendUniqueString(c.meshOnlySvcs, serviceName)
		c.errCode = ErrCodeDataPlaneMeshUnsupported
		c.errMsg = fmt.Sprintf("被测进程仅发起纯 dophi_mesh 订阅 (%s)，未开启 TCP 传输", serviceName)
	})

	seeded := make(map[string]struct{})
	for _, snap := range in.Snapshots {
		if strings.TrimSpace(snap.ServiceName) == "" {
			continue
		}
		seeded[snap.ServiceName] = struct{}{}
		mock.SeedInstance(csgdlink.Instance{
			ClientID: 1,
			Name32:   snap.ServiceName,
			Addr:     snap.Addr,
			Name64:   snap.ServiceName,
		})
		effective := csgdlink.RewriteMeshAddrToTCP(snap.Addr)
		c.resolutions = append(c.resolutions, agentproto.CSGResolution{
			ServiceName:   snap.ServiceName,
			Strategy:      string(agentproto.CSGStrategyMockServer),
			OriginalAddr:  snap.Addr,
			EffectiveAddr: effective,
			MeshRewritten: effective != snap.Addr,
		})
	}

	if in.MockDataPort > 0 {
		for _, svc := range in.TargetServices {
			if _, ok := seeded[svc]; ok || strings.TrimSpace(svc) == "" {
				continue
			}
			stubURI := fmt.Sprintf("tcp://ipv4:127.0.0.1:%d", in.MockDataPort)
			mock.SeedInstance(csgdlink.Instance{
				ClientID: 1,
				Name32:   svc,
				Addr:     stubURI,
				Name64:   svc,
			})
			c.resolutions = append(c.resolutions, agentproto.CSGResolution{
				ServiceName:   svc,
				Strategy:      string(agentproto.CSGStrategyMockServer),
				EffectiveAddr: stubURI,
			})
		}
	}

	if err := mock.Start(); err != nil {
		return nil, fmt.Errorf("启动 MockCSGServer 失败: %w", err)
	}
	c.mockCSG = mock

	return &SimPrepareOutput{
		MockCSGServer: mock,
		SandboxReport: c.buildReportLocked(),
	}, nil
}

// Cleanup idempotently unpublishes stubs and shuts down MockCSGServer across all 5 exits.
func (c *SimController) Cleanup() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cleaned {
		return nil
	}
	c.cleaned = true

	var firstErr error
	if c.stubClient != nil {
		for _, st := range c.activeStubs {
			if err := c.stubClient.Unpub(st.serviceName, st.addr, 2*time.Second); err != nil && firstErr == nil {
				firstErr = err
			}
			c.unpubSvcs = append(c.unpubSvcs, st.serviceName)
		}
		if err := c.stubClient.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.stubClient = nil
		c.activeStubs = nil
	}

	if c.mockCSG != nil {
		if err := c.mockCSG.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Report returns the immutable CSGSandboxReport reflecting observed sandbox subscriptions and cleanup status.
func (c *SimController) Report() *agentproto.CSGSandboxReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buildReportLocked()
}

func (c *SimController) buildReportLocked() *agentproto.CSGSandboxReport {
	rep := &agentproto.CSGSandboxReport{
		Strategy:         c.strategy,
		CSGAddr:          c.csgAddr,
		Resolutions:      append([]agentproto.CSGResolution(nil), c.resolutions...),
		UnpublishedStubs: append([]string(nil), c.unpubSvcs...),
		RealCSGLeakCount: 0,
		CleanupCompleted: c.cleaned || c.strategy == agentproto.CSGStrategyPassthrough || c.strategy == agentproto.CSGStrategyClientInject,
		ErrorCode:        c.errCode,
		ErrorMessage:     c.errMsg,
	}
	if c.mockCSG != nil {
		rep.MockCSGAddr = c.mockCSG.Addr()
		rep.MeshRewrites = c.mockCSG.MeshRewriteCount()
	}
	if len(c.subSet) > 0 {
		subs := make([]string, 0, len(c.subSet))
		for s := range c.subSet {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		rep.SandboxSubscribedSvcs = subs
	}
	return rep
}

func collectTargetServices(explicit []string, snaps []agentproto.CSGSnapshotRecord) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, s := range explicit {
		if t := strings.TrimSpace(s); t != "" {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				out = append(out, t)
			}
		}
	}
	for _, snap := range snaps {
		if t := strings.TrimSpace(snap.ServiceName); t != "" {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				out = append(out, t)
			}
		}
	}
	return out
}
