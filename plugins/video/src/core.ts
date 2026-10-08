export function frameTimes(duration: number, interval = 120): number[] {
  if (!Number.isFinite(duration) || duration <= 0)
    throw new Error("Invalid video duration");
  if (!Number.isFinite(interval) || interval <= 0)
    throw new Error("Invalid frame interval");
  const count = Math.ceil(duration / interval);
  // Keep the frame count within the host limit while covering the whole video.
  // Capping a fixed-interval loop would silently omit the end of long videos.
  if (count > 20) {
    const last = Math.max(duration / 2, duration - 1);
    return Array.from({ length: 20 }, (_, i) => (last * i) / 19);
  }
  return Array.from({ length: count }, (_, i) => i * interval);
}
export function validateVideo(
  size: number,
  duration: number,
  maxMB = 100,
  maxMinutes = 30,
) {
  if (!Number.isFinite(size) || size <= 0 || size > maxMB * 1024 * 1024)
    throw new Error("Video exceeds configured size limit or is empty");
  if (!Number.isFinite(duration) || duration <= 0 || duration > maxMinutes * 60)
    throw new Error("Video exceeds duration limit or has invalid duration");
}

export interface VideoAnalysis {
  summary: string;
  timeline: { timestampSeconds: number | null; description: string }[];
  items: { title: string; dueAt: string | null }[];
}
export function parseAnalysis(text: string, duration: number): VideoAnalysis {
  let value: any;
  try {
    value = JSON.parse(text.trim().replace(/^```(?:json)?\s*|\s*```$/g, ""));
  } catch {
    throw new Error(
      "视觉模型未返回有效 JSON；解析结果已保留，不能视为完整解析",
    );
  }
  const valid =
    value &&
    typeof value.summary === "string" &&
    value.summary.trim().length > 0 &&
    Array.isArray(value.timeline) &&
    value.timeline.length <= 200 &&
    value.timeline.every(
      (entry: any) =>
        entry &&
        typeof entry.description === "string" &&
        entry.description.trim().length > 0 &&
        (entry.timestampSeconds === null ||
          (Number.isFinite(entry.timestampSeconds) &&
            entry.timestampSeconds >= 0 &&
            entry.timestampSeconds < duration)),
    ) &&
    Array.isArray(value.items) &&
    value.items.length <= 200 &&
    value.items.every(
      (entry: any) =>
        entry &&
        typeof entry.title === "string" &&
        entry.title.trim().length > 0 &&
        (entry.dueAt === null ||
          (typeof entry.dueAt === "string" &&
            /^\d{4}-\d{2}-\d{2}T/.test(entry.dueAt) &&
            Number.isFinite(Date.parse(entry.dueAt)))),
    );
  if (!valid)
    throw new Error("视觉模型输出缺少摘要、时间线或事项结构；不能视为完整解析");
  return {
    summary: value.summary,
    timeline: value.timeline.map((entry: any) => ({
      timestampSeconds: entry.timestampSeconds,
      description: entry.description,
    })),
    items: value.items.map((entry: any) => ({
      title: entry.title,
      dueAt: entry.dueAt,
    })),
  };
}

export function audioSegments(duration: number) {
  if (!Number.isFinite(duration) || duration <= 0)
    throw new Error("Invalid video duration");
  const segments: { start: number; duration: number }[] = [];
  for (let start = 0; start < duration; start += 1200)
    segments.push({ start, duration: Math.min(1200, duration - start) });
  return segments;
}
