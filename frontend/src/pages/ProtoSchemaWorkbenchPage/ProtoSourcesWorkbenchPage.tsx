import React from 'react';
import { useSearchParams } from 'react-router-dom';
import { Code2, Settings2 } from 'lucide-react';
import { useModuleUser } from '../../context/ModuleUserContext';
import {
  GitSourcesWorkbenchPane,
  type GitSourcesWorkbenchPaneProps,
} from './GitSourcesWorkbenchPane';
import { ProtoExportGovernancePane } from './ProtoExportGovernancePane';
import './ProtoDocsAndCodeViewer.css';

export type ProtoSourcesWorkbenchPageProps = GitSourcesWorkbenchPaneProps;
export type ProtoSourcesSubView = 'sources' | 'governance';

/**
 * 源码与导出治理工作台 (/contract/proto-sources)
 * 通过 ?view=sources|governance 承载「源码浏览」与「仓库导出与治理」双正交子标签页，
 * 同时保留 ?file_id= / ?repo_id= / ?bundle_id= / #L<n> 既有深链兼容。
 */
export const ProtoSourcesWorkbenchPage: React.FC<ProtoSourcesWorkbenchPageProps> = (props) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const { user } = useModuleUser();
  const defaultIsAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);
  const isAdmin = props.isAdmin ?? defaultIsAdmin;
  const searchQueryParam = searchParams.get('search') || '';
  const repoIdParam = searchParams.get('repo_id');

  const rawView = searchParams.get('view');
  const activeView: ProtoSourcesSubView = rawView === 'governance' ? 'governance' : 'sources';

  const switchSubView = (nextView: ProtoSourcesSubView, extraRepoId?: number) => {
    const next = new URLSearchParams(searchParams);
    next.set('view', nextView);
    if (extraRepoId && extraRepoId > 0) {
      next.set('repo_id', String(extraRepoId));
    }
    setSearchParams(next, { replace: true });
  };

  return (
    <div className="psrc-page-shell" data-testid="proto-sources-workbench-page">
      <div className="psrc-subtab-bar" role="tablist" aria-label="Proto 源码与导出治理子标签">
        <button
          type="button"
          role="tab"
          aria-selected={activeView === 'sources'}
          className={`psrc-subtab-btn ${activeView === 'sources' ? 'is-active' : ''}`}
          data-testid="proto-sources-subtab-sources"
          onClick={() => switchSubView('sources')}
        >
          <Code2 size={15} /> 源码浏览
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={activeView === 'governance'}
          className={`psrc-subtab-btn ${activeView === 'governance' ? 'is-active' : ''}`}
          data-testid="proto-sources-subtab-governance"
          onClick={() => switchSubView('governance')}
        >
          <Settings2 size={15} /> 仓库导出与治理
        </button>
      </div>

      {activeView === 'governance' ? (
        <ProtoExportGovernancePane
          isAdmin={isAdmin}
          onBrowseRepoSources={(repoId) => switchSubView('sources', repoId)}
        />
      ) : (
        <GitSourcesWorkbenchPane
          {...props}
          isAdmin={isAdmin}
          initialSelectedRepoId={
            props.initialSelectedRepoId ?? (repoIdParam ? Number(repoIdParam) : undefined)
          }
          initialSchemaName={props.initialSchemaName ?? searchQueryParam}
        />
      )}
    </div>
  );
};

export default ProtoSourcesWorkbenchPage;
