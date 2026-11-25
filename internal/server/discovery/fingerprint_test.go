package discovery

import (
	"testing"

	"code-contract/models"
)

func TestFingerprint_IgnoresServiceNamesSecretsAndDynamicArgs_SupportsVersions(t *testing.T) {
	// 1. 跨环境不同安装路径同 exeBase + 敏感参数 + 动态参数 ➔ 必须产出完全相同指纹
	fp1, key1, low1 := ComputeIdentityFingerprint(
		"/opt/deploy/sit/bin/order_svr",
		"/opt/deploy/sit/bin/order_svr --port 21001 --pid 4512 --bind=10.0.1.5:21001 --password=topsecret --token mytok123 --env=prod --mode=sync /tmp/run1.sock deadbeef01",
		"order_svr-4512",
	)
	fp2, key2, low2 := ComputeIdentityFingerprint(
		"/usr/local/uat/bin/order_svr",
		"/usr/local/uat/bin/order_svr --mode=sync --env=prod -p 31009 --listen 192.168.56.8:31009 --secret=anothersecret --api_key=abc --key=xyz /var/run/order.pid 99887766aabb",
		"order_svr-9999",
	)
	if low1 || low2 {
		t.Fatalf("带完整 cmdline 的进程不应被判为低置信")
	}
	if fp1 == "" || fp1 != fp2 {
		t.Fatalf("跨环境规范化指纹应当一致: fp1=%q fp2=%q", fp1, fp2)
	}
	if key1 != key2 {
		t.Fatalf("canonicalKey 应当一致: key1=%q key2=%q", key1, key2)
	}

	// 2. 指纹严禁包含任何发布服务名（更改稳定业务参数才会改变指纹）
	fpDiffArg, _, _ := ComputeIdentityFingerprint(
		"/opt/deploy/sit/bin/order_svr",
		"/opt/deploy/sit/bin/order_svr --mode=async --env=prod",
		"order_svr",
	)
	if fpDiffArg == fp1 {
		t.Fatalf("不同稳定参数应生成不同指纹")
	}

	// 3. Java (-jar / 主类) 与 Python / Node 入口脚本提取
	fpJava1, _, _ := ComputeIdentityFingerprint("/usr/lib/jvm/bin/java", "java -Xmx512m -jar /opt/app1/billing-svc.jar --port=8080", "java")
	fpJava2, _, _ := ComputeIdentityFingerprint("/opt/jdk17/bin/java", "java -Xmx512m -jar /srv/rel/billing-svc.jar --port=9090", "java")
	if fpJava1 != fpJava2 {
		t.Fatalf("Java -jar 跨路径同 JAR 名应生成相同指纹: %q vs %q", fpJava1, fpJava2)
	}
	fpJavaOther, _, _ := ComputeIdentityFingerprint("/opt/jdk17/bin/java", "java -Xmx512m -jar /srv/rel/payment-svc.jar --port=9090", "java")
	if fpJavaOther == fpJava1 {
		t.Fatalf("不同 JAR 入口应当生成不同指纹")
	}

	fpPy1, _, _ := ComputeIdentityFingerprint("/usr/bin/python3", "python3 /opt/a/worker.py --port 7000", "python3")
	fpPy2, _, _ := ComputeIdentityFingerprint("/usr/local/bin/python3", "python3 /srv/b/worker.py --port 7001", "python3")
	if fpPy1 != fpPy2 {
		t.Fatalf("Python 入口脚本跨路径应生成相同指纹: %q vs %q", fpPy1, fpPy2)
	}

	// 4. cmdline 为空时退化为 P3 低置信 procName 兜底并剥离 -<pid>
	if got := StripProcPIDSuffix("gateway_svr-12345"); got != "gateway_svr" {
		t.Fatalf("StripProcPIDSuffix 期望 gateway_svr，实际 %q", got)
	}
	_, keyLow, isLow := ComputeIdentityFingerprint("", "", "gateway_svr-12345")
	if !isLow || keyLow != "proc:gateway_svr" {
		t.Fatalf("空 cmdline 应退化为 P3 低置信 proc:gateway_svr，实际 isLow=%v key=%q", isLow, keyLow)
	}

	// 5. 三阶身份解析优先级: P1(re:/子串) > P2(同版本指纹) > P3(低置信) & 多候选歧义
	apP1 := models.ArchProcess{
		ID:                  10,
		Code:                "ap-p1",
		Name:                "P1正则进程",
		DefaultCmdPattern:   `re:order_svr.*--env=prod`,
		IdentityFingerprint: "mismatch_fp",
		FingerprintVersion:  CurrentFingerprintVersion,
		Lifecycle:           models.ArchProcLifecycleConfirmed,
	}
	apP2OldVer := models.ArchProcess{
		ID:                  11,
		Code:                "ap-p2-old",
		Name:                "旧版本指纹",
		IdentityFingerprint: fp1,
		FingerprintVersion:  0, // 旧版本不参与 v1 匹配
		Lifecycle:           models.ArchProcLifecycleConfirmed,
	}
	apP2Curr := models.ArchProcess{
		ID:                  12,
		Code:                "ap-p2-curr",
		Name:                "同版本指纹",
		IdentityFingerprint: fp1,
		FingerprintVersion:  CurrentFingerprintVersion,
		Lifecycle:           models.ArchProcLifecycleConfirmed,
	}

	// P1 优先于 P2
	resP1 := ResolveProcessIdentity([]models.ArchProcess{apP1, apP2OldVer, apP2Curr}, nil, "/opt/deploy/sit/bin/order_svr", "/opt/deploy/sit/bin/order_svr --env=prod --mode=sync", "order_svr", nil, false)
	if resP1.MatchStatus != "MATCHED" || resP1.Matched == nil || resP1.Matched.ID != apP1.ID || resP1.MatchTier != "P1_PATTERN" {
		t.Fatalf("P1 应当优先命中 apP1，实际: %+v", resP1)
	}

	// 仅 P2 同版本命中（忽略旧版本 apP2OldVer）
	resP2 := ResolveProcessIdentity([]models.ArchProcess{apP2OldVer, apP2Curr}, nil, "/opt/deploy/sit/bin/order_svr", "/opt/deploy/sit/bin/order_svr --env=prod --mode=sync", "order_svr", nil, false)
	if resP2.MatchStatus != "MATCHED" || resP2.Matched == nil || resP2.Matched.ID != apP2Curr.ID || resP2.MatchTier != "P2_FINGERPRINT" {
		t.Fatalf("P2 应当命中同版本 apP2Curr，实际: %+v", resP2)
	}

	// 多候选返回 IDENTITY_AMBIGUOUS，绝不自动绑定
	apP2Dup := apP2Curr
	apP2Dup.ID = 13
	apP2Dup.Code = "ap-p2-dup"
	resAmb := ResolveProcessIdentity([]models.ArchProcess{apP2Curr, apP2Dup}, nil, "/opt/deploy/sit/bin/order_svr", "/opt/deploy/sit/bin/order_svr --env=prod --mode=sync", "order_svr", nil, false)
	if resAmb.MatchStatus != "AMBIGUOUS" || resAmb.Matched != nil || len(resAmb.Candidates) != 2 {
		t.Fatalf("多候选应返回 AMBIGUOUS 与 2 个候选，实际: %+v", resAmb)
	}

	// 显式指定 force_arch_process_id 解除歧义
	overrideID := apP2Dup.ID
	resOverride := ResolveProcessIdentity([]models.ArchProcess{apP2Curr, apP2Dup}, nil, "/opt/deploy/sit/bin/order_svr", "/opt/deploy/sit/bin/order_svr --env=prod --mode=sync", "order_svr", &overrideID, false)
	if resOverride.MatchStatus != "MATCHED" || resOverride.Matched == nil || resOverride.Matched.ID != apP2Dup.ID {
		t.Fatalf("force_arch_process_id 应当绑定到 %d，实际: %+v", overrideID, resOverride)
	}

	// P3 低置信未确认 -> LOW_CONFIDENCE；确认后允许 MATCHED/NEW
	resLow := ResolveProcessIdentity([]models.ArchProcess{apP2Curr}, nil, "", "", "gateway_svr-88", nil, false)
	if resLow.MatchStatus != "LOW_CONFIDENCE" || !resLow.IsLowConfidence {
		t.Fatalf("未确认低置信应返回 LOW_CONFIDENCE，实际: %+v", resLow)
	}
	resLowConfirmed := ResolveProcessIdentity([]models.ArchProcess{apP2Curr}, nil, "", "", "gateway_svr-88", nil, true)
	if resLowConfirmed.MatchStatus != "NEW" {
		t.Fatalf("确认低置信且无匹配进程应返回 NEW，实际: %+v", resLowConfirmed)
	}
}
