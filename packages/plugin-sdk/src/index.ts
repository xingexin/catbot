import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import { Ajv } from "ajv";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

export type JSONObject = Record<string, any>;
export interface Manifest {
  id: string;
  name: string;
  version: string;
  tools: Array<{
    name: string;
    description: string;
    inputSchema: JSONObject;
    outputSchema?: JSONObject;
  }>;
}
export interface ToolContext {
  signal: AbortSignal;
  host: Host;
  config: JSONObject;
  operationId: string;
}
export type Handler = (args: JSONObject, ctx: ToolContext) => Promise<unknown>;
export class Host {
  constructor(
    private base = process.env.SECRETARY_HOST_URL ?? "",
    private token = process.env.SECRETARY_HOST_TOKEN ?? "",
  ) {}
  async request(
    path: string,
    init: RequestInit = {},
    signal?: AbortSignal,
  ): Promise<Response> {
    const response = await fetch(this.base + "/internal/plugin" + path, {
      ...init,
      signal,
      headers: { ...init.headers, Authorization: "Bearer " + this.token },
    });
    if (!response.ok) {
      let message = "Host HTTP " + response.status;
      try {
        message =
          ((await response.json()) as { error?: string }).error ?? message;
      } catch {}
      throw new Error(message);
    }
    return response;
  }
  async json(path: string, body?: unknown, signal?: AbortSignal): Promise<any> {
    return (
      await this.request(
        path,
        body === undefined
          ? {}
          : {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify(body),
            },
        signal,
      )
    ).json();
  }
  async get(key: string, signal?: AbortSignal): Promise<any> {
    return (
      await this.json("/kv/" + encodeURIComponent(key), undefined, signal)
    ).value;
  }
  async set(key: string, value: unknown, signal?: AbortSignal): Promise<void> {
    await this.request(
      "/kv/" + encodeURIComponent(key),
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(value),
      },
      signal,
    );
  }
  async save(name: string, data: unknown, signal?: AbortSignal): Promise<any> {
    return this.json("/artifacts", { name, data }, signal);
  }
  async generate(
    configId: string,
    prompt: string,
    images: string[] = [],
    signal?: AbortSignal,
  ): Promise<{ text: string; usage?: unknown }> {
    return this.json("/generate", { configId, prompt, images }, signal);
  }
  async transcribe(
    configId: string,
    artifactId: string,
    model: string,
    signal?: AbortSignal,
  ): Promise<{ text: string }> {
    return this.json("/transcribe", { configId, artifactId, model }, signal);
  }
  async upload(
    name: string,
    data: Uint8Array,
    mime: string,
    signal?: AbortSignal,
  ): Promise<any> {
    const form = new FormData();
    form.append("file", new Blob([Buffer.from(data)], { type: mime }), name);
    return (
      await this.request("/files", { method: "POST", body: form }, signal)
    ).json();
  }
  async download(id: string, signal?: AbortSignal): Promise<Response> {
    return this.request("/files/" + encodeURIComponent(id), {}, signal);
  }
  async task(
    task: JSONObject,
    operationId: string,
    signal?: AbortSignal,
  ): Promise<any> {
    return this.json("/tasks", { ...task, operationId }, signal);
  }
}

export function normalizeResult(value: unknown): JSONObject {
  if (value !== null && typeof value === "object" && !Array.isArray(value))
    return value as JSONObject;
  return { value };
}
export async function serve(handlers: Record<string, Handler>): Promise<void> {
  const manifest = JSON.parse(
    await readFile(resolve("plugin.json"), "utf8"),
  ) as Manifest;
  const config = JSON.parse(
    process.env.SECRETARY_PLUGIN_CONFIG ?? "{}",
  ) as JSONObject;
  const ajv = new Ajv({ strict: false, allErrors: true });
  const schemas = new Map(
    manifest.tools.map((t) => [t.name, ajv.compile(t.inputSchema)]),
  );
  const outputs = new Map(
    manifest.tools
      .filter((t) => t.outputSchema)
      .map((t) => [t.name, ajv.compile(t.outputSchema!)]),
  );
  for (const t of manifest.tools)
    if (!handlers[t.name]) throw new Error("Missing tool handler: " + t.name);
  const host = new Host();
  const server = new Server(
    { name: manifest.id, version: manifest.version },
    { capabilities: { tools: {} } },
  );
  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: manifest.tools.map((t) => ({
      name: t.name,
      description: t.description,
      inputSchema: t.inputSchema as any,
      ...(t.outputSchema ? { outputSchema: t.outputSchema } : {}),
    })),
  }));
  server.setRequestHandler(CallToolRequestSchema, async (request, extra) => {
    const validate = schemas.get(request.params.name);
    const args = request.params.arguments ?? {};
    if (!validate || !handlers[request.params.name])
      throw new Error("Unknown tool");
    if (!validate(args))
      return {
        isError: true,
        content: [
          {
            type: "text" as const,
            text: "Invalid arguments: " + ajv.errorsText(validate.errors),
          },
        ],
      };
    try {
      const operationId = String(
        request.params._meta?.["secretary/operationId"] ?? "",
      );
      const value = normalizeResult(
        await handlers[request.params.name](args, {
          config,
          host,
          signal: extra.signal,
          operationId,
        }),
      );
      const output = outputs.get(request.params.name);
      if (output && !output(value)) throw new Error("Invalid tool output");
      let text = JSON.stringify(value);
      if (Buffer.byteLength(text) > 512 * 1024)
        throw new Error(
          "Tool result exceeds 512 KB; save an artifact and return its ID",
        );
      // Password configuration is never included in protocol output.
      for (const [name, secret] of Object.entries(config))
        if (
          /password|secret|token|key/i.test(name) &&
          typeof secret === "string" &&
          secret
        )
          text = text.split(secret).join("[REDACTED]");
      return {
        content: [{ type: "text" as const, text }],
        structuredContent: JSON.parse(text),
      };
    } catch (error) {
      let message = error instanceof Error ? error.message : "Tool failed";
      for (const [name, secret] of Object.entries(config))
        if (
          /password|secret|token|key/i.test(name) &&
          typeof secret === "string" &&
          secret
        )
          message = message.split(secret).join("[REDACTED]");
      return {
        isError: true,
        content: [{ type: "text" as const, text: message }],
      };
    }
  });
  await server.connect(new StdioServerTransport());
}
