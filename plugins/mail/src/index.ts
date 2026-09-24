import { ImapFlow } from "imapflow";
import { simpleParser } from "mailparser";
import {
  serve,
  type ToolContext,
  type JSONObject,
} from "@secretary/plugin-sdk";
import { cursorFor, summaryPrompt, type Cursor } from "./core.js";

async function mailbox<T>(
  args: JSONObject,
  ctx: ToolContext,
  fn: (client: ImapFlow, folder: string) => Promise<T>,
): Promise<T> {
  const { config, signal } = ctx;
  const client = new ImapFlow({
    host: String(config.host),
    port: Number(config.port ?? 993),
    secure: true,
    auth: { user: String(config.username), pass: String(config.password) },
    logger: false,
    connectionTimeout: 15000,
    socketTimeout: 60000,
  });
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
        subject: metadata.envelope?.subject,
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
    messages.push({
      uid,
      subject: parsed.subject ?? "",
      from: parsed.from?.text ?? "",
      date: parsed.date?.toISOString(),
      text: (parsed.text ?? "").slice(0, 12000),
      seen: item.flags?.has("\\Seen") ?? false,
      attachments: parsed.attachments.map((a) => ({
        name: a.filename,
        size: a.size,
        mime: a.contentType,
      })),
    });
  }
  return messages;
}
serve({
  search: async (args, ctx) =>
    mailbox(args, ctx, async (client, folder) => {
      const uids = await client.search(
        args.query ? { text: String(args.query) } : { all: true },
        { uid: true },
      );
      const ids = (uids || []).slice(-Number(args.limit ?? 20));
      return { folder, messages: await read(client, ids) };
    }),
  sync: async (args, ctx) =>
    mailbox(args, ctx, async (client, folder) => {
      const validity = String(client.mailbox && client.mailbox.uidValidity);
      // Username, folder and UIDVALIDITY scope the cursor. A changed UIDVALIDITY resets it.
      const key =
        "cursor:" + ctx.config.host + ":" + ctx.config.username + ":" + folder;
      const old = (await ctx.host.get(key, ctx.signal)) as Cursor | null;
      const cursor = cursorFor(old, validity);
      const found = await client.search(
        { uid: cursor.uid + 1 + ":*" },
        { uid: true },
      );
      const ids = (found || [])
        .filter((uid) => uid > cursor.uid)
        .sort((a, b) => a - b)
        .slice(0, Number(args.limit ?? 20));
      const messages = await read(client, ids);
      const artifact = await ctx.host.save(
        "邮件同步 · " + folder,
        { folder, uidValidity: validity, messages },
        ctx.signal,
      );
      if (ids.length) cursor.uid = ids[ids.length - 1];
      await ctx.host.set(key, cursor, ctx.signal);
      return {
        artifactId: artifact.id,
        folder,
        uidValidity: validity,
        cursor,
        messages,
        hasMore: (found || []).some((uid) => uid > cursor.uid),
      };
    }),
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
        (found || []).slice(-Number(args.limit ?? 20)),
      );
      const generated = await ctx.host.generate(
        String(ctx.config.summaryConfigId),
        summaryPrompt(messages),
        [],
        ctx.signal,
      );
      let extracted: unknown;
      try {
        extracted = JSON.parse(
          generated.text.replace(/^```(?:json)?\s*|\s*```$/g, ""),
        );
      } catch {
        extracted = { summary: generated.text, items: [], structured: false };
      }
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
      return { artifactId: saved.id, ...data };
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
