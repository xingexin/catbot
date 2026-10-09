import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Layout,
  Menu,
  Modal,
  Popconfirm,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Upload,
} from "antd";
import {
  MessageOutlined,
  ClockCircleOutlined,
  ApiOutlined,
  UserOutlined,
  AppstoreOutlined,
  FileTextOutlined,
  HistoryOutlined,
  PlusOutlined,
  ArrowUpOutlined,
  ReloadOutlined,
  LogoutOutlined,
  SettingOutlined,
  KeyOutlined,
  BellOutlined,
  ArrowRightOutlined,
} from "@ant-design/icons";
import { api, downloadJSON, parseJSON, pretty } from "./api";
import { Editor } from "./editor";
import { QQConnections } from "./qq";
import { ModelConnections } from "./models";
import { MailSettings } from "./mail";
import { CatMark, Brand } from "./brand";
const { TextArea } = Input;
export type Row = Record<string, any>;
const pages = [
  { key: "chat", icon: <MessageOutlined />, label: "对话" },
  { key: "tasks", icon: <ClockCircleOutlined />, label: "定时任务" },
  { key: "mail", icon: <FileTextOutlined />, label: "邮箱监听" },
  { key: "artifacts", icon: <FileTextOutlined />, label: "文件与解析" },
  { key: "personas", icon: <UserOutlined />, label: "人格" },
  { key: "plugins", icon: <AppstoreOutlined />, label: "插件" },
  { key: "configs", icon: <ApiOutlined />, label: "模型配置" },
  { key: "runs", icon: <HistoryOutlined />, label: "运行记录" },
  { key: "settings", icon: <SettingOutlined />, label: "系统与凭证" },
];
const descriptions: Record<string, string> = {
  chat: "把想法说出来，剩下的交给 catbot。",
  tasks: "一次提醒、周期安排，或按步骤执行的后台工作。",
  mail: "连接邮箱，定期检查新邮件，有变化时通知指定会话。",
  artifacts: "上传文件，查看插件保存的摘要和提取事项。",
  personas: "设定表达方式和行为偏好，按会话选择。",
  plugins: "按需接入，让 catbot 多会一点。",
  configs: "选择 Agent SDK，或通过 Key 与 Base URL 直连模型。",
  runs: "检查模型调用、工具结果与后台任务状态。",
  settings: "接入模型与 Agent SDK，管理 QQ 连接和服务凭证。",
};
const statusColor: Record<string, string> = {
  completed: "success",
  active: "success",
  sent: "success",
  running: "processing",
  queued: "processing",
  paused: "warning",
  failed: "error",
  interrupted: "error",
  error: "error",
  uncertain: "warning",
  notification_failed: "warning",
};
function Status({ value }: { value: string }) {
  return <Tag color={statusColor[value]}>{value}</Tag>;
}
function JSONView({ value }: { value: unknown }) {
  return <pre className="json">{pretty(value)}</pre>;
}
export function Catbot() {
  const { message } = App.useApp();
  const [logged, setLogged] = useState<boolean | null>(null),
    [page, setPage] = useState("chat"),
    [data, setData] = useState<Record<string, Row[]>>({}),
    [status, setStatus] = useState<Row>({}),
    [busy, setBusy] = useState(false);
  const [edit, setEdit] = useState<{ kind: string; value: Row } | null>(null),
    [inspect, setInspect] = useState<unknown>(null);
  const [form] = Form.useForm();
  const [sessionId, setSessionId] = useState(""),
    [input, setInput] = useState(""),
    [run, setRun] = useState<Row | null>(null),
    [events, setEvents] = useState<Row[]>([]),
    [stream, setStream] = useState("");
  const source = useRef<EventSource | null>(null);
  const end = useRef<HTMLDivElement>(null);
  const rows = (name: string) => data[name] ?? [];
  const session = rows("sessions").find((s) => s.id === sessionId);
  const selectedPersona = rows("personas").find(
    (p) => p.id === session?.personaId,
  );
  const selectedConfig = rows("configs").find(
    (c) => c.id === session?.configId,
  );
  const relatedTasks = session
    ? rows("tasks")
        .filter(
          (t) =>
            t.sessionId === session.id &&
            t.status !== "cancelled" &&
            t.status !== "completed",
        )
        .slice(0, 4)
    : [];
  function selectSession(id: string) {
    setSessionId(id);
    setRun(null);
    setStream("");
    setEvents([]);
    source.current?.close();
  }
  const active = run?.status === "running" || run?.status === "queued";
  const suggestedConfigId =
    session?.configId ??
    (rows("configs").length === 1 ? rows("configs")[0]?.id : undefined);
  async function refresh(silent = false) {
    if (!silent) setBusy(true);
    try {
      const names = [
        "configs",
        "personas",
        "sessions",
        "plugins",
        "tasks",
        "runs",
        "artifacts",
        "executions",
        "secrets",
        "tools",
        "notifications",
        "deliveries",
        "model-calls",
      ];
      const values = await Promise.all(names.map((n) => api("/" + n)));
      setData(Object.fromEntries(names.map((n, i) => [n, values[i] ?? []])));
      setStatus(await api("/status"));
    } catch (e) {
      message.error((e as Error).message);
    } finally {
      if (!silent) setBusy(false);
    }
  }
  useEffect(() => {
    api("/me")
      .then(() => setLogged(true))
      .catch(() => setLogged(false));
    return () => source.current?.close();
  }, []);
  useEffect(() => {
    if (logged) void refresh();
  }, [logged]);
  useEffect(() => {
    end.current?.scrollIntoView({ behavior: "auto", block: "end" });
  }, [stream, sessionId, page, active, session?.messages?.length]);
  useEffect(() => {
    if (
      !logged ||
      (page !== "tasks" &&
        page !== "runs" &&
        page !== "mail" &&
        page !== "chat")
    )
      return;
    const id = setInterval(() => {
      void refresh(true);
    }, 10000);
    return () => clearInterval(id);
  }, [page, logged]);
  async function act(fn: () => Promise<unknown>, success = "已保存") {
    try {
      const result = await fn();
      message.success(success);
      await refresh();
      return result;
    } catch (e) {
      message.error((e as Error).message);
      return undefined;
    }
  }
  function open(kind: string, value: Row = {}) {
    setEdit({ kind, value });
    form.resetFields();
    if (kind === "config")
      form.setFieldsValue({
        kind: "api",
        protocol: "openai-chat",
        maxSteps: 12,
        maxTokens: 4096,
        maxInputBytes: 98304,
        timeoutSec: 180,
        capabilities: {
          tools: true,
          stream: true,
          images: false,
          resume: false,
        },
        ...value,
      });
    else if (kind === "persona")
      form.setFieldsValue({
        ...value,
        examplesJSON: pretty(value.examples ?? []),
        allTools: value.tools == null,
        tools: value.tools ?? [],
      });
    else if (kind === "plugin")
      form.setFieldsValue({ config: value.config, grants: value.grants });
    else if (kind === "task")
      form.setFieldsValue({
        kind: "manual",
        timeZone: "Asia/Shanghai",
        catchupSec: 3600,
        notify: true,
        configId: session?.configId ?? rows("configs")[0]?.id,
        personaId:
          session?.personaId ??
          rows("personas").find((p) => p.default)?.id ??
          "secretary",
        sessionId: session?.id,
        ...value,
        steps: (
          value.steps ?? [{ id: "step1", kind: "agent", prompt: "" }]
        ).map((s: Row) => ({ ...s, argumentsJSON: pretty(s.arguments ?? {}) })),
      });
    else form.setFieldsValue(value);
  }
  async function save() {
    try {
      const v = await form.validateFields();
      if (!edit) return;
      let result: any;
      switch (edit.kind) {
        case "config":
          result = await api("/configs", { ...edit.value, ...v });
          break;
        case "persona": {
          const { examplesJSON, allTools, ...rest } = v;
          result = await api("/personas", {
            ...edit.value,
            ...rest,
            tools: allTools ? null : (rest.tools ?? []),
            examples: parseJSON(examplesJSON, []),
          });
          break;
        }
        case "session":
          result = await api("/sessions", { ...edit.value, ...v });
          setSessionId(result.id);
          setPage("chat");
          break;
        case "secret":
          result = await api("/secrets", v);
          break;
        case "plugin":
          result = await api("/plugins/" + edit.value.id + "/configure", v);
          break;
        case "register":
          result = await api("/plugins/register", v);
          break;
        case "task": {
          const steps = v.steps.map((s: Row) => ({
            id: s.id,
            kind: s.kind,
            delaySec: s.delaySec ?? 0,
            ...(s.kind === "agent"
              ? { prompt: s.prompt }
              : {
                  tool: s.tool,
                  arguments: parseJSON(s.argumentsJSON ?? "{}", {}),
                }),
          }));
          result = await api("/tasks", {
            ...edit.value,
            ...v,
            steps,
            runAt: v.runAt ? new Date(v.runAt).toISOString() : undefined,
          });
          break;
        }
      }
      setEdit(null);
      message.success("已保存");
      await refresh();
      if (edit.kind === "task" && v.kind === "manual")
        message.info("任务已保存，点击“立即执行”启动。");
      return result;
    } catch (e) {
      if (e instanceof Error) message.error(e.message);
    }
  }
  function watch(value: Row) {
    source.current?.close();
    setRun(value);
    setEvents([]);
    setStream("");
    const s = new EventSource("/api/runs/" + value.id + "/events");
    source.current = s;
    s.onmessage = (event) => {
      const item = JSON.parse(event.data);
      setEvents((old) => [...old.slice(-299), item]);
      if (item.type === "text.delta")
        setStream((old) => old + (item.data.text ?? ""));
      if (item.type === "completed" && item.data.text)
        setStream(item.data.text);
      if (item.type === "finished") {
        s.close();
        setRun({
          ...value,
          status: item.data.status,
          result: item.data.result,
        });
        void refresh();
      }
    };
    s.onerror = () => {
      s.close();
      api("/runs/" + value.id)
        .then((current) => {
          setRun(current);
          setStream(current.result ?? "");
          void refresh();
        })
        .catch((e) => message.error(e.message));
    };
  }
  async function send() {
    if (!sessionId || !input.trim()) return;
    try {
      const value = await api("/sessions/" + sessionId + "/messages", {
        message: input,
        requestId: crypto.randomUUID(),
      });
      setInput("");
      watch(value);
    } catch (e) {
      message.error((e as Error).message);
    }
  }
  async function importPersona(file: File) {
    try {
      const p = JSON.parse(await file.text());
      delete p.id;
      p.default = false;
      await api("/personas", p);
      await refresh();
      message.success("已导入人格");
    } catch (e) {
      message.error((e as Error).message);
    }
    return false;
  }
  function recordColumns(extra: any[] = []) {
    return [
      {
        title: "名称 / ID",
        dataIndex: "id",
        render: (_: unknown, r: Row) => (
          <div>
            <strong>
              {r.name ?? r.prompt?.slice(0, 45) ?? r.taskId ?? r.id}
            </strong>
            <div className="muted mono">{r.id}</div>
          </div>
        ),
      },
      {
        title: "状态",
        dataIndex: "status",
        render: (s: string) => <Status value={s} />,
      },
      ...extra,
      {
        title: "",
        render: (_: unknown, r: Row) => (
          <Button type="link" onClick={() => setInspect(r)}>
            详情
          </Button>
        ),
      },
    ];
  }
  if (logged === null)
    return (
      <div className="loading">
        <Spin size="large" />
      </div>
    );
  if (!logged)
    return (
      <div className="login">
        <div className="login-copy">
          <Brand />
          <div className="login-story">
            <span className="eyebrow">YOUR PERSONAL AI WORKSPACE</span>
            <h1>
              你的日常，
              <br />
              <span>有只猫在打理。</span>
            </h1>
            <p>
              聊聊想法，安排提醒，处理琐事。
              <br />
              一个有性格，也能做事的 AI 搭档。
            </p>
            <div className="login-orbit" aria-hidden="true">
              <span className="orbit-ring" />
              <span className="orbit-ring" />
              <div className="login-cat">
                <CatMark />
              </div>
            </div>
          </div>
          <div className="login-caption">
            A LITTLE PERSONALITY. A LOT LESS BUSYWORK.
          </div>
        </div>
        <Card className="login-card">
          <div className="eyebrow">CATBOT · PERSONAL WORKSPACE</div>
          <h2>欢迎回来</h2>
          <p className="login-intro">进入 catbot，接着把日子安排好。</p>
          <Form
            layout="vertical"
            onFinish={async (v) => {
              try {
                await api("/login", v);
                setLogged(true);
              } catch (e) {
                message.error((e as Error).message);
              }
            }}
          >
            <Form.Item
              name="password"
              label="管理员密码"
              rules={[{ required: true }]}
            >
              <Input.Password
                size="large"
                placeholder="输入管理员密码"
                autoComplete="current-password"
              />
            </Form.Item>
            <Button type="primary" htmlType="submit" block size="large">
              进入工作台 <ArrowRightOutlined />
            </Button>
          </Form>
          <p className="muted login-note">
            使用你的管理员密码，进入个人工作空间。
          </p>
        </Card>
      </div>
    );
  let content: ReactNode;
  if (page === "chat")
    content = (
      <div className="chat-layout">
        <aside className="conversation-list">
          <Button
            block
            icon={<PlusOutlined />}
            onClick={() =>
              open("session", {
                title: "新的对话",
                configId: suggestedConfigId,
                personaId:
                  rows("personas").find((p) => p.default)?.id ?? "secretary",
              })
            }
          >
            新对话
          </Button>
          <div className="mobile-session-select">
            <Select
              aria-label="选择会话"
              placeholder="选择会话"
              value={sessionId || undefined}
              onChange={selectSession}
              showSearch
              optionFilterProp="label"
              options={rows("sessions")
                .filter((s) => s.channel !== "task")
                .map((s) => ({
                  value: s.id,
                  label:
                    s.title ||
                    (s.channelRoom ? `群 ${s.channelRoom}` : "未命名会话"),
                }))}
            />
          </div>
          <div className="list-heading">
            最近会话{" "}
            <span>
              {rows("sessions").filter((s) => s.channel !== "task").length}
            </span>
          </div>
          {rows("sessions")
            .filter((s) => s.channel !== "task")
            .map((s) => (
              <button
                className={
                  "session-item " + (s.id === sessionId ? "selected" : "")
                }
                key={s.id}
                aria-current={s.id === sessionId ? "true" : undefined}
                title={s.title || "未命名会话"}
                onClick={() => selectSession(s.id)}
              >
                <MessageOutlined />
                <span>
                  {s.title ||
                    (s.channelRoom ? `群 ${s.channelRoom}` : "未命名会话")}
                  <small>
                    {rows("personas").find((p) => p.id === s.personaId)?.name ??
                      s.personaId}{" "}
                    ·{" "}
                    {s.channel === "qq"
                      ? s.channelRoom
                        ? `QQ群 ${s.channelRoom} · 发言人 ${s.recipient}`
                        : s.channelProvider === "onebot"
                          ? "QQ 个人号"
                          : "QQ 官方"
                      : s.channel}
                  </small>
                </span>
              </button>
            ))}
        </aside>
        <section className="chat-panel">
          {!session ? (
            <div className="chat-empty">
              <div className="mark">
                <CatMark />
              </div>
              <h2>今天有什么需要安排？</h2>
              <p>选择模型配置，开启第一段对话。</p>
              <div className="prompt-cards">
                <Card size="small">“帮我整理今天的邮件”</Card>
                <Card size="small">“每周五下午提醒我复盘”</Card>
              </div>
              <Button
                type="primary"
                onClick={() =>
                  open("session", {
                    title: "新的对话",
                    configId: suggestedConfigId,
                    personaId:
                      rows("personas").find((p) => p.default)?.id ??
                      "secretary",
                  })
                }
              >
                开启对话
              </Button>
              {rows("configs").length === 0 && (
                <Button
                  type="link"
                  onClick={() => {
                    setPage("configs");
                    open("config");
                  }}
                >
                  先添加模型配置
                </Button>
              )}
            </div>
          ) : (
            <>
              <div className="chat-title">
                <div>
                  <strong>{session.title || "未命名会话"}</strong>
                  {session.channelRoom && (
                    <Tag className="soft-tag">
                      群 {session.channelRoom} · 发言人 {session.recipient}
                    </Tag>
                  )}
                  <span className="muted">
                    {
                      rows("configs").find((c) => c.id === session.configId)
                        ?.name
                    }{" "}
                    /{" "}
                    {
                      rows("personas").find((p) => p.id === session.personaId)
                        ?.name
                    }
                  </span>
                </div>
                <Button size="small" onClick={() => open("session", session)}>
                  会话设置
                </Button>
              </div>
              <div className="messages">
                {(session.messages ?? []).map((m: Row, i: number) => (
                  <div className={"bubble-row " + m.role} key={i}>
                    <div className="avatar">
                      {m.role === "user" ? "我" : <CatMark />}
                    </div>
                    <div className="bubble">{m.content}</div>
                  </div>
                ))}
                {run && active && (
                  <>
                    <div className="bubble-row user">
                      <div className="avatar">我</div>
                      <div className="bubble">{run.prompt}</div>
                    </div>
                    <div className="bubble-row assistant">
                      <div className="avatar">
                        <CatMark />
                      </div>
                      <div className="bubble">
                        {stream || <span className="muted">正在处理…</span>}
                      </div>
                    </div>
                  </>
                )}
                {run && !active && run.status !== "completed" && (
                  <Alert
                    type="warning"
                    showIcon
                    message={"执行状态：" + run.status}
                    description={run.error || "查看运行记录了解详情"}
                  />
                )}
                <div ref={end} />
              </div>
              {events.some((e) => e.type.includes("tool.")) && (
                <div className="tool-strip">
                  <Tag color="processing">工具活动</Tag>
                  <span>
                    {events
                      .filter((e) => e.type.includes("tool."))
                      .slice(-1)
                      .map((e) => e.data.name + " · " + e.type)}
                  </span>
                  <Button
                    type="link"
                    size="small"
                    onClick={() => setInspect(events)}
                  >
                    查看过程
                  </Button>
                </div>
              )}
              {rows("notifications").some(
                (n) => n.sessionId === session.id && n.taskId,
              ) && (
                <div className="tool-strip">
                  <Tag className="soft-tag">任务通知</Tag>
                  <span>
                    {rows("notifications")
                      .filter((n) => n.sessionId === session.id && n.taskId)
                      .slice(-1)[0]
                      ?.text?.slice(0, 100)}
                  </span>
                  <Button
                    type="link"
                    size="small"
                    onClick={() =>
                      setInspect(
                        rows("notifications").filter(
                          (n) => n.sessionId === session.id && n.taskId,
                        ),
                      )
                    }
                  >
                    查看提醒
                  </Button>
                </div>
              )}
              <div className="composer">
                <TextArea
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  autoSize={{ minRows: 2, maxRows: 6 }}
                  placeholder="说说你想做的事… Shift + Enter 换行"
                  onKeyDown={(e) => {
                    if (
                      e.key === "Enter" &&
                      !e.shiftKey &&
                      !e.nativeEvent.isComposing
                    ) {
                      e.preventDefault();
                      if (!active) void send();
                    }
                  }}
                />
                <div>
                  <span className="compose-context">
                    <UserOutlined /> {selectedPersona?.name || "未选择人格"}
                    <span className="compose-divider">/</span>
                    <ApiOutlined /> {selectedConfig?.model || "未选择模型"}
                  </span>
                  {active ? (
                    <Button
                      onClick={() =>
                        act(
                          () => api("/runs/" + run!.id + "/cancel", {}),
                          "已请求取消",
                        )
                      }
                    >
                      停止
                    </Button>
                  ) : (
                    <Button
                      type="primary"
                      icon={<ArrowUpOutlined />}
                      onClick={send}
                      disabled={!input.trim()}
                    >
                      发送
                    </Button>
                  )}
                </div>
              </div>
            </>
          )}
        </section>
        {session && (
          <aside className="chat-context" aria-label="当前对话信息">
            <h3>当前人格</h3>
            <div className="context-persona">
              <div className="context-persona-head">
                <span className="avatar">
                  <CatMark />
                </span>
                <div>
                  <strong>{selectedPersona?.name || "未选择人格"}</strong>
                  <small>
                    {selectedPersona
                      ? `版本 ${selectedPersona.version}`
                      : "在会话设置中选择"}
                  </small>
                </div>
              </div>
              <p>
                {selectedPersona?.description ||
                  "在会话设置中选择人格，定义搭档的表达方式与偏好。"}
              </p>
              {selectedPersona && (
                <span className="context-detail">
                  {selectedPersona.tools == null
                    ? "使用全部已授权工具"
                    : `可用工具 ${selectedPersona.tools.length} 个`}
                </span>
              )}
            </div>
            <h3>这段对话的任务</h3>
            {relatedTasks.length ? (
              relatedTasks.map((task) => (
                <button
                  key={task.id}
                  className="context-task"
                  onClick={() => setPage("tasks")}
                >
                  <span>
                    <BellOutlined />
                    <strong>{task.name || "未命名任务"}</strong>
                  </span>
                  <small>
                    {task.kind === "once" && task.runAt
                      ? new Date(task.runAt).toLocaleString("zh-CN", {
                          month: "numeric",
                          day: "numeric",
                          hour: "2-digit",
                          minute: "2-digit",
                        })
                      : task.kind === "recurring"
                        ? "周期任务"
                        : "手动执行"}
                    {task.paused ? " · 已暂停" : ""}
                  </small>
                </button>
              ))
            ) : (
              <p className="context-empty">
                暂时没有待办任务。
                <br />
                需要提醒时，告诉 catbot 就好。
              </p>
            )}
            <Button
              type="link"
              className="context-link"
              onClick={() => setPage("tasks")}
            >
              查看全部任务 <ArrowRightOutlined />
            </Button>
            <div className="context-model">
              <ApiOutlined />
              <span>
                {selectedConfig?.model || "未选择模型"}
                <small>
                  {selectedConfig?.kind === "sdk"
                    ? "Agent SDK"
                    : "模型 API 直连"}
                </small>
              </span>
            </div>
          </aside>
        )}
      </div>
    );
  else if (page === "configs")
    content = (
      <>
        <div className="toolbar">
          <span>SDK 与 API 使用相同的人格和业务工具</span>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => open("config")}
          >
            添加配置
          </Button>
        </div>
        <div className="cards">
          {rows("configs").map((c) => (
            <Card
              key={c.id}
              title={
                <Space>
                  <ApiOutlined />
                  {c.name}
                </Space>
              }
              extra={<Tag>{c.kind.toUpperCase()}</Tag>}
            >
              <p className="model-name">{c.model}</p>
              <p className="muted">
                {c.kind === "sdk" ? c.provider : c.protocol}
              </p>
              <p className="endpoint">{c.baseUrl || "SDK 默认服务地址"}</p>
              <Space wrap>
                {c.capabilities?.tools && <Tag>工具</Tag>}
                {c.capabilities?.images && <Tag>图片</Tag>}
                {c.capabilities?.stream && <Tag>流式</Tag>}
              </Space>
              <div className="card-actions">
                <Button onClick={() => open("config", c)}>编辑</Button>
                <Button
                  onClick={() =>
                    act(
                      async () =>
                        setInspect(await api("/configs/" + c.id + "/test", {})),
                      "检查完成",
                    )
                  }
                >
                  连接检查
                </Button>
                <Popconfirm
                  title="删除此配置？"
                  onConfirm={() =>
                    act(() => api("/configs/" + c.id, undefined, "DELETE"))
                  }
                >
                  <Button danger type="text">
                    删除
                  </Button>
                </Popconfirm>
              </div>
            </Card>
          ))}
        </div>
        {!rows("configs").length && (
          <Empty description="添加第一条配置后即可开始对话" />
        )}
      </>
    );
  else if (page === "personas")
    content = (
      <>
        <div className="toolbar">
          <span>人格更新会在下一次执行生效</span>
          <Space>
            <Upload
              accept=".json"
              showUploadList={false}
              beforeUpload={importPersona}
            >
              <Button>导入 JSON</Button>
            </Upload>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => open("persona")}
            >
              创建人格
            </Button>
          </Space>
        </div>
        <div className="cards">
          {rows("personas").map((p) => (
            <Card
              key={p.id}
              title={
                <Space>
                  <span className="persona-avatar">{p.name.slice(0, 1)}</span>
                  {p.name}
                </Space>
              }
              extra={
                p.default ? (
                  <Tag className="soft-tag">默认</Tag>
                ) : (
                  <Tag>v{p.version}</Tag>
                )
              }
            >
              <p>{p.description || "尚未填写介绍"}</p>
              <p className="clamp muted">{p.systemPrompt}</p>
              <Tag>
                {p.tools === null
                  ? "全部已授权工具"
                  : (p.tools?.length ?? 0) + " 个工具"}
              </Tag>
              <div className="card-actions">
                <Button onClick={() => open("persona", p)}>编辑</Button>
                <Button
                  onClick={() =>
                    open("persona", {
                      ...p,
                      id: undefined,
                      name: p.name + " 副本",
                      default: false,
                    })
                  }
                >
                  复制
                </Button>
                <Button
                  type="text"
                  onClick={() => downloadJSON(p.name + ".json", p)}
                >
                  导出
                </Button>
                <Popconfirm
                  title="删除此人格？"
                  onConfirm={() =>
                    act(() => api("/personas/" + p.id, undefined, "DELETE"))
                  }
                >
                  <Button type="text" danger>
                    删除
                  </Button>
                </Popconfirm>
              </div>
            </Card>
          ))}
        </div>
      </>
    );
  else if (page === "mail")
    content = (
      <MailSettings
        plugins={rows("plugins")}
        configs={rows("configs")}
        personas={rows("personas")}
        sessions={rows("sessions")}
        tasks={rows("tasks")}
        executions={rows("executions")}
        onRefresh={refresh}
        onInspect={setInspect}
      />
    );
  else if (page === "plugins")
    content = (
      <>
        <div className="toolbar">
          <span>插件包独立运行，通过 MCP 暴露工具</span>
          <Button
            icon={<PlusOutlined />}
            type="primary"
            onClick={() => open("register")}
          >
            注册 / 更新插件
          </Button>
        </div>
        <div className="cards">
          {rows("plugins").map((p) => (
            <Card
              key={p.id}
              title={
                <Space>
                  <AppstoreOutlined />
                  {p.manifest.name}
                </Space>
              }
              extra={
                <Switch
                  checked={p.enabled}
                  onChange={(enabled) =>
                    act(
                      () => api("/plugins/" + p.id + "/enable", { enabled }),
                      enabled ? "已启用" : "已停用",
                    )
                  }
                />
              }
            >
              <Tag>v{p.manifest.version}</Tag>
              <Tag>{p.manifest.runtime === "binary" ? "原生程序" : "Node.js"}</Tag>
              <p className="muted">{p.manifest.description}</p>
              <Space wrap>
                {p.manifest.tools.map((t: Row) => (
                  <Tag key={t.name}>{t.name}</Tag>
                ))}
              </Space>
              <div className="card-actions">
                <Button onClick={() => open("plugin", p)}>配置与权限</Button>
                <Button
                  onClick={() =>
                    act(
                      () => api("/plugins/" + p.id + "/health", {}),
                      "健康检查通过",
                    )
                  }
                >
                  检查
                </Button>
                <Button
                  type="text"
                  onClick={() =>
                    act(
                      async () =>
                        setInspect(await api("/plugins/" + p.id + "/logs")),
                      "已读取日志",
                    )
                  }
                >
                  日志
                </Button>
              </div>
              {p.manifest.templates?.length > 0 && (
                <div className="templates">
                  <span className="muted">任务模板</span>
                  {p.manifest.templates.map((t: Row) => (
                    <Button
                      size="small"
                      key={t.id}
                      onClick={() =>
                        open("task", {
                          name: t.name,
                          steps: t.steps,
                          notifyWhen: t.notifyWhen,
                          notifyText: t.notifyText,
                        })
                      }
                    >
                      {t.name}
                    </Button>
                  ))}
                </div>
              )}
            </Card>
          ))}
        </div>
      </>
    );
  else if (page === "tasks")
    content = (
      <>
        <div className="toolbar">
          <span>默认不重叠执行 · 时区明确 · 每步保留结果</span>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => open("task")}
          >
            创建任务
          </Button>
        </div>
        <Table
          rowKey="id"
          dataSource={rows("tasks")}
          columns={[
            {
              title: "任务",
              dataIndex: "name",
              render: (name: string, t: Row) => (
                <div>
                  <strong>{name}</strong>
                  <div className="muted">
                    {t.steps.length} 个步骤 · {t.timeZone}
                  </div>
                </div>
              ),
            },
            {
              title: "安排",
              render: (_: unknown, t: Row) =>
                t.kind === "recurring" ? (
                  <code>{t.cron}</code>
                ) : t.kind === "once" ? (
                  new Date(t.runAt).toLocaleString()
                ) : (
                  "手动"
                ),
            },
            {
              title: "状态",
              dataIndex: "status",
              render: (s: string) => <Status value={s} />,
            },
            {
              title: "操作",
              render: (_: unknown, t: Row) => (
                <Space wrap>
                  <Button size="small" onClick={() => open("task", t)}>
                    编辑
                  </Button>
                  <Button
                    size="small"
                    onClick={() =>
                      act(
                        () => api("/tasks/" + t.id + "/trigger", {}),
                        "已提交执行",
                      )
                    }
                  >
                    立即执行
                  </Button>
                  <Button
                    size="small"
                    onClick={() =>
                      act(() =>
                        api(
                          "/tasks/" +
                            t.id +
                            "/" +
                            (t.paused ? "resume" : "pause"),
                          {},
                        ),
                      )
                    }
                  >
                    {t.paused ? "恢复" : "暂停"}
                  </Button>
                  <Popconfirm
                    title="取消任务及其正在执行的工作？"
                    onConfirm={() =>
                      act(() => api("/tasks/" + t.id + "/cancel", {}), "已取消")
                    }
                  >
                    <Button size="small" danger>
                      取消
                    </Button>
                  </Popconfirm>
                </Space>
              ),
            },
          ]}
          expandable={{ expandedRowRender: (t) => <JSONView value={t} /> }}
        />
        <h3>最近执行</h3>
        <Table
          rowKey="id"
          size="small"
          dataSource={rows("executions")}
          columns={recordColumns()}
          pagination={{ pageSize: 5 }}
        />
      </>
    );
  else if (page === "artifacts")
    content = (
      <>
        <div className="toolbar">
          <span>上传视频后，可用视频插件模板创建后台解析任务</span>
          <Upload
            action="/api/files"
            showUploadList={false}
            maxCount={1}
            onChange={(info) => {
              if (info.file.status === "done") {
                message.success("上传完成");
                void refresh();
              } else if (info.file.status === "error") {
                message.error(info.file.response?.error ?? "上传失败");
              }
            }}
          >
            <Button type="primary" icon={<PlusOutlined />}>
              上传文件
            </Button>
          </Upload>
        </div>
        <Table
          rowKey="id"
          dataSource={rows("artifacts")}
          columns={[
            {
              title: "文件 / 解析结果",
              dataIndex: "name",
              render: (name: string, r: Row) => (
                <div>
                  <strong>{name}</strong>
                  <div className="muted mono">{r.id}</div>
                </div>
              ),
            },
            { title: "类型", dataIndex: "mime" },
            {
              title: "来源",
              render: (_: unknown, r: Row) => r.pluginId || "上传",
            },
            {
              title: "时间",
              dataIndex: "createdAt",
              render: (t: string) => new Date(t).toLocaleString(),
            },
            {
              title: "操作",
              render: (_: unknown, r: Row) => (
                <Space>
                  <Button size="small" onClick={() => setInspect(r)}>
                    查看
                  </Button>
                  {!r.data && (
                    <Button size="small" href={"/api/files/" + r.id}>
                      下载
                    </Button>
                  )}
                  {!r.data && (
                    <Button
                      size="small"
                      onClick={() =>
                        open("task", {
                          name: "解析 " + r.name,
                          steps: [
                            {
                              id: "parse",
                              kind: "tool",
                              tool: "video__parse",
                              arguments: { artifactId: r.id },
                            },
                          ],
                        })
                      }
                    >
                      解析视频
                    </Button>
                  )}
                  {r.data && (
                    <Button
                      size="small"
                      onClick={() => downloadJSON(r.name + ".json", r.data)}
                    >
                      导出
                    </Button>
                  )}
                  {r.data && (
                    <Button
                      size="small"
                      onClick={() =>
                        open("task", {
                          name: "整理事项 · " + r.name,
                          steps: [
                            {
                              id: "extract",
                              kind: "agent",
                              prompt:
                                "读取产物 " +
                                r.id +
                                "，整理其中的事项。只有明确的事项和日期才创建任务，缺失信息请在结果中说明。",
                            },
                          ],
                        })
                      }
                    >
                      提取事项
                    </Button>
                  )}
                </Space>
              ),
            },
          ]}
        />
      </>
    );
  else if (page === "runs")
    content = (
      <>
        <Table
          rowKey="id"
          dataSource={rows("runs")}
          columns={recordColumns([
            {
              title: "策略",
              render: (_: unknown, r: Row) => (
                <>
                  <Tag>
                    {r.config.kind === "sdk"
                      ? r.config.provider
                      : r.config.protocol}
                  </Tag>
                  <span className="muted">{r.config.model}</span>
                </>
              ),
            },
            {
              title: "人格",
              render: (_: unknown, r: Row) =>
                r.persona.name + " v" + r.persona.version,
            },
            {
              title: "时间",
              dataIndex: "createdAt",
              render: (t: string) => new Date(t).toLocaleString(),
            },
            {
              title: "过程",
              render: (_: unknown, r: Row) => (
                <Button
                  size="small"
                  onClick={() => {
                    setSessionId(r.sessionId);
                    setPage("chat");
                    watch(r);
                  }}
                >
                  查看事件
                </Button>
              ),
            },
          ])}
        />
        <h3>插件模型调用</h3>
        <p className="muted">
          记录插件的生成与转写调用。用量仅显示服务返回的数据，不代表完整成本；中断或未返回用量的调用不会推算为零。
        </p>
        <Table
          rowKey="id"
          size="small"
          dataSource={rows("model-calls")}
          columns={recordColumns([
            {
              title: "插件",
              render: (_: unknown, r: Row) =>
                r.pluginId + " v" + r.pluginVersion,
            },
            {
              title: "模型调用",
              render: (_: unknown, r: Row) => (
                <>
                  <Tag>{r.kind === "transcribe" ? "转写" : "生成"}</Tag>
                  <div>{r.model}</div>
                  <span className="muted">{r.protocol}</span>
                </>
              ),
            },
            {
              title: "关联操作",
              dataIndex: "operationId",
              ellipsis: true,
              render: (v: string) => v || "未关联",
            },
            {
              title: "用量",
              render: (_: unknown, r: Row) =>
                r.usage == null ? (
                  <span className="muted">未返回</span>
                ) : (
                  <Button size="small" onClick={() => setInspect(r.usage)}>
                    查看已知用量
                  </Button>
                ),
            },
            {
              title: "时间 / 耗时",
              render: (_: unknown, r: Row) => (
                <>
                  <div>{new Date(r.startedAt).toLocaleString()}</div>
                  <span className="muted">
                    {r.durationMs == null ? "耗时未知" : r.durationMs + " ms"}
                  </span>
                </>
              ),
            },
          ])}
        />
        <h3>任务通知</h3>
        <Table
          rowKey="id"
          size="small"
          dataSource={rows("notifications")}
          columns={recordColumns([
            { title: "通知内容", dataIndex: "text", ellipsis: true },
            {
              title: "投递",
              render: (_: unknown, n: Row) =>
                n.status === "failed" ? (
                  <Button
                    size="small"
                    onClick={() =>
                      act(
                        () => api("/notifications/" + n.id + "/retry", {}),
                        "已重新投递",
                      )
                    }
                  >
                    重试投递
                  </Button>
                ) : n.status === "uncertain" ? (
                  "结果不确定，请核对接收方"
                ) : null,
            },
          ])}
        />
        <h3>QQ 投递记录</h3>
        <Table
          rowKey="id"
          size="small"
          dataSource={rows("deliveries")}
          columns={recordColumns()}
        />
      </>
    );
  else
    content = (
      <>
        <ModelConnections
          configs={rows("configs")}
          credentials={rows("secrets")}
          onAdd={(kind) => open("config", { kind })}
          onEdit={(config) => open("config", config)}
          onTest={(config) =>
            void act(
              async () =>
                setInspect(await api("/configs/" + config.id + "/test", {})),
              "检查完成",
            )
          }
          onDelete={(config) =>
            void act(() => api("/configs/" + config.id, undefined, "DELETE"))
          }
        />
        <div className="cards service-status">
          <Card title="服务状态">
            <p>
              PostgreSQL{" "}
              <Status
                value={
                  status.database === undefined
                    ? "检查中"
                    : status.database === "ok"
                      ? "active"
                      : "error"
                }
              />
            </p>
            <p>
              Temporal{" "}
              <Status
                value={
                  status.temporal === undefined
                    ? "检查中"
                    : status.temporal === "ok"
                      ? "active"
                      : "error"
                }
              />
            </p>
            <p>
              SDK 服务{" "}
              <Status
                value={
                  status.runtime === undefined
                    ? "检查中"
                    : typeof status.runtime === "object"
                      ? "active"
                      : "error"
                }
              />
            </p>

            <Alert
              type="info"
              message="连接正常不代表真实模型已联调。"
              description="通过对话分别检查各 SDK 和模型协议，并在运行记录中核对工具结果。"
            />
          </Card>
        </div>
        <QQConnections
          configs={rows("configs").map((x) => ({
            id: String(x.id),
            name: String(x.name),
          }))}
          personas={rows("personas").map((x) => ({
            id: String(x.id),
            name: String(x.name),
            tools: Array.isArray(x.tools) ? x.tools.map(String) : null,
          }))}
        />
        <div className="toolbar">
          <h3>
            <KeyOutlined /> 凭证库
          </h3>
          <Button onClick={() => open("secret")}>添加凭证</Button>
        </div>
        <Table
          rowKey="id"
          dataSource={rows("secrets")}
          columns={[
            { title: "名称", dataIndex: "name" },
            { title: "引用 ID", dataIndex: "id" },
            { title: "", render: () => <Tag>仅服务端使用</Tag> },
          ]}
        />
      </>
    );
  return (
    <Layout className="shell">
      <Layout.Sider
        width={222}
        theme="light"
        breakpoint="lg"
        collapsedWidth={64}
      >
        <Brand />
        <div className="nav-label">WORKSPACE</div>
        <Menu
          mode="inline"
          selectedKeys={[page]}
          items={pages}
          onClick={({ key }) => setPage(key)}
        />
        <div className="sidebar-bottom">
          <span
            className={"dot " + (status.temporal === "ok" ? "online" : "")}
          />
          个人工作台
          <Button
            type="text"
            icon={<LogoutOutlined />}
            title="退出登录"
            onClick={async () => {
              await api("/logout", {});
              source.current?.close();
              setLogged(false);
            }}
          />
        </div>
      </Layout.Sider>
      <Layout>
        <header className="topbar">
          <span>
            <span className="breadcrumb-brand">catbot</span>{" "}
            <span className="slash">/</span>{" "}
            {pages.find((p) => p.key === page)?.label}
          </span>
          <Space>
            <span className="workspace-label">个人工作空间</span>
            <Button
              type="text"
              icon={<ReloadOutlined spin={busy} />}
              onClick={() => void refresh()}
            >
              刷新
            </Button>
            <div className="user-dot">我</div>
          </Space>
        </header>
        <main className={"main " + (page === "chat" ? "chat-main" : "")}>
          <div className="page-heading">
            <div>
              <h1>{pages.find((p) => p.key === page)?.label}</h1>
              <p>{descriptions[page]}</p>
            </div>
            {page === "chat" && (
              <span className="subtle-date">
                {new Date().toLocaleDateString("zh-CN", {
                  month: "long",
                  day: "numeric",
                  weekday: "long",
                })}
              </span>
            )}
          </div>
          {content}
        </main>
      </Layout>
      <Editor
        edit={edit}
        form={form}
        rows={rows}
        onSave={save}
        onClose={() => setEdit(null)}
        onCreateCredential={async (value) => {
          const credential = await api("/secrets", value);
          const record = { ...credential, name: value.name };
          setData((current) => ({
            ...current,
            secrets: [...(current.secrets ?? []), record],
          }));
          return record;
        }}
      />
      <Drawer
        title="详情"
        open={inspect !== null}
        onClose={() => setInspect(null)}
        width={Math.min(window.innerWidth - 32, 820)}
      >
        <JSONView value={inspect} />
      </Drawer>
    </Layout>
  );
}
