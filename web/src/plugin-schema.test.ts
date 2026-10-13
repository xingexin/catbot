import test from "node:test";
import assert from "node:assert/strict";
import { pluginSelectOptions } from "./plugin-schema.js";

test("plugin numeric choices retain wire IDs while showing readable labels", () => {
  assert.deepEqual(
    pluginSelectOptions({
      type: "integer",
      oneOf: [
        { const: 1, title: "Tavily" },
        { const: 2, title: "Brave Search" },
      ],
    }),
    [
      { value: 1, label: "Tavily" },
      { value: 2, label: "Brave Search" },
    ],
  );
});
test("existing enum schemas work and other schema unions stay untouched", () => {
  assert.deepEqual(pluginSelectOptions({ enum: ["a", "b"] }), [
    { value: "a", label: "a" },
    { value: "b", label: "b" },
  ]);
  assert.equal(
    pluginSelectOptions({ oneOf: [{ type: "string" }, { type: "number" }] }),
    undefined,
  );
  assert.equal(pluginSelectOptions({ type: "string" }), undefined);
});
