import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, cp, readFile, rm, access } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createPlugin, parseArgs } from "./create-plugin.mjs";

const repository = resolve(dirname(fileURLToPath(import.meta.url)), "..");

test("the existing one-argument command still creates TypeScript", () => {
  assert.deepEqual(parseArgs(["notes"]), { name: "notes", language: "ts" });
  assert.deepEqual(parseArgs(["notes", "--language", "go"]), { name: "notes", language: "go" });
  assert.deepEqual(parseArgs(["notes", "--language=go"]), { name: "notes", language: "go" });
  for (const args of [["../notes"], ["notes", "--language", "python"], ["notes", "unexpected"]]) {
    assert.throws(() => parseArgs(args), /Usage:/);
  }
});

for (const language of ["ts", "go"]) {
  test(`${language} scaffold uses the actual example and refuses overwrite`, async () => {
    const root = await mkdtemp(resolve(tmpdir(), "catbot-plugin-template-"));
    try {
      const example = language === "go" ? "example-go" : "example";
      await cp(resolve(repository, "plugins", example), resolve(root, "plugins", example), {
        recursive: true,
        filter: (source) => !source.includes("/dist") && !source.includes("/node_modules"),
      });
      await createPlugin({ name: "notes", language }, root);
      const manifest = JSON.parse(await readFile(resolve(root, "plugins/notes/plugin.json"), "utf8"));
      assert.equal(manifest.id, "notes");
      assert.equal(manifest.version, "1.0.0");
      assert.deepEqual(manifest.templates, []);
      assert.equal(manifest.runtime ?? "node", language === "go" ? "binary" : "node");
      await access(resolve(root, "plugins/notes", language === "go" ? "main.go" : "src/index.ts"));
      if (language === "go") {
        assert.equal(manifest.entry, "dist/plugin");
        await assert.rejects(access(resolve(root, "plugins/notes/package.json")));
      }
      await assert.rejects(createPlugin({ name: "notes", language }, root), { code: "EEXIST" });
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
}
