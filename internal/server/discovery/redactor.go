package discovery

import (
	"strings"

	"code-contract/pkg/agentproto"
)

// RedactionVersion tracks the sensitive parameter masking rule version stored on snapshots.
const RedactionVersion = 1

// RedactCmdline masks password, passwd, token, secret, api_key, and key= values with *** in O(N) time.
func RedactCmdline(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	tokens := splitCmdTokens(trimmed)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if eqIdx := strings.IndexByte(tok, '='); eqIdx >= 0 {
			key := tok[:eqIdx]
			if isSensitiveArg(key) {
				tokens[i] = key + "=***"
			}
			continue
		}
		if strings.HasPrefix(tok, "-") && isSensitiveArg(tok) {
			if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				tokens[i+1] = "***"
				i++
			}
		}
	}
	return strings.Join(tokens, " ")
}

func splitCmdTokens(raw string) []string {
	tokens := make([]string, 0, 8)
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if quote != 0 {
			cur.WriteByte(ch)
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			cur.WriteByte(ch)
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// isSensitiveArg reports whether a CLI argument or env token carries sensitive credential keys.
func isSensitiveArg(arg string) bool {
	lower := strings.ToLower(strings.TrimSpace(arg))
	if lower == "" {
		return false
	}
	key := lower
	if idx := strings.IndexByte(lower, '='); idx >= 0 {
		key = lower[:idx]
	}
	key = strings.TrimLeft(key, "-")
	for _, marker := range []string{"password", "passwd", "pwd", "token", "secret", "api_key", "api-key", "access_key", "access-key", "private_key", "private-key"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "key" || strings.HasSuffix(key, "_key") || strings.HasSuffix(key, "-key")
}

// RedactCSGReport returns a shallow-safe copy of report with endpoint cmdlines redacted.
func RedactCSGReport(report *agentproto.CSGTopologyReport) *agentproto.CSGTopologyReport {
	if report == nil {
		return nil
	}
	cp := *report
	if len(report.CSGEndpoints) > 0 {
		cp.CSGEndpoints = make([]agentproto.CSGEndpointInfo, len(report.CSGEndpoints))
		for i, ep := range report.CSGEndpoints {
			ep.Cmdline = RedactCmdline(ep.Cmdline)
			cp.CSGEndpoints[i] = ep
		}
	}
	if len(report.PortUsages) > 0 {
		cp.PortUsages = append([]agentproto.CSGPortUsage(nil), report.PortUsages...)
	}
	if len(report.Services) > 0 {
		cp.Services = append([]agentproto.CSGServiceInfo(nil), report.Services...)
	}
	if len(report.Subscriptions) > 0 {
		cp.Subscriptions = append([]agentproto.CSGSubscription(nil), report.Subscriptions...)
	}
	if len(report.RawClients) > 0 {
		cp.RawClients = append([]agentproto.CSGClientEntry(nil), report.RawClients...)
	}
	if len(report.Errors) > 0 {
		cp.Errors = append([]string(nil), report.Errors...)
	}
	return &cp
}

// RedactDiscoveredProcesses returns a copy of discovered processes with redacted cmdlines.
func RedactDiscoveredProcesses(procs []agentproto.DiscoveredProcess) []agentproto.DiscoveredProcess {
	if len(procs) == 0 {
		return nil
	}
	out := make([]agentproto.DiscoveredProcess, len(procs))
	for i, p := range procs {
		p.Cmdline = RedactCmdline(p.Cmdline)
		if len(p.ServiceNames) > 0 {
			p.ServiceNames = append([]string(nil), p.ServiceNames...)
		}
		if len(p.Transports) > 0 {
			p.Transports = append([]string(nil), p.Transports...)
		}
		out[i] = p
	}
	return out
}
