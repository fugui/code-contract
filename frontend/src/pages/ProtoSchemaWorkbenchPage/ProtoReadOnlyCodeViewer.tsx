import React, { useEffect, useMemo, useState } from 'react';
import { Lock, AlertTriangle, Code2, Hash } from 'lucide-react';
import type { ProtoSourceViolationDTO, ProtoSymbolAnchorDTO } from '../../types';
import './ProtoDocsAndCodeViewer.css';

export interface ProtoLexToken {
  kind: 'comment' | 'keyword' | 'type' | 'string' | 'number' | 'tag' | 'plain';
  text: string;
}

const PROTO_KEYWORDS = new Set([
  'syntax',
  'edition',
  'package',
  'import',
  'public',
  'weak',
  'option',
  'message',
  'enum',
  'service',
  'rpc',
  'returns',
  'stream',
  'oneof',
  'repeated',
  'optional',
  'required',
  'reserved',
  'to',
  'max',
  'map',
  'true',
  'false',
]);

const PROTO_SCALAR_TYPES = new Set([
  'double',
  'float',
  'int32',
  'int64',
  'uint32',
  'uint64',
  'sint32',
  'sint64',
  'fixed32',
  'fixed64',
  'sfixed32',
  'sfixed64',
  'bool',
  'string',
  'bytes',
]);

function tokenizeCommentSegment(commentText: string): ProtoLexToken[] {
  const out: ProtoLexToken[] = [];
  const tagRegex = /(@(?:owner|deprecated|error_code))/g;
  let lastIdx = 0;
  let match: RegExpExecArray | null = tagRegex.exec(commentText);
  while (match !== null) {
    if (match.index > lastIdx) {
      out.push({ kind: 'comment', text: commentText.slice(lastIdx, match.index) });
    }
    out.push({ kind: 'tag', text: match[0] });
    lastIdx = match.index + match[0].length;
    match = tagRegex.exec(commentText);
  }
  if (lastIdx < commentText.length) {
    out.push({ kind: 'comment', text: commentText.slice(lastIdx) });
  }
  return out;
}

/** 纯函数词法着色切片：通过 React JSX 节点渲染，严禁拼接 HTML，天然防 XSS */
export function tokenizeProtoLine(line: string): ProtoLexToken[] {
  if (!line) {
    return [{ kind: 'plain', text: '' }];
  }

  const tokens: ProtoLexToken[] = [];
  let idx = 0;

  while (idx < line.length) {
    if (line[idx] === '/' && line[idx + 1] === '/') {
      tokens.push(...tokenizeCommentSegment(line.slice(idx)));
      break;
    }

    if (line[idx] === '"' || line[idx] === "'") {
      const quote = line[idx];
      let end = idx + 1;
      while (end < line.length) {
        if (line[end] === '\\') {
          end += 2;
          continue;
        }
        if (line[end] === quote) {
          end += 1;
          break;
        }
        end += 1;
      }
      tokens.push({ kind: 'string', text: line.slice(idx, end) });
      idx = end;
      continue;
    }

    if (/[A-Za-z_]/.test(line[idx])) {
      let end = idx + 1;
      while (end < line.length && /[A-Za-z0-9_]/.test(line[end])) {
        end += 1;
      }
      const word = line.slice(idx, end);
      if (PROTO_KEYWORDS.has(word)) {
        tokens.push({ kind: 'keyword', text: word });
      } else if (PROTO_SCALAR_TYPES.has(word)) {
        tokens.push({ kind: 'type', text: word });
      } else {
        tokens.push({ kind: 'plain', text: word });
      }
      idx = end;
      continue;
    }

    if (/[0-9]/.test(line[idx])) {
      let end = idx + 1;
      while (end < line.length && /[0-9xa-fA-F.]/.test(line[end])) {
        end += 1;
      }
      tokens.push({ kind: 'number', text: line.slice(idx, end) });
      idx = end;
      continue;
    }

    let end = idx + 1;
    while (
      end < line.length &&
      !(line[end] === '/' && line[end + 1] === '/') &&
      line[end] !== '"' &&
      line[end] !== "'" &&
      !/[A-Za-z0-9_]/.test(line[end])
    ) {
      end += 1;
    }
    tokens.push({ kind: 'plain', text: line.slice(idx, end) });
    idx = end;
  }

  return tokens;
}

export interface ProtoReadOnlyCodeViewerProps {
  filePath: string;
  cleanRelPath?: string;
  repoFilePath?: string;
  content: string;
  rawContent?: string;
  lineMap?: Record<string, number> | null;
  pipelineStatus?: string;
  statusReason?: string;
  normalizerVersion?: string;
  isExternalCopy?: boolean;
  isUServiceSource?: boolean;
  isHarvestedBase?: boolean;
  autoFixCount?: number;
  highlightLine?: number;
  violations?: ProtoSourceViolationDTO[];
  symbolAnchors?: ProtoSymbolAnchorDTO[];
  versionLabel?: string;
  hasSourceInfo?: boolean;
  sourceOrigin?: string;
  onSelectLine?: (line: number) => void;
}

export const ProtoReadOnlyCodeViewer: React.FC<ProtoReadOnlyCodeViewerProps> = ({
  filePath,
  cleanRelPath,
  repoFilePath,
  content,
  rawContent,
  lineMap,
  pipelineStatus,
  statusReason,
  normalizerVersion,
  isExternalCopy = false,
  isUServiceSource = false,
  isHarvestedBase = false,
  autoFixCount = 0,
  highlightLine,
  violations = [],
  symbolAnchors = [],
  versionLabel,
  hasSourceInfo = true,
  sourceOrigin,
  onSelectLine,
}) => {
  const [sourceStageMode, setSourceStageMode] = useState<'clean' | 'raw' | 'split'>('clean');
  const [rawJumpLine, setRawJumpLine] = useState<number | null>(null);
  const hasRawVariant = Boolean(rawContent && rawContent.length > 0);
  const displayedContent =
    sourceStageMode === 'raw' && rawContent ? rawContent : content;
  const activeHighlightLine =
    sourceStageMode === 'raw' && rawJumpLine ? rawJumpLine : highlightLine;

  useEffect(() => {
    setRawJumpLine(null);
  }, [filePath]);

  const lines = useMemo(() => {
    const normalized = displayedContent.replace(/\r\n/g, '\n');
    const split = normalized.split('\n');
    if (split.length > 1 && split[split.length - 1] === '') {
      split.pop();
    }
    return split;
  }, [displayedContent]);

  const violationsByLine = useMemo(() => {
    const map = new Map<number, ProtoSourceViolationDTO[]>();
    for (const v of violations) {
      const arr = map.get(v.line) ?? [];
      arr.push(v);
      map.set(v.line, arr);
    }
    return map;
  }, [violations]);

  useEffect(() => {
    if (!activeHighlightLine || activeHighlightLine <= 0 || typeof document === 'undefined') {
      return;
    }
    const target = document.getElementById(`L${activeHighlightLine}`);
    if (target && typeof target.scrollIntoView === 'function') {
      target.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }, [activeHighlightLine, filePath, sourceStageMode]);

  const handleJumpAnchor = (line: number) => {
    if (onSelectLine) {
      onSelectLine(line);
    }
    if (typeof document !== 'undefined') {
      const target = document.getElementById(`L${line}`);
      if (target && typeof target.scrollIntoView === 'function') {
        target.scrollIntoView({ block: 'center', behavior: 'smooth' });
      }
    }
  };

  const handleJumpRawLine = (origLine: number) => {
    setRawJumpLine(origLine);
    setSourceStageMode('raw');
    if (onSelectLine) {
      onSelectLine(origLine);
    }
  };

  return (
    <div className="pdoc-code-viewer" data-testid="proto-readonly-code-viewer">
      <div className="pdoc-code-viewer__toolbar">
        <div className="pdoc-meta-row">
          <Code2 size={15} />
          <span className="pdoc-code-viewer__filepath">{filePath}</span>
          {versionLabel && <span className="pdoc-pill">{versionLabel}</span>}
          {pipelineStatus && (
            <span
              className={`code-badge ${
                pipelineStatus === 'QUARANTINED'
                  ? 'code-badge-danger'
                  : pipelineStatus === 'NORMALIZED'
                    ? 'code-badge-success'
                    : 'code-badge-info'
              }`}
              title={statusReason || pipelineStatus}
            >
              {pipelineStatus}
            </span>
          )}
          {normalizerVersion && <span className="pdoc-pill">{normalizerVersion}</span>}
          {isUServiceSource && (
            <span className="code-badge code-badge-info" data-testid="badge-uservice-converted">
              uService 已转换
            </span>
          )}
          {autoFixCount > 0 && (
            <span className="code-badge code-badge-success" data-testid="badge-r7-autofix">
              R7 自动修复 ({autoFixCount})
            </span>
          )}
          {isHarvestedBase && (
            <span className="code-badge code-badge-neutral" data-testid="badge-r4-harvested">
              R4 收割基座
            </span>
          )}
          {isExternalCopy && <span className="code-badge code-badge-warning">外部副本</span>}
          {activeHighlightLine && activeHighlightLine > 0 && (
            <span className="code-badge code-badge-warning">锚点 #L{activeHighlightLine}</span>
          )}
        </div>
        <div className="pdoc-meta-row">
          {hasRawVariant && (
            <div className="code-tab-pills" role="group" aria-label="源码视图切换">
              <button
                type="button"
                className={`code-pill-btn ${sourceStageMode === 'clean' ? 'code-pill-btn-active' : ''}`}
                onClick={() => setSourceStageMode('clean')}
              >
                Clean
              </button>
              <button
                type="button"
                className={`code-pill-btn ${sourceStageMode === 'raw' ? 'code-pill-btn-active' : ''}`}
                onClick={() => setSourceStageMode('raw')}
              >
                Raw
              </button>
              <button
                type="button"
                className={`code-pill-btn ${sourceStageMode === 'split' ? 'code-pill-btn-active' : ''}`}
                onClick={() => setSourceStageMode('split')}
              >
                并排对比
              </button>
            </div>
          )}
          {sourceOrigin === 'DESCRIPTOR_RECONSTRUCTED' && (
            <span className="pdoc-pill">M23 描述集重构只读视图</span>
          )}
          <span className="pdoc-pill">
            <Lock size={12} /> 只读 · Git-Only SSOT
          </span>
        </div>
      </div>

      {pipelineStatus === 'QUARANTINED' && statusReason && (
        <div className="pdoc-banner pdoc-banner--warning">
          <AlertTriangle size={15} />
          <span>隔离原因：{statusReason}</span>
        </div>
      )}

      {cleanRelPath && repoFilePath && cleanRelPath !== repoFilePath && (
        <div className="pdoc-banner" data-testid="pg-flatten-path-banner">
          <span>
            raw/ 原路径: <code>{repoFilePath}</code> → clean/ 去 /pg 扁平路径:{' '}
            <code>{cleanRelPath}</code>
          </span>
        </div>
      )}

      {!hasSourceInfo && (
        <div className="pdoc-banner pdoc-banner--warning">
          <AlertTriangle size={15} />
          <span>
            当前制品来自无 <code>SourceCodeInfo</code> 的早期手工 <code>.pb</code> 描述集，源码视图由 AST 签名确定性重构生成（不含原始注释行号）。
          </span>
        </div>
      )}

      {symbolAnchors.length > 0 && (
        <div className="pdoc-code-viewer__anchors" aria-label="符号快速锚点">
          <span className="pdoc-pill">
            <Hash size={11} /> 符号定位
          </span>
          {symbolAnchors.slice(0, 12).map((anchor) => {
            const shortName = anchor.symbol_full_name.split('.').pop() || anchor.symbol_full_name;
            return (
              <button
                key={`${anchor.symbol_kind}:${anchor.symbol_full_name}:${anchor.start_line}`}
                type="button"
                className="pdoc-code-anchor-btn"
                onClick={() => handleJumpAnchor(anchor.start_line)}
                title={`${anchor.symbol_kind} ${anchor.symbol_full_name} (#L${anchor.start_line})`}
              >
                {anchor.symbol_kind.slice(0, 3)} {shortName}#L{anchor.start_line}
              </button>
            );
          })}
        </div>
      )}

      {sourceStageMode === 'split' && hasRawVariant ? (
        <div className="pgit-dual-code-grid" data-testid="proto-split-compare">
          <div className="pgit-dual-code-pane">
            <div className="pgit-dual-code-pane__title">Raw 原始快照</div>
            <pre className="pgit-dual-code-pre">{rawContent}</pre>
          </div>
          <div className="pgit-dual-code-pane">
            <div className="pgit-dual-code-pane__title">Clean 规范化源码 ({normalizerVersion || 'clean'})</div>
            <pre className="pgit-dual-code-pre">{content}</pre>
          </div>
        </div>
      ) : (
        <div className="pdoc-code-scroll">
          <table className="pdoc-code-table" role="presentation">
            <tbody>
              {lines.map((lineText, idx) => {
                const lineNum = idx + 1;
                const isHighlighted = activeHighlightLine === lineNum;
                const mappedOrigLine =
                  sourceStageMode === 'clean' && lineMap ? lineMap[String(lineNum)] : undefined;
                const lineViolations = violationsByLine.get(lineNum) ?? [];
                const hasError = lineViolations.some((v) => v.severity === 'ERROR');
                const hasWarning = !hasError && lineViolations.some((v) => v.severity === 'WARNING');
                const wavyClass = hasError
                  ? 'pdoc-code-line__wavy--error'
                  : hasWarning
                    ? 'pdoc-code-line__wavy--warning'
                    : '';

                const rowClass = isHighlighted
                  ? 'pdoc-code-line pdoc-code-line--highlighted'
                  : 'pdoc-code-line';
                const tokens = tokenizeProtoLine(lineText);

                return (
                  <React.Fragment key={lineNum}>
                    <tr id={`L${lineNum}`} data-line={lineNum} className={rowClass}>
                      <td className="pdoc-code-line__gutter">
                        <a
                          href={`#L${lineNum}`}
                          className="pdoc-code-line__anchor"
                          onClick={(e) => {
                            if (onSelectLine) {
                              e.preventDefault();
                              onSelectLine(lineNum);
                            }
                          }}
                        >
                          {lineNum}
                        </a>
                        {mappedOrigLine !== undefined && hasRawVariant && (
                          <button
                            type="button"
                            className="pgit-line-map-tag"
                            title={`跳转到 Raw 原始第 ${mappedOrigLine} 行`}
                            onClick={() => handleJumpRawLine(mappedOrigLine)}
                          >
                            {`原L${mappedOrigLine}`}
                          </button>
                        )}
                      </td>
                      <td className={`pdoc-code-line__code ${wavyClass}`}>
                        {tokens.map((tok, tkIdx) => (
                          <span key={tkIdx} className={tok.kind === 'plain' ? undefined : `pdoc-tok--${tok.kind}`}>
                            {tok.text}
                          </span>
                        ))}
                      </td>
                    </tr>
                    {lineViolations.map((viol, vIdx) => (
                      <tr key={`v-${lineNum}-${vIdx}`} className="pdoc-code-diag-row">
                        <td className="pdoc-code-line__gutter" />
                        <td className="pdoc-code-diag-row__cell">
                          <span className={`code-badge ${viol.severity === 'ERROR' ? 'code-badge-danger' : 'code-badge-warning'}`}>
                            {viol.rule_code}
                          </span>{' '}
                          {viol.message}
                        </td>
                      </tr>
                    ))}
                  </React.Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
};
