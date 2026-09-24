// Deterministic protocol fixture. This is never a real model compatibility test.
import { createServer } from "node:http";
const server = createServer(async (req, res) => {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  if (req.url === "/health") {
    res.end("ok");
    return;
  }
  if (req.url === "/v1/audio/transcriptions") {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ text: "这是五秒钟的验收视频，画面为绿色。" }));
    return;
  }
  let input;
  try {
    input = JSON.parse(Buffer.concat(chunks).toString());
  } catch {
    res.writeHead(400);
    res.end();
    return;
  }
  const raw = JSON.stringify(input),
    vision = raw.includes("data:image/");
  const result = vision
    ? JSON.stringify({
        summary: "本地协议验收：绿色测试画面。",
        timeline: [{ timestampSeconds: 0, description: "绿色画面" }],
        items: [],
      })
    : "工具调用已完成（本地协议验收端点）。";
  const observation =
    raw.includes('"role":"tool"') ||
    raw.includes('"type":"tool_result"') ||
    raw.includes('"type":"function_call_output"');
  const tools = input.tools ?? [];
  const echo = tools.find(
    (t) => (t.function?.name ?? t.name) === "example__echo",
  );
  const useTool = echo && !observation;
  let body;
  if (req.url === "/v1/responses")
    body = {
      id: "fixture",
      status: "completed",
      usage: { input_tokens: 12, output_tokens: 8 },
      output: useTool
        ? [
            {
              type: "function_call",
              call_id: "fixture-call",
              name: "example__echo",
              arguments: JSON.stringify({ text: "hello" }),
            },
          ]
        : [
            {
              type: "message",
              role: "assistant",
              content: [{ type: "output_text", text: result }],
            },
          ],
    };
  else if (req.url === "/v1/messages")
    body = {
      type: "message",
      role: "assistant",
      stop_reason: useTool ? "tool_use" : "end_turn",
      content: useTool
        ? [
            {
              type: "tool_use",
              id: "fixture-call",
              name: "example__echo",
              input: { text: "hello" },
            },
          ]
        : [{ type: "text", text: result }],
      usage: { input_tokens: 12, output_tokens: 8 },
    };
  else
    body = {
      id: "fixture",
      choices: [
        {
          index: 0,
          message: useTool
            ? {
                role: "assistant",
                content: null,
                tool_calls: [
                  {
                    id: "fixture-call",
                    type: "function",
                    function: {
                      name: "example__echo",
                      arguments: JSON.stringify({ text: "hello" }),
                    },
                  },
                ],
              }
            : { role: "assistant", content: result },
          finish_reason: useTool ? "tool_calls" : "stop",
        },
      ],
      usage: { prompt_tokens: 12, completion_tokens: 8 },
    };
  res.setHeader("Content-Type", "application/json");
  res.end(JSON.stringify(body));
});
server.listen(9090, "0.0.0.0", () =>
  process.stdout.write("Local protocol fixture ready\n"),
);
