package agentproto

import (
	"strings"
	"testing"
)

func TestParseAndNormalizeNoiseRules(t *testing.T) {
	raw := `
# comment line
*LogService*
  , *logservice* ,
MonitorService:Report
  
`
	got := ParseNoiseRulesText(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduplicated rules, got %d (%v)", len(got), got)
	}
	if got[0] != "*LogService*" || got[1] != "MonitorService:Report" {
		t.Fatalf("unexpected normalized rules: %v", got)
	}
}

func TestValidateNoiseRules(t *testing.T) {
	if err := ValidateNoiseRules([]string{"*LogService*", "dophi.Monitor:Ping"}); err != nil {
		t.Fatalf("expected valid rules, got %v", err)
	}

	badCases := [][]string{
		{":Ping"},
		{"LogService:"},
		{"a:b:c"},
		{"bad\nrule"},
		{strings.Repeat("a", MaxNoiseRuleLen+1)},
	}
	for _, bc := range badCases {
		if err := ValidateNoiseRules(bc); err == nil {
			t.Errorf("expected error for invalid rule %v, got nil", bc)
		}
	}

	tooMany := make([]string, MaxNoiseRules+1)
	for i := range tooMany {
		tooMany[i] = "svc" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	if err := ValidateNoiseRules(tooMany); err == nil {
		t.Errorf("expected error when exceeding MaxNoiseRules")
	}
}

func TestNoiseMatcher_WildcardsOpAndPIDNormalization(t *testing.T) {
	m := NewNoiseMatcher([]string{
		"*LogService*",
		"MonitorSvc:Report*",
		"CLS_Monitor-1",
	})

	cases := []struct {
		svc  string
		op   string
		want bool
	}{
		{"dophi.v1.LogService", "WriteLog", true},
		{"LOGSERVICE", "Anything", true},
		{"dophi.v1.LoginService", "Login", false},
		{"MonitorSvc", "ReportMetrics", true},
		{"MonitorSvc", "QueryStatus", false},
		{"CLS_Monitor-1-23456", "Beat", true},
		{"CLS_Monitor-1", "Beat", true},
		{"OrderService", "CreateOrder", false},
		{"", "WriteLog", false},
	}

	for _, tc := range cases {
		if got := m.Match(tc.svc, tc.op); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.svc, tc.op, got, tc.want)
		}
	}
}
