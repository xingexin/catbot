import test from "node:test";
import assert from "node:assert/strict";
import { createServer as createHTTPServer } from "node:http";
import { createServer as createTLSServer, type TLSSocket } from "node:tls";
import { execFileSync } from "node:child_process";
import { mkdtemp, readFile, writeFile, rm, copyFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";

// This speaks actual TLS IMAP to ImapFlow and actual MCP stdio to the plugin.
// It deliberately contains no real mailbox or QQ account credentials. Model
// outputs are fixed host responses that exercise contracts, not semantic quality.
test(
  "TLS IMAP plugin receives mail and validates persisted summary items through MCP",
  { timeout: 30000 },
  async (t) => {
    const root = resolve(fileURLToPath(new URL("../", import.meta.url)));
    const dir = await mkdtemp(join(tmpdir(), "secretary-imap-fixture-"));
    t.after(() => rm(dir, { recursive: true, force: true }));
    const certificate = join(dir, "fixture.crt");
    const key = join(dir, "fixture.key");
    const opensslConfig = join(dir, "openssl.cnf");
    await writeFile(
      opensslConfig,
      "[req]\nprompt=no\ndistinguished_name=dn\nx509_extensions=ext\n[dn]\nCN=localhost\n[ext]\nsubjectAltName=DNS:localhost,IP:127.0.0.1\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\n",
    );
    execFileSync(
      "openssl",
      [
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-keyout",
        key,
        "-out",
        certificate,
        "-days",
        "1",
        "-config",
        opensslConfig,
      ],
      { stdio: "pipe" },
    );
    await build({
      entryPoints: [join(root, "src/index.ts")],
      outfile: join(dir, "plugin.cjs"),
      bundle: true,
      format: "cjs",
      platform: "node",
      logLevel: "silent",
    });
    await copyFile(join(root, "plugin.json"), join(dir, "plugin.json"));

    type Mail = { uid: number; subject: string; text: string; seen: boolean };
    const mails: Mail[] = [
      {
        uid: 1,
        subject: "existing-mail",
        text: "Historical mail body",
        seen: false,
      },
    ];
    const commands: string[] = [];
    const sockets = new Set<TLSSocket>();
    const source = (mail: Mail) =>
      Buffer.from(
        `From: Alice <alice@example.test>\r\nTo: Bot <bot@example.test>\r\nSubject: ${mail.subject}\r\nDate: Sat, 03 Oct 2026 09:00:00 +0800\r\nMessage-ID: <mail-${mail.uid}@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n${mail.text}\r\n`,
      );
    const imap = createTLSServer(
      { key: await readFile(key), cert: await readFile(certificate) },
      (socket) => {
        sockets.add(socket);
        socket.on("close", () => sockets.delete(socket));
        socket.on("error", () => {});
        socket.write("* OK [CAPABILITY IMAP4rev1] local fixture ready\r\n");
        let input = "";
        socket.on("data", (data) => {
          input += data.toString();
          let newline: number;
          while ((newline = input.indexOf("\r\n")) !== -1) {
            const line = input.slice(0, newline);
            input = input.slice(newline + 2);
            const space = line.indexOf(" ");
            const tag = line.slice(0, space);
            const command = line.slice(space + 1);
            commands.push(
              command.startsWith("LOGIN ") ? "LOGIN [fixture]" : command,
            );
            const ok = () => socket.write(`${tag} OK done\r\n`);
            if (command === "CAPABILITY") {
              socket.write("* CAPABILITY IMAP4rev1\r\n");
              ok();
            } else if (command.startsWith("LOGIN ")) ok();
            else if (/^(LIST|LSUB) /.test(command)) {
              socket.write('* LIST (\\HasNoChildren) "/" "INBOX"\r\n');
              ok();
            } else if (/^(SELECT|EXAMINE) /.test(command)) {
              socket.write(
                `* FLAGS (\\Seen)\r\n* ${mails.length} EXISTS\r\n* 0 RECENT\r\n* OK [UIDVALIDITY 42] stable\r\n* OK [UIDNEXT ${(mails.at(-1)?.uid ?? 0) + 1}] next\r\n* OK [PERMANENTFLAGS (\\Seen \\*)] flags\r\n${tag} OK [READ-WRITE] selected\r\n`,
              );
            } else if (command.startsWith("STATUS ")) {
              socket.write(
                `* STATUS "INBOX" (MESSAGES ${mails.length} UNSEEN ${mails.filter((mail) => !mail.seen).length} UIDNEXT ${(mails.at(-1)?.uid ?? 0) + 1} UIDVALIDITY 42)\r\n`,
              );
              ok();
            } else if (command.startsWith("UID SEARCH ")) {
              const start = Number(/UID (\d+):\*/.exec(command)?.[1] ?? 0);
              let found = mails.filter(
                (mail) =>
                  mail.uid >= Math.min(start, mails.at(-1)?.uid ?? start),
              );
              if (command.includes("UNSEEN"))
                found = found.filter((mail) => !mail.seen);
              const textQuery = /\bTEXT (?:"([^"]*)"|([^ )]+))/.exec(command);
              if (textQuery) {
                const query = (textQuery[1] ?? textQuery[2]).toLowerCase();
                found = found.filter((mail) =>
                  (mail.subject + " " + mail.text)
                    .toLowerCase()
                    .includes(query),
                );
              }
              socket.write(
                `* SEARCH${found.length ? " " + found.map((mail) => mail.uid).join(" ") : ""}\r\n`,
              );
              ok();
            } else if (command.startsWith("UID FETCH ")) {
              const uid = Number(/^UID FETCH (\d+)/.exec(command)?.[1]);
              const mail = mails.find((item) => item.uid === uid);
              if (mail) {
                const sequence = mails.indexOf(mail) + 1;
                const flags = mail.seen ? "\\Seen" : "";
                const body = source(mail);
                if (command.includes("BODY.PEEK[]")) {
                  socket.write(
                    `* ${sequence} FETCH (UID ${uid} FLAGS (${flags}) BODY[] {${body.byteLength}}\r\n`,
                  );
                  socket.write(body);
                  socket.write(")\r\n");
                } else {
                  const address = '(("Alice" NIL "alice" "example.test"))';
                  socket.write(
                    `* ${sequence} FETCH (UID ${uid} FLAGS (${flags}) RFC822.SIZE ${body.byteLength} ENVELOPE ("Sat, 03 Oct 2026 09:00:00 +0800" "${mail.subject}" ${address} ${address} ${address} NIL NIL NIL NIL "<mail-${uid}@example.test>"))\r\n`,
                  );
                }
              }
              ok();
            } else if (command.startsWith("UID STORE ")) {
              const uids =
                /^UID STORE ([\d,]+)/
                  .exec(command)?.[1]
                  .split(",")
                  .map(Number) ?? [];
              for (const mail of mails)
                if (uids.includes(mail.uid)) mail.seen = true;
              ok();
            } else if (command === "LOGOUT") {
              socket.write(`* BYE fixture logout\r\n${tag} OK logout\r\n`);
              socket.end();
            } else if (["NOOP", "CLOSE", "UNSELECT"].includes(command)) ok();
            else socket.write(`${tag} BAD Unsupported fixture command\r\n`);
          }
        });
      },
    );
    await new Promise<void>((done) => imap.listen(0, "127.0.0.1", done));
    t.after(async () => {
      for (const socket of sockets) socket.destroy();
      await new Promise<void>((done) => imap.close(() => done()));
    });

    const values = new Map<string, unknown>();
    const artifacts: unknown[] = [];
    const validAnalysis = {
      summary: "固定响应：待确认面试安排",
      items: [{ title: "确认面试时间", dueAt: null, sourceUid: 2 }],
    };
    let generatedText = JSON.stringify(validAnalysis);
    const generationRequests: { configId: string; prompt: string }[] = [];
    const host = createHTTPServer(async (request, response) => {
      if (request.headers.authorization !== "Bearer fixture-host-token") {
        response.writeHead(401);
        response.end();
        return;
      }
      const buffers: Buffer[] = [];
      for await (const part of request) buffers.push(Buffer.from(part));
      const value = buffers.length
        ? JSON.parse(Buffer.concat(buffers).toString())
        : undefined;
      const path = request.url ?? "";
      let result: unknown;
      if (path.startsWith("/internal/plugin/kv/")) {
        const key = decodeURIComponent(
          path.slice("/internal/plugin/kv/".length),
        );
        if (request.method === "PUT") {
          values.set(key, value);
          result = { ok: true };
        } else result = { value: values.get(key) ?? null };
      } else if (path === "/internal/plugin/artifacts") {
        artifacts.push(value);
        result = { id: "artifact-" + artifacts.length };
      } else if (path === "/internal/plugin/generate") {
        generationRequests.push(value);
        result = { text: generatedText };
      } else {
        response.writeHead(404);
        response.end();
        return;
      }
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify(result));
    });
    await new Promise<void>((done) => host.listen(0, "127.0.0.1", done));
    t.after(async () => {
      host.closeAllConnections();
      await new Promise<void>((done) => host.close(() => done()));
    });
    const transport = new StdioClientTransport({
      command: process.execPath,
      args: [join(dir, "plugin.cjs")],
      cwd: dir,
      env: {
        ...Object.fromEntries(
          Object.entries(process.env).filter(
            (entry): entry is [string, string] => typeof entry[1] === "string",
          ),
        ),
        NODE_EXTRA_CA_CERTS: certificate,
        SECRETARY_HOST_URL: `http://127.0.0.1:${(host.address() as { port: number }).port}`,
        SECRETARY_HOST_TOKEN: "fixture-host-token",
        SECRETARY_PLUGIN_CONFIG: JSON.stringify({
          host: "127.0.0.1",
          port: (imap.address() as { port: number }).port,
          username: "bot@example.test",
          password: "fixture-password",
          summaryConfigId: "summary-fixture",
        }),
      },
      stderr: "pipe",
    });
    const client = new Client({ name: "mail-protocol-test", version: "1.0.0" });
    t.after(() => client.close());
    await client.connect(transport);
    const call = async (
      name: string,
      operation: string,
      args: Record<string, unknown> = {},
    ) => {
      const result = await client.callTool({
        name,
        arguments: args,
        _meta: { "secretary/operationId": operation },
      });
      assert.notEqual(result.isError, true, JSON.stringify(result));
      return result.structuredContent as Record<string, any>;
    };
    const health = await call("test_connection", "health");
    assert.equal(health.connected, true);
    assert.equal(health.messageCount, 1);
    assert.equal(health.unreadCount, 1);
    const baseline = await call("watch", "baseline");
    assert.equal(baseline.changed, false);
    assert.equal(baseline.cursor.uid, 1);
    assert.equal(artifacts.length, 0);
    mails.push({
      uid: 2,
      subject: "interview-invitation",
      text: "明天下午讨论面试安排。",
      seen: false,
    });
    const changed = await call("watch", "new-mail");
    assert.equal(changed.changed, true);
    assert.equal(changed.messages[0].uid, 2);
    assert.match(changed.messages[0].from, /alice@example.test/);
    assert.match(changed.messages[0].text, /面试安排/);
    assert.equal(changed.messages[0].seen, false);
    assert.match(changed.notificationText, /interview-invitation/);
    const beforeReplay = commands.length;
    assert.deepEqual(await call("watch", "new-mail"), changed);
    assert.equal(
      commands.length,
      beforeReplay,
      "replay opened an unnecessary IMAP connection",
    );
    assert.equal((await call("watch", "quiet-after-new")).changed, false);
    assert.equal(artifacts.length, 1);
    mails.push({
      uid: 3,
      subject: "second-arrival",
      text: "A separate later delivery.",
      seen: false,
    });
    const second = await call("watch", "second-mail");
    assert.deepEqual(
      second.messages.map((mail: Mail) => mail.uid),
      [3],
    );
    assert.equal((await call("watch", "second-quiet")).changed, false);
    assert.equal(artifacts.length, 2);
    await call("mark_read", "mark", { uids: [2], uidValidity: "42" });
    assert.equal(mails[1].seen, true);
    const stale = await client.callTool({
      name: "mark_read",
      arguments: { uids: [3], uidValidity: "old" },
      _meta: { "secretary/operationId": "stale-mailbox" },
    });
    assert.equal(stale.isError, true);
    assert.equal(mails[2].seen, false);
    const searched = await call("search", "search", {
      query: "interview",
      limit: 1,
    });
    assert.equal(searched.messages[0].uid, 2);
    assert.equal(searched.messages[0].seen, true);
    assert.ok(
      commands.some(
        (command) =>
          command.startsWith("UID SEARCH") && command.includes("2:*"),
      ),
    );
    assert.ok(
      commands.some((command) => command.includes("BODY.PEEK[]")),
      "reading mail must not implicitly mark it seen",
    );
    const summarized = await call("summarize", "summary");
    assert.equal(summarized.changed, true);
    assert.deepEqual(summarized.analysis, validAnalysis);
    assert.equal(summarized.artifactId, "artifact-3");
    assert.match(summarized.notificationText, /待确认面试安排/);
    assert.deepEqual(
      (artifacts[2] as { data: { analysis: unknown } }).data.analysis,
      validAnalysis,
    );
    assert.equal(generationRequests[0].configId, "summary-fixture");
    assert.match(generationRequests[0].prompt, /面试安排/);
    assert.match(generationRequests[0].prompt, /"uid":2/);
    for (const [index, invalid] of [
      "plain prose",
      "null",
      "[]",
      "{}",
      JSON.stringify({
        summary: "摘要",
        items: [{ title: "事项", dueAt: null, sourceUid: 99 }],
      }),
      JSON.stringify({
        summary: "摘要",
        items: [{ title: "事项", dueAt: "2026-02-30T15:00:00Z", sourceUid: 2 }],
      }),
    ].entries()) {
      generatedText = invalid;
      const rejected = await client.callTool({
        name: "summarize",
        arguments: {},
        _meta: { "secretary/operationId": "invalid-summary-" + index },
      });
      assert.equal(
        rejected.isError,
        true,
        `invalid output ${index} was accepted`,
      );
      assert.match(JSON.stringify(rejected.content), /未保存成功分析结果/);
      assert.equal(
        artifacts.length,
        3,
        "invalid analysis was saved as successful artifact",
      );
    }
    // UID 2 exists in this mailbox, but limit=1 only fetches UID 3. An item
    // referencing mail outside the actual model input must also be rejected.
    generatedText = JSON.stringify(validAnalysis);
    const unfetched = await client.callTool({
      name: "summarize",
      arguments: { limit: 1 },
      _meta: { "secretary/operationId": "unfetched-summary-source" },
    });
    assert.equal(unfetched.isError, true);
    assert.match(JSON.stringify(unfetched.content), /sourceUid/);
    assert.equal(artifacts.length, 3);
    for (const mail of mails) mail.seen = true;
    const requestsBeforeEmpty = generationRequests.length;
    const empty = await call("summarize", "empty-summary", {
      unreadOnly: true,
    });
    assert.equal(empty.changed, false);
    assert.deepEqual(empty.analysis.items, []);
    assert.equal(generationRequests.length, requestsBeforeEmpty);
    assert.equal(artifacts.length, 3);
  },
);
