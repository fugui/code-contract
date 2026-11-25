package discovery

import (
	"strings"
	"testing"

	"code-contract/pkg/agentproto"
)

func TestRedactCmdline_MasksPasswordTokenSecretKeyValues(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		mustHave  []string
		mustAvoid []string
	}{
		{
			name:      "equals flags and env assignments",
			raw:       "/opt/app/billing_svr --port=18080 --password=abc --token=xyz --api_key=ak_12345 secret=s1 key=k_secret --config=/etc/billing.yaml",
			mustHave:  []string{"--port=18080", "--password=***", "--token=***", "--api_key=***", "secret=***", "key=***", "--config=/etc/billing.yaml"},
			mustAvoid: []string{"abc", "xyz", "ak_12345", "s1", "k_secret"},
		},
		{
			name:      "space separated sensitive flags and short -p",
			raw:       "/usr/bin/order_svr --passwd myPass99 --secret s1 -ptoken=tok999 --db-password=pg_secret --service-name=OrderService",
			mustHave:  []string{"--passwd ***", "--secret ***", "-ptoken=***", "--db-password=***", "--service-name=OrderService"},
			mustAvoid: []string{"myPass99", "s1", "tok999", "pg_secret"},
		},
		{
			name:      "uppercase env vars and bearer style",
			raw:       "DB_PASSWD=123456 API_TOKEN=secretTok /bin/worker --listen=9090",
			mustHave:  []string{"DB_PASSWD=***", "API_TOKEN=***", "--listen=9090"},
			mustAvoid: []string{"123456", "secretTok"},
		},
		{
			name:      "normal business arguments preserved",
			raw:       "/opt/csg/csg_server --csg-addr=10.0.0.1:31806 --worker-threads=8 --env=prod",
			mustHave:  []string{"--csg-addr=10.0.0.1:31806", "--worker-threads=8", "--env=prod"},
			mustAvoid: []string{"***"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactCmdline(tc.raw)
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Fatalf("RedactCmdline(%q) = %q, expected substring %q", tc.raw, got, want)
				}
			}
			for _, forbid := range tc.mustAvoid {
				if strings.Contains(got, forbid) {
					t.Fatalf("RedactCmdline(%q) = %q, leaked forbidden secret %q", tc.raw, got, forbid)
				}
			}
		})
	}

	// Verify report & discovered processes deep redaction
	report := &agentproto.CSGTopologyReport{
		CSGEndpoints: []agentproto.CSGEndpointInfo{
			{PID: 10, ExeName: "csg_server", Cmdline: "/opt/csg --token=topsecret99 --port=31806"},
		},
	}
	redactedReport := RedactCSGReport(report)
	if strings.Contains(redactedReport.CSGEndpoints[0].Cmdline, "topsecret99") {
		t.Fatalf("expected endpoint cmdline to be redacted, got %q", redactedReport.CSGEndpoints[0].Cmdline)
	}
	if !strings.Contains(report.CSGEndpoints[0].Cmdline, "topsecret99") {
		t.Fatalf("expected original report not to be mutated")
	}
}
