import { Alert, Select, Table, Tag } from "antd";
import { useState } from "react";
import { Resource, resourceLabels, type LifecycleItem } from "./lifecycle";
import {
  LifecycleFeedback,
  useLifecycleSelection,
  withLifecycleActions,
} from "./lifecycle-ui";

export interface ArchiveRecord extends LifecycleItem {
  archivedAt: string;
}
export function Archives({
  records,
  onRefresh,
}: {
  records: ArchiveRecord[];
  onRefresh: () => Promise<void>;
}) {
  const [resource, setResource] = useState(Resource.Unknown);
  const filtered = records.filter(
    (record) => resource === Resource.Unknown || record.resource === resource,
  );
  const selection = useLifecycleSelection(filtered, onRefresh);
  return (
    <>
      <Alert
        showIcon
        type="info"
        message="已归档的内容集中保存在这里"
        description="恢复后重新显示在原列表；任务保持暂停、插件保持停用。永久删除不可恢复，仍被使用或正在执行的项目会保留并说明原因。"
        style={{ marginBottom: 20 }}
      />
      <Select
        aria-label="筛选归档类型"
        value={resource}
        onChange={setResource}
        style={{ minWidth: 190, marginBottom: 16 }}
        options={[
          { value: Resource.Unknown, label: "全部类型" },
          ...Object.entries(resourceLabels)
            .filter(([key]) => Number(key) !== Resource.Unknown)
            .map(([key, label]) => ({ value: Number(key), label })),
        ]}
      />
      <LifecycleFeedback selection={selection} />
      <Table<ArchiveRecord>
        rowKey="id"
        dataSource={filtered}
        rowSelection={selection.rowSelection}
        locale={{ emptyText: "暂无归档内容" }}
        columns={withLifecycleActions<ArchiveRecord>(
          [
            { title: "名称", dataIndex: "name" },
            {
              title: "类型",
              dataIndex: "resource",
              render: (value: Resource) => (
                <Tag>
                  {resourceLabels[value] ?? resourceLabels[Resource.Unknown]}
                </Tag>
              ),
            },
            {
              title: "归档时间",
              dataIndex: "archivedAt",
              render: (value: string) => new Date(value).toLocaleString(),
            },
          ],
          selection,
          true,
        )}
      />
    </>
  );
}
