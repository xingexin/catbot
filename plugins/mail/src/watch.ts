import { createHash } from "node:crypto";
import type { JSONObject, ToolContext } from "@secretary/plugin-sdk";
import type { Cursor } from "./core.js";

export interface WatchMailbox {
  folder: string;
  uidValidity: string;
  uidNext: number;
  searchAfter(uid: number): Promise<number[]>;
  read(uids: number[]): Promise<JSONObject[]>;
}

export interface MailBatch {
  changed: boolean;
  notificationText: string;
  messages: JSONObject[];
  artifactId?: string;
  folder: string;
  uidValidity: string;
  cursor: Cursor;
  hasMore: boolean;
  initialized: boolean;
  reset: boolean;
  checkedAt: string;
}

interface StoredOperation {
  id: string;
  fingerprint: string;
  result: MailBatch;
}

interface MailState {
  cursor: Cursor;
  lastOperation: StoredOperation;
}

function hash(value: unknown): string {
  return createHash("sha256").update(JSON.stringify(value)).digest("hex");
}

function plain(value: unknown, limit: number): string {
  return Array.from(
    String(value ?? "")
      .replace(/\s+/g, " ")
      .trim(),
  )
    .slice(0, limit)
    .join("");
}

export function notificationText(
  folder: string,
  messages: JSONObject[],
): string {
  if (!messages.length) return "";
  const lines = [`邮箱收到 ${messages.length} 封新邮件 · ${plain(folder, 60)}`];
  for (const [index, message] of messages.slice(0, 8).entries()) {
    lines.push(
      `${index + 1}. ${plain(message.subject, 80) || "（无主题）"}`,
      `发件人：${plain(message.from, 50) || "（未知）"}`,
    );
  }
  if (messages.length > 8)
    lines.push(`另有 ${messages.length - 8} 封，完整列表已保存到解析结果。`);
  return lines.join("\n");
}

/** Calls are serialized by the host across all versions of this plugin. */
export async function incrementalMail(
  mode: "watch" | "sync",
  args: JSONObject,
  ctx: ToolContext,
  withMailbox: (
    fn: (mailbox: WatchMailbox) => Promise<MailBatch>,
  ) => Promise<MailBatch>,
): Promise<MailBatch> {
  if (!ctx.operationId)
    throw new Error("Incremental mail requires a stable operation ID");
  const folder = String(args.folder ?? "INBOX");
  const limit = Number(args.limit ?? 20);
  if (!Number.isInteger(limit) || limit < 1 || limit > 50)
    throw new Error("Mail batch limit must be between 1 and 50");
  const scope = hash([
    String(ctx.config.host).toLowerCase(),
    Number(ctx.config.port ?? 993),
    String(ctx.config.username),
    folder,
    mode,
    mode === "watch" ? String(args.monitorId ?? "default") : "",
  ]);
  const key = "incremental:v1:" + scope;
  const fingerprint = hash([
    mode,
    folder,
    limit,
    args.includeExisting === true,
  ]);
  const replayKey = (id: string) =>
    "incremental-result:" + scope + ":" + hash(id);
  const state = (await ctx.host.get(key, ctx.signal)) as MailState | null;
  const replay =
    state?.lastOperation.id === ctx.operationId
      ? state.lastOperation
      : ((await ctx.host.get(
          replayKey(ctx.operationId),
          ctx.signal,
        )) as StoredOperation | null);
  if (replay) {
    if (replay.fingerprint !== fingerprint)
      throw new Error("Operation ID reused with different mail arguments");
    return replay.result;
  }

  const result = await withMailbox(async (mailbox) => {
    if (
      !mailbox.uidValidity ||
      !Number.isSafeInteger(mailbox.uidNext) ||
      mailbox.uidNext < 1
    )
      throw new Error(
        "IMAP server did not provide valid UIDVALIDITY and UIDNEXT",
      );
    let previous = state?.cursor;
    // Preserve the pre-watcher sync position when upgrading the default TLS port.
    if (!previous && mode === "sync" && Number(ctx.config.port ?? 993) === 993)
      previous = (await ctx.host.get(
        "cursor:" + ctx.config.host + ":" + ctx.config.username + ":" + folder,
        ctx.signal,
      )) as Cursor | undefined;
    const initialized = !previous;
    const reset = !!previous && previous.uidValidity !== mailbox.uidValidity;
    const establishBaseline =
      mode === "watch" &&
      (initialized || reset) &&
      args.includeExisting !== true;
    const cursor: Cursor = {
      uidValidity: mailbox.uidValidity,
      uid: establishBaseline
        ? mailbox.uidNext - 1
        : previous?.uidValidity === mailbox.uidValidity
          ? previous.uid
          : 0,
    };
    const found = establishBaseline
      ? []
      : [...new Set(await mailbox.searchAfter(cursor.uid))]
          .filter((uid) => Number.isSafeInteger(uid) && uid > cursor.uid)
          .sort((a, b) => a - b);
    const ids = found.slice(0, limit);
    const messages = await mailbox.read(ids);
    if (ids.length) cursor.uid = ids[ids.length - 1];
    const result: MailBatch = {
      changed: messages.length > 0,
      notificationText: notificationText(folder, messages),
      folder,
      uidValidity: mailbox.uidValidity,
      cursor,
      messages,
      hasMore: found.length > ids.length,
      initialized,
      reset,
      checkedAt: new Date().toISOString(),
    };
    if (messages.length) {
      const artifact = await ctx.host.save(
        (mode === "watch" ? "新邮件提醒 · " : "邮件同步 · ") + folder,
        { ...result, operationId: ctx.operationId },
        ctx.signal,
      );
      result.artifactId = artifact.id;
    }
    return result;
  });
  // A single atomic KV write advances the cursor and saves the result together.
  // Before replacing it, archive the preceding result so a delayed retry remains
  // replayable even after a newer poll has run. A failed write never consumes mail.
  if (state?.lastOperation)
    await ctx.host.set(
      replayKey(state.lastOperation.id),
      state.lastOperation,
      ctx.signal,
    );
  await ctx.host.set(
    key,
    {
      cursor: result.cursor,
      lastOperation: { id: ctx.operationId, fingerprint, result },
    } satisfies MailState,
    ctx.signal,
  );
  return result;
}
