import React, { useCallback, useEffect, useState } from 'react';
import { RefreshCw, ShieldAlert, Trash2 } from 'lucide-react';
import { Drawer } from '@code/common';
import * as schemaApi from '../../api/schema';
import { useToast } from '../../utils/toast';
import type { ProtoConflictsReportDTO, ProtoImportOwnerDTO } from '../../types';
import './ProtoDocsAndCodeViewer.css';

export interface ProtoConflictArbitrationDrawerProps {
  open: boolean;
  isAdmin: boolean;
  initialConflicts?: ProtoConflictsReportDTO | null;
  initialOwners?: ProtoImportOwnerDTO[];
  onClose: () => void;
  onResolved?: () => void;
}

export const ProtoConflictArbitrationDrawer: React.FC<ProtoConflictArbitrationDrawerProps> = ({
  open,
  isAdmin,
  initialConflicts,
  initialOwners,
  onClose,
  onResolved,
}) => {
  const toast = useToast();
  const [loading, setLoading] = useState(false);
  const [resolvingKey, setResolvingKey] = useState<string | null>(null);
  const [conflicts, setConflicts] = useState<ProtoConflictsReportDTO | null>(
    initialConflicts ?? null
  );
  const [owners, setOwners] = useState<ProtoImportOwnerDTO[]>(initialOwners ?? []);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const [confRep, ownerRep] = await Promise.all([
        schemaApi.listProtoConflicts(),
        schemaApi.listProtoImportOwners({ page: 1, page_size: 100 }),
      ]);
      setConflicts(confRep);
      setOwners(ownerRep.items ?? []);
    } catch (err) {
      toast.error((err as Error).message || '加载导入歧义与符号冲突列表失败');
    } finally {
      setLoading(false);
    }
  }, [toast]);

  useEffect(() => {
    if (!open || initialConflicts !== undefined) return;
    void loadData();
  }, [open, initialConflicts, loadData]);

  const handleResolve = async (
    kind: 'IMPORT' | 'SYMBOL',
    targetKey: string,
    ownerGitRepoID: number,
    ownerBundleID: number
  ) => {
    if (!isAdmin) return;
    setResolvingKey(`${kind}:${targetKey}:${ownerBundleID}`);
    try {
      await schemaApi.resolveProtoConflict({
        kind,
        logical_path: kind === 'IMPORT' ? targetKey : undefined,
        symbol_full_name: kind === 'SYMBOL' ? targetKey : undefined,
        owner_git_repo_id: ownerGitRepoID,
        owner_bundle_id: ownerBundleID,
      });
      toast.success(`已指认 [${targetKey}] 权威提供方并标记非权威副本 is_external_copy=true`);
      if (onResolved) onResolved();
      await loadData();
    } catch (err) {
      toast.error((err as Error).message || '冲突裁决失败');
    } finally {
      setResolvingKey(null);
    }
  };

  const handleDeleteOwner = async (ownerId: number) => {
    if (!isAdmin) return;
    try {
      await schemaApi.deleteProtoImportOwner(ownerId);
      toast.success('已移除权威导入归属记录');
      await loadData();
    } catch (err) {
      toast.error((err as Error).message || '删除归属记录失败');
    }
  };

  const importAmbiguities = conflicts?.import_ambiguities ?? [];
  const symbolConflicts = conflicts?.symbol_conflicts ?? [];

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title="跨仓导入歧义与符号冲突仲裁 (contract_proto_import_owners)"
      width={920}
    >
      <div className="pgit-drawer-stack" data-testid="proto-conflict-arbitration-drawer">
        <div className="pdoc-card pgit-toolbar-row">
          <div className="pdoc-meta-row">
            <ShieldAlert size={16} />
            <span>
              多仓出现同逻辑路径或全限定符号分歧时，必须在此落定唯一权威提供方；其余同哈希副本将自动标记为{' '}
              <code>is_external_copy=true</code> 并退出跨仓链接仲裁池。
            </span>
          </div>
          <button
            type="button"
            className="btn btn-secondary"
            disabled={loading}
            onClick={() => void loadData()}
          >
            <RefreshCw size={14} className={loading ? 'pgit-spin' : undefined} /> 刷新冲突检测
          </button>
        </div>

        <div className="pdoc-card">
          <h4 className="pdoc-card__title">
            导入路径歧义 (IMPORT_AMBIGUOUS · {importAmbiguities.length})
          </h4>
          {importAmbiguities.length === 0 ? (
            <p className="pdoc-card__subtitle">当前无跨仓导入路径歧义。</p>
          ) : (
            <div className="plint-dup-grid">
              {importAmbiguities.map((amb) => (
                <div key={amb.logical_path} className="plint-dup-card">
                  <div className="plint-dup-card__head">
                    <code>{amb.logical_path}</code>
                    <span className="code-badge code-badge-warning">
                      {amb.candidates.length} 个候选提供方
                    </span>
                  </div>
                  <div className="plint-dup-card__members">
                    {amb.candidates.map((cand) => {
                      const k = `IMPORT:${amb.logical_path}:${cand.bundle_id}`;
                      return (
                        <div key={`${cand.bundle_id}:${cand.repo_file_path}`} className="plint-dup-member">
                          <div>
                            <strong>{cand.repo_name}</strong> / {cand.bundle_name} (
                            <span className="pdoc-pill">{cand.category}</span>) ·{' '}
                            <code>{cand.repo_file_path}</code>
                          </div>
                          {isAdmin && (
                            <button
                              type="button"
                              className="btn btn-primary"
                              disabled={resolvingKey === k}
                              onClick={() =>
                                void handleResolve(
                                  'IMPORT',
                                  amb.logical_path,
                                  cand.git_repo_id,
                                  cand.bundle_id
                                )
                              }
                            >
                              {resolvingKey === k ? '裁决中...' : '设为权威提供方'}
                            </button>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="pdoc-card">
          <h4 className="pdoc-card__title">
            跨仓符号冲突 ({symbolConflicts.length})
          </h4>
          {symbolConflicts.length === 0 ? (
            <p className="pdoc-card__subtitle">当前无全限定符号跨仓内容分歧。</p>
          ) : (
            <div className="plint-dup-grid">
              {symbolConflicts.map((sym) => (
                <div key={sym.symbol_full_name} className="plint-dup-card">
                  <div className="plint-dup-card__head">
                    <code>
                      {sym.symbol_kind} {sym.symbol_full_name}
                    </code>
                    <span
                      className={`code-badge ${
                        sym.content_matches ? 'code-badge-info' : 'code-badge-danger'
                      }`}
                    >
                      {sym.content_matches ? '文件内容一致' : '文件内容存在差异'}
                    </span>
                  </div>
                  <div className="plint-dup-card__members">
                    {sym.candidates.map((cand) => {
                      const k = `SYMBOL:${sym.symbol_full_name}:${cand.bundle_id}`;
                      return (
                        <div key={cand.proto_file_id} className="plint-dup-member">
                          <div>
                            <strong>{cand.repo_name}</strong> / {cand.bundle_name} ·{' '}
                            <code>{cand.logical_path}</code>{' '}
                            {cand.is_external_copy && (
                              <span className="code-badge code-badge-warning">外部副本</span>
                            )}
                          </div>
                          {isAdmin && (
                            <button
                              type="button"
                              className="btn btn-primary"
                              disabled={resolvingKey === k}
                              onClick={() =>
                                void handleResolve(
                                  'SYMBOL',
                                  sym.symbol_full_name,
                                  cand.git_repo_id,
                                  cand.bundle_id
                                )
                              }
                            >
                              {resolvingKey === k ? '裁决中...' : '设为权威提供方'}
                            </button>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="pdoc-card">
          <h4 className="pdoc-card__title">已落定权威归属清单 ({owners.length})</h4>
          <div className="plint-table-wrap">
            <table className="plint-table">
              <thead>
                <tr>
                  <th>逻辑导入路径</th>
                  <th>权威仓库 / Bundle</th>
                  <th>来源</th>
                  <th>操作人</th>
                  {isAdmin && <th>操作</th>}
                </tr>
              </thead>
              <tbody>
                {owners.length === 0 ? (
                  <tr>
                    <td colSpan={isAdmin ? 5 : 4} className="ucat-empty-cell">
                      暂无权威归属记录
                    </td>
                  </tr>
                ) : (
                  owners.map((ow) => (
                    <tr key={ow.id}>
                      <td>
                        <code>{ow.logical_path}</code>
                      </td>
                      <td>
                        Repo #{ow.owner_git_repo_id} / Bundle #{ow.owner_bundle_id}
                      </td>
                      <td>
                        <span
                          className={`code-badge ${
                            ow.source === 'MANUAL' ? 'code-badge-success' : 'code-badge-info'
                          }`}
                        >
                          {ow.source}
                        </span>
                      </td>
                      <td>{ow.updated_by || 'system'}</td>
                      {isAdmin && (
                        <td>
                          <button
                            type="button"
                            className="btn btn-secondary"
                            aria-label={`删除归属 ${ow.logical_path}`}
                            onClick={() => void handleDeleteOwner(ow.id)}
                          >
                            <Trash2 size={12} /> 移除
                          </button>
                        </td>
                      )}
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </Drawer>
  );
};

export default ProtoConflictArbitrationDrawer;
