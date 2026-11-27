package gitclient

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	uServiceDeclRe    = regexp.MustCompile(`^\s*uService\s+([A-Za-z_][A-Za-z0-9_]*)\s*(\{)?\s*$`)
	uServiceBlockImp  = regexp.MustCompile(`^\s*import\s+(?:(public|weak)\s+)?["']([^"']+)["']\s*;?\s*$`)
	uServiceIfaceRe   = regexp.MustCompile(`^interface\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(\s*([^)]*?)\s*\)(?:\s*return\s*\(\s*([^)]*?)\s*\))?(?:\s*(.+?))?\s*;\s*$`)
	uServiceMetaParen = regexp.MustCompile(`\(([^()]+)\)`)
	fileImportLineRe  = regexp.MustCompile(`^\s*import\s+(?:(public|weak)\s+)?["']([^"']+)["']\s*;?\s*$`)
)

type hoistedImportItem struct {
	modifier string
	path     string
	rawLine  int
}

// stripLineCommentsAndTrackBlock 剥离源码行中的注释（// 与 /* ... */），保留字符串字面量与跨行块注释状态。
func stripLineCommentsAndTrackBlock(line string, inBlockComment bool) (code string, nextInBlock bool) {
	var b strings.Builder
	n := len(line)
	inSingle := false
	inDouble := false
	i := 0
	for i < n {
		if inBlockComment {
			if i+1 < n && line[i] == '*' && line[i+1] == '/' {
				inBlockComment = false
				i += 2
				continue
			}
			i++
			continue
		}
		ch := line[i]
		if !inSingle && !inDouble {
			if i+1 < n && line[i] == '/' && line[i+1] == '/' {
				break
			}
			if i+1 < n && line[i] == '/' && line[i+1] == '*' {
				inBlockComment = true
				i += 2
				continue
			}
		}
		if ch == '"' && !inSingle && (i == 0 || line[i-1] != '\\') {
			inDouble = !inDouble
		} else if ch == '\'' && !inDouble && (i == 0 || line[i-1] != '\\') {
			inSingle = !inSingle
		}
		b.WriteByte(ch)
		i++
	}
	return b.String(), inBlockComment
}

// containsSymbolOutsideStrings 在纯代码片段中按词边界判断符号是否出现（避免子串误匹配）。
func containsSymbolOutsideStrings(code string, sym string) bool {
	if sym == "" || code == "" {
		return false
	}
	// 先将字符串字面量替换为占位空格，避免把 import "..." 里的路径误判为符号
	var clean strings.Builder
	inS, inD := false, false
	for i := 0; i < len(code); i++ {
		ch := code[i]
		if ch == '"' && !inS && (i == 0 || code[i-1] != '\\') {
			inD = !inD
			clean.WriteByte(' ')
			continue
		}
		if ch == '\'' && !inD && (i == 0 || code[i-1] != '\\') {
			inS = !inS
			clean.WriteByte(' ')
			continue
		}
		if inS || inD {
			clean.WriteByte(' ')
			continue
		}
		clean.WriteByte(ch)
	}
	s := clean.String()
	isIdent := func(c byte) bool {
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.'
	}
	start := 0
	for {
		idx := strings.Index(s[start:], sym)
		if idx < 0 {
			return false
		}
		abs := start + idx
		leftOK := abs == 0 || !isIdent(s[abs-1])
		rightPos := abs + len(sym)
		rightOK := rightPos >= len(s) || !isIdent(s[rightPos])
		if leftOK && rightOK {
			return true
		}
		start = abs + len(sym)
	}
}

func formatUServiceMetaComment(tail string) string {
	trimmed := strings.TrimSpace(tail)
	if trimmed == "" {
		return ""
	}
	matches := uServiceMetaParen.FindAllStringSubmatch(trimmed, -1)
	if len(matches) == 0 {
		return trimmed
	}
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		inner := strings.TrimSpace(m[1])
		if inner != "" {
			parts = append(parts, inner)
		}
	}
	return strings.Join(parts, ", ")
}

// convertUServiceLines 执行 NORM-006（注释感知状态机转换 uService -> service + 块内 import 提升 + 隐式依赖补齐），并精确维护 rawLine。
func convertUServiceLines(
	rp string,
	lines []trackedLine,
	stripPG bool,
	implicitImports map[string]string,
	normEnabled bool,
) (out []trackedLine, isUService bool, diags []NormalizeDiagnostic) {
	out = make([]trackedLine, 0, len(lines)+4)

	inBlockComment := false
	inUServiceHeader := false
	inUServiceBlock := false
	braceDepth := 0

	var ifaceBuf strings.Builder
	ifaceStartRawLine := 0
	var hoistedImports []hoistedImportItem
	usedEmpty := false
	convertedIfaceCount := 0

	flushUnconverted := func(text string, rawLine int) {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return
		}
		out = append(out, trackedLine{
			text:    "  // UNCONVERTED: " + trimmed,
			rawLine: rawLine,
		})
		diags = append(diags, NormalizeDiagnostic{
			RepoFilePath: rp,
			RuleID:       NormRuleUServiceConvert,
			Severity:     "WARNING",
			Line:         len(out),
			Column:       1,
			Message:      fmt.Sprintf("uService 块内存在无法自动转换的语句: %s", trimmed),
		})
	}

	emitInterface := func(stmt string, rawLine int) bool {
		m := uServiceIfaceRe.FindStringSubmatch(strings.TrimSpace(stmt))
		if len(m) != 5 {
			return false
		}
		methodName := strings.TrimSpace(m[1])
		reqType := strings.TrimSpace(m[2])
		repType := strings.TrimSpace(m[3])
		tailMeta := formatUServiceMetaComment(m[4])

		if repType == "" {
			repType = "google.protobuf.Empty"
			usedEmpty = true
		}
		rpcLine := fmt.Sprintf("  rpc %s(%s) returns (%s);", methodName, reqType, repType)
		if tailMeta != "" {
			rpcLine += " // @uservice: " + tailMeta
		}
		out = append(out, trackedLine{
			text:    rpcLine,
			rawLine: rawLine,
		})
		convertedIfaceCount++
		return true
	}

	for _, tl := range lines {
		code, nextInBlock := stripLineCommentsAndTrackBlock(tl.text, inBlockComment)
		inBlockComment = nextInBlock
		trimmedCode := strings.TrimSpace(code)

		if !normEnabled {
			if !inUServiceBlock && uServiceDeclRe.MatchString(trimmedCode) {
				isUService = true
			}
			out = append(out, tl)
			continue
		}

		if inUServiceHeader {
			if trimmedCode == "{" {
				inUServiceHeader = false
				inUServiceBlock = true
				braceDepth = 1
				out = append(out, tl)
				continue
			}
			if strings.HasPrefix(trimmedCode, "{") {
				inUServiceHeader = false
				inUServiceBlock = true
				braceDepth = 1
				// 继续处理大括号后的同行业务内容
				rest := strings.TrimSpace(strings.TrimPrefix(trimmedCode, "{"))
				out = append(out, trackedLine{text: "{", rawLine: tl.rawLine})
				if rest == "" {
					continue
				}
				trimmedCode = rest
			} else {
				out = append(out, tl)
				continue
			}
		}

		if !inUServiceBlock {
			if m := uServiceDeclRe.FindStringSubmatch(trimmedCode); len(m) == 3 {
				isUService = true
				svcName := m[1]
				hasBrace := m[2] == "{"
				if hasBrace {
					inUServiceBlock = true
					braceDepth = 1
					out = append(out, trackedLine{
						text:    fmt.Sprintf("service %s {", svcName),
						rawLine: tl.rawLine,
					})
				} else {
					inUServiceHeader = true
					out = append(out, trackedLine{
						text:    fmt.Sprintf("service %s", svcName),
						rawLine: tl.rawLine,
					})
				}
				continue
			}
			out = append(out, tl)
			continue
		}

		// 在 uService 块内部：
		// 1. 若正在累积多行 interface 声明
		if ifaceBuf.Len() > 0 {
			if trimmedCode == "" {
				continue
			}
			ifaceBuf.WriteByte(' ')
			ifaceBuf.WriteString(trimmedCode)
			curStmt := ifaceBuf.String()
			if strings.Contains(curStmt, ";") {
				ifaceBuf.Reset()
				if !emitInterface(curStmt, ifaceStartRawLine) {
					flushUnconverted(curStmt, ifaceStartRawLine)
				}
				ifaceStartRawLine = 0
			}
			continue
		}

		// 纯注释行或空行原样保留
		if trimmedCode == "" {
			out = append(out, tl)
			continue
		}

		// 2. 块内 import 语句 -> 提升到文件头部
		if impM := uServiceBlockImp.FindStringSubmatch(trimmedCode); len(impM) == 3 {
			hoistedImports = append(hoistedImports, hoistedImportItem{
				modifier: strings.TrimSpace(impM[1]),
				path:     strings.TrimSpace(impM[2]),
				rawLine:  tl.rawLine,
			})
			continue
		}

		// 3. 块结束大括号处理
		if trimmedCode == "}" || trimmedCode == "};" {
			braceDepth--
			out = append(out, trackedLine{text: "}", rawLine: tl.rawLine})
			if braceDepth <= 0 {
				inUServiceBlock = false
				braceDepth = 0
			}
			continue
		}

		// 4. interface 声明（单行或多行）
		if strings.HasPrefix(trimmedCode, "interface ") || strings.HasPrefix(trimmedCode, "interface\t") || trimmedCode == "interface" {
			if strings.Contains(trimmedCode, ";") {
				if !emitInterface(trimmedCode, tl.rawLine) {
					flushUnconverted(trimmedCode, tl.rawLine)
				}
			} else {
				ifaceBuf.Reset()
				ifaceBuf.WriteString(trimmedCode)
				ifaceStartRawLine = tl.rawLine
			}
			continue
		}

		// 跟踪其他花括号以免嵌套大括号提前退出 service 块
		opens := strings.Count(trimmedCode, "{")
		closes := strings.Count(trimmedCode, "}")
		if opens > 0 || closes > 0 {
			braceDepth += opens - closes
			if braceDepth <= 0 {
				inUServiceBlock = false
				braceDepth = 0
				out = append(out, trackedLine{text: "}", rawLine: tl.rawLine})
				continue
			}
		}

		flushUnconverted(trimmedCode, tl.rawLine)
	}

	if ifaceBuf.Len() > 0 {
		flushUnconverted(ifaceBuf.String(), ifaceStartRawLine)
	}

	if isUService && normEnabled {
		diags = append(diags, NormalizeDiagnostic{
			RepoFilePath: rp,
			RuleID:       NormRuleUServiceConvert,
			Severity:     "INFO",
			Line:         1,
			Column:       1,
			Message:      fmt.Sprintf("已转换 uService 声明 (%d 个接口)", convertedIfaceCount),
		})
	}

	if !normEnabled {
		return out, isUService, diags
	}

	// 收集已有文件级 import（归一化后去重）
	existingImpNorm := make(map[string]struct{})
	lastImportIdx := -1
	pkgOrSyntaxIdx := -1
	inBlk := false
	for idx, tl := range out {
		code, nextBlk := stripLineCommentsAndTrackBlock(tl.text, inBlk)
		inBlk = nextBlk
		tc := strings.TrimSpace(code)
		if tc == "" {
			continue
		}
		if strings.HasPrefix(tc, "syntax") || strings.HasPrefix(tc, "package ") {
			pkgOrSyntaxIdx = idx
		}
		if m := fileImportLineRe.FindStringSubmatch(tc); len(m) == 3 {
			normP := NormalizeLogicalPath(m[2], stripPG)
			if normP != "" {
				existingImpNorm[normP] = struct{}{}
			}
			lastImportIdx = idx
		}
	}

	// 汇总待插入的 import（块内提升 + 隐式符号注入）
	var toInsert []trackedLine
	for _, hi := range hoistedImports {
		normP := NormalizeLogicalPath(hi.path, stripPG)
		if normP == "" {
			continue
		}
		if _, dup := existingImpNorm[normP]; dup {
			continue
		}
		existingImpNorm[normP] = struct{}{}
		mod := ""
		if hi.modifier != "" {
			mod = hi.modifier + " "
		}
		toInsert = append(toInsert, trackedLine{
			text:    fmt.Sprintf(`import %s"%s";`, mod, hi.path),
			rawLine: hi.rawLine,
		})
	}

	// 扫描非注释代码区隐式符号并注入 implicit_symbol_imports
	curFileNorm := NormalizeLogicalPath(rp, stripPG)
	if len(implicitImports) > 0 || usedEmpty {
		var codeTextBuf strings.Builder
		inBlk = false
		for _, tl := range out {
			code, nextBlk := stripLineCommentsAndTrackBlock(tl.text, inBlk)
			inBlk = nextBlk
			if strings.TrimSpace(code) != "" {
				codeTextBuf.WriteString(code)
				codeTextBuf.WriteByte('\n')
			}
		}
		allCode := codeTextBuf.String()

		// 确定性排序符号键
		symKeys := make([]string, 0, len(implicitImports)+1)
		hasEmptyKey := false
		for sym := range implicitImports {
			if sym == "google.protobuf.Empty" {
				hasEmptyKey = true
			}
			symKeys = append(symKeys, sym)
		}
		if usedEmpty && !hasEmptyKey {
			symKeys = append(symKeys, "google.protobuf.Empty")
		}
		sort.Strings(symKeys)

		for _, sym := range symKeys {
			target := implicitImports[sym]
			if sym == "google.protobuf.Empty" && strings.TrimSpace(target) == "" {
				target = "google/protobuf/empty.proto"
			}
			normTarget := NormalizeLogicalPath(target, stripPG)
			if normTarget == "" || normTarget == curFileNorm {
				continue
			}
			if _, exists := existingImpNorm[normTarget]; exists {
				continue
			}
			if containsSymbolOutsideStrings(allCode, sym) {
				existingImpNorm[normTarget] = struct{}{}
				toInsert = append(toInsert, trackedLine{
					text:    fmt.Sprintf(`import "%s";`, normTarget),
					rawLine: 1,
				})
			}
		}
	}

	if len(toInsert) > 0 {
		insertAt := 0
		if lastImportIdx >= 0 {
			insertAt = lastImportIdx + 1
		} else if pkgOrSyntaxIdx >= 0 {
			insertAt = pkgOrSyntaxIdx + 1
		} else {
			insertAt = findHeaderInsertIndex(out)
		}
		if insertAt < len(out) && out[insertAt].rawLine > 0 {
			for idx := range toInsert {
				if toInsert[idx].rawLine <= 0 {
					toInsert[idx].rawLine = out[insertAt].rawLine
				}
			}
		}
		merged := make([]trackedLine, 0, len(out)+len(toInsert))
		merged = append(merged, out[:insertAt]...)
		merged = append(merged, toInsert...)
		merged = append(merged, out[insertAt:]...)
		out = merged
	}

	return out, isUService, diags
}
