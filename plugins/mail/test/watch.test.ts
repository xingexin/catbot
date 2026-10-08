import test from "node:test";
import assert from "node:assert/strict";
import type { JSONObject, ToolContext } from "@secretary/plugin-sdk";
import {
  incrementalMail,
  notificationText,
  type WatchMailbox,
} from "../src/watch.js";

function harness() {
  const values = new Map<string, unknown>();
  const artifacts: unknown[] = [];
  let connected = 0;
  let failure: "before-write" | "after-write" | undefined;
  const mailbox: WatchMailbox = {
    folder: "INBOX",
    uidValidity: "42",
    uidNext: 4,
    searchAfter: async () => [1, 2, 3],
    read: async (uids) =>
      uids.map((uid) => ({
        uid,
        subject: `邮件 ${uid}`,
        from: "Alice <alice@example.test>",
      })),
  };
  const host = {
    get: async (key: string) => structuredClone(values.get(key) ?? null),
    set: async (key: string, value: unknown) => {
      if (key.startsWith("incremental:v1:") && failure === "before-write") {
        failure = undefined;
        throw new Error("database write failed");
      }
      values.set(key, structuredClone(value));
      if (key.startsWith("incremental:v1:") && failure === "after-write") {
        failure = undefined;
        throw new Error("response lost after commit");
      }
    },
    save: async (_name: string, value: unknown) => {
      artifacts.push(structuredClone(value));
      return { id: "mail-artifact-" + artifacts.length };
    },
  };
  const ctx = {
    signal: new AbortController().signal,
    config: {
      host: "imap.example.test",
      username: "alice@example.test",
      port: 993,
    },
    host,
    operationId: "initial",
  } as unknown as ToolContext;
  const call = (
    operationId: string,
    args: JSONObject = {},
    mode: "watch" | "sync" = "watch",
  ) =>
    incrementalMail(mode, args, { ...ctx, operationId }, async (fn) => {
      connected++;
      return fn(mailbox);
    });
  return {
    values,
    artifacts,
    mailbox,
    ctx,
    call,
    fail: (value: typeof failure) => {
      failure = value;
    },
    connections: () => connected,
  };
}

test("watch baselines existing mail, stays quiet, then returns only new messages", async () => {
  const h = harness();
  const initial = await h.call("baseline");
  assert.equal(initial.initialized, true);
  assert.equal(initial.changed, false);
  assert.equal(initial.notificationText, "");
  assert.equal(initial.cursor.uid, 3);
  assert.equal(h.artifacts.length, 0);
  const quiet = await h.call("quiet");
  assert.equal(quiet.changed, false);
  h.mailbox.uidNext = 7;
  h.mailbox.searchAfter = async () => [6, 3, 5, 4, 4];
  const batch = await h.call("batch", { limit: 2 });
  assert.deepEqual(
    batch.messages.map((m) => m.uid),
    [4, 5],
  );
  assert.equal(batch.hasMore, true);
  assert.equal(batch.cursor.uid, 5);
  assert.match(batch.notificationText, /收到 2 封新邮件/);
  assert.match(batch.notificationText, /Alice/);
  assert.equal(batch.artifactId, "mail-artifact-1");
  const next = await h.call("next", { limit: 2 });
  assert.deepEqual(
    next.messages.map((m) => m.uid),
    [6],
  );
  assert.equal(next.hasMore, false);
});

test("committed batch survives lost response and replays without a mailbox connection", async () => {
  const h = harness();
  h.fail("after-write");
  await assert.rejects(
    h.call("delivery", { includeExisting: true }),
    /response lost/,
  );
  assert.equal(h.connections(), 1);
  const replay = await h.call("delivery", { includeExisting: true });
  assert.equal(h.connections(), 1);
  assert.deepEqual(
    replay.messages.map((m) => m.uid),
    [1, 2, 3],
  );
  assert.equal(h.artifacts.length, 1);
  await h.call("later-poll");
  const older = await h.call("delivery", { includeExisting: true });
  assert.deepEqual(older, replay);
  assert.equal(h.connections(), 2);
});

test("failed cursor write or mailbox read never consumes a notification batch", async () => {
  const h = harness();
  h.fail("before-write");
  await assert.rejects(
    h.call("failed", { includeExisting: true }),
    /database write/,
  );
  const next = await h.call("different-operation", { includeExisting: true });
  assert.deepEqual(
    next.messages.map((m) => m.uid),
    [1, 2, 3],
  );
  h.mailbox.searchAfter = async () => [4];
  const reader = h.mailbox.read;
  h.mailbox.read = async () => {
    throw new Error("socket disconnected");
  };
  await assert.rejects(h.call("read-failure"), /socket disconnected/);
  h.mailbox.read = reader;
  const retried = await h.call("read-failure");
  assert.deepEqual(
    retried.messages.map((m) => m.uid),
    [4],
  );
});

test("UIDVALIDITY change quietly establishes a new baseline by default", async () => {
  const h = harness();
  await h.call("start", { includeExisting: true });
  h.mailbox.uidValidity = "43";
  h.mailbox.uidNext = 8;
  const reset = await h.call("reset");
  assert.equal(reset.reset, true);
  assert.equal(reset.initialized, false);
  assert.equal(reset.changed, false);
  assert.equal(reset.cursor.uid, 7);
  h.mailbox.searchAfter = async () => [7, 8];
  const fresh = await h.call("fresh");
  assert.deepEqual(
    fresh.messages.map((m) => m.uid),
    [8],
  );
});

test("independent monitors, accounts, folders and ports do not consume one another's cursor", async () => {
  const h = harness();
  await h.call("main");
  for (const [index, args] of [
    { monitorId: "other" },
    { folder: "Important" },
  ].entries()) {
    const result = await h.call("scope-" + index, {
      ...args,
      includeExisting: true,
    });
    assert.deepEqual(
      result.messages.map((m) => m.uid),
      [1, 2, 3],
    );
  }
  h.ctx.config.username = "other@example.test";
  assert.equal(
    (await h.call("other-user", { includeExisting: true })).messages.length,
    3,
  );
  h.ctx.config.port = 1993;
  assert.equal(
    (await h.call("other-port", { includeExisting: true })).messages.length,
    3,
  );
});

test("sync retains upgrade cursor and uses the same durable replay boundary", async () => {
  const h = harness();
  h.values.set("cursor:imap.example.test:alice@example.test:INBOX", {
    uidValidity: "42",
    uid: 2,
  });
  const result = await h.call("sync-1", {}, "sync");
  assert.deepEqual(
    result.messages.map((m) => m.uid),
    [3],
  );
  assert.equal(result.initialized, false);
  assert.deepEqual(await h.call("sync-1", {}, "sync"), result);
  assert.equal(h.connections(), 1);
  assert.equal((await h.call("separate-watch")).initialized, true);
});

test("operation IDs and arguments are checked before state mutation", async () => {
  const h = harness();
  await assert.rejects(h.call(""), /stable operation ID/);
  await assert.rejects(h.call("invalid", { limit: 0 }), /between 1 and 50/);
  await h.call("start");
  await assert.rejects(
    h.call("start", { includeExisting: true }),
    /different mail arguments/,
  );
  assert.equal(h.connections(), 1);
  h.mailbox.uidNext = 0;
  await assert.rejects(h.call("bad-server"), /UIDVALIDITY and UIDNEXT/);
});

test("large batches produce a bounded readable reminder with a saved-list notice", () => {
  const text = notificationText(
    "INBOX",
    Array.from({ length: 50 }, (_, uid) => ({
      uid,
      subject: "很长的邮件主题".repeat(100),
      from: "发件人".repeat(100),
    })),
  );
  assert.ok(Array.from(text).length < 1600);
  assert.match(text, /收到 50 封/);
  assert.match(text, /另有 42 封/);
  assert.equal(notificationText("INBOX", []), "");
});
