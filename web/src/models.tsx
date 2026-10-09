import { useState } from "react";
import { Button, Card, Empty, Input, Space, Table, Tag } from "antd";
import { ApiOutlined, PlusOutlined } from "@ant-design/icons";
import { LifecycleAction, Resource, lifecycleItems } from "./lifecycle";
import {
  LifecycleFeedback,
  useLifecycleSelection,
  withLifecycleActions,
} from "./lifecycle-ui";
import type { Row } from "./catbot";

export function ModelConnections({
  configs,
  credentials,
  onAdd,
  onEdit,
  onTest,
  onRefresh,
}: {
  configs: Row[];
  credentials: Row[];
  onAdd: (kind: "api" | "sdk") => void;
  onEdit: (config: Row) => void;
  onTest: (config: Row) => void;
  onRefresh: () => Promise<void>;
}) {
  const [search, setSearch] = useState("");
  const query = search.trim().toLowerCase();
  const filtered = configs.filter((c) =>
    [c.name, c.model, c.provider, c.protocol].some((v) =>
      String(v ?? "")
        .toLowerCase()
        .includes(query),
    ),
  );
  const selection = useLifecycleSelection(
    lifecycleItems(Resource.Configs, filtered),
    onRefresh,
  );
  return (
    <Card
      className="model-connections"
      title={
        <Space>
          <ApiOutlined />
          模型接入
        </Space>
      }
    >
      <div className="toolbar">
        <p className="muted">
          添加模型后，可在对话、QQ 绑定和定时任务中选择使用。
        </p>
        <Space wrap>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => onAdd("api")}
          >
            添加模型 API
          </Button>
          <Button icon={<PlusOutlined />} onClick={() => onAdd("sdk")}>
            添加 Agent SDK
          </Button>
        </Space>
      </div>
      <p>
        API 直连支持 Key、Base URL 和 Model；Agent SDK 支持
        CodeBuddy、Claude、Codex。
      </p>
      <Input.Search
        aria-label="搜索模型接入"
        placeholder="搜索配置名称、模型或提供商"
        allowClear
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        style={{ maxWidth: 360, marginBottom: 16 }}
      />
      <LifecycleFeedback selection={selection} />
      <Table
        rowSelection={selection.rowSelection}
        rowKey="id"
        dataSource={filtered}
        pagination={{
          pageSize: 5,
          showSizeChanger: false,
          hideOnSinglePage: true,
        }}
        scroll={{ x: 860 }}
        locale={{
          emptyText: (
            <Empty
              description={
                query ? "没有匹配的模型配置" : "添加第一个模型，开始使用 catbot"
              }
            />
          ),
        }}
        columns={withLifecycleActions<Row>(
          [
            { title: "配置名称", dataIndex: "name" },
            { title: "模型", dataIndex: "model" },
            {
              title: "接入方式",
              render: (_, c) => (
                <>
                  <Tag>{c.kind === "sdk" ? "Agent SDK" : "模型 API"}</Tag>
                  <div className="muted">
                    {c.kind === "sdk" ? c.provider : c.protocol}
                  </div>
                </>
              ),
            },
            {
              title: "凭证",
              render: (_, c) =>
                credentials.find((s) => s.id === c.credentialId)?.name ??
                (c.credentialId ? "凭证引用不存在" : "未设置"),
            },
            {
              title: "操作",
              render: (_, c) => (
                <Space wrap>
                  <Button size="small" onClick={() => onEdit(c)}>
                    编辑
                  </Button>
                  <Button size="small" onClick={() => onTest(c)}>
                    连接检查
                  </Button>
                  <Button
                    size="small"
                    type="text"
                    disabled={selection.busy}
                    onClick={() =>
                      selection.confirm(
                        LifecycleAction.Archive,
                        lifecycleItems(Resource.Configs, [c]),
                      )
                    }
                  >
                    归档
                  </Button>
                </Space>
              ),
            },
          ],
          selection,
        )}
      />
    </Card>
  );
}
