import test from "node:test";
import assert from "node:assert/strict";
import { groupHistoryForSave, initialGroupHistory } from "./qq-group-history";

const defaults = {
  enabled: false,
  roomIds: [],
  retentionDays: 7,
  recentMessages: 30,
  maxMessages: 10000,
};

test("legacy absent group recording stays disabled with explicit empty scope and bounded defaults", () => {
  assert.deepEqual(initialGroupHistory(), defaults);
  assert.deepEqual(initialGroupHistory(null), defaults);
  assert.deepEqual(groupHistoryForSave(undefined, ["1001"]), defaults);
  const empty = initialGroupHistory();
  empty.roomIds.push("1001");
  assert.deepEqual(initialGroupHistory().roomIds, []);
});

test("group settings round-trip values including zero automatic context and preserve source record", () => {
  const persisted = {
    enabled: true,
    roomIds: ["1001"],
    retentionDays: 365,
    recentMessages: 0,
    maxMessages: 100000,
  };
  const form = initialGroupHistory(persisted);
  assert.deepEqual(groupHistoryForSave(form, ["1001", "1002"]), persisted);
  form.roomIds.push("1002");
  assert.deepEqual(persisted.roomIds, ["1001"]);
  assert.deepEqual(
    groupHistoryForSave(
      { ...persisted, roomIds: ["1001", "1001"], unknownField: "not-saved" },
      ["1001"],
    ),
    persisted,
  );
});

test("recording never treats empty scope as all groups or accepts a group outside allowed list", () => {
  assert.deepEqual(groupHistoryForSave(defaults, ["1001"]), defaults);
  assert.throws(
    () => groupHistoryForSave({ ...defaults, enabled: true }, ["1001"]),
    /至少选择一个/,
  );
  assert.throws(
    () => groupHistoryForSave({ ...defaults, roomIds: ["1002"] }, ["1001"]),
    /允许使用/,
  );
  assert.throws(
    () => groupHistoryForSave({ ...defaults, roomIds: [1001] }, ["1001"]),
    /有效/,
  );
  assert.throws(
    () => groupHistoryForSave({ ...defaults, roomIds: [""] }, ["1001"]),
    /有效/,
  );
  assert.throws(
    () => groupHistoryForSave({ ...defaults, enabled: "true" }, ["1001"]),
    /是否开启/,
  );
});

test("record limits reject invalid or fractional input and accept exact boundaries", () => {
  for (const [field, low, high] of [
    ["retentionDays", 1, 365],
    ["recentMessages", 0, 100],
    ["maxMessages", 100, 100000],
  ] as const) {
    for (const value of [low, high])
      assert.equal(
        groupHistoryForSave({ ...defaults, [field]: value }, [])[field],
        value,
      );
    for (const value of [
      low - 1,
      high + 1,
      low + 0.5,
      null,
      NaN,
      Infinity,
      "30",
    ])
      assert.throws(
        () => groupHistoryForSave({ ...defaults, [field]: value }, []),
        /整数/,
      );
  }
});
