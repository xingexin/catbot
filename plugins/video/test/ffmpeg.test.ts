import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseVideo, type VideoHost } from "../src/parse.js";

const available =
  spawnSync("ffmpeg", ["-version"], { stdio: "ignore" }).status === 0;
if (process.env.REQUIRE_FFMPEG === "1")
  assert.ok(available, "FFmpeg must be installed for the media acceptance check");
test(
  "real FFmpeg pipeline extracts playable audio and JPEG frames; model responses are local fixtures",
  { skip: !available, timeout: 30000 },
  async () => {
    const temp = await mkdtemp(join(tmpdir(), "secretary-media-test-"));
    try {
      const file = join(temp, "fixture.mp4");
      execFileSync(
        "ffmpeg",
        [
          "-nostdin",
          "-v",
          "error",
          "-f",
          "lavfi",
          "-i",
          "color=c=blue:s=160x120:d=2",
          "-f",
          "lavfi",
          "-i",
          "sine=frequency=440:duration=2",
          "-shortest",
          "-c:v",
          "mpeg4",
          "-c:a",
          "aac",
          file,
        ],
        { timeout: 15000 },
      );
      const uploads: Buffer[] = [];
      let saved: any;
      const host: VideoHost = {
        download: async () => new Response(await readFile(file)),
        upload: async (_name, data) => {
          uploads.push(Buffer.from(data));
          return { id: "audio" };
        },
        transcribe: async () => ({
          text: "本地转写测试替身，没有调用真实转写模型",
        }),
        generate: async (_config, _prompt, images = []) => {
          assert.equal(images.length, 1);
          const frame = Buffer.from(images[0].split(",")[1], "base64");
          assert.equal(frame.readUInt16BE(0), 0xffd8);
          return {
            text: JSON.stringify({
              summary: "本地视觉模型测试替身",
              timeline: [{ timestampSeconds: 0, description: "测试画面" }],
              items: [],
            }),
          };
        },
        save: async (_name, data) => {
          saved = data;
          return { id: "result" };
        },
      };
      const result = await parseVideo("source", {
        host,
        config: {
          transcriptionConfigId: "fixture",
          transcriptionModel: "fixture",
          visionConfigId: "fixture",
        },
        signal: new AbortController().signal,
      });
      assert.equal(result.artifactId, "result");
      assert.equal(saved.complete, true);
      assert.equal(uploads.length, 1);
      const audio = join(temp, "output.mp3");
      const { writeFile } = await import("node:fs/promises");
      await writeFile(audio, uploads[0]);
      const probe = JSON.parse(
        execFileSync(
          "ffprobe",
          ["-v", "error", "-show_streams", "-of", "json", audio],
          { encoding: "utf8" },
        ),
      );
      assert.equal(probe.streams[0].codec_type, "audio");
      assert.equal(probe.streams[0].sample_rate, "16000");
    } finally {
      await rm(temp, { recursive: true, force: true });
    }
  },
);
