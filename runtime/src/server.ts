import {
  createServer,
  type IncomingMessage,
  type ServerResponse,
} from "node:http";
import { mkdir, open, writeFile, rename } from "node:fs/promises";
import { resolve, join } from "node:path";
import { timingSafeEqual } from "node:crypto";
import { execute, type RunRequest, type RunEvent } from "./adapters.js";

const root = resolve(process.env.RUNTIME_DATA_DIR ?? "../data/sdk");
const token = process.env.RUNTIME_TOKEN;
if (!token) throw new Error("RUNTIME_TOKEN is required");
await mkdir(join(root, "runs"), { recursive: true, mode: 0o700 });
const active = new Map<string, AbortController>();
function json(res: ServerResponse, status: number, data: unknown) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(data));
}
function authorized(req: IncomingMessage): boolean {
  const got = Buffer.from(req.headers.authorization ?? ""),
    want = Buffer.from("Bearer " + token);
  return got.length === want.length && timingSafeEqual(got, want);
}
async function readBody(req: IncomingMessage): Promise<RunRequest> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 2 * 1024 * 1024) throw new Error("Request too large");
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as RunRequest;
}
const server = createServer(async (req, res) => {
  if (!authorized(req)) {
    json(res, 401, { error: "Unauthorized" });
    return;
  }
  if (req.method === "GET" && req.url === "/health") {
    json(res, 200, {
      status: "ok",
      providers: ["codebuddy", "claude", "codex"],
      active: active.size,
      liveVerification: "not_performed",
    });
    return;
  }
  if (req.method !== "POST" || req.url !== "/runs") {
    json(res, 404, { error: "Not found" });
    return;
  }
  let input: RunRequest;
  try {
    input = await readBody(req);
  } catch {
    json(res, 400, { error: "Invalid request" });
    return;
  }
  if (
    !input.config ||
    !input.persona ||
    !/^[a-zA-Z0-9:_-]{1,240}$/.test(input.runId)
  ) {
    json(res, 400, { error: "Invalid run" });
    return;
  }
  if (active.size >= 8) {
    json(res, 429, { error: "Runtime busy" });
    return;
  }
  if (active.has(input.runId)) {
    json(res, 409, { error: "Run already active" });
    return;
  }
  const controller = new AbortController();
  active.set(input.runId, controller);
  const recordPath = join(root, "runs", input.runId + ".json");
  try {
    const reservation = await open(recordPath, "wx", 0o600);
    await reservation.close();
  } catch (e) {
    active.delete(input.runId);
    json(res, (e as NodeJS.ErrnoException).code === "EEXIST" ? 409 : 500, {
      error: "Cannot reserve run; inspect prior execution before retry",
    });
    return;
  }
  const deadline = setTimeout(
    () => controller.abort(),
    Math.min(
      3600,
      Number((input.config as unknown as { timeoutSec?: number }).timeoutSec) ||
        180,
    ) * 1000,
  );
  let status = "running";
  const events: RunEvent[] = [];
  const persist = async () => {
    const temp = recordPath + ".tmp";
    await writeFile(temp, JSON.stringify({ id: input.runId, status, events }), {
      mode: 0o600,
    });
    await rename(temp, recordPath);
  };
  try {
    await persist();
  } catch {
    clearTimeout(deadline);
    active.delete(input.runId);
    json(res, 500, { error: "Cannot persist run journal" });
    return;
  }
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache",
  });
  res.flushHeaders();
  res.on("close", () => {
    if (status === "running") controller.abort();
  });
  const emit = async (e: RunEvent) => {
    let safe = JSON.stringify(e);
    for (const value of [input.apiKey, input.gatewayToken])
      if (value) safe = safe.split(value).join("[REDACTED]");
    const sanitized = JSON.parse(safe) as RunEvent;
    events.push(sanitized);
    if (events.length > 10000) throw new Error("Event limit reached");
    if (e.type === "completed") status = "completed";
    await persist();
    if (!res.destroyed)
      res.write("data: " + JSON.stringify(sanitized) + "\n\n");
  };
  try {
    await execute(input, root, controller, emit);
  } catch (error) {
    status = controller.signal.aborted ? "interrupted" : "failed";
    try {
      await emit({
        type: "error",
        data: {
          message:
            error instanceof Error ? error.message : "SDK execution failed",
        },
      });
    } catch {
      process.stderr.write("Run journal write failed\n");
    }
  } finally {
    clearTimeout(deadline);
    active.delete(input.runId);
    try {
      await persist();
    } catch {
      process.stderr.write("Cannot finalize run journal\n");
    }
    res.end();
  }
});
server.requestTimeout = 0;
server.listen(
  Number(process.env.RUNTIME_PORT ?? 8091),
  process.env.RUNTIME_HOST ?? "127.0.0.1",
  () => process.stdout.write("SDK execution service ready\n"),
);
for (const signal of ["SIGTERM", "SIGINT"] as const)
  process.on(signal, () => {
    for (const c of active.values()) c.abort();
    server.close();
  });
