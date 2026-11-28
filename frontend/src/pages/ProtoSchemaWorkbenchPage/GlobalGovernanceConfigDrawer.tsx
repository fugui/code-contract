import React, { useCallback, useEffect, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  Download,
  FileCode2,
  Play,
  RefreshCw,
  Save,
  Sparkles,
  Upload,
} from 'lucide-react';
import { Drawer } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import type {
  BuiltinRuleMetaDTO,
  GlobalGovernanceDryRunResultDTO,
  GlobalGovernanceSectionDTO,
  GovernanceConfigDocDTO,
} from '../../types';
import './ProtoDocsAndCodeViewer.css';

export const PRODUCTION_USERVICE_PRESET_GLOBAL: GlobalGovernanceSectionDTO = {
  collect_globs: ['pg/**/*.proto', '**/pg/**/*.proto'],
  exclude_globs: [
    'build/**',
    'test/**',
    'doc/**',
    '**/test/**',
    '**/testdata/**',
    '**/*_test.proto',
    '**/mock/**',
  ],
  exclude_subsystems: ['build', 'test', 'doc', 'tools'],
  exclude_repo_patterns: ['-doc$', '-test$'],
  enabled_rules: [
    'NORM-001',
    'NORM-002',
    'NORM-003',
    'NORM-004',
    'NORM-005',
    'NORM-006',
    'NORM-007',
  ],
  default_syntax: 'auto',
  strip_pg_segment: true,
  implicit_symbol_imports: {
    'google.protobuf.Empty': 'google/protobuf/empty.proto',
    'google.protobuf.Any': 'google/protobuf/any.proto',
    'google.protobuf.Timestamp': 'google/protobuf/timestamp.proto',
  },
  shared_base_sources: [
    { path_hint: 'il/pg', priority: 1, description: 'IL 基础接口层（免疫 R0 排除）' },
    { path_hint: 'sw/pg', priority: 2, description: 'SW 公共底座层' },
    { path_hint: 'public/pg', priority: 3, description: 'Public 公共契约模型' },
  ],
  pool_reconcile: {
    closure_only: true,
    enable_r7_auto_fix: true,
    max_r7_rounds: 10,
    enable_r8_quarantine: true,
    max_r8_rounds: 150,
    block_on_unresolved_conflict: false,
  },
};

export interface GlobalGovernanceConfigDrawerProps {
  open: boolean;
  isAdmin: boolean;
  onClose: () => void;
  onApplied?: () => void;
}

export const GlobalGovernanceConfigDrawer: React.FC<GlobalGovernanceConfigDrawerProps> = ({
  open,
  isAdmin,
  onClose,
  onApplied,
}) => {
  const toast = useToast();
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  const [configSHA, setConfigSHA] = useState('');
  const [builtinRules, setBuiltinRules] = useState<BuiltinRuleMetaDTO[]>([]);
  const [jsonText, setJsonText] = useState('');
  const [parseError, setParseError] = useState('');
  const [dryRunResult, setDryRunResult] = useState<GlobalGovernanceDryRunResultDTO | null>(null);

  const loadConfig = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await schemaApi.getProtoGovernanceConfig();
      setRevision(resp.revision ?? resp.doc?.revision ?? 0);
      setConfigSHA(resp.config_sha256 || '');
      setBuiltinRules(resp.builtin_rules || []);
      setJsonText(JSON.stringify(resp.doc, null, 2));
      setParseError('');
      setDryRunResult(null);
    } catch (err) {
      toast.error((err as Error).message || '加载全局治理配置失败');
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    if (open) {
      void loadConfig();
    }
  }, [open, loadConfig]);

  const parseCurrentDoc = (): GovernanceConfigDocDTO | null => {
    try {
      const parsed = JSON.parse(jsonText) as GovernanceConfigDocDTO;
      if (!parsed || typeof parsed !== 'object' || parsed.schema_version !== 'proto-governance/v1') {
        setParseError('JSON 必须包含 "schema_version": "proto-governance/v1"');
        return null;
      }
      setParseError('');
      return parsed;
    } catch (err) {
      setParseError((err as Error).message || 'JSON 解析失败');
      return null;
    }
  };

  const handleToggleGlobalRule = (ruleId: string, checked: boolean) => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    const current = new Set(doc.global?.enabled_rules || []);
    if (checked) {
      current.add(ruleId);
    } else {
      current.delete(ruleId);
    }
    const next: GovernanceConfigDocDTO = {
      ...doc,
      global: {
        ...doc.global,
        enabled_rules: Array.from(current),
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
  };

  const handleDefaultSyntaxChange = (syn: 'auto' | 'proto2' | 'proto3') => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    const next: GovernanceConfigDocDTO = {
      ...doc,
      global: {
        ...doc.global,
        default_syntax: syn,
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
  };

  const handleStripPGChange = (checked: boolean) => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    const next: GovernanceConfigDocDTO = {
      ...doc,
      global: {
        ...doc.global,
        strip_pg_segment: checked,
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
  };

  const handleBlockOnConflictChange = (checked: boolean) => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    const next: GovernanceConfigDocDTO = {
      ...doc,
      global: {
        ...doc.global,
        pool_reconcile: {
          ...(doc.global?.pool_reconcile || {}),
          block_on_unresolved_conflict: checked,
        },
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
  };

  const handleExcludeSubsystemsChange = (raw: string) => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    const list = raw
      .split(/[,，\n]/)
      .map((s) => s.trim())
      .filter(Boolean);
    const next: GovernanceConfigDocDTO = {
      ...doc,
      global: {
        ...doc.global,
        exclude_subsystems: list,
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
  };

  const handleSharedBaseHintsChange = (raw: string) => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    const items = raw
      .split(/[,，\n]/)
      .map((s) => s.trim())
      .filter(Boolean)
      .map((hint, idx) => ({
        path_hint: hint,
        priority: idx + 1,
      }));
    const next: GovernanceConfigDocDTO = {
      ...doc,
      global: {
        ...doc.global,
        shared_base_sources: items,
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
  };

  const handleLoadProductionPreset = () => {
    let baseDoc: GovernanceConfigDocDTO = {
      schema_version: 'proto-governance/v1',
      revision,
      global: {},
      repos: {},
    };
    try {
      const existing = JSON.parse(jsonText) as GovernanceConfigDocDTO;
      if (existing && existing.schema_version === 'proto-governance/v1') {
        baseDoc = existing;
      }
    } catch {
      // 使用默认骨架
    }
    const next: GovernanceConfigDocDTO = {
      ...baseDoc,
      global: {
        ...baseDoc.global,
        ...PRODUCTION_USERVICE_PRESET_GLOBAL,
      },
    };
    setJsonText(JSON.stringify(next, null, 2));
    setParseError('');
    setDryRunResult(null);
    toast.info('已载入现网 uService + 去 /pg 治理预设，建议执行全局 Dry-Run 后应用');
  };

  const handleExportJson = () => {
    const doc = parseCurrentDoc();
    const content = doc ? JSON.stringify(doc, null, 2) : jsonText;
    const blob = new Blob([content], { type: 'application/json;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `proto-governance.v${revision}.json`;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    URL.revokeObjectURL(url);
  };

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      JSON.parse(text);
      setJsonText(text);
      setParseError('');
      setDryRunResult(null);
      toast.info(`已载入 ${file.name}，建议先执行「全局 Dry-Run 校验」`);
    } catch (err) {
      toast.error(`导入文件 JSON 格式非法: ${(err as Error).message}`);
    } finally {
      e.target.value = '';
    }
  };

  const handleDryRun = async () => {
    const doc = parseCurrentDoc();
    if (!doc) return;
    setBusy(true);
    try {
      const res = await schemaApi.dryRunProtoGovernance({ config: doc });
      setDryRunResult(res);
      if (!res.schema_valid) {
        toast.warning(`Schema 校验未通过: ${res.validation_error || '格式非法'}`);
      } else if (res.global_collisions && res.global_collisions.length > 0) {
        toast.warning(`检测到 ${res.global_collisions.length} 组全局导出目录碰撞`);
      } else {
        toast.success(`全局 Dry-Run 校验通过（试算 ${res.affected_repos.length} 仓）`);
      }
    } catch (err) {
      toast.error((err as Error).message || '全局 Dry-Run 失败');
    } finally {
      setBusy(false);
    }
  };

  const handleSaveOnly = async () => {
    if (!isAdmin) return;
    const doc = parseCurrentDoc();
    if (!doc) return;
    setBusy(true);
    try {
      const res = await schemaApi.updateProtoGovernanceConfig(doc);
      setRevision(res.revision);
      setConfigSHA(res.config_sha256);
      setJsonText(JSON.stringify(res.doc, null, 2));
      toast.success(`已保存全局治理配置 (rev=${res.revision})，涉及 ${res.affected_repos} 仓`);
      onApplied?.();
    } catch (err) {
      toast.error((err as Error).message || '保存全局治理配置失败');
    } finally {
      setBusy(false);
    }
  };

  const handleApplyAndRenormalize = async () => {
    if (!isAdmin) return;
    const doc = parseCurrentDoc();
    if (!doc) return;
    setBusy(true);
    try {
      const res = await schemaApi.applyAndRenormalizeGovernance({
        config: doc,
        force_recompile: true,
      });
      setRevision(res.revision);
      setConfigSHA(res.config_sha256);
      toast.success(
        `已应用配置 (rev=${res.revision}) 并免网络重清洗 ${res.succeeded_count}/${res.processed_count} 仓`
      );
      onApplied?.();
      onClose();
    } catch (err) {
      toast.error((err as Error).message || '应用并重出树失败');
    } finally {
      setBusy(false);
    }
  };

  let parsedEnabledRules: string[] = [];
  let parsedDefaultSyntax = 'auto';
  let parsedStripPG = true;
  let parsedBlockOnConflict = false;
  let parsedExcludeSubsystems = '';
  let parsedSharedBaseHints = '';
  let parsedImplicitImportCount = 0;
  try {
    const cur = JSON.parse(jsonText) as GovernanceConfigDocDTO;
    parsedEnabledRules = cur.global?.enabled_rules || [];
    parsedDefaultSyntax = cur.global?.default_syntax || 'auto';
    parsedStripPG = cur.global?.strip_pg_segment !== false;
    parsedBlockOnConflict = Boolean(cur.global?.pool_reconcile?.block_on_unresolved_conflict);
    parsedExcludeSubsystems = (cur.global?.exclude_subsystems || []).join(', ');
    parsedSharedBaseHints = (cur.global?.shared_base_sources || [])
      .map((s) => s.path_hint)
      .filter(Boolean)
      .join(', ');
    parsedImplicitImportCount = Object.keys(cur.global?.implicit_symbol_imports || {}).length;
  } catch {
    // JSON 编辑态暂未合法时忽略快捷控件回显
  }

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="全局导出与 Stage B 清洗治理配置 (proto-governance/v1)"
      subtitle="支持 Web 编辑与标准 .json 双向导入导出，变更摘要自动并入 normalizer_version 触发增量重洗"
      width={820}
    >
      <div className="pgit-drawer-stack" data-testid="global-governance-drawer">
        <div className="pdoc-card">
          <div className="pdoc-card__header">
            <div className="pdoc-meta-row">
              <span className="code-badge code-badge-neutral">Revision #{revision}</span>
              {configSHA && (
                <span className="pdoc-pill">
                  SHA256: <code>{configSHA.slice(0, 12)}</code>
                </span>
              )}
            </div>
            <div className="pgit-toolbar-actions">
              {isAdmin && (
                <button
                  type="button"
                  className="btn-secondary btn-sm"
                  data-testid="load-uservice-preset-btn"
                  onClick={handleLoadProductionPreset}
                  disabled={loading || busy}
                >
                  <Sparkles size={13} /> 载入现网 uService + 去 /pg 治理预设
                </button>
              )}
              <button
                type="button"
                className="btn-secondary btn-sm"
                onClick={() => void loadConfig()}
                disabled={loading || busy}
              >
                <RefreshCw size={13} /> 重新加载
              </button>
              <button
                type="button"
                className="btn-secondary btn-sm"
                onClick={handleExportJson}
                disabled={loading}
              >
                <Download size={13} /> 导出 JSON
              </button>
              {isAdmin && (
                <label className="btn-secondary btn-sm" style={{ cursor: 'pointer' }}>
                  <Upload size={13} /> 导入 JSON
                  <input
                    type="file"
                    accept=".json,application/json"
                    style={{ display: 'none' }}
                    onChange={(e) => void handleImportFile(e)}
                  />
                </label>
              )}
            </div>
          </div>

          <div className="pgit-rules-grid">
            <div className="pgit-field pgit-field--wide">
              <span className="pgit-field__label">Stage B 全局内置规则库（NORM-001 ~ NORM-007）</span>
              <div className="pgit-rule-chips">
                {builtinRules.map((rule) => {
                  const checked = parsedEnabledRules.includes(rule.id);
                  return (
                    <label key={rule.id} className="pgit-rule-chip" title={rule.description}>
                      <input
                        type="checkbox"
                        checked={checked}
                        disabled={!isAdmin || busy}
                        onChange={(e) => handleToggleGlobalRule(rule.id, e.target.checked)}
                      />
                      <span>
                        <strong>{rule.id}</strong> · {rule.name}
                      </span>
                    </label>
                  );
                })}
              </div>
            </div>

            <label className="pgit-field">
              <span className="pgit-field__label">缺省 syntax 补齐头（NORM-002）</span>
              <select
                className="pgit-input"
                aria-label="缺省 syntax 补齐头"
                value={parsedDefaultSyntax}
                disabled={!isAdmin || busy}
                onChange={(e) =>
                  handleDefaultSyntaxChange(
                    e.target.value === 'proto2'
                      ? 'proto2'
                      : e.target.value === 'proto3'
                        ? 'proto3'
                        : 'auto'
                  )
                }
              >
                <option value="auto">auto (proto2/proto3 自动探测)</option>
                <option value="proto3">proto3</option>
                <option value="proto2">proto2</option>
              </select>
            </label>

            <label className="pgit-field">
              <span className="pgit-field__label">clean/ 目录去 /pg 扁平化（NORM-007）</span>
              <span className="pgit-check">
                <input
                  type="checkbox"
                  checked={parsedStripPG}
                  disabled={!isAdmin || busy}
                  onChange={(e) => handleStripPGChange(e.target.checked)}
                />
                <span>剥离路径中 standalone /pg 段（保留 raw/ 不变）</span>
              </span>
            </label>

            <label className="pgit-field">
              <span className="pgit-field__label">跨仓冲突发布拦截（ADR-29）</span>
              <span className="pgit-check">
                <input
                  type="checkbox"
                  data-testid="toggle-block-on-conflict"
                  checked={parsedBlockOnConflict}
                  disabled={!isAdmin || busy}
                  onChange={(e) => handleBlockOnConflictChange(e.target.checked)}
                />
                <span>存在未人工裁决的内容不等价冲突时，阻断涉及契约包发布生效</span>
              </span>
            </label>

            <label className="pgit-field">
              <span className="pgit-field__label">R0 子系统目录排除清单 (exclude_subsystems)</span>
              <input
                type="text"
                className="pgit-input"
                placeholder="build, test, doc"
                value={parsedExcludeSubsystems}
                readOnly={!isAdmin || busy}
                onChange={(e) => handleExcludeSubsystemsChange(e.target.value)}
              />
            </label>

            <label className="pgit-field">
              <span className="pgit-field__label">
                R4 共享基座收割源路径 (shared_base_sources · 隐式映射 {parsedImplicitImportCount} 条)
              </span>
              <input
                type="text"
                className="pgit-input"
                placeholder="il/pg, sw/pg, public/pg"
                value={parsedSharedBaseHints}
                readOnly={!isAdmin || busy}
                onChange={(e) => handleSharedBaseHintsChange(e.target.value)}
              />
            </label>
          </div>
        </div>

        <div className="pdoc-card">
          <div className="pdoc-card__header">
            <h4 className="pdoc-card__title">
              <FileCode2 size={15} /> proto-governance/v1 完整配置文档 (.json SSOT)
            </h4>
          </div>
          <textarea
            className="pgit-json-editor"
            rows={16}
            value={jsonText}
            readOnly={!isAdmin}
            spellCheck={false}
            data-testid="global-governance-json-editor"
            onChange={(e) => {
              setJsonText(e.target.value);
              setParseError('');
            }}
          />
          {parseError && (
            <div className="pgit-error-banner" role="alert">
              <AlertTriangle size={14} /> {parseError}
            </div>
          )}
        </div>

        {dryRunResult && (
          <div className="pdoc-card" data-testid="global-governance-dryrun-result">
            <div className="pdoc-card__header">
              <h4 className="pdoc-card__title">
                {dryRunResult.schema_valid &&
                (!dryRunResult.global_collisions || dryRunResult.global_collisions.length === 0) ? (
                  <CheckCircle2 size={15} />
                ) : (
                  <AlertTriangle size={15} />
                )}{' '}
                全局 Dry-Run 预演结果（{dryRunResult.affected_repos.length} 仓）
              </h4>
            </div>
            {!dryRunResult.schema_valid && (
              <div className="pgit-error-banner">{dryRunResult.validation_error}</div>
            )}
            {dryRunResult.global_collisions && dryRunResult.global_collisions.length > 0 && (
              <div className="pgit-error-banner">
                检测到目录碰撞：
                {dryRunResult.global_collisions
                  .map(
                    (c) =>
                      `clean/${c.target_subsystem}/${c.target_repo_dir} (${c.repo_names.join(', ')})`
                  )
                  .join('；')}
              </div>
            )}
            {dryRunResult.affected_repos.length > 0 && (
              <div className="pgit-dryrun-list">
                {dryRunResult.affected_repos.map((r) => (
                  <div key={r.git_repo_id} className="pgit-dryrun-repo-row">
                    <span>
                      <strong>{r.repo_name}</strong> · <code>{r.before_target_dir}</code> →{' '}
                      <code>{r.after_target_dir}</code>
                    </span>
                    <span className="pdoc-meta-row">
                      <span>文件: {r.total_files}</span>
                      <span>
                        语法通过率: {r.raw_syntax_pass_rate.toFixed(0)}% →{' '}
                        {r.clean_syntax_pass_rate.toFixed(0)}%
                      </span>
                    </span>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        <div className="pgit-drawer-footer">
          <button
            type="button"
            className="btn-secondary"
            data-testid="global-governance-dryrun-btn"
            onClick={() => void handleDryRun()}
            disabled={loading || busy}
          >
            <Play size={14} /> 全局 Dry-Run 校验
          </button>
          {isAdmin && (
            <>
              <button
                type="button"
                className="btn-secondary"
                data-testid="global-governance-save-btn"
                onClick={() => void handleSaveOnly()}
                disabled={loading || busy}
              >
                <Save size={14} /> 仅保存配置
              </button>
              <button
                type="button"
                className="btn-primary"
                data-testid="global-governance-apply-btn"
                onClick={() => void handleApplyAndRenormalize()}
                disabled={loading || busy}
              >
                <RefreshCw size={14} /> 保存并免网络重出树
              </button>
            </>
          )}
        </div>
      </div>
    </Drawer>
  );
};

export default GlobalGovernanceConfigDrawer;
