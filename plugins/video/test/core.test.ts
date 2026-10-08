import test from "node:test";
import assert from "node:assert/strict";
import {
  audioSegments,
  frameTimes,
  parseAnalysis,
  validateVideo,
} from "../src/core.js";

test("video limits reject oversized and overlong input before model calls", () => {
  assert.throws(() => validateVideo(101 * 1024 * 1024, 20), /size/);
  assert.throws(() => validateVideo(0, 20), /empty/);
  assert.throws(() => validateVideo(10, 1801), /duration/);
  assert.throws(() => validateVideo(10, NaN), /duration/);
  assert.doesNotThrow(() => validateVideo(100 * 1024 * 1024, 1800));
  assert.doesNotThrow(() => validateVideo(120 * 1024 * 1024, 1900, 200, 60));
});
test("bounded samples cover the entire video instead of discarding its tail", () => {
  assert.deepEqual(frameTimes(250), [0, 120, 240]);
  const times = frameTimes(7200, 30);
  assert.equal(times.length, 20);
  assert.equal(times[0], 0);
  assert.ok(times.at(-1)! >= 7199 && times.at(-1)! < 7200);
  assert.throws(() => frameTimes(0));
  assert.throws(() => frameTimes(50, 0));
  assert.deepEqual(frameTimes(0.01), [0]);
});
test("long audio is split into bounded contiguous ranges", () => {
  assert.deepEqual(audioSegments(2500), [
    { start: 0, duration: 1200 },
    { start: 1200, duration: 1200 },
    { start: 2400, duration: 100 },
  ]);
});
test("malformed or incomplete model analysis cannot be marked complete", () => {
  const valid = {
    summary: "摘要",
    timeline: [
      { timestampSeconds: 10, description: "画面" },
      { timestampSeconds: null, description: "无法精确定位的音频" },
    ],
    items: [{ title: "事项", dueAt: null }],
  };
  assert.deepEqual(
    parseAnalysis("```json\n" + JSON.stringify(valid) + "\n```", 100),
    valid,
  );
  for (const value of [
    "plain prose",
    "null",
    JSON.stringify({ summary: "摘要" }),
    JSON.stringify({
      ...valid,
      timeline: [{ timestampSeconds: 101, description: "超出范围" }],
    }),
    JSON.stringify({
      ...valid,
      items: [{ title: "不确定日期", dueAt: "下周" }],
    }),
  ])
    assert.throws(() => parseAnalysis(value, 100));
});
