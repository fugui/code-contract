import React, { useCallback, useState } from 'react';
import { Drawer } from '@code/common';
import * as schemaApi from '../../api/schema';
import type { ReconstructedProtoFileDTO } from '../../types';
import { useToast } from '../../utils/toast';
import { ProtoReadOnlyCodeViewer } from './ProtoReadOnlyCodeViewer';

export interface UseSourceAnchorDrawerResult {
  open: boolean;
  loading: boolean;
  file: ReconstructedProtoFileDTO | null;
  line: number;
  close: () => void;
  setLine: (line: number) => void;
  openBySchemaFile: (schemaId: number, filePath: string, targetLine: number) => Promise<void>;
  openByFilePath: (protoFile: string, targetLine: number, onFallback?: () => void) => Promise<void>;
}

/**
 * 复用只读源码 #L<n> 定位抽屉状态钩子，避免多工作台重复维护抽屉与异步加载逻辑
 */
export function useSourceAnchorDrawer(): UseSourceAnchorDrawerResult {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [file, setFile] = useState<ReconstructedProtoFileDTO | null>(null);
  const [line, setLine] = useState<number>(1);

  const close = useCallback(() => {
    setOpen(false);
    setFile(null);
  }, []);

  const openBySchemaFile = useCallback(
    async (schemaId: number, filePath: string, targetLine: number) => {
      setOpen(true);
      setLine(targetLine > 0 ? targetLine : 1);
      setLoading(true);
      try {
        const res = await schemaApi.getProtoSchemaSources(schemaId, filePath);
        const hit = res.files.find((f) => f.file_path === filePath) ?? res.files[0] ?? null;
        setFile(hit);
      } catch (err) {
        toast.error(err instanceof Error ? err.message : '加载只读源码视图失败');
        setFile(null);
      } finally {
        setLoading(false);
      }
    },
    [toast]
  );

  const openByFilePath = useCallback(
    async (protoFile: string, targetLine: number, onFallback?: () => void) => {
      try {
        const listResp = await schemaApi.listProtoFiles({ search: protoFile, page_size: 15 });
        const matched =
          listResp.items.find((item) => item.file_path === protoFile) ?? listResp.items[0];
        if (matched) {
          await openBySchemaFile(matched.schema_id, matched.file_path, targetLine);
          return;
        }
      } catch {
        // 找不到虚拟文件映射时由调用方决定降级跳转
      }
      if (onFallback) {
        onFallback();
      }
    },
    [openBySchemaFile]
  );

  return {
    open,
    loading,
    file,
    line,
    close,
    setLine,
    openBySchemaFile,
    openByFilePath,
  };
}

export interface ProtoSourceAnchorDrawerProps {
  drawer: UseSourceAnchorDrawerResult;
}

/** 轻量只读 .proto 源码行号定位抽屉 */
export const ProtoSourceAnchorDrawer: React.FC<ProtoSourceAnchorDrawerProps> = ({ drawer }) => {
  return (
    <Drawer
      open={drawer.open}
      onClose={drawer.close}
      title={
        drawer.file
          ? `只读源码锚点浏览: ${drawer.file.file_path}#L${drawer.line}`
          : '只读源码锚点浏览'
      }
      width={880}
    >
      {drawer.loading ? (
        <p className="pdoc-card__subtitle">正在加载只读源码与精确行号锚点...</p>
      ) : drawer.file ? (
        <ProtoReadOnlyCodeViewer
          filePath={drawer.file.file_path}
          content={drawer.file.content}
          highlightLine={drawer.line}
          symbolAnchors={drawer.file.symbol_anchors}
          versionLabel={`${drawer.file.schema_name} v${drawer.file.schema_version}`}
          hasSourceInfo={drawer.file.has_source_info}
          sourceOrigin={drawer.file.source_origin}
          onSelectLine={drawer.setLine}
        />
      ) : (
        <p className="pdoc-card__subtitle">未能加载指定描述集的只读源码内容。</p>
      )}
    </Drawer>
  );
};
