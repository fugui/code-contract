import React from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import {
  AlertTriangle,
  BookCheck,
  FileSearch,
  GitPullRequest,
  ShieldAlert,
  ShieldCheck,
} from 'lucide-react';
import { useModuleUser } from '../../context/ModuleUserContext';
import { useModulePath } from '../../hooks/useModulePath';
import type {
  BreakingChangeEventDTO,
  CheckReportDTO,
  CheckRuleDTO,
  MRCheckRunDTO,
  MRCheckRunDetailDTO,
  VocabularyTermDTO,
} from '../../types';
import { MRGateWorkbenchPane } from './MRGateWorkbenchPane';
import { BreakingAuditWorkbenchPane } from './BreakingAuditWorkbenchPane';
import { LintRuleAndVocabPanel, type LintDrawerSubTab } from './LintRuleAndVocabDrawer';
import { ProtoSourceAnchorDrawer, useSourceAnchorDrawer } from './ProtoSourceAnchorDrawer';
import './ProtoDocsAndCodeViewer.css';

export type ProtoGovernanceTab = 'mr-gates' | 'breaking-audit' | 'rules' | 'vocab' | 'report';

export interface ProtoGovernanceWorkbenchPageProps {
  initialTab?: ProtoGovernanceTab;
  initialRuns?: MRCheckRunDTO[];
  initialTotal?: number;
  initialDetail?: MRCheckRunDetailDTO | null;
  initialBreakingEvents?: BreakingChangeEventDTO[];
  initialBreakingTotal?: number;
  initialBreakingOpenCount?: number;
  initialBreakingDetail?: BreakingChangeEventDTO | null;
  initialRules?: CheckRuleDTO[];
  initialRulesTotal?: number;
  initialVocab?: VocabularyTermDTO[];
  initialVocabTotal?: number;
  initialReport?: CheckReportDTO | null;
}

function resolveGovernanceTab(
  govTabParam: string | null,
  legacyTabParam: string | null,
  fallback: ProtoGovernanceTab
): ProtoGovernanceTab {
  if (
    govTabParam === 'mr-gates' ||
    govTabParam === 'breaking-audit' ||
    govTabParam === 'rules' ||
    govTabParam === 'vocab' ||
    govTabParam === 'report'
  ) {
    return govTabParam;
  }
  switch (legacyTabParam) {
    case 'lint-rules':
    case 'rules':
      return 'rules';
    case 'lint-vocab':
    case 'vocab':
      return 'vocab';
    case 'lint-report':
    case 'report':
      return 'report';
    case 'breaking-audit':
      return 'breaking-audit';
    case 'mr-gates':
      return 'mr-gates';
    default:
      return fallback;
  }
}

/**
 * MR 门禁与规范治理工作台 (/proto-governance)
 * 平铺承载 Git MR 四合一门禁流水、上线破坏性变更审计、静态检查规则、领域标准词库与体检报告/AST 重复簇分析。
 */
export const ProtoGovernanceWorkbenchPage: React.FC<ProtoGovernanceWorkbenchPageProps> = ({
  initialTab = 'mr-gates',
  initialRuns,
  initialTotal,
  initialDetail = null,
  initialBreakingEvents,
  initialBreakingTotal,
  initialBreakingOpenCount,
  initialBreakingDetail = null,
  initialRules,
  initialRulesTotal,
  initialVocab,
  initialVocabTotal,
  initialReport,
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const navigate = useNavigate();
  const resolvePath = useModulePath();
  const { user } = useModuleUser();
  const isAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);

  const activeTab = resolveGovernanceTab(
    searchParams.get('gov_tab'),
    searchParams.get('tab'),
    initialTab
  );

  const setActiveTab = (tab: ProtoGovernanceTab) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('gov_tab', tab);
      next.delete('tab');
      return next;
    });
  };

  const sourceAnchorDrawer = useSourceAnchorDrawer();

  const handleOpenSources = (schemaName?: string) => {
    const query = schemaName
      ? `?docs_view=sources&search=${encodeURIComponent(schemaName)}`
      : '?docs_view=sources';
    navigate(resolvePath(`/proto-docs${query}`));
  };

  const handleJumpToSource = async (protoFile: string, line: number) => {
    await sourceAnchorDrawer.openByFilePath(protoFile, line, () => {
      navigate(resolvePath(`/proto-docs?docs_view=sources&search=${encodeURIComponent(protoFile)}`));
    });
  };

  const activeLintSubTab: LintDrawerSubTab =
    activeTab === 'mr-gates' || activeTab === 'breaking-audit' ? 'rules' : activeTab;

  return (
    <div className="pgov-page-shell" data-testid="proto-governance-workbench-page">
      <div className="pgov-page-header">
        <div className="pdoc-page-header__meta">
          <h2 className="pdoc-page-header__title">
            <ShieldCheck size={17} />
            <span>MR 门禁与 Proto 静态规范治理</span>
          </h2>
          <p className="pdoc-page-header__desc">
            统筹 Git MR 事前四维门禁、契约上线破坏性变更审计、命名与注释规则矩阵、领域标准词库及 AST 重复结构体识别。
          </p>
        </div>
      </div>

      <div className="pdoc-top-tabs">
        <div className="pdoc-top-tabs__list" role="tablist" aria-label="契约治理与门禁视图">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'mr-gates'}
            onClick={() => setActiveTab('mr-gates')}
            className={`pdoc-top-tab ${activeTab === 'mr-gates' ? 'pdoc-top-tab--active' : ''}`}
          >
            <GitPullRequest size={15} />
            <span>Git MR 门禁流水</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'breaking-audit'}
            onClick={() => setActiveTab('breaking-audit')}
            className={`pdoc-top-tab ${activeTab === 'breaking-audit' ? 'pdoc-top-tab--active' : ''}`}
          >
            <AlertTriangle size={15} />
            <span>破坏性变更审计</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'rules'}
            onClick={() => setActiveTab('rules')}
            className={`pdoc-top-tab ${activeTab === 'rules' ? 'pdoc-top-tab--active' : ''}`}
          >
            <ShieldAlert size={15} />
            <span>静态检查规则</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'vocab'}
            onClick={() => setActiveTab('vocab')}
            className={`pdoc-top-tab ${activeTab === 'vocab' ? 'pdoc-top-tab--active' : ''}`}
          >
            <BookCheck size={15} />
            <span>领域标准词库</span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'report'}
            onClick={() => setActiveTab('report')}
            className={`pdoc-top-tab ${activeTab === 'report' ? 'pdoc-top-tab--active' : ''}`}
          >
            <FileSearch size={15} />
            <span>体检报告与 AST 重复簇</span>
          </button>
        </div>
      </div>

      {activeTab === 'mr-gates' ? (
        <MRGateWorkbenchPane
          isAdmin={isAdmin}
          onOpenSourcesTab={handleOpenSources}
          onOpenLintCenter={() => setActiveTab('rules')}
          initialRuns={initialRuns}
          initialTotal={initialTotal}
          initialDetail={initialDetail}
        />
      ) : activeTab === 'breaking-audit' ? (
        <BreakingAuditWorkbenchPane
          isAdmin={isAdmin}
          initialEvents={initialBreakingEvents}
          initialTotal={initialBreakingTotal}
          initialOpenCount={initialBreakingOpenCount}
          initialDetail={initialBreakingDetail}
        />
      ) : (
        <div className="pgov-workbench-body">
          <LintRuleAndVocabPanel
            embedded
            hideInternalTabs
            open
            isAdmin={isAdmin}
            activeSubTab={activeLintSubTab}
            onSubTabChange={(sub) => setActiveTab(sub)}
            initialRules={initialRules}
            initialRulesTotal={initialRulesTotal}
            initialVocab={initialVocab}
            initialVocabTotal={initialVocabTotal}
            initialReport={initialReport}
            onJumpToSource={(protoFile, line) => void handleJumpToSource(protoFile, line)}
          />
        </div>
      )}

      <ProtoSourceAnchorDrawer drawer={sourceAnchorDrawer} />
    </div>
  );
};

export default ProtoGovernanceWorkbenchPage;
