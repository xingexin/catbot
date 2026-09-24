import test from "node:test";
import assert from "node:assert/strict";
import { frameTimes, validateVideo } from "../src/core.js";
test("video limits reject oversized and overlong input before model calls", () => {
  assert.throws(() => validateVideo(101 * 1024 * 1024, 20), /size/);
  assert.throws(() => validateVideo(10, 1801), /duration/);
  assert.throws(() => validateVideo(10, NaN), /duration/);
  assert.doesNotThrow(() => validateVideo(100 * 1024 * 1024, 1800));
  assert.doesNotThrow(() => validateVideo(120 * 1024 * 1024, 1900, 200, 60));
});
test("sample times are bounded and never beyond video end", () => {
  assert.deepEqual(frameTimes(250), [0, 120, 240]);
  assert.equal(frameTimes(7200, 30).length, 20);
  assert.throws(() => frameTimes(0));
});
