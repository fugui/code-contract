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
 * 源码与 Git 仓工作台 (/proto-sources)
 * 直接承载 Git 仓库/Bundle 同步、文件树只读浏览 (#L<n>)、历史提交 Diff 与文件合规透视。
 */
export const ProtoSourcesWorkbenchPage: React.FC<ProtoSourcesWorkbenchPageProps> = (props) => {
  const [searchParams] = useSearchParams();
  const { user } = useModuleUser();
  const defaultIsAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);
  const searchQueryParam = searchParams.get('search') || '';

  return (
    <div className="psrc-page-shell" data-testid="proto-sources-workbench-page">
      <GitSourcesWorkbenchPane
        {...props}
        isAdmin={props.isAdmin ?? defaultIsAdmin}
        initialSchemaName={props.initialSchemaName ?? searchQueryParam}
      />
    </div>
  );
};

export default ProtoSourcesWorkbenchPage;
