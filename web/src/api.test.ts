import test from "node:test";
import assert from "node:assert/strict";
import { parseJSON, pretty } from "./api";
test("configuration JSON errors are reported instead of silently ignored", () => {
  assert.throws(() => parseJSON("{bad}", {}));
  assert.deepEqual(parseJSON("", []), []);
  assert.deepEqual(parseJSON(pretty({ tools: [] }), null), { tools: [] });
});
