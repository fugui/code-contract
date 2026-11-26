package gitclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"code-contract/models"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/types/descriptorpb"
)

// NormalizeInput 为 Stage B Normalizer 的输入契约（含辅助 .json 映射配置预留字段）。
type NormalizeInput struct {
	Repo          models.GitRepo
	Files         map[string][]byte // repo_file_path -> 原始字节
	AuxConfigJSON []byte            // 预留：辅助 .json 映射与专属清理规则配置（后续 ADR 定义）
}

// NormalizeDiagnostic 描述 Stage B 清理整顿或语法门禁产生的单条诊断信息。
type NormalizeDiagnostic struct {
	RepoFilePath string `json:"repo_file_path"`
	RuleID       string `json:"rule_id"`
	Severity     string `json:"severity"` // ERROR | WARNING | INFO
	Line         int    `json:"line"`
	Column       int    `json:"column"`
	Message      string `json:"message"`
}

// NormalizedFileResult 为单文件清理产物及行号映射。
type NormalizedFileResult struct {
	RepoFilePath   string                `json:"repo_file_path"`
	CleanContent   []byte                `json:"-"`
	CleanSHA256    string                `json:"clean_sha256"`
	LineCount      int                   `json:"line_count"`
	LinePreserving bool                  `json:"line_preserving"`
	LineMap        map[int]int           `json:"line_map,omitempty"` // 清理后行号 -> 原始行号（LinePreserving 为 true 时可为空）
	Diagnostics    []NormalizeDiagnostic `json:"diagnostics,omitempty"`
}

// NormalizeOutput 为 Stage B Normalizer 的确定性输出。
type NormalizeOutput struct {
	NormalizerVersion string                          `json:"normalizer_version"`
	Files             map[string]NormalizedFileResult `json:"files"`
}

// Normalizer 将单仓原始 .proto 集合清理整顿为合法 Proto 源码（ADR-26 §2.4.2）。
type Normalizer interface {
	Version() string
	Normalize(ctx context.Context, in NormalizeInput) (NormalizeOutput, error)
}

// PassthroughNormalizer ADR-26 Phase 2 默认实现：原样透传原始源码，保证链路端到端运行。
type PassthroughNormalizer struct{}

// NewPassthroughNormalizer 创建默认透传清理器。
func NewPassthroughNormalizer() *PassthroughNormalizer {
	return &PassthroughNormalizer{}
}

// Version 返回包含规则版本与辅助配置摘要（若非空）的确定性版本键。
func (n *PassthroughNormalizer) Version() string {
	return "passthrough-0"
}

// EffectiveNormalizerVersion 将辅助配置摘要并入版本键，保证规则或映射变更自动失效增量缓存。
func EffectiveNormalizerVersion(n Normalizer, auxConfigJSON []byte) string {
	base := "passthrough-0"
	if n != nil && strings.TrimSpace(n.Version()) != "" {
		base = strings.TrimSpace(n.Version())
	}
	trimmedAux := bytes.TrimSpace(auxConfigJSON)
	if len(trimmedAux) == 0 {
		return base
	}
	sum := sha256.Sum256(trimmedAux)
	return fmt.Sprintf("%s+%s", base, hex.EncodeToString(sum[:])[:8])
}

// Normalize 执行确定性原样透传（不修改源码内容，保留行号）。
func (n *PassthroughNormalizer) Normalize(_ context.Context, in NormalizeInput) (NormalizeOutput, error) {
	ver := EffectiveNormalizerVersion(n, in.AuxConfigJSON)
	out := NormalizeOutput{
		NormalizerVersion: ver,
		Files:             make(map[string]NormalizedFileResult, len(in.Files)),
	}
	keys := make([]string, 0, len(in.Files))
	for k := range in.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		raw := in.Files[k]
		copied := make([]byte, len(raw))
		copy(copied, raw)
		sum := sha256.Sum256(copied)
		out.Files[k] = NormalizedFileResult{
			RepoFilePath:   k,
			CleanContent:   copied,
			CleanSHA256:    hex.EncodeToString(sum[:]),
			LineCount:      countNormalizedLines(string(copied)),
			LinePreserving: true,
			LineMap:        nil,
		}
	}
	return out, nil
}

// ParsedProtoSymbol 描述从清理后源码中解析出的全限定符号。
type ParsedProtoSymbol struct {
	Kind      string `json:"kind"` // SERVICE | MESSAGE | ENUM | EXTENSION
	FullName  string `json:"full_name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// SyntaxGateResult 表示 Stage B 后置「仅解析、不链接」语法门禁的判定结果。
type SyntaxGateResult struct {
	Passed       bool                  `json:"passed"`
	PackageName  string                `json:"package_name"`
	Imports      []string              `json:"imports"`
	Symbols      []ParsedProtoSymbol   `json:"symbols"`
	Diagnostics  []NormalizeDiagnostic `json:"diagnostics,omitempty"`
	ErrorSummary string                `json:"error_summary,omitempty"`
}

// ParseProtoSyntaxOnly 使用 protocompile/parser 执行仅解析、不链接的语法与基础语义门禁。
// 缺失 import 的链接期错误不会在此阶段拦截，而是在 Stage D 分仓编译阶段处理。
func ParseProtoSyntaxOnly(filename string, content []byte) SyntaxGateResult {
	var mu sync.Mutex
	var diags []NormalizeDiagnostic

	errRep := reporter.NewReporter(func(errWithPos reporter.ErrorWithPos) error {
		pos := errWithPos.GetPosition()
		msg := errWithPos.Error()
		if uw := errWithPos.Unwrap(); uw != nil {
			msg = uw.Error()
		}
		mu.Lock()
		diags = append(diags, NormalizeDiagnostic{
			RepoFilePath: filename,
			RuleID:       "PARSE_SYNTAX",
			Severity:     "ERROR",
			Line:         pos.Line,
			Column:       pos.Col,
			Message:      msg,
		})
		mu.Unlock()
		return nil
	}, nil)
	handler := reporter.NewHandler(errRep)

	astFile, parseErr := parser.Parse(filename, bytes.NewReader(content), handler)
	if parseErr != nil || astFile == nil {
		summary := "Proto 语法解析失败"
		if len(diags) > 0 {
			summary = fmt.Sprintf("L%d:%d %s", diags[0].Line, diags[0].Column, diags[0].Message)
		} else if parseErr != nil {
			summary = parseErr.Error()
		}
		return SyntaxGateResult{
			Passed:       false,
			Diagnostics:  diags,
			ErrorSummary: summary,
		}
	}

	res, resErr := parser.ResultFromAST(astFile, true, handler)
	if resErr != nil || res == nil {
		summary := "Proto 基础语义校验失败"
		if len(diags) > 0 {
			summary = fmt.Sprintf("L%d:%d %s", diags[0].Line, diags[0].Column, diags[0].Message)
		} else if resErr != nil {
			summary = resErr.Error()
		}
		return SyntaxGateResult{
			Passed:       false,
			Diagnostics:  diags,
			ErrorSummary: summary,
		}
	}

	fdp := res.FileDescriptorProto()
	pkgName := fdp.GetPackage()
	imports := append([]string(nil), fdp.GetDependency()...)
	symbols := extractSymbolsFromASTResult(res, pkgName)

	return SyntaxGateResult{
		Passed:      true,
		PackageName: pkgName,
		Imports:     imports,
		Symbols:     symbols,
	}
}

func extractSymbolsFromASTResult(res parser.Result, pkgName string) []ParsedProtoSymbol {
	fdp := res.FileDescriptorProto()
	if fdp == nil {
		return nil
	}
	prefix := ""
	if pkgName != "" {
		prefix = pkgName + "."
	}
	var out []ParsedProtoSymbol
	astFile := res.AST()
	nodeSpan := func(n ast.Node) (int, int) {
		if astFile == nil || n == nil {
			return 0, 0
		}
		ni := astFile.NodeInfo(n)
		return ni.Start().Line, ni.End().Line
	}

	for _, svc := range fdp.GetService() {
		fullName := prefix + svc.GetName()
		startLine, endLine := nodeSpan(res.ServiceNode(svc))
		out = append(out, ParsedProtoSymbol{
			Kind:      "SERVICE",
			FullName:  fullName,
			StartLine: startLine,
			EndLine:   endLine,
		})
	}

	var walkMsgs func(scopePrefix string, msgs []*descriptorpb.DescriptorProto)
	walkMsgs = func(scopePrefix string, msgs []*descriptorpb.DescriptorProto) {
		for _, m := range msgs {
			fullName := scopePrefix + m.GetName()
			startLine, endLine := nodeSpan(res.MessageNode(m))
			out = append(out, ParsedProtoSymbol{
				Kind:      "MESSAGE",
				FullName:  fullName,
				StartLine: startLine,
				EndLine:   endLine,
			})
			for _, en := range m.GetEnumType() {
				eStart, eEnd := nodeSpan(res.EnumNode(en))
				out = append(out, ParsedProtoSymbol{
					Kind:      "ENUM",
					FullName:  fullName + "." + en.GetName(),
					StartLine: eStart,
					EndLine:   eEnd,
				})
			}
			for _, ext := range m.GetExtension() {
				xStart, xEnd := nodeSpan(res.FieldNode(ext))
				out = append(out, ParsedProtoSymbol{
					Kind:      "EXTENSION",
					FullName:  fullName + "." + ext.GetName(),
					StartLine: xStart,
					EndLine:   xEnd,
				})
			}
			if len(m.GetNestedType()) > 0 {
				walkMsgs(fullName+".", m.GetNestedType())
			}
		}
	}
	walkMsgs(prefix, fdp.GetMessageType())

	for _, en := range fdp.GetEnumType() {
		startLine, endLine := nodeSpan(res.EnumNode(en))
		out = append(out, ParsedProtoSymbol{
			Kind:      "ENUM",
			FullName:  prefix + en.GetName(),
			StartLine: startLine,
			EndLine:   endLine,
		})
	}
	for _, ext := range fdp.GetExtension() {
		startLine, endLine := nodeSpan(res.FieldNode(ext))
		out = append(out, ParsedProtoSymbol{
			Kind:      "EXTENSION",
			FullName:  prefix + ext.GetName(),
			StartLine: startLine,
			EndLine:   endLine,
		})
	}
	return out
}
