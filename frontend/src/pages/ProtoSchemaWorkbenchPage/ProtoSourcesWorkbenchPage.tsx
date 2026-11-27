import React from 'react';
import { useSearchParams } from 'react-router-dom';
import { useModuleUser } from '../../context/ModuleUserContext';
import {
  GitSourcesWorkbenchPane,
  type GitSourcesWorkbenchPaneProps,
} from './GitSourcesWorkbenchPane';
import './ProtoDocsAndCodeViewer.css';

export type ProtoSourcesWorkbenchPageProps = GitSourcesWorkbenchPaneProps;

/**
 * 只读 Proto 源码浏览包装页 (/proto-sources 历史路由兼容组件)
 * 已彻底剥离生产治理动作，仅作为只读契约源码消费视图；生产路线中 /proto-sources 会平滑重定向至 /proto-docs?docs_view=sources。
 */
export const ProtoSourcesWorkbenchPage: React.FC<ProtoSourcesWorkbenchPageProps> = (props) => {
  const [searchParams] = useSearchParams();
  const { user } = useModuleUser();
  const defaultIsAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);
  const isAdmin = props.isAdmin ?? defaultIsAdmin;
  const searchQueryParam = searchParams.get('search') || '';
  const repoIdParam = searchParams.get('repo_id');

  return (
    <div className="psrc-page-shell" data-testid="proto-sources-workbench-page">
      <GitSourcesWorkbenchPane
        {...props}
        readOnlyConsumer
        isAdmin={isAdmin}
        initialSelectedRepoId={
          props.initialSelectedRepoId ?? (repoIdParam ? Number(repoIdParam) : undefined)
        }
        initialSchemaName={props.initialSchemaName ?? searchQueryParam}
      />
    </div>
  );
};

export default ProtoSourcesWorkbenchPage;
