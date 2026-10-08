import test from "node:test";
import assert from "node:assert/strict";
import { cursorFor, parseAnalysis, summaryPrompt } from "../src/core.js";
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
  assert.match(prompt, /RFC3339/);
  assert.match(prompt, /不得杜撰日期、时间或时区/);
});

test("mail analysis retains typed actionable items and unknown deadlines", () => {
  const value = {
    summary: " 待确认面试安排 ",
    items: [
      { title: " 确认面试时间 ", dueAt: null, sourceUid: 7 },
      { title: "提交材料", dueAt: "2026-10-04T15:00:00+08:00", sourceUid: 8 },
    ],
  };
  assert.deepEqual(
    parseAnalysis("```json\n" + JSON.stringify(value) + "\n```", [7, 8]),
    {
      summary: "待确认面试安排",
      items: [
        { title: "确认面试时间", dueAt: null, sourceUid: 7 },
        value.items[1],
      ],
    },
  );
  assert.deepEqual(
    parseAnalysis('{"summary":"本批邮件没有待办事项","items":[]}', [7]),
    {
      summary: "本批邮件没有待办事项",
      items: [],
    },
  );
});

test("mail analysis rejects malformed JSON and missing summary/item structures", async (t) => {
  for (const [name, input] of [
    ["invalid JSON", "plain prose"],
    ["null", "null"],
    ["array", "[]"],
    ["empty object", "{}"],
    ["blank summary", '{"summary":" ","items":[]}'],
    ["non-string summary", '{"summary":3,"items":[]}'],
    ["missing items", '{"summary":"mail"}'],
    ["items object", '{"summary":"mail","items":{}}'],
  ]) {
    await t.test(name, () =>
      assert.throws(() => parseAnalysis(input, [7]), /JSON|summary.*items/),
    );
  }
});

test("mail analysis rejects invalid item titles and fabricated source references", async (t) => {
  for (const [name, item, expected] of [
    ["null item", null, /title/],
    ["array item", [], /title/],
    ["missing title", { dueAt: null, sourceUid: 7 }, /title/],
    ["blank title", { title: "  ", dueAt: null, sourceUid: 7 }, /title/],
    ["missing deadline", { title: "事项", sourceUid: 7 }, /dueAt/],
    ["missing source", { title: "事项", dueAt: null }, /sourceUid/],
    [
      "unfetched UID",
      { title: "事项", dueAt: null, sourceUid: 99 },
      /sourceUid/,
    ],
    ["string UID", { title: "事项", dueAt: null, sourceUid: "7" }, /sourceUid/],
    [
      "fractional UID",
      { title: "事项", dueAt: null, sourceUid: 7.5 },
      /sourceUid/,
    ],
  ] as const) {
    await t.test(name, () =>
      assert.throws(
        () =>
          parseAnalysis(
            JSON.stringify({ summary: "摘要", items: [item] }),
            [7, 8],
          ),
        expected,
      ),
    );
  }
});

test("mail deadlines reject invalid calendar dates, missing zones and normalized overflows", async (t) => {
  for (const dueAt of [
    "明天下午",
    "2026-10-04",
    "2026-10-04T15:00:00",
    "2026-10-04 15:00:00Z",
    "2026-02-30T15:00:00Z",
    "2025-02-29T15:00:00Z",
    "1900-02-29T15:00:00Z",
    "2026-13-01T15:00:00Z",
    "2026-00-01T15:00:00Z",
    "2026-10-00T15:00:00Z",
    "2026-10-04T24:00:00Z",
    "2026-10-04T15:60:00Z",
    "2026-10-04T15:00:60Z",
    "2026-10-04T15:00:00+0800",
    "2026-10-04T15:00:00+24:00",
    "2026-10-04T15:00:00+08:60",
  ]) {
    await t.test(dueAt, () =>
      assert.throws(
        () =>
          parseAnalysis(
            JSON.stringify({
              summary: "摘要",
              items: [{ title: "事项", dueAt, sourceUid: 7 }],
            }),
            [7],
          ),
        /dueAt.*RFC3339/,
      ),
    );
  }
});

test("mail deadlines preserve valid leap dates, numeric offsets and fractional seconds", () => {
  for (const dueAt of [
    "2000-02-29T15:00:00Z",
    "2028-02-29T15:00:00-05:00",
    "2026-10-04T15:00:00.123456789+05:30",
  ]) {
    const result = parseAnalysis(
      JSON.stringify({
        summary: "摘要",
        items: [{ title: "事项", dueAt, sourceUid: 7 }],
      }),
      [7],
    );
    assert.equal(result.items[0].dueAt, dueAt);
  }
});
