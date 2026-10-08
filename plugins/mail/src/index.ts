import { ImapFlow } from "imapflow";
import { simpleParser } from "mailparser";
import {
  serve,
  type ToolContext,
  type JSONObject,
} from "@secretary/plugin-sdk";
import { parseAnalysis, summaryPrompt } from "./core.js";
import { incrementalMail, type WatchMailbox } from "./watch.js";

async function mailbox<T>(
  args: JSONObject,
  ctx: ToolContext,
  fn: (client: ImapFlow, folder: string) => Promise<T>,
): Promise<T> {
  const { config, signal } = ctx;
  if (!config.host || !config.username || !config.password)
    throw new Error(
      "请先配置 IMAP 服务器、邮箱账号和授权码；密码保存在凭证库。",
    );
  const client = new ImapFlow({
    host: String(config.host),
    port: Number(config.port ?? 993),
    secure: true,
    auth: { user: String(config.username), pass: String(config.password) },
    logger: false,
    connectionTimeout: 15000,
    socketTimeout: 60000,
  });
  // ImapFlow rejects pending requests as well as emitting an error event.
  // Always consume the event so a network disconnect cannot crash the plugin.
  client.on("error", () => {});
  const abort = () => client.close();
  signal.addEventListener("abort", abort, { once: true });
  try {
    signal.throwIfAborted();
    await client.connect();
    const folder = String(args.folder ?? "INBOX");
    const lock = await client.getMailboxLock(folder);
    try {
      return await fn(client, folder);
    } finally {
      lock.release();
    }
  } finally {
    signal.removeEventListener("abort", abort);
    try {
      await client.logout();
    } catch {
      client.close();
    }
  }
}
async function read(client: ImapFlow, uids: number[]) {
  const messages: JSONObject[] = [];
  let remainingBytes = 128 * 1024;
  for (const uid of uids) {
    const metadata = await client.fetchOne(
      uid,
      { uid: true, size: true, envelope: true },
      { uid: true },
    );
    if (!metadata) continue;
    if ((metadata.size ?? 0) > 2 * 1024 * 1024) {
      messages.push({
        uid,
        subject: metadata.envelope?.subject?.slice(0, 500) ?? "",
        from:
          metadata.envelope?.from
            ?.map((address) => address.name || address.address || "")
            .join(", ")
            .slice(0, 500) ?? "",
        date: metadata.envelope?.date?.toISOString(),
        skipped: "message exceeds 2 MB; attachment/body parsing omitted",
      });
      continue;
    }
    const item = await client.fetchOne(
      uid,
      { uid: true, source: true, flags: true },
      { uid: true },
    );
    if (!item || !item.source) continue;
    const parsed = await simpleParser(item.source, {
      skipHtmlToText: false,
      skipTextToHtml: true,
      skipImageLinks: true,
    });
    const original = parsed.text ?? "";
    let text = original.slice(0, 12000);
    while (Buffer.byteLength(text) > remainingBytes)
      text = text.slice(0, Math.floor(text.length * 0.8));
    // Avoid leaving half of a surrogate pair after truncation.
    if (/[\uD800-\uDBFF]$/.test(text)) text = text.slice(0, -1);
    remainingBytes -= Buffer.byteLength(text);
    messages.push({
      uid,
      subject: parsed.subject?.slice(0, 500) ?? "",
      from: parsed.from?.text?.slice(0, 500) ?? "",
      date: parsed.date?.toISOString(),
      text,
      textTruncated: text.length < original.length,
      seen: item.flags?.has("\\Seen") ?? false,
      attachments: parsed.attachments.slice(0, 10).map((a) => ({
        name: a.filename?.slice(0, 256),
        size: a.size,
        mime: a.contentType,
      })),
      attachmentCount: parsed.attachments.length,
    });
  }
  return messages;
}

function incremental(
  mode: "watch" | "sync",
  args: JSONObject,
  ctx: ToolContext,
) {
  return incrementalMail(mode, args, ctx, (fn) =>
    mailbox(args, ctx, async (client, folder) => {
      if (!client.mailbox) throw new Error("IMAP mailbox was not opened");
      const selected: WatchMailbox = {
        folder,
        uidValidity: String(client.mailbox.uidValidity),
        uidNext: client.mailbox.uidNext,
        searchAfter: async (uid) =>
          (await client.search({ uid: uid + 1 + ":*" }, { uid: true })) || [],
        read: (uids) => read(client, uids),
      };
      return fn(selected);
    }),
  );
}

serve({
  test_connection: async (args, ctx) =>
    mailbox(args, ctx, async (client, folder) => {
      if (!client.mailbox) throw new Error("IMAP mailbox was not opened");
      const status = await client.status(folder, { unseen: true });
      return {
        connected: true,
        folder,
        uidValidity: String(client.mailbox.uidValidity),
        messageCount: client.mailbox.exists,
        unreadCount: status.unseen,
        checkedAt: new Date().toISOString(),
      };
    }),
  watch: (args, ctx) => incremental("watch", args, ctx),
  search: async (args, ctx) =>
    mailbox(args, ctx, async (client, folder) => {
      const uids = await client.search(
        args.query ? { text: String(args.query) } : { all: true },
        { uid: true },
      );
      const ids = (uids || []).slice(-Number(args.limit ?? 20));
      return { folder, messages: await read(client, ids) };
    }),
  sync: (args, ctx) => incremental("sync", args, ctx),
  summarize: async (args, ctx) =>
    mailbox(args, ctx, async (client, folder) => {
      if (!ctx.config.summaryConfigId)
        throw new Error("Configure summaryConfigId with an API model");
      const found = await client.search(
        args.unreadOnly ? { seen: false } : { all: true },
        { uid: true },
      );
      const messages = await read(
        client,
        (found || []).sort((a, b) => a - b).slice(-Number(args.limit ?? 20)),
      );
      if (!messages.length)
        return {
          changed: false,
          notificationText: "邮箱暂无符合条件的邮件。",
          folder,
          messages: [],
          analysis: { summary: "暂无邮件", items: [] },
        };
      const generated = await ctx.host.generate(
        String(ctx.config.summaryConfigId),
        summaryPrompt(messages),
        [],
        ctx.signal,
      );
      const extracted = parseAnalysis(
        generated.text,
        messages.map((message) => message.uid),
      );
      const data = {
        folder,
        uidValidity: String(client.mailbox && client.mailbox.uidValidity),
        messages,
        analysis: extracted,
      };
      const saved = await ctx.host.save(
        "邮箱摘要 · " + folder,
        data,
        ctx.signal,
      );
      return {
        artifactId: saved.id,
        changed: true,
        notificationText:
          "邮箱摘要 · " +
          folder +
          "\n" +
          Array.from(extracted.summary).slice(0, 1500).join(""),
        ...data,
      };
    }),
  mark_read: async (args, ctx) =>
    mailbox(args, ctx, async (client, folder) => {
      if (
        String(client.mailbox && client.mailbox.uidValidity) !==
        String(args.uidValidity)
      )
        throw new Error("UIDVALIDITY changed; resync before marking messages");
      const uids = (args.uids as number[]).join(",");
      await client.messageFlagsAdd(uids, ["\\Seen"], { uid: true });
      return { folder, uids: args.uids, read: true };
    }),
}).catch((error) => {
  process.stderr.write(String(error) + "\n");
  process.exitCode = 1;
});
