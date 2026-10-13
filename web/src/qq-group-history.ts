export interface GroupHistorySettings {
  enabled: boolean;
  roomIds: string[];
  retentionDays: number;
  recentMessages: number;
  maxMessages: number;
}

export function initialGroupHistory(
  value?: Partial<GroupHistorySettings> | null,
): GroupHistorySettings {
  return {
    enabled: value?.enabled ?? false,
    roomIds: [...(value?.roomIds ?? [])],
    retentionDays: value?.retentionDays ?? 7,
    recentMessages: value?.recentMessages ?? 30,
    maxMessages: value?.maxMessages ?? 10000,
  };
}

function integerInRange(
  value: unknown,
  min: number,
  max: number,
  label: string,
): number {
  if (
    typeof value !== "number" ||
    !Number.isInteger(value) ||
    value < min ||
    value > max
  )
    throw new Error(`${label}须为 ${min}–${max} 的整数`);
  return value;
}

export function groupHistoryForSave(
  value: unknown,
  allowedGroups: string[],
): GroupHistorySettings {
  const input =
    value == null
      ? initialGroupHistory()
      : (value as Partial<GroupHistorySettings>);
  if (typeof input.enabled !== "boolean")
    throw new Error("请选择是否开启群聊记录");
  if (
    !Array.isArray(input.roomIds) ||
    input.roomIds.some((id) => typeof id !== "string" || !id.trim())
  )
    throw new Error("请选择有效的采集群");
  const roomIds = [...new Set(input.roomIds.map((id) => id.trim()))];
  if (input.enabled && roomIds.length === 0)
    throw new Error("开启群聊记录时，至少选择一个采集群；空列表不会采集任何群");
  const allowed = new Set(allowedGroups);
  if (roomIds.some((id) => !allowed.has(id)))
    throw new Error("采集群必须在允许使用的 QQ 群号中，请调整采集群或允许列表");
  return {
    enabled: input.enabled,
    roomIds,
    retentionDays: integerInRange(input.retentionDays, 1, 365, "保留天数"),
    recentMessages: integerInRange(
      input.recentMessages,
      0,
      100,
      "自动附带近期条数",
    ),
    maxMessages: integerInRange(
      input.maxMessages,
      100,
      100000,
      "每群保留条数",
    ),
  };
}
