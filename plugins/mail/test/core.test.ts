import test from "node:test";
import assert from "node:assert/strict";
import { cursorFor, summaryPrompt } from "../src/core.js";
test("UIDVALIDITY changes reset the cursor", () => {
  assert.deepEqual(cursorFor({ uidValidity: "10", uid: 500 }, "11"), {
    uidValidity: "11",
    uid: 0,
  });
  assert.deepEqual(cursorFor({ uidValidity: "11", uid: 7 }, "11"), {
    uidValidity: "11",
    uid: 7,
  });
});
test("mail extraction asks for sources and preserves unknown dates", () => {
  const prompt = summaryPrompt([{ uid: 4, text: "hello" }]);
  assert.match(prompt, /sourceUid/);
  assert.match(prompt, /null/);
  assert.match(prompt, /"uid":4/);
});
