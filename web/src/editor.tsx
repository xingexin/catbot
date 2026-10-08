import { useEffect, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  type FormInstance,
} from "antd";
import { PlusOutlined } from "@ant-design/icons";
import type { Row } from "./secretary";
const { TextArea } = Input;
function SelectField({
  name,
  label,
  rows,
  required = true,
}: {
  name: string;
  label: string;
  rows: Row[];
  required?: boolean;
}) {
  return (
    <Form.Item
      name={name}
      label={label}
      rules={required ? [{ required: true }] : []}
    >
      <Select
        allowClear
        showSearch
        optionFilterProp="label"
        options={rows.map((r) => ({
          value: r.id,
          label: r.name ?? r.title ?? r.id,
        }))}
      />
    </Form.Item>
  );
}
export function Editor({
  edit,
  form,
  rows,
  onSave,
  onClose,
  onCreateCredential,
}: {
  edit: { kind: string; value: Row } | null;
  form: FormInstance;
  rows: (name: string) => Row[];
  onSave: () => Promise<any>;
  onClose: () => void;
  onCreateCredential: (value: { name: string; value: string }) => Promise<Row>;
}) {
  const [credentialOpen, setCredentialOpen] = useState(false);
  const [savingCredential, setSavingCredential] = useState(false);
  const [credentialForm] = Form.useForm();
  const executionKind = Form.useWatch("kind", form);
  const { message } = App.useApp();
  useEffect(() => {
    if (edit?.kind === "config" && executionKind === "sdk") {
      form.setFieldValue(["capabilities", "images"], false);
      form.setFieldValue(["capabilities", "stream"], true);
      form.setFieldValue(["capabilities", "resume"], true);
    }
  }, [edit?.kind, executionKind, form]);
  useEffect(() => {
    if (!edit) {
      setCredentialOpen(false);
      credentialForm.resetFields();
    }
  }, [edit, credentialForm]);
  async function saveCredential() {
    if (savingCredential) return;
    try {
      const value = await credentialForm.validateFields();
      setSavingCredential(true);
      const record = await onCreateCredential({
        name: value.name.trim(),
        value: value.value.trim(),
      });
      form.setFieldValue("credentialId", record.id);
      setCredentialOpen(false);
      credentialForm.resetFields();
      message.success("API Key 已保存并选用");
    } catch (error) {
      if (error instanceof Error) message.error(error.message);
    } finally {
      setSavingCredential(false);
    }
  }
  return (
    <Modal
      open={!!edit}
      title={
        edit
          ? (
              {
                config: "模型配置",
                persona: "人格设置",
                session: "会话设置",
                secret: "添加凭证",
                plugin: "插件配置与权限",
                task: "任务设置",
                register: "注册插件",
              } as Record<string, string>
            )[edit.kind]
          : ""
      }
      onCancel={() => {
        if (!credentialOpen) onClose();
      }}
      keyboard={!credentialOpen}
      maskClosable={!credentialOpen}
      closable={!credentialOpen}
      okButtonProps={{ disabled: credentialOpen }}
      cancelButtonProps={{ disabled: credentialOpen }}
      onOk={onSave}
      width={edit?.kind === "task" ? 800 : 640}
      forceRender
    >
      <Form form={form} layout="vertical">
        {edit?.value.id && <p className="muted mono">ID: {edit.value.id}</p>}
        {edit?.kind === "config" && (
          <>
            <Form.Item
              name="name"
              label="配置名称"
              rules={[{ required: true }]}
            >
              <Input />
            </Form.Item>
            <Form.Item
              name="kind"
              label="接入方式"
              extra="使用 CodeBuddy、Claude 或 Codex SDK 时，请选择 Agent SDK。"
            >
              <Select
                options={[
                  { value: "api", label: "模型 API 直连" },
                  { value: "sdk", label: "Agent SDK" },
                ]}
              />
            </Form.Item>
            <Form.Item noStyle shouldUpdate>
              {() =>
                form.getFieldValue("kind") === "sdk" ? (
                  <Form.Item
                    name="provider"
                    label="Agent SDK"
                    rules={[{ required: true, message: "请选择 Agent SDK" }]}
                  >
                    <Select
                      placeholder="选择 CodeBuddy、Claude 或 Codex"
                      options={[
                        { value: "codebuddy", label: "CodeBuddy Agent SDK" },
                        { value: "claude", label: "Claude Agent SDK" },
                        { value: "codex", label: "Codex SDK" },
                      ]}
                    />
                  </Form.Item>
                ) : (
                  <Form.Item
                    name="protocol"
                    label="接口协议"
                    extra="这里选择 API 的请求格式；请按你的 Base URL 实际支持的协议选择。"
                  >
                    <Select
                      options={[
                        {
                          value: "openai-chat",
                          label: "OpenAI Chat Completions",
                        },
                        {
                          value: "openai-responses",
                          label: "OpenAI Responses",
                        },
                        { value: "anthropic", label: "Anthropic Messages" },
                      ]}
                    />
                  </Form.Item>
                )
              }
            </Form.Item>
            <Form.Item name="model" label="Model" rules={[{ required: true }]}>
              <Input placeholder="端点实际支持的模型名称" />
            </Form.Item>
            <Form.Item name="baseUrl" label="Base URL">
              <Input placeholder="例如 https://api.openai.com/v1；SDK 可留空" />
            </Form.Item>
            <SelectField
              name="credentialId"
              label="API Key 凭证引用"
              rows={rows("secrets")}
              required={false}
            />
            <Button
              type="link"
              onClick={() => {
                credentialForm.resetFields();
                credentialForm.setFieldValue(
                  "name",
                  form.getFieldValue("name") || "模型 API Key",
                );
                setCredentialOpen(true);
              }}
            >
              添加 API Key
            </Button>
            <div className="form-grid">
              <Form.Item
                name="maxSteps"
                label="最大循环步数"
                help="API 限制模型轮次；Claude / CodeBuddy 使用 SDK turn 限制。Codex SDK 当前以超时限制整轮执行。"
              >
                <InputNumber min={1} max={50} />
              </Form.Item>
              <Form.Item name="timeoutSec" label="超时（秒）">
                <InputNumber min={1} max={3600} />
              </Form.Item>
              <Form.Item noStyle shouldUpdate>
                {() =>
                  form.getFieldValue("kind") === "api" && (
                    <Form.Item name="maxTokens" label="最大输出 Token">
                      <InputNumber min={1} max={32768} />
                    </Form.Item>
                  )
                }
              </Form.Item>
            </div>
            <Form.Item noStyle shouldUpdate>
              {() =>
                form.getFieldValue("kind") === "api" && (
                  <Form.Item
                    name="maxInputBytes"
                    label="上下文输入预算（字节）"
                    help="含人格、工具声明和历史。超出时裁剪旧对话；保留当前请求。需根据模型窗口调整，这不是精确 Token 数。"
                  >
                    <InputNumber
                      min={8192}
                      max={2097152}
                      step={8192}
                      style={{ width: 200 }}
                    />
                  </Form.Item>
                )
              }
            </Form.Item>
            <Form.Item noStyle shouldUpdate>
              {() => (
                <>
                  <Space wrap>
                    {[
                      ["tools", "工具调用"],
                      ...(form.getFieldValue("kind") === "sdk"
                        ? []
                        : [
                            ["stream", "流式输出"],
                            ["images", "图片输入"],
                          ]),
                    ].map(([key, label]) => (
                      <Form.Item
                        key={key}
                        name={["capabilities", key]}
                        valuePropName="checked"
                      >
                        <Checkbox>{label}</Checkbox>
                      </Form.Item>
                    ))}
                  </Space>
                  {form.getFieldValue("kind") === "sdk" && (
                    <p className="muted">
                      SDK 提供流式输出和会话恢复；当前 SDK 仅支持文本输入。
                      输出上限由 SDK、模型和宿主的字节限制共同控制。
                    </p>
                  )}
                </>
              )}
            </Form.Item>
            <p className="muted">
              能力需与所选模型一致。SDK 原生工具循环由 SDK 执行；模型 API
              的循环由本框架执行。
            </p>
          </>
        )}
        {edit?.kind === "persona" && (
          <>
            <Form.Item name="name" label="名称" rules={[{ required: true }]}>
              <Input />
            </Form.Item>
            <Form.Item name="description" label="介绍">
              <Input />
            </Form.Item>
            <Form.Item
              name="systemPrompt"
              label="系统提示词"
              rules={[{ required: true }]}
            >
              <TextArea rows={5} />
            </Form.Item>
            <Form.Item name="preferences" label="行为偏好">
              <TextArea rows={2} />
            </Form.Item>
            <Form.Item
              name="examplesJSON"
              label="示例对话（JSON，role 为 user 或 assistant）"
            >
              <TextArea rows={4} className="mono" />
            </Form.Item>
            <Form.Item name="allTools" valuePropName="checked">
              <Checkbox>使用所有已授权工具</Checkbox>
            </Form.Item>
            <Form.Item noStyle shouldUpdate>
              {() =>
                !form.getFieldValue("allTools") && (
                  <Form.Item name="tools" label="允许的人格工具">
                    <Select
                      mode="multiple"
                      options={rows("tools").map((t) => ({
                        value: t.name,
                        label: t.name,
                      }))}
                    />
                  </Form.Item>
                )
              }
            </Form.Item>
            <Form.Item name="default" valuePropName="checked">
              <Checkbox>默认人格</Checkbox>
            </Form.Item>
          </>
        )}
        {edit?.kind === "session" && (
          <>
            <Form.Item name="title" label="标题" rules={[{ required: true }]}>
              <Input />
            </Form.Item>
            <SelectField
              name="configId"
              label="模型配置"
              rows={rows("configs")}
            />
            <SelectField
              name="personaId"
              label="人格"
              rows={rows("personas")}
            />
            <p className="muted">
              切换执行策略后，以公共会话历史建立新上下文。
            </p>
          </>
        )}
        {edit?.kind === "secret" && (
          <>
            <Form.Item
              name="name"
              label="凭证名称"
              rules={[{ required: true }]}
            >
              <Input />
            </Form.Item>
            <Form.Item name="value" label="凭证值" rules={[{ required: true }]}>
              <Input.Password autoComplete="new-password" />
            </Form.Item>
            <p className="muted">
              使用 MASTER_KEY 加密保存；列表只返回凭证引用。
            </p>
          </>
        )}
        {edit?.kind === "register" && (
          <>
            <Form.Item
              name="directory"
              label="插件目录（相对于 PLUGIN_DIR）"
              rules={[{ required: true }]}
            >
              <Input placeholder="my-plugin" />
            </Form.Item>
            <p className="muted">
              目录必须包含 plugin.json
              和构建后的独立入口。更新代码需要提升版本号。
            </p>
          </>
        )}
        {edit?.kind === "plugin" && (
          <>
            {Object.entries(
              edit.value.manifest.configSchema.properties ?? {},
            ).map(([name, raw]) => {
              const prop = raw as Row;
              const password = prop.format === "password";
              return (
                <Form.Item
                  key={name}
                  name={["config", name]}
                  label={prop.title ?? name}
                  help={
                    password && edit.value.secrets?.[name]
                      ? "已保存；留空保留原凭证"
                      : prop.description
                  }
                  valuePropName={prop.type === "boolean" ? "checked" : "value"}
                  rules={
                    edit.value.manifest.configSchema.required?.includes(name) &&
                    !(password && edit.value.secrets?.[name])
                      ? [{ required: true }]
                      : []
                  }
                >
                  {prop.enum ? (
                    <Select
                      options={prop.enum.map((v: any) => ({
                        value: v,
                        label: String(v),
                      }))}
                    />
                  ) : prop.type === "boolean" ? (
                    <Switch />
                  ) : prop.type === "integer" || prop.type === "number" ? (
                    <InputNumber
                      min={prop.minimum}
                      max={prop.maximum}
                      placeholder={String(prop.default ?? "")}
                    />
                  ) : password ? (
                    <Input.Password autoComplete="new-password" />
                  ) : name.endsWith("ConfigId") ? (
                    <Select
                      allowClear
                      options={rows("configs")
                        .filter(
                          (c) =>
                            c.kind === "api" &&
                            (name !== "visionConfigId" ||
                              c.capabilities?.images) &&
                            (name !== "transcriptionConfigId" ||
                              ["openai-chat", "openai-responses"].includes(
                                c.protocol,
                              )),
                        )
                        .map((c) => ({ value: c.id, label: c.name }))}
                    />
                  ) : (
                    <Input />
                  )}
                </Form.Item>
              );
            })}
            <Form.Item name="grants" label="服务端授权">
              <Checkbox.Group
                options={[
                  ...new Set<string>(
                    edit.value.manifest.tools.flatMap(
                      (t: Row) => t.permissions ?? [],
                    ),
                  ),
                ].map((p) => ({ value: p, label: p }))}
              />
            </Form.Item>
            <Alert
              type="info"
              message="插件是受信任的本地进程。只安装你检查过的包；进程隔离不等于操作系统沙箱。"
            />
          </>
        )}
        {edit?.kind === "task" && (
          <>
            <Form.Item
              name="name"
              label="任务名称"
              rules={[{ required: true }]}
            >
              <Input />
            </Form.Item>
            <div className="form-grid">
              <Form.Item name="kind" label="触发方式">
                <Select
                  options={[
                    { value: "manual", label: "手动执行" },
                    { value: "once", label: "一次性延时" },
                    { value: "recurring", label: "周期执行" },
                  ]}
                />
              </Form.Item>
              <Form.Item
                name="timeZone"
                label="时区"
                rules={[{ required: true }]}
              >
                <Input />
              </Form.Item>
            </div>
            <Form.Item noStyle shouldUpdate>
              {() =>
                form.getFieldValue("kind") === "recurring" ? (
                  <>
                    <Form.Item
                      name="cron"
                      label="Cron 表达式"
                      rules={[{ required: true }]}
                    >
                      <Input placeholder="0 9 * * *（每天 9:00）" />
                    </Form.Item>
                    <Form.Item
                      name="catchupSec"
                      label="错过后的补执行窗口（秒）"
                    >
                      <InputNumber min={10} />
                    </Form.Item>
                  </>
                ) : form.getFieldValue("kind") === "once" ? (
                  <Form.Item
                    name="runAt"
                    label="执行时间（带时区）"
                    rules={[{ required: true }]}
                  >
                    <Input placeholder="2026-10-01T09:00:00+08:00" />
                  </Form.Item>
                ) : null
              }
            </Form.Item>
            <div className="form-grid">
              <SelectField
                name="configId"
                label="模型配置"
                rows={rows("configs")}
              />
              <SelectField
                name="personaId"
                label="人格"
                rows={rows("personas")}
              />
            </div>
            <Form.Item noStyle shouldUpdate>
              {() => (
                <SelectField
                  name="sessionId"
                  label="结果通知会话"
                  rows={rows("sessions").filter((s) => s.channel !== "task")}
                  required={!!form.getFieldValue("notify")}
                />
              )}
            </Form.Item>
            <Form.List name="steps">
              {(fields, { add, remove }) => (
                <>
                  {fields.map(({ key, name }) => (
                    <Card
                      key={key}
                      size="small"
                      className="step-card"
                      title={"步骤 " + (name + 1)}
                      extra={
                        <Button
                          size="small"
                          danger
                          type="text"
                          onClick={() => remove(name)}
                        >
                          移除
                        </Button>
                      }
                    >
                      <div className="form-grid">
                        <Form.Item
                          name={[name, "id"]}
                          label="步骤 ID"
                          rules={[{ required: true }]}
                        >
                          <Input />
                        </Form.Item>
                        <Form.Item name={[name, "kind"]} label="类型">
                          <Select
                            options={[
                              { value: "tool", label: "插件工具" },
                              { value: "agent", label: "Agent 请求" },
                            ]}
                          />
                        </Form.Item>
                      </div>
                      <Form.Item
                        name={[name, "delaySec"]}
                        label="执行前等待（秒，持久化 Timer）"
                      >
                        <InputNumber min={0} max={2678400} />
                      </Form.Item>
                      <Form.Item noStyle shouldUpdate>
                        {() =>
                          form.getFieldValue(["steps", name, "kind"]) ===
                          "tool" ? (
                            <>
                              <Form.Item
                                name={[name, "tool"]}
                                label="工具"
                                rules={[{ required: true }]}
                              >
                                <Select
                                  showSearch
                                  options={rows("tools")
                                    .filter(
                                      (t) => !t.name.startsWith("system__"),
                                    )
                                    .map((t) => ({
                                      value: t.name,
                                      label: t.name + " — " + t.description,
                                    }))}
                                />
                              </Form.Item>
                              <Form.Item
                                name={[name, "argumentsJSON"]}
                                label="参数 JSON"
                              >
                                <TextArea rows={4} className="mono" />
                              </Form.Item>
                            </>
                          ) : (
                            <Form.Item
                              name={[name, "prompt"]}
                              label="任务要求"
                              rules={[{ required: true }]}
                            >
                              <TextArea rows={3} />
                            </Form.Item>
                          )
                        }
                      </Form.Item>
                    </Card>
                  ))}
                  <Button
                    block
                    type="dashed"
                    icon={<PlusOutlined />}
                    onClick={() =>
                      add({ id: "step" + (fields.length + 1), kind: "agent" })
                    }
                  >
                    添加步骤
                  </Button>
                </>
              )}
            </Form.List>
            <p className="muted">
              后续工具参数可引用前面步骤的输出，例如：
              {"${steps.parse.artifactId}"}
            </p>
            <details>
              <summary>条件通知（高级）</summary>
              <div className="form-grid">
                <Form.Item
                  name="notifyWhen"
                  label="仅在此步骤结果为 true 时提醒（可选）"
                >
                  <Input placeholder="${steps.watch.changed}" />
                </Form.Item>
                <Form.Item name="notifyText" label="提醒内容来自步骤（可选）">
                  <Input placeholder="${steps.watch.notificationText}" />
                </Form.Item>
              </div>
            </details>
            <Space wrap>
              <Form.Item name="notify" valuePropName="checked">
                <Checkbox>完成后通知</Checkbox>
              </Form.Item>
              <Form.Item name="paused" valuePropName="checked">
                <Checkbox>暂时暂停</Checkbox>
              </Form.Item>
            </Space>
          </>
        )}
      </Form>
      <Modal
        open={credentialOpen}
        title="添加 API Key"
        forceRender
        okText="保存并选用"
        cancelText="取消"
        confirmLoading={savingCredential}
        closable={!savingCredential}
        maskClosable={!savingCredential}
        keyboard={!savingCredential}
        onOk={saveCredential}
        onCancel={() => {
          if (savingCredential) return;
          setCredentialOpen(false);
          credentialForm.resetFields();
        }}
      >
        <Form name="model-credential" form={credentialForm} layout="vertical">
          <Form.Item
            name="name"
            label="凭证名称"
            rules={[{ required: true, whitespace: true }]}
          >
            <Input />
          </Form.Item>
          <Form.Item
            name="value"
            label="API Key"
            rules={[{ required: true, whitespace: true }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <p className="muted">
            Key 加密保存在服务端。保存后自动选用于当前配置；列表不显示原文。
          </p>
        </Form>
      </Modal>
    </Modal>
  );
}
