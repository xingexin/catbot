import test from "node:test";
import assert from "node:assert/strict";
import {
  applyLifecycle,
  previewPurge,
  confirmPurge,
  purgeTargets,
  LifecycleAction,
  lifecycleItems,
  Resource,
  retainSelection,
  actionDescription,
  type LifecycleItem,
  type PurgePreview,
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
    LifecycleAction.Archive,
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

function previewFor(records: LifecycleItem[]): PurgePreview {
  return {
    token: "preview-test-token",
    items: records.map((item) => ({
      resource: item.resource,
      id: item.recordId,
      name: item.name,
      selected: true,
      archived: true,
    })),
    blockers: [],
  };
}

test("ordinary lifecycle requests cannot bypass the dependency preview for purge", async () => {
  let requests = 0;
  const result = await applyLifecycle(
    LifecycleAction.Purge,
    items(Resource.Sessions, 1),
    async () => {
      requests++;
      return {};
    },
  );
  assert.equal(requests, 0);
  assert.deepEqual(result.succeeded, []);
  assert.match(result.failed[0].error, /先预览关联内容并确认/);
});

test("purge preview keeps complete related records and deduplicates by resource plus record ID", async () => {
  const records = [
    ...items(Resource.Configs, 1),
    ...items(Resource.Sessions, 1),
  ];
  const expected = previewFor(records);
  expected.items.push(
    ...items(Resource.Runs, 120).map((item) => ({
      resource: item.resource,
      id: item.recordId,
      name: item.name,
      selected: false,
      archived: false,
    })),
  );
  let requests = 0;
  const preview = await previewPurge([...records, records[0]], async (body) => {
    requests++;
    assert.deepEqual(body.items, [
      { resource: Resource.Configs, id: "record-0" },
      { resource: Resource.Sessions, id: "record-0" },
    ]);
    assert.equal(
      "token" in body,
      false,
      "preview does not submit a confirmation token",
    );
    return expected;
  });
  assert.equal(requests, 1);
  assert.equal(
    preview.items.length,
    122,
    "confirmation must retain every related record, not only a few examples",
  );
  assert.equal(preview.items[121].archived, false);
  assert.deepEqual(
    purgeTargets([...records, records[0]]),
    purgeTargets(records),
  );
});

test("purge preview rejects missing token or mismatched targets without a confirmation", async () => {
  const records = items(Resource.Configs, 1);
  await assert.rejects(
    previewPurge(records, async () => ({ ...previewFor(records), token: "" })),
    /预览不完整/,
  );
  await assert.rejects(
    previewPurge(records, async () => previewFor(items(Resource.Sessions, 1))),
    /与所选内容不一致/,
  );
  await assert.rejects(
    previewPurge([], async () => {
      throw new Error("must not request");
    }),
    /请先选择/,
  );
});

test("runtime blockers stay visible in preview and prohibit confirmation requests", async () => {
  const records = items(Resource.Configs, 1);
  const expected = previewFor(records);
  expected.blockers.push({
    resource: Resource.Tasks,
    id: "task-running",
    name: "邮箱监听",
    reason: "任务正在执行",
  });
  const preview = await previewPurge(records, async () => expected);
  assert.equal(preview.blockers[0].name, "邮箱监听");
  let requests = 0;
  await assert.rejects(
    confirmPurge(records, preview, async () => {
      requests++;
      return { deleted: [], failed: [] };
    }),
    /先处理预览中的限制/,
  );
  assert.equal(requests, 0);
});

test("confirmation sends the approved token and maps only deleted roots back to selection keys", async () => {
  const records = [
    ...items(Resource.Configs, 2),
    ...items(Resource.Sessions, 1),
  ];
  const preview = previewFor(records);
  const related = {
    resource: Resource.Tasks,
    id: "task-related",
    name: "自动摘要",
    selected: false,
    archived: false,
  };
  preview.items.push(related);
  const result = await confirmPurge(records, preview, async (body) => {
    assert.equal(body.token, preview.token);
    assert.deepEqual(body.items, purgeTargets(records));
    return {
      deleted: [preview.items[0], related],
      failed: [
        {
          resource: Resource.Configs,
          id: "record-1",
          name: "配置 1",
          reason: "清理失败",
        },
      ],
    };
  });
  assert.deepEqual(result.succeeded, [records[0].id]);
  assert.deepEqual(
    result.deleted.map((item) => item.name),
    [records[0].name, related.name],
  );
  assert.equal(result.failed.length, 2);
  assert.ok(
    result.failed.some(
      (item) => item.id === records[2].id && /未确认删除结果/.test(item.error),
    ),
    "same record ID in another resource is not falsely removed",
  );
  assert.ok(
    result.failed.some(
      (item) => item.id === records[1].id && item.error === "清理失败",
    ),
  );
});

test("related failures preserve their display name and a contradictory success is not counted", async () => {
  const records = items(Resource.Configs, 1);
  const preview = previewFor(records);
  const result = await confirmPurge(records, preview, async () => ({
    deleted: preview.items,
    failed: [
      {
        resource: Resource.Configs,
        id: "record-0",
        name: "已选模型",
        reason: "删除失败",
      },
      {
        resource: Resource.Tasks,
        id: "task-1",
        name: "定时摘要",
        reason: "停止调度失败",
      },
    ],
  }));
  assert.deepEqual(result.succeeded, []);
  assert.deepEqual(result.deleted, []);
  assert.equal(result.failed[1].name, "定时摘要");
  assert.equal(result.failed[1].error, "停止调度失败");
});

test("a stale preview error is propagated without silently retrying confirmation", async () => {
  const records = items(Resource.Configs, 1);
  let requests = 0;
  await assert.rejects(
    confirmPurge(records, previewFor(records), async () => {
      requests++;
      throw new Error("预览已过期，请重新预览");
    }),
    /预览已过期/,
  );
  assert.equal(requests, 1);
});

test("archiving models explains preserved historical references and future execution requirements", () => {
  const description = actionDescription(
    LifecycleAction.Archive,
    items(Resource.Configs, 1),
  );
  assert.match(description, /可归档并保留历史引用/);
  assert.match(description, /先恢复配置或切换模型/);
  assert.doesNotMatch(description, /仍被使用或正在执行/);
});
