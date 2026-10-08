import type { Sink } from "../contracts.js";

export function sdkText(text: string): string {
  // NUL is invalid in PostgreSQL JSONB. Reject malformed provider output before
  // emitting it, so the run records the provider error instead of a SQL failure.
  if (text.includes("\0"))
    throw new Error("SDK returned malformed text containing NUL characters");
  return text;
}

export async function emitQueryEvents(
  query: AsyncIterable<unknown>,
  emit: Sink,
): Promise<void> {
  let finished = false;
  for await (const raw of query) {
    const message = raw as {
      type: string;
      subtype?: string;
      session_id?: string;
      model?: string;
      tools?: string[];
      mcp_servers?: { name: string; status: string }[];
      event?: { type: string; delta?: { type: string; text?: string } };
      result?: string;
      is_error?: boolean;
      errors?: string[];
      usage?: unknown;
    };
    if (message.type === "system" && message.session_id)
      await emit({ type: "native.session", data: { id: message.session_id } });
    if (message.type === "system" && message.subtype === "init")
      await emit({
        type: "sdk.initialized",
        data: {
          model: message.model,
          tools: message.tools ?? [],
          mcpServers: message.mcp_servers ?? [],
        },
      });
    if (
      message.type === "stream_event" &&
      message.event?.type === "content_block_delta" &&
      message.event.delta?.type === "text_delta"
    ) {
      await emit({
        type: "text.delta",
        data: { text: sdkText(message.event.delta.text ?? "") },
      });
    }
    if (message.type === "result") {
      if (
        message.is_error ||
        (message.subtype && message.subtype !== "success")
      )
        throw new Error(
          message.errors?.join("; ") || "SDK run did not complete successfully",
        );
      finished = true;
      await emit({
        type: "completed",
        data: {
          text: sdkText(message.result ?? ""),
          usage: message.usage ?? {},
        },
      });
    }
  }
  if (!finished) throw new Error("SDK stream ended without a result");
}
