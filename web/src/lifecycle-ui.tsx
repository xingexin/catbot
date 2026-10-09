import { useEffect, useRef, useState, type Key } from "react";
import { Alert, App, Button, Table, Tag, type TableProps } from "antd";
import { api } from "./api";
import {
  actionDescription,
  actionLabels,
  applyLifecycle,
  previewPurge,
  confirmPurge,
  purgeTargetKey,
  lifecycleItems,
  isLifecycleAction,
  LifecycleAction,
  Resource,
  resourceLabels,
  retainSelection,
  type LifecycleFailure,
  type LifecycleItem,
  type LifecycleResult,
  type PurgePreview,
  type PurgePreviewItem,
} from "./lifecycle";

function PurgeImpactList({
  title,
  items,
}: {
  title: string;
  items: PurgePreviewItem[];
}) {
  if (!items.length) return null;
  const groups = new Map<Resource, PurgePreviewItem[]>();
  for (const item of items) {
    if (!groups.has(item.resource)) groups.set(item.resource, []);
    groups.get(item.resource)!.push(item);
  }
  return (
    <section aria-label={title}>
      <h4>
        {title}（{items.length} 项）
      </h4>
      {[...groups].map(([resource, records]) => (
        <div key={resource}>
          <strong>
            {resourceLabels[resource]} · {records.length} 项
          </strong>
          <ul>
            {records.map((item) => (
              <li key={purgeTargetKey(item)}>
                {item.name}
                {!item.archived && <Tag color="orange">未归档</Tag>}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </section>
  );
}

function PurgePreviewContent({ preview }: { preview: PurgePreview }) {
  const selected = preview.items.filter((item) => item.selected);
  const related = preview.items.filter((item) => !item.selected);
  return (
    <>
      <p>
        所选 {selected.length} 项
        {related.length > 0 ? `及关联的 ${related.length} 项内容将一起` : "将"}
        永久删除，<strong>无法恢复</strong>。
        {related.some((item) => !item.archived) &&
          "关联列表中标为「未归档」的内容也会永久删除。"}
      </p>
      {preview.items.some((item) => item.resource === Resource.Tasks) && (
        <p>相关任务将停止调度，执行记录和通知等关联内容也会删除。</p>
      )}
      {preview.items.some((item) => item.resource === Resource.Plugins) && (
        <p>相关插件将停用，并清理插件私有数据。</p>
      )}
      {preview.blockers.length > 0 && (
        <Alert
          type="warning"
          showIcon
          message="请先处理以下运行限制，再重新预览并确认删除。"
        />
      )}
      <div
        className="purge-preview-list"
        tabIndex={0}
        aria-label="永久删除范围"
      >
        {preview.blockers.length > 0 && (
          <section aria-label="需要先处理的项目">
            <h4>需要先处理的项目（{preview.blockers.length} 项）</h4>
            <ul>
              {preview.blockers.map((item) => (
                <li key={purgeTargetKey(item)}>
                  <strong>
                    {resourceLabels[item.resource]} · {item.name}
                  </strong>
                  ：{item.reason}
                </li>
              ))}
            </ul>
          </section>
        )}
        <PurgeImpactList title="所选内容" items={selected} />
        <PurgeImpactList title="将同时删除的关联内容" items={related} />
      </div>
    </>
  );
}

export function useLifecycleSelection(
  items: LifecycleItem[],
  onRefresh: () => Promise<void>,
  onSucceeded?: (ids: string[], action: LifecycleAction) => void,
) {
  const { modal, message } = App.useApp();
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [failures, setFailures] = useState<LifecycleFailure[]>([]);
  const locked = useRef(false);
  useEffect(() => {
    setSelected((old) => {
      const next = retainSelection(old, items);
      return next.length === old.length ? old : next;
    });
  }, [items]);
  const selectedIds = retainSelection(selected, items);
  const selectedSet = new Set(selectedIds);
  function toggle(id: string) {
    if (!locked.current)
      setSelected((old) =>
        old.includes(id) ? old.filter((value) => value !== id) : [...old, id],
      );
  }
  async function showResult(
    result: LifecycleResult,
    action: LifecycleAction,
    deletedCount?: number,
  ) {
    const succeeded = new Set(result.succeeded);
    setSelected((old) => old.filter((id) => !succeeded.has(id)));
    setFailures(result.failed);
    onSucceeded?.(result.succeeded, action);
    const count = deletedCount ?? result.succeeded.length;
    if (count)
      message.success(
        `已${actionLabels[action]} ${count} 项${deletedCount !== undefined ? "内容" : ""}`,
      );
    if (result.failed.length)
      message.warning(
        `${result.failed.length} 项未${actionLabels[action]}，请查看列表上方的原因。`,
      );
    try {
      await onRefresh();
    } catch {
      message.error("操作结果已返回，但列表刷新失败，请刷新页面核对。");
    }
  }
  async function showPurgeConfirmation(targets: LifecycleItem[]) {
    setBusy(true);
    setFailures([]);
    let preview: PurgePreview;
    try {
      preview = await previewPurge(targets, (body) =>
        api("/lifecycle/purge-preview", body),
      );
    } catch (error) {
      message.error(
        error instanceof Error
          ? error.message
          : "无法加载删除范围，请重新预览。",
      );
      locked.current = false;
      return;
    } finally {
      setBusy(false);
    }
    const hasRelated = preview.items.some((item) => !item.selected);
    const dialog = modal.confirm({
      title: hasRelated
        ? "是否同时删除关联内容？"
        : `永久删除所选 ${targets.length} 项？`,
      width: 720,
      content: <PurgePreviewContent preview={preview} />,
      okText: hasRelated ? "确认全部永久删除" : "确认永久删除",
      cancelText: "取消",
      okButtonProps: { danger: true, disabled: preview.blockers.length > 0 },
      maskClosable: false,
      onCancel: () => {
        locked.current = false;
      },
      onOk: async () => {
        dialog.update({
          cancelButtonProps: { disabled: true },
          keyboard: false,
        });
        setBusy(true);
        setFailures([]);
        try {
          const result = await confirmPurge(targets, preview, (body) =>
            api("/lifecycle/purge-confirm", body),
          );
          await showResult(
            result,
            LifecycleAction.Purge,
            result.deleted.length,
          );
        } catch (error) {
          const reason = `${error instanceof Error ? error.message : "删除结果尚未确认。"} 请刷新核对，重新点击「永久删除」预览范围并确认。`;
          setFailures(
            targets.map((item) => ({
              id: item.id,
              name: item.name,
              error: reason,
            })),
          );
          message.error(reason);
          // Never resubmit the old token automatically. The next click performs
          // a fresh preview so changed dependencies require another confirmation.
          try {
            await onRefresh();
          } catch {
            /* Keep the original deletion error visible. */
          }
        } finally {
          setBusy(false);
          locked.current = false;
        }
      },
    });
  }
  function confirm(action: LifecycleAction, only?: LifecycleItem[]) {
    if (locked.current) return;
    if (!isLifecycleAction(action)) {
      message.error("不支持的操作，未提交请求。");
      return;
    }
    const targets = only ?? items.filter((item) => selectedSet.has(item.id));
    if (!targets.length) return;
    locked.current = true;
    if (action === LifecycleAction.Purge) {
      void showPurgeConfirmation(targets);
      return;
    }
    modal.confirm({
      title: `${actionLabels[action]}所选 ${targets.length} 项？`,
      content: (
        <>
          <p>{actionDescription(action, targets)}</p>
          <p className="muted">
            {targets
              .slice(0, 3)
              .map((item) => item.name)
              .join("、")}
            {targets.length > 3 ? ` 等 ${targets.length} 项` : ""}
          </p>
          {targets.length > 100 && <p>将按类型分组，每批最多处理 100 项。</p>}
        </>
      ),
      okText: `确认${actionLabels[action]}`,
      cancelText: "取消",
      maskClosable: false,
      onCancel: () => {
        locked.current = false;
      },
      onOk: async () => {
        setBusy(true);
        setFailures([]);
        try {
          const result = await applyLifecycle(action, targets, (body) =>
            api("/lifecycle", body),
          );
          await showResult(result, action);
        } catch (error) {
          message.error(
            error instanceof Error
              ? error.message
              : "刷新失败，请重新刷新列表核对结果。",
          );
        } finally {
          setBusy(false);
          locked.current = false;
        }
      },
    });
  }
  const resources = [...new Set(items.map((item) => item.resource))];
  return {
    label: resources.length === 1 ? resourceLabels[resources[0]] : "归档内容",
    selectedIds,
    busy,
    failures,
    clearFailures: () => setFailures([]),
    checked: (id: string) => selectedSet.has(id),
    toggle,
    selectAll: (checked: boolean) => {
      if (!locked.current)
        setSelected(checked ? items.map((item) => item.id) : []);
    },
    clear: () => {
      if (!locked.current) setSelected([]);
    },
    confirm,
    rowSelection: {
      selectedRowKeys: selectedIds,
      onChange: (keys: Key[]) => {
        if (!locked.current) setSelected(keys.map(String));
      },
      getCheckboxProps: () => ({ disabled: busy }),
      columnTitle: (node: React.ReactNode) => (
        <span title="全选当前页">{node}</span>
      ),
    },
  };
}
export type LifecycleSelection = ReturnType<typeof useLifecycleSelection>;
export function LifecycleActions({
  selection,
  archive = false,
}: {
  selection: LifecycleSelection;
  archive?: boolean;
}) {
  const hasSelection = selection.selectedIds.length > 0;
  return (
    <span
      className={`lifecycle-actions${hasSelection ? "" : " is-empty"}`}
      aria-hidden={!hasSelection}
      role="group"
      aria-label={`${selection.label}批量操作`}
    >
      {archive ? (
        <>
          <Button
            size="small"
            disabled={!hasSelection || selection.busy}
            onClick={() => selection.confirm(LifecycleAction.Restore)}
          >
            恢复所选
          </Button>
          <Button
            size="small"
            danger
            disabled={!hasSelection}
            loading={selection.busy}
            onClick={() => selection.confirm(LifecycleAction.Purge)}
          >
            永久删除
          </Button>
        </>
      ) : (
        <Button
          size="small"
          disabled={!hasSelection}
          loading={selection.busy}
          onClick={() => selection.confirm(LifecycleAction.Archive)}
        >
          归档所选
        </Button>
      )}
    </span>
  );
}

export function LifecycleFeedback({
  selection,
}: {
  selection: LifecycleSelection;
}) {
  if (!selection.failures.length) return null;
  return (
    <div className="lifecycle-controls">
      <Alert
        type="warning"
        showIcon
        closable
        onClose={selection.clearFailures}
        message={`${selection.failures.length} 项操作未完成`}
        description={
          <ul className="lifecycle-failures">
            {selection.failures.map((failure) => (
              <li key={failure.id}>
                <strong>{failure.name}</strong>：{failure.error}
              </li>
            ))}
          </ul>
        }
      />
    </div>
  );
}

export function withLifecycleActions<T extends Record<string, any>>(
  columns: NonNullable<TableProps<T>["columns"]>,
  selection: LifecycleSelection,
  archive = false,
): NonNullable<TableProps<T>["columns"]> {
  return columns.map((column, index) =>
    index === columns.length - 1
      ? {
          ...column,
          title: (props) => (
            <div className="lifecycle-column-heading">
              <span>
                {typeof column.title === "function"
                  ? column.title(props)
                  : column.title}
              </span>
              <LifecycleActions selection={selection} archive={archive} />
            </div>
          ),
        }
      : column,
  );
}

export function LifecycleTable<T extends Record<string, any>>({
  resource,
  onRefresh,
  dataSource = [],
  columns = [],
  ...props
}: TableProps<T> & { resource: Resource; onRefresh: () => Promise<void> }) {
  const selection = useLifecycleSelection(
    lifecycleItems(resource, [...dataSource]),
    onRefresh,
  );
  return (
    <>
      <LifecycleFeedback selection={selection} />
      <Table<T>
        {...props}
        dataSource={dataSource}
        columns={withLifecycleActions(columns, selection)}
        rowSelection={selection.rowSelection}
      />
    </>
  );
}
