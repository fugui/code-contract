package ebpf

import (
	"bytes"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNatLookupCandidateIPs_AndRetry(t *testing.T) {
	// 1. 非 127.0.0.1 IP 应排在第一位，127.0.0.1 兜底第二位
	c1 := natLookupCandidateIPs(net.ParseIP("10.1.64.25"))
	if len(c1) != 2 || c1[0].String() != "10.1.64.25" || c1[1].String() != "127.0.0.1" {
		t.Fatalf("unexpected candidates for 10.1.64.25: %v", c1)
	}

	// 2. 127.0.0.1 不重复
	c2 := natLookupCandidateIPs(net.IPv4(127, 0, 0, 1))
	if len(c2) != 1 || c2[0].String() != "127.0.0.1" {
		t.Fatalf("unexpected candidates for 127.0.0.1: %v", c2)
	}

	// 3. IPv4-mapped IPv6 (::ffff:10.1.64.25) 正确规范化
	c3 := natLookupCandidateIPs(net.ParseIP("::ffff:10.1.64.25"))
	if len(c3) != 2 || c3[0].String() != "10.1.64.25" || c3[1].String() != "127.0.0.1" {
		t.Fatalf("unexpected candidates for ::ffff:10.1.64.25: %v", c3)
	}

	// 4. 瞬时竞态退避重试：前 2 次未命中，第 3 次命中
	attempts := 0
	pidOut, _, origIP, origPort, ok := lookupNatWithRetry(
		net.ParseIP("10.1.64.25"),
		53471,
		[]time.Duration{time.Microsecond, time.Microsecond, time.Microsecond},
		func(ip net.IP, port uint16) (uint64, uint32, net.IP, uint16, bool) {
			attempts++
			// 每次重试最多试 2 个候选 IP，第 5 次尝试（第 3 轮）返回命中
			if attempts >= 5 && ip.String() == "10.1.64.25" && port == 53471 {
				return 77, 1234, net.IPv4(10, 1, 64, 99), 8080, true
			}
			return 0, 0, nil, 0, false
		},
	)
	if !ok || pidOut != 77 || origIP.String() != "10.1.64.99" || origPort != 8080 {
		t.Fatalf("expected retry hit, got ok=%v pidOut=%d origIP=%v origPort=%d attempts=%d", ok, pidOut, origIP, origPort, attempts)
	}

	// 5. 优先命中真实源 IP，而不误用 127.0.0.1
	var tried []string
	pidOut, _, origIP, origPort, ok = lookupNatWithRetry(
		net.ParseIP("10.1.64.25"),
		53471,
		nil,
		func(ip net.IP, port uint16) (uint64, uint32, net.IP, uint16, bool) {
			tried = append(tried, ip.String())
			if ip.String() == "10.1.64.25" {
				return 88, 2234, net.IPv4(10, 2, 3, 4), 9000, true
			}
			return 99, 3234, net.IPv4(127, 0, 0, 1), 9001, true
		},
	)
	if !ok || pidOut != 88 || origIP.String() != "10.2.3.4" || origPort != 9000 || len(tried) != 1 || tried[0] != "10.1.64.25" {
		t.Fatalf("expected immediate primary IP hit, got ok=%v pidOut=%d origIP=%v origPort=%d tried=%v", ok, pidOut, origIP, origPort, tried)
	}
}

func TestEgressProxy_SetNatAddrLookup_AndMissDiagLog(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend failed: %v", err)
	}
	defer backendLn.Close()
	backendPort := uint16(backendLn.Addr().(*net.TCPAddr).Port)

	go func() {
		for {
			c, aErr := backendLn.Accept()
			if aErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 64)
				if n, rErr := conn.Read(buf); rErr == nil && n > 0 {
					_, _ = conn.Write([]byte("OK"))
				}
			}(c)
		}
	}()

	var hitMode atomic.Bool
	var seenClientIPMu sync.Mutex
	var seenClientIPs []string
	var diagCalls atomic.Int32

	proxy, err := NewEgressProxy(nil, nil)
	if err != nil {
		t.Fatalf("NewEgressProxy failed: %v", err)
	}
	defer proxy.Close()

	proxy.SetNatAddrLookup(func(clientIP net.IP, clientPort uint16) (uint64, uint32, net.IP, uint16, bool) {
		seenClientIPMu.Lock()
		seenClientIPs = append(seenClientIPs, clientIP.String())
		seenClientIPMu.Unlock()
		if !hitMode.Load() {
			return 0, 0, nil, 0, false
		}
		return 101, 2002, net.IPv4(127, 0, 0, 1), backendPort, true
	})
	proxy.SetNatMissDiag(func(clientIP net.IP, clientPort uint16) string {
		diagCalls.Add(1)
		return "diag[connect4=true sockops=true natMap=0 cookieMap=3]"
	})

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	oldOut := log.Writer()
	log.SetOutput(&lockedBufWriter{mu: &logMu, buf: &logBuf})
	defer log.SetOutput(oldOut)

	proxyAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(proxy.ProxyPort())))

	// 连续触发 8 次 NAT 未命中，验证诊断日志与限频 (前 5 次立即打印，后续 5s 内抑制)
	for i := 0; i < 8; i++ {
		c, dErr := net.Dial("tcp", proxyAddr)
		if dErr != nil {
			t.Fatalf("dial miss #%d failed: %v", i, dErr)
		}
		// 读到 EOF 确认服务端已经完成 handleConnection 关闭
		_, _ = c.Read(make([]byte, 16))
		_ = c.Close()
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if proxy.Stats().NatMiss >= 8 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := proxy.Stats().NatMiss; got != 8 {
		t.Fatalf("expected NatMiss=8, got %d", got)
	}
	if dc := diagCalls.Load(); dc != 5 {
		t.Fatalf("expected rate-limited diagCalls=5 (first 5 immediate, remaining 3 suppressed), got %d", dc)
	}

	logMu.Lock()
	logOut := logBuf.String()
	logMu.Unlock()

	if !strings.Contains(logOut, "[EgressProxy] NAT 未命中，关闭连接") ||
		!strings.Contains(logOut, "client=127.0.0.1:") ||
		!strings.Contains(logOut, "reason=lookup_miss") ||
		!strings.Contains(logOut, "diag[connect4=true sockops=true natMap=0 cookieMap=3]") {
		t.Fatalf("unexpected NAT miss log output:\n%s", logOut)
	}

	// 切换命中模式，验证 SetNatAddrLookup 正常转发
	hitMode.Store(true)
	cHit, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial hit failed: %v", err)
	}
	defer cHit.Close()
	if _, err := cHit.Write([]byte("PING")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	rsp := make([]byte, 16)
	_ = cHit.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := cHit.Read(rsp)
	if err != nil || string(rsp[:n]) != "OK" {
		t.Fatalf("expected OK from backend via SetNatAddrLookup, got n=%d data=%q err=%v", n, string(rsp[:n]), err)
	}

	seenClientIPMu.Lock()
	defer seenClientIPMu.Unlock()
	if len(seenClientIPs) == 0 || seenClientIPs[0] != "127.0.0.1" {
		t.Fatalf("expected SetNatAddrLookup to receive clientIP=127.0.0.1, got %v", seenClientIPs)
	}
}

func TestParseCgroupV2Mount(t *testing.T) {
	// 1. 纯 cgroup v2 环境
	unifiedMounts := `
sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
cgroup2 /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec,relatime,nsdelegate,memory_recursiveprot 0 0
`
	p, fs := parseCgroupV2Mount(unifiedMounts, "")
	if p != "/sys/fs/cgroup" || fs != "cgroup2" {
		t.Fatalf("expected (/sys/fs/cgroup, cgroup2), got (%q, %q)", p, fs)
	}

	// 2. cgroup v1 + cgroup2 hybrid 环境
	hybridMounts := `
tmpfs /sys/fs/cgroup tmpfs ro,nosuid,nodev,noexec,mode=755 0 0
cgroup2 /sys/fs/cgroup/unified cgroup2 rw,nosuid,nodev,noexec,relatime,nsdelegate 0 0
cgroup /sys/fs/cgroup/systemd cgroup rw,nosuid,nodev,noexec,relatime,xattr,name=systemd 0 0
`
	p, fs = parseCgroupV2Mount(hybridMounts, "")
	if p != "/sys/fs/cgroup/unified" || fs != "cgroup2" {
		t.Fatalf("expected (/sys/fs/cgroup/unified, cgroup2), got (%q, %q)", p, fs)
	}

	// 3. 显式传入子路径，保留路径并匹配最长挂载 fstype
	p, fs = parseCgroupV2Mount(unifiedMounts, "/sys/fs/cgroup/system.slice")
	if p != "/sys/fs/cgroup/system.slice" || fs != "cgroup2" {
		t.Fatalf("expected (/sys/fs/cgroup/system.slice, cgroup2), got (%q, %q)", p, fs)
	}
}

type lockedBufWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedBufWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
