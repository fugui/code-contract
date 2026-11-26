package agentproto

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNoiseRules 单次生效噪音规则数量上限
	MaxNoiseRules = 200
	// MaxNoiseRuleLen 单条噪音规则最大字符数
	MaxNoiseRuleLen = 160
)

var noiseServicePIDPattern = regexp.MustCompile(`^(.+-\d+)-(\d+)$`)

type parsedNoiseRule struct {
	raw        string
	svcPattern string
	opPattern  string
}

// NormalizeNoiseRules 清洗、去注释、大小写去重并保序返回噪音规则列表
func NormalizeNoiseRules(rules []string) []string {
	if len(rules) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(rules))
	out := make([]string, 0, len(rules))
	for _, raw := range rules {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// ParseNoiseRulesText 解析按换行或逗号分隔的噪音规则文本
func ParseNoiseRulesText(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	lines := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	return NormalizeNoiseRules(lines)
}

// ValidateNoiseRules 校验噪音规则合法性（长度、数量、控制字符、Service:Operation 语法）
func ValidateNoiseRules(rules []string) error {
	normalized := NormalizeNoiseRules(rules)
	if len(normalized) > MaxNoiseRules {
		return fmt.Errorf("噪音规则数量超过上限 (%d)", MaxNoiseRules)
	}
	for _, rule := range normalized {
		if utf8.RuneCountInString(rule) > MaxNoiseRuleLen {
			return fmt.Errorf("噪音规则 [%s] 长度超过上限 (%d)", rule, MaxNoiseRuleLen)
		}
		for _, r := range rule {
			if unicode.IsControl(r) {
				return fmt.Errorf("噪音规则 [%s] 包含非法控制字符", rule)
			}
		}
		if strings.Contains(rule, ":") {
			parts := strings.Split(rule, ":")
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return fmt.Errorf("噪音规则 [%s] 格式不合法，Service:Operation 必须两侧均非空且仅含单个冒号", rule)
			}
		}
	}
	return nil
}

// NoiseMatcher 纯标准库、线性时间通配噪音服务匹配器（供 Agent 与 Server 共用）
type NoiseMatcher struct {
	rules   []string
	entries []parsedNoiseRule
}

// NewNoiseMatcher 构造噪音规则匹配器
func NewNoiseMatcher(rules []string) *NoiseMatcher {
	normalized := NormalizeNoiseRules(rules)
	entries := make([]parsedNoiseRule, 0, len(normalized))
	for _, rule := range normalized {
		svcPat := rule
		opPat := ""
		if idx := strings.Index(rule, ":"); idx >= 0 {
			svcPat = strings.TrimSpace(rule[:idx])
			opPat = strings.TrimSpace(rule[idx+1:])
		}
		if svcPat == "" {
			continue
		}
		entries = append(entries, parsedNoiseRule{
			raw:        rule,
			svcPattern: strings.ToLower(svcPat),
			opPattern:  strings.ToLower(opPat),
		})
	}
	return &NoiseMatcher{
		rules:   normalized,
		entries: entries,
	}
}

// Empty 返回匹配器是否不包含任何有效规则
func (m *NoiseMatcher) Empty() bool {
	return m == nil || len(m.entries) == 0
}

// Rules 返回归一化后的规则副本
func (m *NoiseMatcher) Rules() []string {
	if m == nil || len(m.rules) == 0 {
		return []string{}
	}
	out := make([]string, len(m.rules))
	copy(out, m.rules)
	return out
}

// Match 判断给定服务名与方法名是否命中噪音规则（支持 * 通配与 <Class>-<svrName>-<pid> 归一化）
func (m *NoiseMatcher) Match(serviceName, operation string) bool {
	if m == nil || len(m.entries) == 0 {
		return false
	}
	svc := strings.TrimSpace(serviceName)
	if svc == "" {
		return false
	}
	op := strings.TrimSpace(operation)

	svcLower := strings.ToLower(svc)
	opLower := strings.ToLower(op)

	var baseSvcLower string
	if sub := noiseServicePIDPattern.FindStringSubmatch(svc); len(sub) == 3 {
		baseSvcLower = strings.ToLower(sub[1])
	}

	for _, entry := range m.entries {
		if entry.opPattern != "" && !matchGlobLower(entry.opPattern, opLower) {
			continue
		}
		if matchGlobLower(entry.svcPattern, svcLower) {
			return true
		}
		if baseSvcLower != "" && matchGlobLower(entry.svcPattern, baseSvcLower) {
			return true
		}
	}
	return false
}

// matchGlobLower 线性时间两指针通配匹配（pattern 与 text 均已小写，仅将 '*' 视为通配符）
func matchGlobLower(pattern, text string) bool {
	p := []rune(pattern)
	t := []rune(text)
	pi, ti := 0, 0
	starIdx := -1
	matchIdx := 0

	for ti < len(t) {
		if pi < len(p) && p[pi] == t[ti] {
			pi++
			ti++
		} else if pi < len(p) && p[pi] == '*' {
			starIdx = pi
			matchIdx = ti
			pi++
		} else if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			ti = matchIdx
		} else {
			return false
		}
	}

	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
