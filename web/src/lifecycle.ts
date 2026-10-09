export enum Resource {
  Unknown = 0,
  Sessions = 1,
  Tasks = 2,
  Artifacts = 3,
  Personas = 4,
  Plugins = 5,
  Configs = 6,
  Runs = 7,
  Executions = 8,
  Notifications = 9,
  Deliveries = 10,
  ModelCalls = 11,
  Secrets = 12,
}
export enum LifecycleAction {
  Unknown = 0,
  Archive = 1,
  Restore = 2,
  Purge = 3,
}
export interface LifecycleItem {
  id: string;
  recordId: string;
  resource: Resource;
  name: string;
}
export interface LifecycleFailure {
  id: string;
  name: string;
  error: string;
}
export interface LifecycleResult {
  succeeded: string[];
  failed: LifecycleFailure[];
}
export const resourceLabels: Record<Resource, string> = {
  [Resource.Unknown]: "未知类型",
  [Resource.Sessions]: "对话",
  [Resource.Tasks]: "定时任务",
  [Resource.Artifacts]: "文件与解析",
  [Resource.Personas]: "人格",
  [Resource.Plugins]: "插件",
  [Resource.Configs]: "模型配置",
  [Resource.Runs]: "对话运行记录",
  [Resource.Executions]: "任务执行记录",
  [Resource.Notifications]: "任务通知",
  [Resource.Deliveries]: "QQ 投递记录",
  [Resource.ModelCalls]: "插件模型调用",
  [Resource.Secrets]: "凭证",
};
export const actionLabels: Record<LifecycleAction, string> = {
  [LifecycleAction.Unknown]: "未知操作",
  [LifecycleAction.Archive]: "归档",
  [LifecycleAction.Restore]: "恢复",
  [LifecycleAction.Purge]: "永久删除",
};
export function isLifecycleAction(action: LifecycleAction): boolean {
  switch (action) {
    case LifecycleAction.Archive:
    case LifecycleAction.Restore:
    case LifecycleAction.Purge:
      return true;
    default:
      return false;
  }
}
export function lifecycleItems(
  resource: Resource,
  rows: Record<string, any>[],
): LifecycleItem[] {
  return rows.map((row) => ({
    id: String(row.id),
    recordId: String(row.id),
    resource,
    name: String(
      row.title ||
        row.name ||
        row.manifest?.name ||
        row.prompt?.slice(0, 45) ||
        row.text?.slice(0, 45) ||
        row.id,
    ),
  }));
}
export function retainSelection(
  selected: string[],
  items: LifecycleItem[],
): string[] {
  const ids = new Set(items.map((item) => item.id));
  return selected.filter((id) => ids.has(id));
}
export function actionDescription(
  action: LifecycleAction,
  items: LifecycleItem[],
): string {
  if (!isLifecycleAction(action)) return "不支持的操作，不会提交请求。";
  if (action === LifecycleAction.Restore)
    return "恢复后会重新显示在原列表。定时任务保持暂停，插件保持停用，请按需手动启用。";
  if (action === LifecycleAction.Purge)
    return "将永久删除所选业务记录，无法恢复。系统仅保留防止重复执行所需的无正文标识。仍被使用或正在执行的项目会拒绝删除。";
  const notes = ["归档后从当前列表移除，可在归档栏恢复或永久删除。"];
  if (items.some((item) => item.resource === Resource.Sessions))
    notes.push("QQ 会话收到新消息后会重新显示，归档不会停止接收消息。");
  if (items.some((item) => item.resource === Resource.Tasks))
    notes.push("定时任务将停止后续调度。");
  if (items.some((item) => item.resource === Resource.Plugins))
    notes.push("插件将停用；已有任务所需的固定版本仍会保留。");
  notes.push("仍被使用或正在执行的项目会保留，并显示原因。");
  return notes.join(" ");
}

// Each request has a single resource and at most 100 IDs. Keep the UI selection key
// separate from recordId because archive rows use compound keys.
export async function applyLifecycle(
  action: LifecycleAction,
  items: LifecycleItem[],
  request: (body: {
    resource: Resource;
    action: LifecycleAction;
    ids: string[];
  }) => Promise<{
    succeeded?: string[];
    failed?: { id: string; error: string }[];
  }>,
): Promise<LifecycleResult> {
  const result: LifecycleResult = { succeeded: [], failed: [] };
  if (!isLifecycleAction(action)) {
    result.failed = items.map((item) => ({
      id: item.id,
      name: item.name,
      error: "不支持的操作，未提交请求。",
    }));
    return result;
  }
  const groups = new Map<Resource, Map<string, LifecycleItem>>();
  for (const item of items) {
    if (!groups.has(item.resource)) groups.set(item.resource, new Map());
    groups.get(item.resource)!.set(item.recordId, item);
  }
  for (const [resource, group] of groups) {
    const values = [...group.values()];
    for (let start = 0; start < values.length; start += 100) {
      const batch = values.slice(start, start + 100);
      try {
        const response = await request({
          resource,
          action,
          ids: batch.map((item) => item.recordId),
        });
        const succeeded = new Set(response.succeeded ?? []);
        const failed = new Map(
          (response.failed ?? []).map((item) => [item.id, item.error]),
        );
        for (const item of batch) {
          if (succeeded.has(item.recordId) && !failed.has(item.recordId))
            result.succeeded.push(item.id);
          else
            result.failed.push({
              id: item.id,
              name: item.name,
              error:
                failed.get(item.recordId) ||
                "服务未确认操作结果，请刷新后核对。",
            });
        }
      } catch (error) {
        for (const item of batch)
          result.failed.push({
            id: item.id,
            name: item.name,
            error:
              error instanceof Error
                ? error.message
                : "请求失败，请刷新后核对。",
          });
      }
    }
  }
  return result;
}
