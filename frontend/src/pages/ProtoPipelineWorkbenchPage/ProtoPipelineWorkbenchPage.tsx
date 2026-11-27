import React, { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import * as schemaApi from '../../api/schema';
import { subscribeServerEvents } from '../../api/sse';
import { useModuleUser } from '../../context/ModuleUserContext';
import { useToast } from '../../utils/toast';
import type {
  PipelineOverviewDTO,
  PipelineProgressEventPayload,
  PoolReconcileProgressEventPayload,
  PublishProgressEventPayload,
  SyncJobProgressEventPayload,
} from '../../types';
import { PipelineStepperHeader, type PipelineStageKey } from './PipelineStepperHeader';
import { StageCollectPane } from './StageCollectPane';
import { StageNormalizePane } from './StageNormalizePane';
import { StageReconcilePane } from './StageReconcilePane';
import { StagePublishPane } from './StagePublishPane';
import './ProtoPipelineWorkbenchPage.css';

function normalizeStageParam(raw: string | null): PipelineStageKey {
  switch (raw) {
    case 'collect':
    case 'normalize':
    case 'reconcile':
    case 'publish':
      return raw;
    default:
      return 'publish';
  }
}

export interface ProtoPipelineWorkbenchPageProps {
  initialOverview?: PipelineOverviewDTO | null;
}

export const ProtoPipelineWorkbenchPage: React.FC<ProtoPipelineWorkbenchPageProps> = ({
  initialOverview = null,
}) => {
  const [searchParams, setSearchParams] = useSearchParams();
  const { user } = useModuleUser();
  const toast = useToast();
  const isAdmin = Boolean(user?.is_module_admin || user?.is_super_admin);

  const activeStage = normalizeStageParam(searchParams.get('stage'));
  const setActiveStage = (stage: PipelineStageKey) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('stage', stage);
      return next;
    });
  };

  const [overview, setOverview] = useState<PipelineOverviewDTO | null>(initialOverview);
  const [running, setRunning] = useState(false);
  const [fromStage, setFromStage] = useState<'COLLECT_NORMALIZE' | 'RECONCILE' | 'PUBLISH'>(
    'COLLECT_NORMALIZE'
  );

  const [pipelineProgress, setPipelineProgress] = useState<PipelineProgressEventPayload | null>(
    null
  );
  const [publishProgress, setPublishProgress] = useState<PublishProgressEventPayload | null>(null);
  const [poolProgress, setPoolProgress] = useState<PoolReconcileProgressEventPayload | null>(null);
  const [syncProgress, setSyncProgress] = useState<SyncJobProgressEventPayload | null>(null);

  const loadOverview = useCallback(async () => {
    try {
      const ov = await schemaApi.fetchProtoPipelineOverview();
      setOverview(ov);
    } catch (err) {
      toast.error((err as Error).message || '加载契约流水线状态失败');
    }
  }, [toast]);

  useEffect(() => {
    void loadOverview();
  }, [loadOverview]);

  useEffect(() => {
    if (typeof window === 'undefined' || typeof fetch === 'undefined') {
      return;
    }
    const unsubscribe = subscribeServerEvents({
      onMessage: (evt) => {
        if (evt.event === 'proto_pipeline.progress') {
          const pp = evt.data as PipelineProgressEventPayload | undefined;
          if (!pp) return;
          setPipelineProgress(pp);
          if (pp.status === 'COMPLETED' || pp.status === 'PARTIAL_BLOCKED' || pp.status === 'FAILED') {
            setRunning(false);
            void loadOverview();
          }
        } else if (evt.event === 'proto_publish.progress') {
          const pub = evt.data as PublishProgressEventPayload | undefined;
          if (!pub) return;
          setPublishProgress(pub);
          if (
            pub.stage === 'COMPLETED' ||
            pub.stage === 'FAILED' ||
            pub.status === 'COMPLETED' ||
            pub.status === 'PARTIAL_BLOCKED'
          ) {
            void loadOverview();
          }
        } else if (evt.event === 'proto_pool_reconcile.progress') {
          const rec = evt.data as PoolReconcileProgressEventPayload | undefined;
          if (!rec) return;
          setPoolProgress(rec);
          if (rec.stage === 'COMPLETED' || rec.status === 'COMPLETED' || rec.status === 'FAILED') {
            void loadOverview();
          }
        } else if (evt.event === 'proto_sync_job.progress') {
          const sj = evt.data as SyncJobProgressEventPayload | undefined;
          if (!sj) return;
          setSyncProgress(sj);
          if (sj.status === 'COMPLETED' || sj.status === 'FAILED') {
            void loadOverview();
          }
        }
      },
    });
    return () => {
      unsubscribe();
    };
  }, [loadOverview]);

  const handleRunAll = async () => {
    if (!isAdmin || running || overview?.lock_holder) return;
    setRunning(true);
    try {
      const res = await schemaApi.runProtoPipeline({
        from_stage: fromStage,
      });
      toast.success(`已启动契约流水线 #${res.pipeline_run.id}（起始阶段: ${fromStage}）`);
      if (fromStage === 'RECONCILE') {
        setActiveStage('reconcile');
      } else if (fromStage === 'PUBLISH') {
        setActiveStage('publish');
      }
      await loadOverview();
    } catch (err) {
      setRunning(false);
      toast.error((err as Error).message || '触发四阶段流水线失败');
    }
  };

  const lockHolder = overview?.lock_holder || null;
  const locked = Boolean(lockHolder);

  return (
    <div className="ppipe-page" data-testid="proto-pipeline-workbench-page">
      <PipelineStepperHeader
        activeStage={activeStage}
        onSelectStage={setActiveStage}
        overview={overview}
        lockHolder={lockHolder}
        pipelineProgress={pipelineProgress}
        isAdmin={isAdmin}
        running={running}
        fromStage={fromStage}
        onChangeFromStage={setFromStage}
        onRunAll={() => void handleRunAll()}
        onRefresh={() => void loadOverview()}
      />

      {activeStage === 'collect' && (
        <StageCollectPane
          overview={overview}
          isAdmin={isAdmin}
          locked={locked}
          syncProgress={syncProgress}
          onRefresh={() => void loadOverview()}
        />
      )}

      {activeStage === 'normalize' && (
        <StageNormalizePane
          overview={overview}
          isAdmin={isAdmin}
          locked={locked}
          onRefresh={() => void loadOverview()}
        />
      )}

      {activeStage === 'reconcile' && (
        <StageReconcilePane
          overview={overview}
          isAdmin={isAdmin}
          locked={locked}
          poolProgress={poolProgress}
          onRefresh={() => void loadOverview()}
        />
      )}

      {activeStage === 'publish' && (
        <StagePublishPane
          overview={overview}
          isAdmin={isAdmin}
          locked={locked}
          publishProgress={publishProgress}
          onRefresh={() => void loadOverview()}
        />
      )}
    </div>
  );
};

export default ProtoPipelineWorkbenchPage;
