// Package csgmock implements an in-memory CSG dlink + RPCMonitorService
// sandbox server without touching any live CSG server or importing pkg/ebpf.
package csgmock

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"code-contract/pkg/csgdlink"
	"code-contract/pkg/rpcmon"
)

// MockPortPoolTotal aligns with the CSG dynamic port pool range [32768, 49151].
const MockPortPoolTotal = 49151 - 32768 + 1

var pidSuffixPattern = regexp.MustCompile(`^(.+-\d+)-(\d+)$`)

// MockCSGServer simulates a local isolated CSG control plane and RPCMonitorService provider.
type MockCSGServer struct {
	addr     string
	uuid     uint32
	clientID uint64

	regLock          sync.RWMutex
	reg              map[string]regEntry
	sessions         map[string]*mockSession
	conns            map[net.Conn]struct{}
	observedSubtypes []uint16
	meshRewrites     int64
	rewriteMeshTCP   bool
	onMeshOnlySub    func(serviceName string)
	onSubObserved    func(clientPID uint32, serviceName string)

	ln     net.Listener
	monLn  net.Listener
	closed atomic.Bool
}

type regEntry struct {
	inst   csgdlink.Instance
	seeded bool
}

type mockSession struct {
	conn     net.Conn
	uuid     uint32
	clientID uint64
	pid      uint32
	subs     map[string]struct{}
	pubKeys  map[string]struct{}
}

// NewMockCSGServer creates an in-memory MockCSGServer configured to listen on addr.
func NewMockCSGServer(addr string) *MockCSGServer {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	return &MockCSGServer{
		addr:     addr,
		reg:      make(map[string]regEntry),
		sessions: make(map[string]*mockSession),
		conns:    make(map[net.Conn]struct{}),
	}
}

// Addr returns the bound TCP address after Start().
func (m *MockCSGServer) Addr() string {
	if m.ln != nil {
		return m.ln.Addr().String()
	}
	return m.addr
}

// MonitorAddr returns the bound RPCMonitorService monitor TCP address after StartMonitor().
func (m *MockCSGServer) MonitorAddr() string {
	if m.monLn != nil {
		return m.monLn.Addr().String()
	}
	return ""
}

// EnableMeshToTCPRewrite controls whether SUB_ACK rewrites dophi_mesh:// URIs to tcp://.
func (m *MockCSGServer) EnableMeshToTCPRewrite(enabled bool) {
	m.regLock.Lock()
	defer m.regLock.Unlock()
	m.rewriteMeshTCP = enabled
}

// SetMeshOnlySubHook registers a callback invoked when a SUB requests MESH without TCP.
func (m *MockCSGServer) SetMeshOnlySubHook(fn func(serviceName string)) {
	m.regLock.Lock()
	defer m.regLock.Unlock()
	m.onMeshOnlySub = fn
}

// SetSubObservedHook registers a callback invoked whenever a SUB arrives.
func (m *MockCSGServer) SetSubObservedHook(fn func(clientPID uint32, serviceName string)) {
	m.regLock.Lock()
	defer m.regLock.Unlock()
	m.onSubObserved = fn
}

// SeedInstance pre-registers an external/mock instance independent of client connections.
func (m *MockCSGServer) SeedInstance(inst csgdlink.Instance) {
	key := instanceKey(inst.Name64, inst.Addr)
	m.regLock.Lock()
	defer m.regLock.Unlock()
	m.reg[key] = regEntry{inst: inst, seeded: true}
}

// Start binds the dlink TCP listener and starts accepting connections.
func (m *MockCSGServer) Start() error {
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		return err
	}
	m.ln = ln
	go m.acceptLoop()
	return nil
}

// StartMonitor binds an in-process CrosTCP RPCMonitorService listener on addr.
func (m *MockCSGServer) StartMonitor(addr string) error {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	m.monLn = ln
	go func() {
		_ = rpcmon.NewServer(m).Serve(ln)
	}()
	return nil
}

// Close shuts down listeners and active dlink sessions cleanly.
func (m *MockCSGServer) Close() error {
	if m.closed.Swap(true) {
		return nil
	}
	var firstErr error
	if m.monLn != nil {
		if err := m.monLn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			firstErr = err
		}
	}
	if m.ln != nil {
		if err := m.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) && firstErr == nil {
			firstErr = err
		}
	}
	m.regLock.Lock()
	for c := range m.conns {
		_ = c.Close()
	}
	m.conns = make(map[net.Conn]struct{})
	m.regLock.Unlock()
	return firstErr
}

func (m *MockCSGServer) acceptLoop() {
	for {
		conn, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.ServeConn(conn)
	}
}

// ServeConn handles a single dlink TCP stream (both from acceptLoop and in-process EgressProxy mux).
func (m *MockCSGServer) ServeConn(conn net.Conn) {
	key := connKey(conn)
	m.regLock.Lock()
	m.conns[conn] = struct{}{}
	m.regLock.Unlock()

	defer func() {
		m.regLock.Lock()
		delete(m.conns, conn)
		if sess := m.sessions[key]; sess != nil {
			for pk := range sess.pubKeys {
				if entry, ok := m.reg[pk]; ok && !entry.seeded {
					delete(m.reg, pk)
				}
			}
			delete(m.sessions, key)
		}
		m.regLock.Unlock()
		_ = conn.Close()
	}()

	for {
		f, body, err := csgdlink.ReadFrame(conn)
		if err != nil {
			return
		}
		if err := m.dispatch(conn, f, body); err != nil {
			return
		}
	}
}

func (m *MockCSGServer) dispatch(conn net.Conn, f csgdlink.Frame, body []byte) error {
	m.regLock.Lock()
	m.observedSubtypes = append(m.observedSubtypes, f.SubType)
	m.regLock.Unlock()

	switch f.SubType {
	case csgdlink.SubTypeRegister:
		return m.handleRegister(conn, f, body)
	case csgdlink.SubTypeClientID:
		return m.handleClientID(conn, f, body)
	case csgdlink.SubTypePub:
		return m.handlePub(conn, f, body)
	case csgdlink.SubTypeSub:
		return m.handleSub(conn, f, body)
	case csgdlink.SubTypeUnpub:
		return m.handleUnpub(conn, f, body)
	case csgdlink.SubTypeUnsub:
		return m.handleUnsub(conn, f, body)
	default:
		// Tolerate heartbeats/batch/unknown control frames without breaking connection.
		return nil
	}
}

func (m *MockCSGServer) ensureSessionLocked(conn net.Conn) *mockSession {
	key := connKey(conn)
	sess := m.sessions[key]
	if sess == nil {
		sess = &mockSession{
			conn:    conn,
			subs:    make(map[string]struct{}),
			pubKeys: make(map[string]struct{}),
		}
		m.sessions[key] = sess
	}
	return sess
}

func (m *MockCSGServer) handleRegister(conn net.Conn, f csgdlink.Frame, body []byte) error {
	if len(body) < 8 {
		return fmt.Errorf("csgmock: short REGISTER body %d", len(body))
	}
	uuid := atomic.AddUint32(&m.uuid, 1)
	pid := binary.BigEndian.Uint32(body[4:8])

	m.regLock.Lock()
	sess := m.ensureSessionLocked(conn)
	sess.uuid = uuid
	if sess.pid == 0 {
		sess.pid = pid
	}
	m.regLock.Unlock()

	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[0:4], 0)
	binary.BigEndian.PutUint32(payload[4:8], uuid)
	return m.reply(conn, f, csgdlink.SubTypeRegisterAck, payload)
}

func (m *MockCSGServer) handleClientID(conn net.Conn, f csgdlink.Frame, body []byte) error {
	if len(body) < 8 {
		return fmt.Errorf("csgmock: short CLIENT_ID body %d", len(body))
	}
	clientID := atomic.AddUint64(&m.clientID, 1)
	realPid := binary.BigEndian.Uint32(body[0:4])

	m.regLock.Lock()
	sess := m.ensureSessionLocked(conn)
	sess.clientID = clientID
	if realPid != 0 {
		sess.pid = realPid
	}
	m.regLock.Unlock()

	payload := make([]byte, 16)
	binary.BigEndian.PutUint32(payload[0:4], 0)
	binary.BigEndian.PutUint32(payload[4:8], 0)
	binary.BigEndian.PutUint64(payload[8:16], clientID)
	return m.reply(conn, f, csgdlink.SubTypeClientIDAck, payload)
}

func (m *MockCSGServer) handlePub(conn net.Conn, f csgdlink.Frame, body []byte) error {
	req, err := csgdlink.DecodePubRequest(body)
	if err != nil {
		return err
	}
	pk := instanceKey(req.Instance.Name64, req.Instance.Addr)

	m.regLock.Lock()
	m.reg[pk] = regEntry{inst: req.Instance, seeded: false}
	sess := m.ensureSessionLocked(conn)
	if req.Instance.ClientID != 0 {
		sess.clientID = req.Instance.ClientID
	}
	if req.Instance.ProcID != 0 && sess.pid == 0 {
		sess.pid = req.Instance.ProcID
	}
	sess.pubKeys[pk] = struct{}{}
	m.regLock.Unlock()

	return m.reply(conn, f, csgdlink.SubTypePubAck, make([]byte, 8))
}

func (m *MockCSGServer) handleSub(conn net.Conn, f csgdlink.Frame, body []byte) error {
	req, err := csgdlink.DecodeSubRequest(body)
	if err != nil {
		return err
	}

	m.regLock.Lock()
	sess := m.ensureSessionLocked(conn)
	sess.subs[req.Name] = struct{}{}
	clientPID := sess.pid
	rewrite := m.rewriteMeshTCP
	meshHook := m.onMeshOnlySub
	subHook := m.onSubObserved

	matched := collectMatchingInstances(m.reg, req.Name, req.Trans, rewrite, &m.meshRewrites)
	m.regLock.Unlock()

	if subHook != nil {
		subHook(clientPID, req.Name)
	}
	if meshHook != nil && req.Trans[1] == 0 && req.Trans[4] != 0 {
		meshHook(req.Name)
	}

	payload := make([]byte, 16, 16+len(matched)*csgdlink.ServiceInstLen)
	binary.BigEndian.PutUint32(payload[0:4], uint32(len(matched)))
	binary.BigEndian.PutUint32(payload[4:8], 0)
	binary.BigEndian.PutUint32(payload[8:12], uint32(len(matched)*csgdlink.ServiceInstLen))
	binary.BigEndian.PutUint32(payload[12:16], 0)
	for _, inst := range matched {
		payload = append(payload, csgdlink.EncodeInstance(inst)...)
	}
	return m.reply(conn, f, csgdlink.SubTypeSubAck, payload)
}

func (m *MockCSGServer) handleUnpub(conn net.Conn, f csgdlink.Frame, body []byte) error {
	inst, err := csgdlink.ParseInstance(body)
	if err != nil {
		return err
	}
	pk := instanceKey(inst.Name64, inst.Addr)

	m.regLock.Lock()
	delete(m.reg, pk)
	if sess := m.sessions[connKey(conn)]; sess != nil {
		delete(sess.pubKeys, pk)
	}
	m.regLock.Unlock()

	return m.reply(conn, f, csgdlink.SubTypeUnpubAck, make([]byte, 8))
}

func (m *MockCSGServer) handleUnsub(conn net.Conn, f csgdlink.Frame, body []byte) error {
	name := unsubName(body)
	m.regLock.Lock()
	if sess := m.sessions[connKey(conn)]; sess != nil && name != "" {
		delete(sess.subs, name)
	}
	m.regLock.Unlock()
	return m.reply(conn, f, csgdlink.SubTypeUnsubAck, make([]byte, 8))
}

// ObservedControlSubtypes returns a copy of all control frame subtypes handled by this server.
func (m *MockCSGServer) ObservedControlSubtypes() []uint16 {
	m.regLock.RLock()
	defer m.regLock.RUnlock()
	return append([]uint16(nil), m.observedSubtypes...)
}

// MeshRewriteCount returns the count of dophi_mesh:// URIs rewritten to tcp://.
func (m *MockCSGServer) MeshRewriteCount() int {
	return int(atomic.LoadInt64(&m.meshRewrites))
}

func collectMatchingInstances(reg map[string]regEntry, target string, trans [7]byte, rewriteMeshTCP bool, meshRewriteCounter *int64) []csgdlink.Instance {
	exact := matchInstances(reg, target, trans, rewriteMeshTCP, false, meshRewriteCounter)
	if len(exact) > 0 {
		return exact
	}
	return matchInstances(reg, target, trans, rewriteMeshTCP, true, meshRewriteCounter)
}

func matchInstances(reg map[string]regEntry, target string, trans [7]byte, rewriteMeshTCP bool, allowPIDSuffix bool, meshRewriteCounter *int64) []csgdlink.Instance {
	targetNorm := stripPIDSuffix(target)
	keys := make([]string, 0, len(reg))
	for k := range reg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []csgdlink.Instance
	for _, k := range keys {
		inst := reg[k].inst
		matchedName := inst.Name64 == target
		if !matchedName && allowPIDSuffix {
			matchedName = stripPIDSuffix(inst.Name64) == targetNorm
		}
		if !matchedName {
			continue
		}
		outInst := inst
		if rewriteMeshTCP && csgdlink.IsMeshAddr(outInst.Addr) {
			outInst.Addr = csgdlink.RewriteMeshAddrToTCP(outInst.Addr)
			if meshRewriteCounter != nil {
				atomic.AddInt64(meshRewriteCounter, 1)
			}
		}
		if !transMatches(inst.Addr, outInst.Addr, trans) {
			continue
		}
		out = append(out, outInst)
	}
	return out
}

func transMatches(origAddr, effectiveAddr string, trans [7]byte) bool {
	anyRequested := false
	for i := 0; i < 6; i++ {
		if trans[i] != 0 {
			anyRequested = true
			break
		}
	}
	if !anyRequested || trans[0] != 0 {
		return true
	}
	if csgdlink.IsMeshAddr(origAddr) && trans[4] != 0 {
		return true
	}
	if (csgdlink.IsTCPAddr(effectiveAddr) || csgdlink.IsTCPAddr(origAddr)) && trans[1] != 0 {
		return true
	}
	if strings.Contains(origAddr, "dms://") && trans[2] != 0 {
		return true
	}
	if strings.Contains(origAddr, "dpuc://") && trans[3] != 0 {
		return true
	}
	if strings.Contains(origAddr, "domain://") && trans[5] != 0 {
		return true
	}
	return false
}

func stripPIDSuffix(name string) string {
	if m := pidSuffixPattern.FindStringSubmatch(name); len(m) == 3 {
		return m[1]
	}
	return name
}

func unsubName(body []byte) string {
	if len(body) >= csgdlink.ServiceInstLen {
		return csgdlink.CString(body[184:248])
	}
	if len(body) >= csgdlink.UnsubPayloadLen {
		return csgdlink.CString(body[8:72])
	}
	return ""
}

func (m *MockCSGServer) reply(conn net.Conn, req csgdlink.Frame, ack uint16, payload []byte) error {
	f := csgdlink.Frame{
		Magic:   csgdlink.DlinkMagic,
		Type:    csgdlink.MsgTypeReply,
		SubType: ack,
		CorrID:  req.CorrID,
		LinkID:  req.LinkID,
	}
	return csgdlink.WriteFrame(conn, f, payload)
}

func instanceKey(name64, addr string) string {
	return name64 + "|" + addr
}

func connKey(conn net.Conn) string {
	if conn.RemoteAddr() != nil {
		return fmt.Sprintf("%p:%s", conn, conn.RemoteAddr().String())
	}
	return fmt.Sprintf("%p", conn)
}

// Snapshot implements rpcmon.Provider with deterministically sorted entries.
func (m *MockCSGServer) Snapshot() rpcmon.Snapshot {
	m.regLock.RLock()
	defer m.regLock.RUnlock()

	snap := rpcmon.Snapshot{}
	ports := make(map[string]map[uint16]struct{})

	regKeys := make([]string, 0, len(m.reg))
	for k := range m.reg {
		regKeys = append(regKeys, k)
	}
	sort.Strings(regKeys)

	for _, k := range regKeys {
		inst := m.reg[k].inst
		effectiveAddr := inst.Addr
		if m.rewriteMeshTCP {
			effectiveAddr = csgdlink.RewriteMeshAddrToTCP(effectiveAddr)
		}
		snap.Servers = append(snap.Servers, rpcmon.ServerEntry{
			ProcName:   procNameFromPID(inst.ProcID),
			Pid:        inst.ProcID,
			ServerName: inst.Name64,
			ServerAddr: effectiveAddr,
		})
		if host, port, ok := csgdlink.ParseAddrIPPort(effectiveAddr); ok {
			if ports[host] == nil {
				ports[host] = make(map[uint16]struct{})
			}
			ports[host][port] = struct{}{}
		}
	}

	hosts := make([]string, 0, len(ports))
	for h := range ports {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		snap.Ports = append(snap.Ports, rpcmon.PortUsage{
			IPAddr: h,
			Used:   uint32(len(ports[h])),
			Total:  MockPortPoolTotal,
		})
	}

	sessKeys := make([]string, 0, len(m.sessions))
	for k := range m.sessions {
		sessKeys = append(sessKeys, k)
	}
	sort.Strings(sessKeys)

	for _, k := range sessKeys {
		sess := m.sessions[k]
		host := "127.0.0.1"
		if sess.conn.RemoteAddr() != nil {
			if h, _, err := net.SplitHostPort(sess.conn.RemoteAddr().String()); err == nil && h != "" {
				host = h
			}
		}
		subs := make([]string, 0, len(sess.subs))
		for name := range sess.subs {
			subs = append(subs, name)
		}
		sort.Strings(subs)
		for _, name := range subs {
			snap.Clients = append(snap.Clients, rpcmon.ClientEntry{
				ProcName:   procNameFromPID(sess.pid),
				Pid:        sess.pid,
				ClientAddr: host,
				ServerName: name,
			})
		}
	}

	return snap
}

func procNameFromPID(pid uint32) string {
	if pid == 0 {
		return "mock-proc"
	}
	return "pid-" + strconv.FormatUint(uint64(pid), 10)
}
