import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

test(
  "runtime reserves IDs atomically and never journals API keys",
  { timeout: 15000 },
  async () => {
    const reservation = createServer();
    await new Promise<void>((done) => reservation.listen(0, "127.0.0.1", done));
    const port = (reservation.address() as { port: number }).port;
    await new Promise<void>((done) => reservation.close(() => done()));
    const root = await mkdtemp(join(tmpdir(), "secretary-runtime-test-"));
    const child = spawn(
      process.execPath,
      ["--import", "tsx", "src/server.ts"],
      {
        cwd: resolve("."),
        env: {
          PATH: process.env.PATH,
          RUNTIME_HOST: "127.0.0.1",
          RUNTIME_PORT: String(port),
          RUNTIME_TOKEN: "fixture-token",
          RUNTIME_DATA_DIR: root,
        },
        stdio: ["ignore", "pipe", "pipe"],
      },
    );
    try {
      await new Promise<void>((done, reject) => {
        const timer = setTimeout(
          () => reject(new Error("runtime startup timed out")),
          7000,
        );
        child.stdout.on("data", (data) => {
          if (String(data).includes("ready")) {
            clearTimeout(timer);
            done();
          }
        });
        child.once("exit", (code) => {
          clearTimeout(timer);
          reject(new Error("runtime exited: " + code));
        });
      });
      const url = "http://127.0.0.1:" + port;
      assert.equal((await fetch(url + "/health")).status, 401);
      for (const invalid of [null, {}, { config: {}, persona: {} }]) {
        const rejected = await fetch(url + "/runs", {
          method: "POST",
          headers: {
            Authorization: "Bearer fixture-token",
            "Content-Type": "application/json",
          },
          body: JSON.stringify(invalid),
        });
        assert.equal(rejected.status, 400);
        await rejected.text();
      }
      const health = await fetch(url + "/health", {
        headers: { Authorization: "Bearer fixture-token" },
      });
      assert.equal(health.status, 200);
      assert.deepEqual(await health.json(), {
        status: "ok",
        providers: ["codebuddy", "claude", "codex"],
        active: 0,
        liveVerification: "not_performed",
      });
      const input = {
        runId: "one-run",
        sessionId: "one-session",
        config: {
          provider: "unsupported-fixture",
          model: "fixture",
          maxSteps: 1,
        },
        persona: { systemPrompt: "test" },
        history: [],
        prompt: "DO_NOT_JOURNAL_REQUEST",
        apiKey: "DO_NOT_STORE_KEY",
        gatewayToken: "DO_NOT_STORE_GATEWAY",
        gatewayUrl: "http://127.0.0.1:1",
      };
      const send = () =>
        fetch(url + "/runs", {
          method: "POST",
          headers: {
            Authorization: "Bearer fixture-token",
            "Content-Type": "application/json",
          },
          body: JSON.stringify(input),
        });
      const responses = await Promise.all(Array.from({ length: 12 }, send));
      assert.equal(responses.filter((r) => r.status === 200).length, 1);
      await Promise.all(responses.map((r) => r.text()));
      const repeated = await send();
      assert.equal(repeated.status, 409);
      await repeated.text();
      const journal = await readFile(join(root, "runs/one-run.json"), "utf8");
      assert.doesNotMatch(journal, /DO_NOT_STORE|DO_NOT_JOURNAL_REQUEST/);
      assert.equal(JSON.parse(journal).status, "failed");
    } finally {
      child.kill("SIGTERM");
      await new Promise<void>((done) => {
        if (child.exitCode !== null) return done();
        const timer = setTimeout(() => {
          child.kill("SIGKILL");
          done();
        }, 2000);
        child.once("exit", () => {
          clearTimeout(timer);
          done();
        });
      });
      await rm(root, { recursive: true, force: true });
    }
  },
);
