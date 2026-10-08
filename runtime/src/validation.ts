import type { RunRequest } from "./contracts.js";
const identifier = /^[a-zA-Z0-9:_-]{1,240}$/;
const object = (value: unknown): value is Record<string, any> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown): value is string =>
  typeof value === "string" && !value.includes("\0");

export function validRun(value: unknown): value is RunRequest {
  if (
    !object(value) ||
    !text(value.runId) ||
    !text(value.sessionId) ||
    !identifier.test(value.runId) ||
    !identifier.test(value.sessionId)
  )
    return false;
  const { config, persona } = value;
  if (
    !object(config) ||
    !object(persona) ||
    !text(config.provider) ||
    !text(config.model) ||
    !config.model ||
    !Number.isInteger(config.maxSteps) ||
    config.maxSteps < 1 ||
    config.maxSteps > 1000 ||
    (config.timeoutSec !== undefined &&
      (!Number.isInteger(config.timeoutSec) ||
        config.timeoutSec < 1 ||
        config.timeoutSec > 3600)) ||
    !text(persona.systemPrompt) ||
    (persona.preferences !== undefined && !text(persona.preferences)) ||
    !text(value.prompt) ||
    !text(value.gatewayToken) ||
    !value.gatewayToken ||
    !text(value.gatewayUrl) ||
    (value.apiKey !== undefined && !text(value.apiKey)) ||
    (value.nativeId !== undefined &&
      (!text(value.nativeId) || value.nativeId.length > 256)) ||
    (config.baseUrl !== undefined && !text(config.baseUrl))
  )
    return false;
  const messages = (list: unknown) =>
    Array.isArray(list) &&
    list.every(
      (entry) => object(entry) && text(entry.role) && text(entry.content),
    );
  if (
    (value.history != null && !messages(value.history)) ||
    (persona.examples != null && !messages(persona.examples))
  )
    return false;
  try {
    const url = new URL(value.gatewayUrl);
    if (
      !["http:", "https:"].includes(url.protocol) ||
      url.username ||
      url.password
    )
      return false;
  } catch {
    return false;
  }
  return true;
}

export function safeEvent(
  event: unknown,
  secrets: (string | undefined)[],
): { json: string; bytes: number } {
  let json = JSON.stringify(event, (_key, value) => {
    if (typeof value === "string" && value.includes("\0"))
      throw new Error("SDK returned malformed NUL text");
    return value;
  });
  for (const secret of secrets) {
    if (!secret) continue;
    const encoded = JSON.stringify(secret).slice(1, -1);
    json = json.split(encoded).join("[REDACTED]");
  }
  const bytes = Buffer.byteLength(json);
  if (bytes > 1024 * 1024)
    throw new Error("SDK event exceeds the 1 MB output limit");
  return { json, bytes };
}
