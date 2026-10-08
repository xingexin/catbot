import { useEffect, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Empty,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
} from "antd";
import { api } from "./api";
import type { Row } from "./catbot";

const permissionOptions = [
  { label: "读取邮件", value: "mail.read" },
  { label: "保存游标和解析结果", value: "storage" },
  { label: "调用摘要模型", value: "models" },
  { label: "标记邮件已读", value: "mail.write" },
];
const intervalOptions = [1, 2, 5, 10, 15, 30, 60].map((value) => ({
  value,
  label: `每 ${value} 分钟`,
}));
const labels: Record<string, string> = {
  active: "运行中",
  scheduled: "已安排",
  paused: "已暂停",
  canceled: "已取消",
  cancelled: "已取消",
  completed: "已完成",
  running: "检查中",
  pending: "等待执行",
  failed: "失败",
  interrupted: "已中断",
  notification_failed: "通知失败",
  notify_failed: "通知失败",
  blocked: "依赖不可用",
};
const choices = (rows: Row[]) =>
  rows.map((row) => ({
    label: row.name ?? row.title ?? row.id,
    value: row.id,
  }));
function watcherStep(task: Row): Row | undefined {
  return task.steps?.find(
    (step: Row) => step.kind === "tool" && step.tool === "mail__watch",
  );
}
function interval(task: Row): number {
  return (
    task.intervalMinutes ??
    (task.cron === "0 * * * *"
      ? 60
      : Number(/^\*\/(\d+) \* \* \* \*$/.exec(task.cron ?? "")?.[1] ?? 5))
  );
}
function date(value: unknown): string {
  return value ? new Date(String(value)).toLocaleString() : "尚未执行";
}

export function MailSettings({
  plugins,
  configs,
  personas,
  sessions,
  tasks,
  executions,
  onRefresh,
  onInspect,
}: {
  plugins: Row[];
  configs: Row[];
  personas: Row[];
  sessions: Row[];
  tasks: Row[];
  executions: Row[];
  onRefresh: () => Promise<void>;
  onInspect: (value: unknown) => void;
}) {
  const plugin = plugins.find((item) => item.id === "mail");
  const [configForm] = Form.useForm();
  const [watchForm] = Form.useForm();
  const [busy, setBusy] = useState("");
  const [dirty, setDirty] = useState(false);
  const [connection, setConnection] = useState<Row>();
  const [connectionError, setConnectionError] = useState("");
  const [editing, setEditing] = useState<Row>();
  const { message } = App.useApp();
  const snapshot = JSON.stringify({
    config: plugin?.config ?? {},
    grants: plugin?.grants ?? [],
  });
  useEffect(() => {
    const saved = JSON.parse(snapshot);
    configForm.setFieldsValue({
      host: saved.config.host ?? "",
      port: saved.config.port ?? 993,
      username: saved.config.username ?? "",
      password: "",
      summaryConfigId: saved.config.summaryConfigId,
      grants: saved.config.host
        ? saved.grants
        : ["mail.read", "storage", "models"],
    });
    setDirty(false);
    setConnection(undefined);
  }, [snapshot, configForm]);
  const watchers = tasks.filter((task) => watcherStep(task));
  const watchIds = new Set(watchers.map((task) => task.id));
  const history = executions
    .filter((run) => watchIds.has(run.taskId))
    .sort((a, b) => String(b.startedAt).localeCompare(String(a.startedAt)));
  const selectedSession = Form.useWatch("sessionId", watchForm);
  const selected = sessions.find((session) => session.id === selectedSession);
  async function action(
    key: string,
    fn: () => Promise<unknown>,
    success: string,
  ) {
    setBusy(key);
    try {
      await fn();
      message.success(success);
      await onRefresh();
    } catch (error) {
      if (error instanceof Error) message.error(error.message);
    } finally {
      setBusy("");
    }
  }
  function resetWatch() {
    setEditing(undefined);
    watchForm.resetFields();
  }
  function edit(task: Row) {
    setEditing(task);
    const step = watcherStep(task);
    watchForm.setFieldsValue({
      name: task.name,
      folder: step?.arguments?.folder ?? "INBOX",
      intervalMinutes: interval(task),
      sessionId: task.sessionId,
      configId: task.configId,
      personaId: task.personaId,
      includeExisting: !!step?.arguments?.includeExisting,
    });
  }
  if (!plugin)
    return (
      <Alert
        type="warning"
        showIcon
        message="邮箱插件尚未安装"
        description="在插件页注册内置 mail 插件后，即可在这里配置邮箱监听。"
      />
    );
  return (
    <Space direction="vertical" size="large" style={{ width: "100%" }}>
      <Alert
        type="info"
        showIcon
        message="定时检查新邮件，有变化才提醒"
        description="邮箱监听通过 Temporal 周期轮询运行，重启后继续检查。默认首次只建立基线，不提醒历史邮件；后续新增邮件会发送到指定 QQ 或 Web 会话。"
      />
      <div className="cards">
        <Card
          title="邮箱连接"
          extra={
            <Tag color={plugin.enabled ? "green" : "default"}>
              {plugin.enabled ? "插件已启用" : "插件未启用"}
            </Tag>
          }
        >
          <Form
            form={configForm}
            layout="vertical"
            name="mail-settings"
            onValuesChange={() => setDirty(true)}
            onFinish={async (values) => {
              await action(
                "save-config",
                async () => {
                  const config: Row = {
                    host: values.host.trim(),
                    port: values.port,
                    username: values.username.trim(),
                  };
                  if (values.password) config.password = values.password;
                  if (values.summaryConfigId)
                    config.summaryConfigId = values.summaryConfigId;
                  await api("/plugins/mail/configure", {
                    config,
                    grants: values.grants ?? [],
                  });
                  configForm.setFieldValue("password", "");
                  setDirty(false);
                  setConnection(undefined);
                  setConnectionError("");
                },
                "邮箱配置已保存",
              );
            }}
          >
            <Form.Item
              label="IMAP 服务器"
              name="host"
              rules={[
                {
                  required: true,
                  whitespace: true,
                  message: "填写邮箱的 IMAP 服务器",
                },
              ]}
            >
              <Input placeholder="例如 imap.qq.com" autoComplete="off" />
            </Form.Item>
            <Form.Item
              label="TLS 端口"
              name="port"
              rules={[{ required: true }]}
            >
              <InputNumber min={1} max={65535} style={{ width: "100%" }} />
            </Form.Item>
            <Form.Item
              label="邮箱账号"
              name="username"
              rules={[
                {
                  required: true,
                  whitespace: true,
                  message: "填写完整邮箱账号",
                },
              ]}
            >
              <Input placeholder="完整邮箱地址" autoComplete="off" />
            </Form.Item>
            <Form.Item
              label="IMAP 授权码"
              name="password"
              rules={
                plugin.secrets?.password
                  ? []
                  : [{ required: true, message: "填写邮箱授权码或应用密码" }]
              }
              extra={
                plugin.secrets?.password
                  ? "已保存到凭证库，留空保留现有授权码。"
                  : "先在邮箱设置中启用 IMAP，填写授权码或应用密码。"
              }
            >
              <Input.Password
                autoComplete="new-password"
                placeholder={
                  plugin.secrets?.password ? "留空保留现有授权码" : "填写授权码"
                }
              />
            </Form.Item>
            <Form.Item
              label="邮件摘要模型（可选）"
              name="summaryConfigId"
              extra="新邮件提醒无需模型；生成内容摘要时使用此 API 模型。"
            >
              <Select
                allowClear
                showSearch
                optionFilterProp="label"
                options={choices(
                  configs.filter((config) => config.kind === "api"),
                )}
                placeholder="选择 API 模型配置"
              />
            </Form.Item>
            <Form.Item name="grants" label="允许的能力">
              <Checkbox.Group options={permissionOptions} />
            </Form.Item>
            <Space wrap>
              <Button
                type="primary"
                htmlType="submit"
                loading={busy === "save-config"}
                disabled={!!busy && busy !== "save-config"}
              >
                保存邮箱配置
              </Button>
              <Button
                disabled={!!busy || dirty || !plugin.config?.host}
                onClick={() =>
                  void action(
                    "enable",
                    () =>
                      api("/plugins/mail/enable", { enabled: !plugin.enabled }),
                    plugin.enabled ? "邮箱插件已停用" : "邮箱插件已启用",
                  )
                }
              >
                {plugin.enabled ? "停用邮箱" : "启用邮箱"}
              </Button>
              <Button
                loading={busy === "connection"}
                disabled={
                  (!!busy && busy !== "connection") || dirty || !plugin.enabled
                }
                onClick={async () => {
                  setBusy("connection");
                  setConnectionError("");
                  try {
                    const result = await api("/mail/connection", {
                      folder: watchForm.getFieldValue("folder") || "INBOX",
                    });
                    setConnection(result);
                    message.success("IMAP 连接正常");
                  } catch (error) {
                    setConnection(undefined);
                    setConnectionError(
                      error instanceof Error ? error.message : "连接失败",
                    );
                  } finally {
                    setBusy("");
                  }
                }}
              >
                测试邮箱连接
              </Button>
            </Space>
          </Form>
          <p className="muted">
            先保存并启用邮箱，再测试连接。停用插件会暂停依赖它的任务，重新启用后可在下方恢复监听。
          </p>
          {connectionError && (
            <Alert
              type="error"
              showIcon
              message="邮箱连接失败"
              description={connectionError}
            />
          )}
          {connection && (
            <Alert
              type="success"
              showIcon
              message="邮箱连接正常"
              description={`${connection.folder ?? "INBOX"} · 共 ${connection.messageCount ?? "—"} 封邮件${connection.unreadCount === undefined ? "" : `，${connection.unreadCount} 封未读`} · ${date(connection.checkedAt)}`}
            />
          )}
        </Card>
        <Card
          title={editing ? "修改邮箱监听" : "添加邮箱监听"}
          extra={
            editing && (
              <Button size="small" onClick={resetWatch}>
                取消编辑
              </Button>
            )
          }
        >
          <Form
            form={watchForm}
            layout="vertical"
            name="mail-watch"
            initialValues={{
              name: "新邮件提醒",
              folder: "INBOX",
              intervalMinutes: 5,
              includeExisting: false,
            }}
            onFinish={async (values) => {
              await action(
                "save-watch",
                async () => {
                  await api("/mail/watch", {
                    ...values,
                    ...(editing ? { id: editing.id } : {}),
                  });
                  resetWatch();
                },
                editing ? "邮箱监听已更新" : "邮箱监听已创建",
              );
            }}
          >
            <Form.Item
              label="监听名称"
              name="name"
              rules={[{ required: true, whitespace: true }]}
            >
              <Input maxLength={100} />
            </Form.Item>
            <Form.Item
              label="邮件文件夹"
              name="folder"
              rules={[{ required: true, whitespace: true }]}
            >
              <Input placeholder="INBOX" maxLength={512} />
            </Form.Item>
            <Form.Item
              label="检查频率"
              name="intervalMinutes"
              rules={[{ required: true }]}
            >
              <Select options={intervalOptions} />
            </Form.Item>
            <Form.Item
              label="提醒发送到"
              name="sessionId"
              rules={[{ required: true, message: "请选择接收提醒的会话" }]}
              extra={
                sessions.length
                  ? "选择 QQ 会话可收到私聊提醒；Web 会话可在管理端查看通知。"
                  : "请先创建 Web 对话，或用授权的 QQ 联系 catbot 建立会话。"
              }
            >
              <Select
                showSearch
                optionFilterProp="label"
                placeholder="选择 QQ 或 Web 会话"
                options={sessions
                  .filter((session) => session.channel !== "task")
                  .map((session) => ({
                    value: session.id,
                    label: `${session.channel === "qq" ? "QQ" : "Web"} · ${session.title || session.recipient || session.id}`,
                  }))}
                onChange={(id) => {
                  const session = sessions.find((item) => item.id === id);
                  watchForm.setFieldsValue({
                    configId: session?.configId,
                    personaId: session?.personaId,
                  });
                }}
              />
            </Form.Item>
            {selected?.channel === "qq" && (
              <p className="muted">
                QQ 接收人：{selected.recipient}
                。请保持消息通道在线，并保留该联系人的授权。
              </p>
            )}
            <Form.Item
              label="模型配置"
              name="configId"
              rules={[{ required: true }]}
              extra="自动采用会话的模型配置；轮询收件箱本身不会消耗模型额度。"
            >
              <Select
                options={choices(configs)}
                showSearch
                optionFilterProp="label"
              />
            </Form.Item>
            <Form.Item
              label="人格"
              name="personaId"
              rules={[{ required: true }]}
            >
              <Select
                options={choices(personas)}
                showSearch
                optionFilterProp="label"
              />
            </Form.Item>
            <Form.Item name="includeExisting" valuePropName="checked">
              <Checkbox>首次检查也提醒已有邮件</Checkbox>
            </Form.Item>
            <p className="muted">
              每次最多处理 20
              封，积压邮件在后续检查中继续处理；没有新邮件时保持安静。修改频率会保留已有游标。
            </p>
            <Button
              type="primary"
              htmlType="submit"
              loading={busy === "save-watch"}
              disabled={!plugin.enabled || (!!busy && busy !== "save-watch")}
            >
              {editing ? "保存监听" : "创建监听"}
            </Button>
          </Form>
        </Card>
      </div>
      <Card
        title="邮箱监听任务"
        extra={
          <Button
            disabled={!!busy}
            onClick={() => void action("refresh", async () => {}, "已刷新")}
          >
            刷新
          </Button>
        }
      >
        <Table
          rowKey="id"
          dataSource={watchers}
          pagination={{ pageSize: 5, hideOnSinglePage: true }}
          scroll={{ x: 880 }}
          locale={{
            emptyText: (
              <Empty description="还没有邮箱监听，填写上方表单即可开始" />
            ),
          }}
          columns={[
            {
              title: "监听",
              render: (_, task) => (
                <>
                  <strong>{task.name}</strong>
                  <div className="muted">
                    {watcherStep(task)?.arguments?.folder ?? "INBOX"} · 每{" "}
                    {interval(task)} 分钟
                  </div>
                </>
              ),
            },
            {
              title: "接收会话",
              render: (_, task) =>
                sessions.find((session) => session.id === task.sessionId)
                  ?.title ?? task.sessionId,
            },
            {
              title: "任务状态",
              render: (_, task) => (
                <>
                  <Tag
                    color={
                      task.error ? "red" : task.paused ? "default" : "blue"
                    }
                  >
                    {["cancelled", "canceled"].includes(task.status)
                      ? "已取消"
                      : task.paused
                        ? "已暂停"
                        : (labels[task.status] ?? task.status ?? "已安排")}
                  </Tag>
                  {task.error && (
                    <div style={{ color: "#cf1322", maxWidth: 260 }}>
                      {task.error}
                    </div>
                  )}
                </>
              ),
            },
            {
              title: "最近检查",
              render: (_, task) => {
                const last = history.find((run) => run.taskId === task.id);
                const result =
                  last?.results?.[watcherStep(task)?.id ?? "watch"];
                return (
                  <>
                    <div>{date(last?.startedAt)}</div>
                    {last && (
                      <Button
                        type="link"
                        size="small"
                        onClick={() => onInspect(last)}
                      >
                        {last.status === "completed" &&
                        result?.changed === false
                          ? result?.initialized
                            ? "已建立基线，等待新邮件"
                            : "无新邮件"
                          : (labels[last.status] ?? last.status)}
                      </Button>
                    )}
                    {last?.error && (
                      <div style={{ color: "#cf1322", maxWidth: 260 }}>
                        {last.error}
                      </div>
                    )}
                  </>
                );
              },
            },
            {
              title: "操作",
              render: (_, task) => {
                const canceled = ["canceled", "cancelled"].includes(
                  task.status,
                );
                return (
                  <Space wrap>
                    <Button
                      size="small"
                      disabled={!!busy || canceled}
                      onClick={() => edit(task)}
                    >
                      编辑
                    </Button>
                    <Button
                      size="small"
                      disabled={!!busy || canceled || !plugin.enabled}
                      onClick={() =>
                        void action(
                          task.id,
                          () => api(`/tasks/${task.id}/trigger`, {}),
                          "已提交邮箱检查",
                        )
                      }
                    >
                      立即检查
                    </Button>
                    <Button
                      size="small"
                      disabled={!!busy || canceled || !plugin.enabled}
                      onClick={() =>
                        void action(
                          task.id,
                          () =>
                            api(
                              `/tasks/${task.id}/${task.paused ? "resume" : "pause"}`,
                              {},
                            ),
                          task.paused ? "监听已恢复" : "监听已暂停",
                        )
                      }
                    >
                      {task.paused ? "恢复" : "暂停"}
                    </Button>
                    <Popconfirm
                      title="取消此邮箱监听？"
                      description="将停止后续检查并取消正在执行的任务。"
                      onConfirm={() =>
                        action(
                          task.id,
                          () => api(`/tasks/${task.id}/cancel`, {}),
                          "监听已取消",
                        )
                      }
                    >
                      <Button size="small" danger disabled={!!busy || canceled}>
                        取消
                      </Button>
                    </Popconfirm>
                  </Space>
                );
              },
            },
          ]}
        />
      </Card>
      <Card title="最近邮箱检查记录">
        <Table
          rowKey="id"
          size="small"
          dataSource={history.slice(0, 20)}
          pagination={{ pageSize: 5, hideOnSinglePage: true }}
          columns={[
            {
              title: "监听",
              render: (_, run) =>
                watchers.find((task) => task.id === run.taskId)?.name ??
                run.taskId,
            },
            { title: "开始时间", render: (_, run) => date(run.startedAt) },
            {
              title: "状态",
              render: (_, run) => (
                <Tag color={run.error ? "red" : "default"}>
                  {labels[run.status] ?? run.status}
                </Tag>
              ),
            },
            { title: "错误", dataIndex: "error", ellipsis: true },
            {
              title: "详情",
              render: (_, run) => (
                <Button size="small" onClick={() => onInspect(run)}>
                  查看记录
                </Button>
              ),
            },
          ]}
        />
      </Card>
    </Space>
  );
}
