export function frameTimes(duration: number, interval = 120): number[] {
  if (!Number.isFinite(duration) || duration <= 0)
    throw new Error("Invalid video duration");
  const times: number[] = [];
  for (let t = 0; t < duration && times.length < 20; t += interval)
    times.push(t);
  return times;
}
export function validateVideo(
  size: number,
  duration: number,
  maxMB = 100,
  maxMinutes = 30,
) {
  if (size > maxMB * 1024 * 1024)
    throw new Error("Video exceeds configured size limit");
  if (!Number.isFinite(duration) || duration <= 0 || duration > maxMinutes * 60)
    throw new Error("Video exceeds duration limit or has invalid duration");
}
