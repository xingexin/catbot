import { useEffect, useRef, useState, type Key } from "react";
import { Alert, App, Button, Table, type TableProps } from "antd";
import { api } from "./api";
import {
  actionDescription,
  actionLabels,
  applyLifecycle,
  lifecycleItems,
  isLifecycleAction,
  LifecycleAction,
  Resource,
  resourceLabels,
  retainSelection,
  type LifecycleFailure,
  type LifecycleItem,
} from "./lifecycle";

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
  function confirm(action: LifecycleAction, only?: LifecycleItem[]) {
    if (locked.current) return;
    if (!isLifecycleAction(action)) {
      message.error("不支持的操作，未提交请求。");
      return;
    }
    const targets = only ?? items.filter((item) => selectedSet.has(item.id));
    if (!targets.length) return;
    locked.current = true;
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
      okText:
        action === LifecycleAction.Purge
          ? "确认永久删除"
          : `确认${actionLabels[action]}`,
      cancelText: "取消",
      okButtonProps: { danger: action === LifecycleAction.Purge },
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
          const succeeded = new Set(result.succeeded);
          setSelected((old) => old.filter((id) => !succeeded.has(id)));
          setFailures(result.failed);
          onSucceeded?.(result.succeeded, action);
          if (result.succeeded.length)
            message.success(
              `已${actionLabels[action]} ${result.succeeded.length} 项`,
            );
          if (result.failed.length)
            message.warning(
              `${result.failed.length} 项未${actionLabels[action]}，请查看列表上方的原因。`,
            );
          await onRefresh();
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
