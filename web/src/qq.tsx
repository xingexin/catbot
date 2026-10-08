import { useEffect, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Switch,
  Tag,
} from "antd";
import { api } from "./api";

const states: Record<string, string> = {
  unconfigured: "待配置",
  configured: "已配置 · 待真实联调",
  unavailable: "未登录或连接不可用",
  disabled: "已登录 · 接收已停用",
  account_mismatch: "登录账号与绑定不一致",
  online: "在线 · 已启用",
  error: "配置读取失败",
  unknown: "实现未提供连接检查",
};

const qqIDs = (value: string = "") => [
  ...new Set(value.split(/[\s,，]+/).filter(Boolean)),
];

export function QQConnections({
  configs,
  personas,
}: {
  configs: { id: string; name: string }[];
  personas: { id: string; name: string; tools?: string[] | null }[];
}) {
  const [data, setData] = useState<any>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [form] = Form.useForm();
  const { message } = App.useApp();
  async function refresh(fill = false) {
    setBusy(true);
    try {
      const result = await api("/qq");
      setData(result);
      setError("");
      if (fill)
        form.setFieldsValue({
          ...result.onebot,
          allowedUsers: result.onebot.allowedUserIds?.join("\n") ?? "",
          allowedGroups: result.onebot.allowedGroupIds?.join("\n") ?? "",
        });
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => {
    void refresh(true);
  }, []);
  const personal = data?.strategies?.find((s: any) => s.provider === "onebot");
  const official = data?.strategies?.find(
    (s: any) => s.provider === "official",
  );
  const choices = (rows: { id: string; name: string }[]) =>
    rows.map((x) => ({ value: x.id, label: x.name }));
  const groupPersonas = personas.filter((persona) =>
    Array.isArray(persona.tools),
  );
  return (
    <div className="cards qq-connections">
      <Card
        title="个人 QQ · OneBot 通道"
        extra={
          <Button loading={busy} onClick={() => void refresh()}>
            刷新连接
          </Button>
        }
      >
        {error && <Alert type="error" message={error} showIcon />}
        <p>
          <Tag color={personal?.state === "online" ? "success" : "default"}>
            {states[personal?.state] ?? "读取中"}
          </Tag>
          {personal?.account && (
            <span>
              {personal.nickname}（{personal.account}）
            </span>
          )}
        </p>
        <p>接入实现：{personal?.implementation ?? "读取中"}</p>
        <p>
          {data?.napcatWebUrl
            ? "先打开 NapCat 扫码登录，再配置允许使用 catbot 的联系人或群。"
            : "在你配置的消息接入服务中登录 QQ，再配置允许使用 catbot 的联系人或群。"}
        </p>
        <Space wrap style={{ marginBottom: 16 }}>
          {data?.napcatWebUrl && (
            <Button href={data.napcatWebUrl} target="_blank" rel="noreferrer">
              打开 NapCat 登录
            </Button>
          )}
          {personal?.account && (
            <Button
              onClick={() => form.setFieldValue("selfId", personal.account)}
            >
              填入当前登录 QQ
            </Button>
          )}
        </Space>
        <Form
          form={form}
          layout="vertical"
          onFinish={async (values) => {
            setBusy(true);
            try {
              await api(
                "/qq/onebot",
                {
                  enabled: !!values.enabled,
                  selfId: values.selfId?.trim() ?? "",
                  allowedUserIds: qqIDs(values.allowedUsers),
                  allowedGroupIds: qqIDs(values.allowedGroups),
                  configId: values.configId ?? "",
                  personaId: values.personaId ?? "",
                  groupPersonaId: values.groupPersonaId ?? "",
                },
                "PUT",
              );
              message.success("QQ 绑定已保存");
              await refresh(true);
            } catch (e: any) {
              message.error(e.message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Form.Item
            name="enabled"
            label="启用 QQ 消息接收与通知"
            valuePropName="checked"
          >
            <Switch />
          </Form.Item>
          <Form.Item name="selfId" label="接入服务登录的 QQ 号">
            <Input placeholder="作为 catbot 使用的 QQ 号" />
          </Form.Item>
          <Form.Item
            name="allowedUsers"
            label="允许联系人的 QQ 号"
            extra="一行一个，最多 20 个。控制私聊使用权限；只使用群聊时可留空。"
          >
            <Input.TextArea rows={2} placeholder="你的另一个 QQ 号" />
          </Form.Item>
          <Form.Item name="configId" label="新会话的模型配置">
            <Select
              options={choices(configs)}
              placeholder="选择模型或 Agent SDK"
            />
          </Form.Item>
          <Form.Item name="personaId" label="新私聊会话的人格">
            <Select options={choices(personas)} />
          </Form.Item>
          <Form.Item
            name="allowedGroups"
            label="允许使用的 QQ 群号"
            dependencies={["enabled", "allowedUsers"]}
            extra="一行一个，最多 20 个。机器人须已加入这些群；群内所有成员都可通过 @ 机器人使用，无需加入私聊联系人列表。"
            rules={[
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (
                    getFieldValue("enabled") &&
                    qqIDs(value).length === 0 &&
                    qqIDs(getFieldValue("allowedUsers")).length === 0
                  )
                    return Promise.reject(
                      new Error("启用时至少填写一个允许的联系人或 QQ 群"),
                    );
                  return Promise.resolve();
                },
              }),
            ]}
          >
            <Input.TextArea rows={2} placeholder="允许接收 @ 消息的 QQ 群号" />
          </Form.Item>
          <Form.Item
            name="groupPersonaId"
            label="新群聊会话的人格"
            dependencies={["allowedGroups", "enabled"]}
            extra="在「人格」中创建群人格，取消「使用所有已授权工具」，再选择群内确需使用的工具；也可不开放任何工具。"
            rules={[
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (
                    getFieldValue("enabled") &&
                    qqIDs(getFieldValue("allowedGroups")).length > 0 &&
                    !groupPersonas.some((persona) => persona.id === value)
                  )
                    return Promise.reject(
                      new Error("群聊需选择设有明确工具清单的人格"),
                    );
                  return Promise.resolve();
                },
              }),
            ]}
          >
            <Select
              allowClear
              showSearch
              optionFilterProp="label"
              options={choices(groupPersonas)}
              placeholder="选择单独配置工具权限的群人格"
              notFoundContent="请先创建具有明确工具清单的群人格"
            />
          </Form.Item>
          <Alert
            type="info"
            showIcon
            message="群聊始终仅在 @ 机器人时触发"
            description="不同群、不同发言人的上下文分别保存。回复和定时提醒发送到原群并 @ 发言人，群内成员均可看到；请只为群人格开放适合公开使用的工具。"
            style={{ marginBottom: 16 }}
          />
          <Button type="primary" htmlType="submit" loading={busy}>
            保存绑定
          </Button>
        </Form>
        <p className="muted">
          支持私聊文本与指定群的 @
          文本消息；图片、语音等消息会标注未解析。群人格独立配置，私聊设置保持不变。现有会话的模型和人格在对话页调整。停用后阻止新的消息接收和发送，已进入队列的执行仍可在运行记录取消。
        </p>
      </Card>
      <Card title="官方 QQ 机器人">
        <Tag>{states[official?.state] ?? "读取中"}</Tag>
        <p>接入实现：{official?.implementation ?? "读取中"}</p>
        <p>
          通过部署环境设置 QQ_APP_ID、QQ_SECRET、QQ_USER_OPENID、QQ_CONFIG_ID 和
          QQ_PERSONA_ID。
        </p>
        <p>
          回调路径：<code>/qq/webhook</code>。在 QQ 开放平台配置可访问的 HTTPS
          回调地址。
        </p>
        <p className="muted">
          可与个人号同时使用。回复与定时通知沿用会话来源；官方主动消息受平台权限及额度约束，发送结果在运行记录中查看。
        </p>
      </Card>
    </div>
  );
}
