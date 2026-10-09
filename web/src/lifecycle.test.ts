import test from "node:test";
import assert from "node:assert/strict";
import {
  applyLifecycle,
  LifecycleAction,
  lifecycleItems,
  Resource,
  retainSelection,
  actionDescription,
  type LifecycleItem,
} from "./lifecycle";

function items(resource: Resource, count: number): LifecycleItem[] {
  return Array.from({ length: count }, (_, i) => ({
    id: `${resource}:${i}`,
    recordId: `record-${i}`,
    resource,
    name: `记录 ${i}`,
  }));
}
test("lifecycle wire types remain explicit numeric enums", () => {
  assert.equal(Resource.Sessions, 1);
  assert.equal(LifecycleAction.Unknown, 0);
  assert.equal(Resource.Secrets, 12);
  assert.deepEqual(
    [LifecycleAction.Archive, LifecycleAction.Restore, LifecycleAction.Purge],
    [1, 2, 3],
  );
});
test("mixed archive selection groups resources and splits every batch at 100 without losing records", async () => {
  const records = [
    ...items(Resource.Sessions, 205),
    ...items(Resource.Tasks, 2),
  ];
  const calls: {
    resource: Resource;
    action: LifecycleAction;
    ids: string[];
  }[] = [];
  const result = await applyLifecycle(
    LifecycleAction.Restore,
    records,
    async (body) => {
      calls.push(body);
      return { succeeded: body.ids };
    },
  );
  assert.deepEqual(
    calls.map((call) => call.ids.length),
    [100, 100, 5, 2],
  );
  assert.deepEqual(
    calls.map((call) => call.resource),
    [Resource.Sessions, Resource.Sessions, Resource.Sessions, Resource.Tasks],
  );
  assert.ok(calls.every((call) => call.action === LifecycleAction.Restore));
  assert.deepEqual(
    result.succeeded,
    records.map((item) => item.id),
  );
  assert.deepEqual(result.failed, []);
  assert.equal(
    calls[0].ids[0],
    "record-0",
    "API receives record ID, not archive composite row key",
  );
});
test("partial success retains failures with display names and server reasons", async () => {
  const records = items(Resource.Personas, 3);
  const result = await applyLifecycle(
    LifecycleAction.Archive,
    records,
    async () => ({
      succeeded: ["record-0", "unrequested"],
      failed: [{ id: "record-1", error: "默认人格无法归档" }],
    }),
  );
  assert.deepEqual(result.succeeded, [records[0].id]);
  assert.equal(result.failed[0].name, "记录 1");
  assert.equal(result.failed[0].error, "默认人格无法归档");
  assert.match(result.failed[1].error, /未确认/);
});
test("one failed request does not falsely mark success or skip later resource groups", async () => {
  const records = [
    ...items(Resource.Plugins, 2),
    ...items(Resource.Artifacts, 1),
  ];
  const result = await applyLifecycle(
    LifecycleAction.Purge,
    records,
    async (body) => {
      if (body.resource === Resource.Plugins) throw new Error("插件仍被使用");
      return { succeeded: body.ids };
    },
  );
  assert.deepEqual(result.succeeded, [records[2].id]);
  assert.equal(result.failed.length, 2);
  assert.equal(result.failed[0].error, "插件仍被使用");
});
test("duplicate record IDs are submitted once within each resource", async () => {
  const records = items(Resource.Tasks, 1);
  let requests = 0;
  const result = await applyLifecycle(
    LifecycleAction.Archive,
    [...records, ...records],
    async (body) => {
      requests++;
      assert.equal(body.ids.length, 1);
      return { succeeded: body.ids };
    },
  );
  assert.equal(requests, 1);
  assert.deepEqual(result.succeeded, [records[0].id]);
});
test("refresh removes invalid selections while keeping failed records selected", () => {
  const records = lifecycleItems(Resource.Sessions, [
    { id: "a", title: "第一段对话" },
    { id: "b", title: "第二段对话" },
  ]);
  assert.equal(records[0].name, "第一段对话");
  assert.deepEqual(retainSelection(["a", "b", "missing"], records.slice(1)), [
    "b",
  ]);
});
test("confirmation explains archive, paused restore and irreversible deletion", () => {
  const records = [...items(Resource.Tasks, 1), ...items(Resource.Plugins, 1)];
  assert.match(
    actionDescription(LifecycleAction.Archive, records),
    /停止后续调度/,
  );
  assert.match(actionDescription(LifecycleAction.Archive, records), /固定版本/);
  assert.match(actionDescription(LifecycleAction.Restore, records), /保持暂停/);
  assert.match(actionDescription(LifecycleAction.Purge, records), /无法恢复/);
});

test("unknown and invalid actions never submit requests or fall through to purge", async () => {
  for (const action of [LifecycleAction.Unknown, 999 as LifecycleAction]) {
    const result = await applyLifecycle(
      action,
      items(Resource.Sessions, 1),
      async () => {
        throw new Error("request must not run");
      },
    );
    assert.deepEqual(result.succeeded, []);
    assert.match(result.failed[0].error, /不支持的操作/);
  }
});
