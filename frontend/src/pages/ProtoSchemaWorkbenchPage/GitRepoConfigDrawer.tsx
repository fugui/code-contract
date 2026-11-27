import React, { useEffect, useState } from 'react';
import {
  AlertTriangle,
  CheckCircle2,
  FolderGit2,
  Layers,
  Play,
  Plus,
  RefreshCw,
  Save,
  Sliders,
  Trash2,
} from 'lucide-react';
import { Drawer } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import type {
  GitProtoBundleDTO,
  GitRepoBundleInput,
  GitRepoDTO,
  GitSyncStrategy,
  MRAutoPublishMode,
  ProtoCategory,
  RegexRewriteRuleDTO,
  RepoGovernanceDryRunResultDTO,
  UpdateGitRepoGovernancePayload,
} from '../../types';
import './ProtoDocsAndCodeViewer.css';

export interface GitRepoConfigDrawerProps {
  open: boolean;
  isAdmin: boolean;
  repo?: GitRepoDTO | null;
  onClose: () => void;
  onSaved: (repo: GitRepoDTO, created?: boolean) => void;
}

const BUILTIN_RULES: Array<{ id: string; label: string }> = [
  { id: 'NORM-001', label: 'NORM-001 编码与 LF 换行' },
  { id: 'NORM-002', label: 'NORM-002 Syntax 声明补齐' },
  { id: 'NORM-003', label: 'NORM-003 Package 规范化' },
  { id: 'NORM-004', label: 'NORM-004 非标宏/指令剥离' },
  { id: 'NORM-005', label: 'NORM-005 Import 路径重写' },
];

interface GovernanceFormState {
  sync_enabled: boolean;
  sync_strategy: GitSyncStrategy;
  export_target_subsystem: string;
  export_target_repo_dir: string;
  strip_prefixes_text: string;
  path_rewrites_text: string;
  collect_globs_text: string;
  exclude_globs_text: string;
  disabled_rules: string[];
  regex_rewrites_json: string;
  import_rewrites_text: string;
  exempt_files_text: string;
  quarantined_files_text: string;
}

interface BundleFormState {
  bundle_name: string;
  target_schema_name: string;
  category: ProtoCategory;
  subdir_path: string;
  include_dirs_text: string;
  entry_files_text: string;
  file_globs_text: string;
  exclude_globs_text: string;
  auto_compile: boolean;
  auto_activate: boolean;
  mr_auto_publish: MRAutoPublishMode;
  lint_on_sync: boolean;
  description: string;
}

function splitCommaOrLineList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0);
}

function formatKeyValueMap(rec?: Record<string, string>): string {
  if (!rec) return '';
  return Object.entries(rec)
    .map(([k, v]) => `${k} = ${v}`)
    .join('\n');
}

function parseKeyValueMap(raw: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of raw.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const eqIdx = trimmed.indexOf('=');
    if (eqIdx <= 0) continue;
    const k = trimmed.slice(0, eqIdx).trim();
    const v = trimmed.slice(eqIdx + 1).trim();
    if (k) out[k] = v;
  }
  return out;
}

function createInitialGovForm(repo?: GitRepoDTO | null): GovernanceFormState {
  const norm = repo?.repo_normalize_config;
  return {
    sync_enabled: repo?.sync_enabled ?? true,
    sync_strategy: (repo?.sync_strategy as GitSyncStrategy) || 'LOCAL_CACHE_ONLY',
    export_target_subsystem: repo?.export_target_subsystem || '',
    export_target_repo_dir: repo?.export_target_repo_dir || '',
    strip_prefixes_text: (repo?.strip_prefixes || []).join(', '),
    path_rewrites_text: formatKeyValueMap(repo?.path_rewrites),
    collect_globs_text: (repo?.collect_globs || []).join(', '),
    exclude_globs_text: (repo?.exclude_globs || []).join(', '),
    disabled_rules: norm?.disabled_rules ? [...norm.disabled_rules] : [],
    regex_rewrites_json:
      norm?.regex_rewrites && norm.regex_rewrites.length > 0
        ? JSON.stringify(norm.regex_rewrites, null, 2)
        : '[]',
    import_rewrites_text: formatKeyValueMap(norm?.import_rewrites),
    exempt_files_text: (norm?.exempt_files || []).join('\n'),
    quarantined_files_text: (norm?.quarantined_files || []).join('\n'),
  };
}

function createInitialBundleForm(bundle?: GitProtoBundleDTO | null): BundleFormState {
  return {
    bundle_name: bundle?.bundle_name || 'default',
    target_schema_name: bundle?.target_schema_name || '',
    category: (bundle?.category as ProtoCategory) || 'BUSINESS',
    subdir_path: bundle?.subdir_path || '',
    include_dirs_text: bundle?.include_dirs?.join(', ') || '.',
    entry_files_text: bundle?.entry_files?.join(', ') || '',
    file_globs_text: bundle?.file_globs?.join(', ') || '*.proto',
    exclude_globs_text: bundle?.exclude_globs?.join(', ') || '',
    auto_compile: bundle?.auto_compile ?? true,
    auto_activate: bundle?.auto_activate ?? false,
    mr_auto_publish: (bundle?.mr_auto_publish as MRAutoPublishMode) || 'OFF',
    lint_on_sync: bundle?.lint_on_sync ?? true,
    description: bundle?.description || '',
  };
}

function toBundlePayload(form: BundleFormState): GitRepoBundleInput {
  return {
    bundle_name: form.bundle_name.trim() || 'default',
    target_schema_name: form.target_schema_name.trim(),
    category: form.category,
    subdir_path: form.subdir_path.trim() || '.',
    include_dirs: splitCommaOrLineList(form.include_dirs_text),
    entry_files: splitCommaOrLineList(form.entry_files_text),
    file_globs: splitCommaOrLineList(form.file_globs_text),
    exclude_globs: splitCommaOrLineList(form.exclude_globs_text),
    auto_compile: form.auto_compile,
    auto_activate: form.auto_activate,
    mr_auto_publish: form.mr_auto_publish,
    lint_on_sync: form.lint_on_sync,
    description: form.description.trim(),
  };
}

export const GitRepoConfigDrawer: React.FC<GitRepoConfigDrawerProps> = ({
  open,
  isAdmin,
  repo,
  onClose,
  onSaved,
}) => {
  const toast = useToast();
  const [form, setForm] = useState<GovernanceFormState>(() => createInitialGovForm(repo));
  const [submitting, setSubmitting] = useState(false);
  const [dryRunLoading, setDryRunLoading] = useState(false);
  const [dryRunResult, setDryRunResult] = useState<RepoGovernanceDryRunResultDTO | null>(null);
  const [expandedDiffPath, setExpandedDiffPath] = useState<string | null>(null);

  const [bundleEditorOpen, setBundleEditorOpen] = useState(false);
  const [editingBundle, setEditingBundle] = useState<GitProtoBundleDTO | null>(null);
  const [bundleForm, setBundleForm] = useState<BundleFormState>(() => createInitialBundleForm(null));
  const [bundleSaving, setBundleSaving] = useState(false);

  useEffect(() => {
    if (open) {
      setForm(createInitialGovForm(repo));
      setDryRunResult(null);
      setExpandedDiffPath(null);
      setBundleEditorOpen(false);
      setEditingBundle(null);
    }
  }, [open, repo]);

  const buildGovernancePayload = (): UpdateGitRepoGovernancePayload | null => {
    let parsedRegex: RegexRewriteRuleDTO[] = [];
    const trimmedRegex = form.regex_rewrites_json.trim();
    if (trimmedRegex) {
      try {
        const val = JSON.parse(trimmedRegex) as RegexRewriteRuleDTO[];
        if (!Array.isArray(val)) {
          toast.warning('regex_rewrites 必须为 JSON 数组');
          return null;
        }
        parsedRegex = val;
      } catch (err) {
        toast.warning(`regex_rewrites JSON 格式非法: ${(err as Error).message}`);
        return null;
      }
    }

    const collectGlobs = splitCommaOrLineList(form.collect_globs_text);
    const excludeGlobs = splitCommaOrLineList(form.exclude_globs_text);

    return {
      sync_enabled: form.sync_enabled,
      sync_strategy: form.sync_strategy,
      export_target_subsystem: form.export_target_subsystem.trim(),
      export_target_repo_dir: form.export_target_repo_dir.trim(),
      strip_prefixes: splitCommaOrLineList(form.strip_prefixes_text),
      path_rewrites: parseKeyValueMap(form.path_rewrites_text),
      ...(collectGlobs.length > 0 ? { collect_globs: collectGlobs } : {}),
      ...(excludeGlobs.length > 0 ? { exclude_globs: excludeGlobs } : {}),
      disabled_rules: form.disabled_rules,
      regex_rewrites: parsedRegex,
      import_rewrites: parseKeyValueMap(form.import_rewrites_text),
      exempt_files: splitCommaOrLineList(form.exempt_files_text),
      quarantined_files: splitCommaOrLineList(form.quarantined_files_text),
    };
  };

  const handleDryRun = async () => {
    if (!repo) return;
    const payload = buildGovernancePayload();
    if (!payload) return;
    setDryRunLoading(true);
    try {
      const res = await schemaApi.dryRunGitRepoGovernance(repo.id, payload);
      setDryRunResult(res);
      if (res.global_collisions && res.global_collisions.length > 0) {
        toast.warning('检测到全局目标导出目录碰撞 (409)，请修改目标子系统或目录名');
      } else if (res.path_collisions && res.path_collisions.length > 0) {
        toast.warning('检测到仓内路径裁剪后碰撞，请调整 strip_prefixes / path_rewrites');
      } else {
        toast.success(
          `Dry-Run 完成：语法通过率 ${res.raw_syntax_pass_rate.toFixed(0)}% → ${res.clean_syntax_pass_rate.toFixed(0)}%`
        );
      }
    } catch (err) {
      toast.error((err as Error).message || 'Dry-Run 试算失败');
    } finally {
      setDryRunLoading(false);
    }
  };

  const handleSaveGovernance = async (renormalizeAfter = false) => {
    if (!isAdmin || !repo) return;
    const payload = buildGovernancePayload();
    if (!payload) return;
    setSubmitting(true);
    try {
      const updated = await schemaApi.updateGitRepoGovernance(repo.id, payload);
      if (renormalizeAfter) {
        const res = await schemaApi.reNormalizeGitRepo(repo.id, true);
        const refreshed = await schemaApi.getGitRepoDetail(repo.id);
        toast.success(
          `治理配置已保存并免网络重出树（通过 ${res.normalized_count} / 隔离 ${res.quarantined_count}）`
        );
        onSaved(refreshed, false);
      } else {
        toast.success(`仓库 [${updated.name}] 导出与清洗治理配置已保存`);
        onSaved(updated, false);
      }
      onClose();
    } catch (err) {
      toast.error((err as Error).message || '保存治理配置失败');
    } finally {
      setSubmitting(false);
    }
  };

  const toggleDisabledRule = (ruleId: string, enableRule: boolean) => {
    setForm((prev) => {
      const disabled = new Set(prev.disabled_rules);
      if (enableRule) {
        disabled.delete(ruleId);
      } else {
        disabled.add(ruleId);
      }
      return { ...prev, disabled_rules: Array.from(disabled) };
    });
  };

  const handleSaveBundle = async () => {
    if (!isAdmin || !repo) return;
    if (!bundleForm.bundle_name.trim() || !bundleForm.target_schema_name.trim()) {
      toast.warning('请填写 Bundle 名称与目标 Schema 资产名称');
      return;
    }
    setBundleSaving(true);
    try {
      const payload = toBundlePayload(bundleForm);
      if (editingBundle) {
        await schemaApi.updateGitRepoBundle(repo.id, editingBundle.id, payload);
        toast.success(`Bundle [${payload.bundle_name}] 已更新`);
      } else {
        await schemaApi.createGitRepoBundle(repo.id, payload);
        toast.success(`Monorepo Bundle [${payload.bundle_name}] 已创建`);
      }
      const refreshed = await schemaApi.getGitRepoDetail(repo.id);
      setBundleEditorOpen(false);
      setEditingBundle(null);
      onSaved(refreshed, false);
    } catch (err) {
      toast.error((err as Error).message || '保存 Bundle 失败');
    } finally {
      setBundleSaving(false);
    }
  };

  const handleDeleteBundle = async (bundleId: number) => {
    if (!isAdmin || !repo) return;
    setBundleSaving(true);
    try {
      await schemaApi.deleteGitRepoBundle(repo.id, bundleId);
      const refreshed = await schemaApi.getGitRepoDetail(repo.id);
      toast.success('Bundle 映射已移除');
      onSaved(refreshed, false);
    } catch (err) {
      toast.error((err as Error).message || '删除 Bundle 失败');
    } finally {
      setBundleSaving(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={repo ? `仓库导出与清洗治理：${repo.name}` : '仓库导出与清洗治理'}
      subtitle="平台仓库元数据由 code-bench 只读同步，此面板管理 Clean 导出目录、路径裁剪、Stage B 规则与 Dry-Run 试算"
      width={820}
    >
      {!repo ? (
        <div className="pdoc-card">未选择仓库</div>
      ) : (
        <div className="pgit-drawer-stack" data-testid="git-repo-governance-drawer">
          {/* 1. 只读平台 SSOT 元数据 */}
          <div className="pdoc-card">
            <div className="pdoc-card__header">
              <h4 className="pdoc-card__title">
                <FolderGit2 size={15} /> 平台唯一真值源（只读）
              </h4>
              <div className="pdoc-meta-row">
                <span className="code-badge code-badge-neutral">
                  Platform #{repo.platform_repo_id}
                </span>
                {repo.service_group && (
                  <span className="code-badge code-badge-info">{repo.service_group}</span>
                )}
                {repo.proto_status && (
                  <span className="code-badge code-badge-neutral">{repo.proto_status}</span>
                )}
              </div>
            </div>
            <div className="pgit-readonly-grid">
              <div>
                <span className="pgit-field__label">仓库名称</span>
                <div className="pgit-readonly-val">{repo.name}</div>
              </div>
              <div>
                <span className="pgit-field__label">默认基线分支</span>
                <div className="pgit-readonly-val">
                  <code>{repo.default_branch}</code>
                </div>
              </div>
              <div className="pgit-field--wide">
                <span className="pgit-field__label">Git 仓库地址</span>
                <div className="pgit-readonly-val">
                  <code>{repo.repo_url}</code>
                </div>
              </div>
            </div>
          </div>

          {/* 2. 方案 A 导出目录与仓内路径裁剪 */}
          <div className="pdoc-card">
            <div className="pdoc-card__header">
              <h4 className="pdoc-card__title">
                <Sliders size={15} /> 方案 A 导出目录治理（Raw 保持不变，仅调整 Clean 输出）
              </h4>
            </div>
            <div className="pgit-form-grid">
              <label className="pgit-checkbox-field">
                <input
                  type="checkbox"
                  checked={form.sync_enabled}
                  disabled={!isAdmin || submitting}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, sync_enabled: e.target.checked }))
                  }
                />
                <span>启用契约同步与 Clean 导出（停用后自动归档为 EXCLUDED 并回收 Clean 软链）</span>
              </label>

              <label className="pgit-field">
                <span className="pgit-field__label">缓存拉取策略</span>
                <select
                  className="pgit-input"
                  value={form.sync_strategy}
                  disabled={!isAdmin || submitting}
                  onChange={(e) =>
                    setForm((prev) => ({
                      ...prev,
                      sync_strategy: e.target.value as GitSyncStrategy,
                    }))
                  }
                >
                  <option value="LOCAL_CACHE_ONLY">仅读取宿主机共享缓存 (LOCAL_CACHE_ONLY)</option>
                  <option value="FETCH_AND_READ">允许缺失时自动拉取 (FETCH_AND_READ)</option>
                </select>
              </label>

              <label className="pgit-field">
                <span className="pgit-field__label">目标导出子系统 (export_target_subsystem)</span>
                <input
                  type="text"
                  className="pgit-input"
                  value={form.export_target_subsystem}
                  disabled={!isAdmin || submitting}
                  placeholder={`默认 ${repo.subsystem_slug || '_unassigned'}`}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, export_target_subsystem: e.target.value }))
                  }
                />
              </label>

              <label className="pgit-field">
                <span className="pgit-field__label">目标导出仓库目录名 (export_target_repo_dir)</span>
                <input
                  type="text"
                  className="pgit-input"
                  value={form.export_target_repo_dir}
                  disabled={!isAdmin || submitting}
                  placeholder={`默认 ${repo.name}`}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, export_target_repo_dir: e.target.value }))
                  }
                />
              </label>

              <label className="pgit-field pgit-field--wide">
                <span className="pgit-field__label">剥离路径前缀 (strip_prefixes，逗号分隔)</span>
                <input
                  type="text"
                  className="pgit-input"
                  value={form.strip_prefixes_text}
                  disabled={!isAdmin || submitting}
                  placeholder="例如: server/pg/proto/, pg/"
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, strip_prefixes_text: e.target.value }))
                  }
                />
              </label>

              <label className="pgit-field pgit-field--wide">
                <span className="pgit-field__label">仓内路径重写 (path_rewrites，每行 from = to)</span>
                <textarea
                  className="pgit-input"
                  rows={2}
                  value={form.path_rewrites_text}
                  disabled={!isAdmin || submitting}
                  placeholder="legacy/ = v1/"
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, path_rewrites_text: e.target.value }))
                  }
                />
              </label>
            </div>
          </div>

          {/* 3. Stage B 清理规则覆写与文件级豁免/隔离 */}
          <div className="pdoc-card">
            <div className="pdoc-card__header">
              <h4 className="pdoc-card__title">Stage B 规则覆写与文件豁免/隔离</h4>
            </div>
            <div className="pgit-rule-chips" style={{ marginBottom: 10 }}>
              {BUILTIN_RULES.map((rule) => {
                const enabled = !form.disabled_rules.includes(rule.id);
                return (
                  <label key={rule.id} className="pgit-rule-chip">
                    <input
                      type="checkbox"
                      checked={enabled}
                      disabled={!isAdmin || submitting}
                      onChange={(e) => toggleDisabledRule(rule.id, e.target.checked)}
                    />
                    <span>{rule.label}</span>
                  </label>
                );
              })}
            </div>

            <div className="pgit-form-grid">
              <label className="pgit-field pgit-field--wide">
                <span className="pgit-field__label">专属正则替换 (regex_rewrites JSON 数组)</span>
                <textarea
                  className="pgit-json-editor"
                  rows={4}
                  value={form.regex_rewrites_json}
                  disabled={!isAdmin || submitting}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, regex_rewrites_json: e.target.value }))
                  }
                />
              </label>

              <label className="pgit-field">
                <span className="pgit-field__label">Import 路径重写 (每行 old = new)</span>
                <textarea
                  className="pgit-input"
                  rows={3}
                  value={form.import_rewrites_text}
                  disabled={!isAdmin || submitting}
                  placeholder="legacy/base.proto = common/v1/base.proto"
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, import_rewrites_text: e.target.value }))
                  }
                />
              </label>

              <label className="pgit-field">
                <span className="pgit-field__label">白名单豁免文件 (exempt_files，换行分隔)</span>
                <textarea
                  className="pgit-input"
                  rows={3}
                  value={form.exempt_files_text}
                  disabled={!isAdmin || submitting}
                  placeholder="pg/vendor/*.proto"
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, exempt_files_text: e.target.value }))
                  }
                />
              </label>

              <label className="pgit-field pgit-field--wide">
                <span className="pgit-field__label">强制隔离文件 (quarantined_files，换行分隔)</span>
                <textarea
                  className="pgit-input"
                  rows={2}
                  value={form.quarantined_files_text}
                  disabled={!isAdmin || submitting}
                  placeholder="pg/deprecated/broken.proto"
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, quarantined_files_text: e.target.value }))
                  }
                />
              </label>
            </div>
          </div>

          {/* 4. Dry-Run 零网络预览结果 */}
          {dryRunResult && (
            <div className="pdoc-card" data-testid="repo-governance-dryrun-result">
              <div className="pdoc-card__header">
                <h4 className="pdoc-card__title">
                  {(!dryRunResult.global_collisions || dryRunResult.global_collisions.length === 0) &&
                  (!dryRunResult.path_collisions || dryRunResult.path_collisions.length === 0) ? (
                    <CheckCircle2 size={15} />
                  ) : (
                    <AlertTriangle size={15} />
                  )}{' '}
                  Dry-Run 预演对照
                </h4>
                <span className="pdoc-meta-row">
                  <code>{dryRunResult.before_target_dir}</code> →{' '}
                  <code>{dryRunResult.after_target_dir}</code>
                  <span className="code-badge code-badge-info">
                    语法通过率 {dryRunResult.raw_syntax_pass_rate.toFixed(0)}% →{' '}
                    {dryRunResult.clean_syntax_pass_rate.toFixed(0)}%
                  </span>
                </span>
              </div>

              {dryRunResult.global_collisions && dryRunResult.global_collisions.length > 0 && (
                <div className="pgit-error-banner">
                  全局目录碰撞：
                  {dryRunResult.global_collisions
                    .map((c) => `clean/${c.target_subsystem}/${c.target_repo_dir} (${c.repo_names.join(', ')})`)
                    .join('；')}
                </div>
              )}
              {dryRunResult.path_collisions && dryRunResult.path_collisions.length > 0 && (
                <div className="pgit-error-banner">
                  仓内路径碰撞：
                  {dryRunResult.path_collisions
                    .map((c) => `${c.mapped_path} <- [${c.source_paths.join(', ')}]`)
                    .join('；')}
                </div>
              )}

              {dryRunResult.files.length > 0 && (
                <div className="pgit-dryrun-list">
                  {dryRunResult.files.map((f) => (
                    <div key={f.raw_path} className="pgit-dryrun-file-item">
                      <div className="pgit-dryrun-repo-row">
                        <span>
                          <code>{f.raw_path}</code> → <code>{f.clean_path}</code>
                        </span>
                        <span className="pdoc-meta-row">
                          <span
                            className={`code-badge ${
                              f.clean_syntax_ok ? 'code-badge-success' : 'code-badge-danger'
                            }`}
                          >
                            {f.clean_syntax_ok ? 'SYNTAX_OK' : f.status}
                          </span>
                          {f.unified_diff && (
                            <button
                              type="button"
                              className="btn-secondary btn-xs"
                              onClick={() =>
                                setExpandedDiffPath((cur) =>
                                  cur === f.raw_path ? null : f.raw_path
                                )
                              }
                            >
                              {expandedDiffPath === f.raw_path ? '收起 Diff' : '查看 Diff'}
                            </button>
                          )}
                        </span>
                      </div>
                      {expandedDiffPath === f.raw_path && f.unified_diff && (
                        <pre className="pgit-dual-code-pre">{f.unified_diff}</pre>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          {/* 5. Monorepo 编译 Bundle 单元（保留只读查与 Admin 微调） */}
          <div className="pdoc-card">
            <div className="pdoc-card__header">
              <h4 className="pdoc-card__title">
                <Layers size={15} /> 编译打包 Bundle 单元 ({repo.bundles?.length ?? 0})
              </h4>
              {isAdmin && (
                <button
                  type="button"
                  className="btn-secondary btn-sm"
                  onClick={() => {
                    setEditingBundle(null);
                    setBundleForm(createInitialBundleForm(null));
                    setBundleEditorOpen(true);
                  }}
                >
                  <Plus size={13} /> 新增 Bundle
                </button>
              )}
            </div>
            {(repo.bundles?.length ?? 0) === 0 ? (
              <div className="pgit-empty-hint">
                系统将在首次同步命中 pg/ 后自动生成默认 Bundle；也可手动补充精细切分单元。
              </div>
            ) : (
              <div className="pgit-bundle-list">
                {repo.bundles.map((b) => (
                  <div key={b.id} className="pgit-bundle-row">
                    <div>
                      <strong>{b.bundle_name}</strong> · <code>{b.subdir_path}</code> →{' '}
                      <code>{b.target_schema_name}</code> ({b.category})
                    </div>
                    {isAdmin && (
                      <div className="pgit-toolbar-actions">
                        <button
                          type="button"
                          className="btn-secondary btn-xs"
                          onClick={() => {
                            setEditingBundle(b);
                            setBundleForm(createInitialBundleForm(b));
                            setBundleEditorOpen(true);
                          }}
                        >
                          编辑
                        </button>
                        <button
                          type="button"
                          className="btn-secondary btn-xs"
                          onClick={() => void handleDeleteBundle(b.id)}
                          disabled={bundleSaving}
                        >
                          <Trash2 size={12} />
                        </button>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}

            {bundleEditorOpen && (
              <div className="pgit-subform-box">
                <div className="pgit-form-grid">
                  <label className="pgit-field">
                    <span className="pgit-field__label">Bundle 名称</span>
                    <input
                      type="text"
                      className="pgit-input"
                      value={bundleForm.bundle_name}
                      onChange={(e) =>
                        setBundleForm((prev) => ({ ...prev, bundle_name: e.target.value }))
                      }
                    />
                  </label>
                  <label className="pgit-field">
                    <span className="pgit-field__label">目标 Schema 名称</span>
                    <input
                      type="text"
                      className="pgit-input"
                      value={bundleForm.target_schema_name}
                      onChange={(e) =>
                        setBundleForm((prev) => ({ ...prev, target_schema_name: e.target.value }))
                      }
                    />
                  </label>
                  <label className="pgit-field">
                    <span className="pgit-field__label">子目录 (subdir_path)</span>
                    <input
                      type="text"
                      className="pgit-input"
                      value={bundleForm.subdir_path}
                      onChange={(e) =>
                        setBundleForm((prev) => ({ ...prev, subdir_path: e.target.value }))
                      }
                    />
                  </label>
                  <label className="pgit-field">
                    <span className="pgit-field__label">资产分类</span>
                    <select
                      className="pgit-input"
                      value={bundleForm.category}
                      onChange={(e) =>
                        setBundleForm((prev) => ({
                          ...prev,
                          category: e.target.value as ProtoCategory,
                        }))
                      }
                    >
                      <option value="BUSINESS">BUSINESS</option>
                      <option value="BASE_LIB">BASE_LIB</option>
                    </select>
                  </label>
                </div>
                <div className="pgit-toolbar-actions" style={{ marginTop: 8 }}>
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() => setBundleEditorOpen(false)}
                  >
                    取消
                  </button>
                  <button
                    type="button"
                    className="btn-primary btn-sm"
                    disabled={bundleSaving}
                    onClick={() => void handleSaveBundle()}
                  >
                    保存 Bundle
                  </button>
                </div>
              </div>
            )}
          </div>

          {/* Footer actions */}
          <div className="pgit-drawer-footer">
            <button
              type="button"
              className="btn-secondary"
              data-testid="repo-governance-dryrun-btn"
              onClick={() => void handleDryRun()}
              disabled={dryRunLoading || submitting}
            >
              <Play size={14} /> Dry-Run 预览
            </button>
            {isAdmin && (
              <>
                <button
                  type="button"
                  className="btn-secondary"
                  data-testid="repo-governance-save-btn"
                  onClick={() => void handleSaveGovernance(false)}
                  disabled={submitting || dryRunLoading}
                >
                  <Save size={14} /> 保存治理配置
                </button>
                <button
                  type="button"
                  className="btn-primary"
                  data-testid="repo-governance-save-apply-btn"
                  onClick={() => void handleSaveGovernance(true)}
                  disabled={submitting || dryRunLoading}
                >
                  <RefreshCw size={14} /> 保存并免网络重出树
                </button>
              </>
            )}
          </div>
        </div>
      )}
    </Drawer>
  );
};

export default GitRepoConfigDrawer;
