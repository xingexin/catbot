import test from "node:test";
import assert from "node:assert/strict";
import { readFile, writeFile, access } from "node:fs/promises";
import { dirname } from "node:path";
import { parseVideo, type Command, type VideoHost } from "../src/parse.js";

const analysis = {
  summary: "完整摘要",
  timeline: [{ timestampSeconds: 0, description: "测试画面" }],
  items: [],
};
function fixture(duration = 10) {
  const saved: any[] = [];
  const calls: string[] = [];
  const generated: { prompt: string; images: string[] }[] = [];
  let inputPath = "";
  const command: Command = async (name, args, options) => {
    assert.ok(options.timeout > 0);
    calls.push(name);
    if (name === "ffprobe") {
      inputPath = args.at(-1)!;
      assert.equal((await readFile(inputPath)).toString(), "video-content");
      return {
        stdout: JSON.stringify({
          format: { duration },
          streams: [{ codec_type: "video" }, { codec_type: "audio" }],
        }),
      };
    }
    await writeFile(args.at(-1)!, "media-content");
    return { stdout: "" };
  };
  const host: VideoHost = {
    download: async () => new Response("video-content"),
    upload: async (name) => ({ id: name }),
    transcribe: async (_id, artifactId) => ({ text: "音频内容 " + artifactId }),
    generate: async (_id, prompt, images = []) => {
      generated.push({ prompt, images });
      return { text: JSON.stringify(analysis) };
    },
    save: async (name, data) => {
      saved.push({ name, data });
      return { id: "artifact-" + saved.length };
    },
  };
  const context = {
    config: {
      transcriptionConfigId: "audio",
      transcriptionModel: "transcriber",
      visionConfigId: "vision",
      maxMinutes: 120,
      frameInterval: 30,
    },
    host,
    signal: new AbortController().signal,
  };
  return {
    context,
    command,
    saved,
    calls,
    generated,
    temp: () => dirname(inputPath),
  };
}

test("video pipeline transcribes bounded audio segments and saves a validated complete artifact", async () => {
  const f = fixture(2500);
  const result = await parseVideo("source", f.context, f.command);
  assert.equal(result.complete, true);
  assert.equal(f.saved.length, 1);
  assert.equal(f.saved[0].data.transcriptSegments.length, 3);
  assert.equal(f.saved[0].data.transcriptSegments.at(-1).endSeconds, 2500);
  assert.equal(f.generated.at(-1)?.images.length, 20);
  assert.ok(f.saved[0].data.frameTimes.at(-1) >= 2499);
  await assert.rejects(access(f.temp()));
});

test("invalid model output is kept as an incomplete artifact and fails the tool", async () => {
  const f = fixture();
  f.context.host.generate = async () => ({ text: "模型未按结构返回" });
  await assert.rejects(
    parseVideo("source", f.context, f.command),
    /未完成结果 artifact-1/,
  );
  assert.equal(f.saved.length, 1);
  assert.equal(f.saved[0].data.complete, false);
  assert.equal(f.saved[0].data.failedStage, "生成视频摘要");
  assert.equal(f.saved[0].data.rawAnalysis, "模型未按结构返回");
  await assert.rejects(access(f.temp()));
});

test("failed transcription preserves completed segments and never claims completeness", async () => {
  const f = fixture(2500);
  f.context.host.transcribe = async (_id, artifact) => {
    if (artifact === "audio-2.mp3") throw new Error("provider unavailable");
    return { text: "已完成片段" };
  };
  await assert.rejects(
    parseVideo("source", f.context, f.command),
    /转写音频片段 2失败/,
  );
  assert.equal(f.saved[0].data.complete, false);
  assert.equal(f.saved[0].data.transcriptSegments.length, 1);
  assert.equal(f.generated.length, 0);
});

test("oversized input is cancelled before any media or model call", async () => {
  const f = fixture();
  let cancelled = false;
  f.context.host.download = async () =>
    new Response(
      new ReadableStream({
        pull(controller) {
          controller.enqueue(new Uint8Array(1024 * 1024 + 1));
        },
        cancel() {
          cancelled = true;
        },
      }),
    );
  f.context.config = {
    ...f.context.config,
    maxMB: 1,
  } as typeof f.context.config;
  await assert.rejects(parseVideo("source", f.context, f.command), /大小限制/);
  assert.equal(f.calls.length, 0);
  assert.equal(cancelled, true);
});

test("pre-cancelled parsing does not download or make model calls", async () => {
  const f = fixture();
  const controller = new AbortController();
  controller.abort();
  f.context.signal = controller.signal;
  await assert.rejects(parseVideo("source", f.context, f.command), {
    name: "AbortError",
  });
  assert.equal(f.calls.length, 0);
  assert.equal(f.saved.length, 0);
});
