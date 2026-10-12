import { useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  AutoComplete,
  Button,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  type FormInstance,
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import type { Row } from "./catbot";
import {
  CodeBuddyEnvironment,
  ExecutionKind,
  ModelLookup,
  SDKProvider,
  canListModels,
  codeBuddyEnvironmentOptions,
  executionKind,
  modelConnectionKey,
  modelAuthContextChanged,
  sdkProvider,
  type AvailableModel,
} from "./model-config";

import {
  APIProviderPreset,
  apiProviderOptions,
  apiEndpointKey,
  applyAPIProvider,
  inferAPIProvider,
  modelOriginChanged,
} from "./model-api-presets";

export function ModelConfigFields({
  form,
  credentials,
  onCreateCredential,
}: {
  form: FormInstance;
  credentials: Row[];
  onCreateCredential: () => void;
}) {
  const kindValue = Form.useWatch("kind", form);
  const providerValue = Form.useWatch("provider", form);
  const protocol = Form.useWatch("protocol", form);
  const baseUrl = Form.useWatch("baseUrl", form);
  const credentialId = Form.useWatch("credentialId", form);
  const environment = Form.useWatch("codebuddyEnvironment", {
    form,
    preserve: true,
  });
  const kind = executionKind(kindValue);
  const isSDK = kind === ExecutionKind.SDK;
  const codeBuddy =
    isSDK && sdkProvider(providerValue) === SDKProvider.CodeBuddy;
  const config = {
    kind: kindValue,
    provider: providerValue,
    protocol,
    baseUrl,
    credentialId,
    codebuddyEnvironment: environment,
  };
  const connectionKey = modelConnectionKey(config);
  const supportsList = canListModels(config);
  const lookup = useMemo(() => new ModelLookup(), []);
  const requestSequence = useRef(0);
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string>();
  const [selectedPreset, setSelectedPreset] = useState<{
    key: string;
    value: APIProviderPreset;
  }>();
  const endpointKey = apiEndpointKey(config);
  const preset =
    selectedPreset?.key === endpointKey
      ? selectedPreset.value
      : inferAPIProvider(config);
  function changeAPIPreset(value: APIProviderPreset) {
    const draft = applyAPIProvider(form.getFieldsValue(true), value);
    form.setFieldsValue({
      protocol: draft.protocol,
      baseUrl: draft.baseUrl,
      credentialId: draft.credentialId,
    });
    setSelectedPreset({ key: apiEndpointKey(draft), value });
  }

  useEffect(() => {
    lookup.invalidate();
    requestSequence.current += 1;
    setModels([]);
    setLoading(false);
    setLoaded(false);
    setError(undefined);
    return () => {
      lookup.invalidate();
      requestSequence.current += 1;
    };
  }, [lookup, connectionKey]);

  async function loadModels() {
    const sequence = ++requestSequence.current;
    setLoading(true);
    setError(undefined);
    try {
      // Do not validate name/model: discovery is also useful for an unsaved form.
      const result = await lookup.load(form.getFieldsValue(true), () =>
        modelConnectionKey(form.getFieldsValue(true)),
      );
      if (result) {
        setModels(result.models);
        setLoaded(true);
      }
    } catch (error) {
      setError(error instanceof Error ? error.message : "获取模型列表失败");
      setModels([]);
      setLoaded(false);
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }
  function changeKind(value: string) {
    if (
      modelAuthContextChanged(
        { kind: kindValue, provider: providerValue },
        { kind: value, provider: providerValue },
      )
    )
      form.setFieldValue("credentialId", undefined);
    if (executionKind(value) === ExecutionKind.SDK) {
      form.setFieldsValue({
        capabilities: {
          ...form.getFieldValue("capabilities"),
          images: false,
          stream: true,
          resume: true,
        },
      });
      if (sdkProvider(form.getFieldValue("provider")) === SDKProvider.CodeBuddy)
        form.setFieldValue(
          "codebuddyEnvironment",
          CodeBuddyEnvironment.Inherit,
        );
    } else {
      form.setFieldValue("codebuddyEnvironment", undefined);
    }
  }
  function changeProvider(value: string) {
    if (
      modelAuthContextChanged(
        { kind: kindValue, provider: providerValue },
        { kind: kindValue, provider: value },
      )
    )
      form.setFieldValue("credentialId", undefined);
    if (sdkProvider(providerValue) === sdkProvider(value)) return;
    form.setFieldValue(
      "codebuddyEnvironment",
      sdkProvider(value) === SDKProvider.CodeBuddy
        ? CodeBuddyEnvironment.Inherit
        : undefined,
    );
  }

  return (
    <>
      <Form.Item name="name" label="配置名称" rules={[{ required: true }]}>
        <Input />
      </Form.Item>
      <Form.Item
        name="kind"
        label="接入方式"
        extra="直接使用 GPT、Claude、DeepSeek、Kimi 或 GLM 的 Key 可选 API 直连；使用 Agent SDK 时选择对应 SDK。"
      >
        <Select
          onChange={changeKind}
          options={[
            { value: "api", label: "模型 API 直连" },
            { value: "sdk", label: "Agent SDK" },
          ]}
        />
      </Form.Item>
      {isSDK ? (
        <Form.Item
          name="provider"
          label="Agent SDK"
          rules={[{ required: true, message: "请选择 Agent SDK" }]}
        >
          <Select
            onChange={changeProvider}
            placeholder="选择 CodeBuddy、Claude 或 Codex"
            options={[
              { value: "codebuddy", label: "CodeBuddy Agent SDK" },
              { value: "claude", label: "Claude Agent SDK" },
              { value: "codex", label: "OpenAI Codex SDK（GPT）" },
            ]}
          />
        </Form.Item>
      ) : (
        <>
          <Form.Item
            label="服务商快捷配置"
            extra="选择后填入官方 API 地址与协议，可继续修改；模型和凭证由你选择。"
          >
            <Select
              aria-label="服务商快捷配置"
              value={preset === APIProviderPreset.Unknown ? undefined : preset}
              options={apiProviderOptions}
              onChange={changeAPIPreset}
            />
          </Form.Item>
          <Form.Item
            name="protocol"
            label="接口协议"
            extra="这里选择 API 的请求格式；请按你的 Base URL 实际支持的协议选择。"
          >
            <Select
              options={[
                { value: "openai-chat", label: "OpenAI Chat Completions" },
                { value: "openai-responses", label: "OpenAI Responses" },
                { value: "anthropic", label: "Anthropic Messages" },
              ]}
            />
          </Form.Item>
        </>
      )}
      {codeBuddy && (
        <Form.Item
          name="codebuddyEnvironment"
          label="CodeBuddy 接入环境"
          getValueProps={(value) => ({
            value:
              value == null || value === CodeBuddyEnvironment.Unknown
                ? CodeBuddyEnvironment.Inherit
                : value,
          })}
          rules={[{ required: true, message: "请选择 CodeBuddy 接入环境" }]}
          extra={
            environment === CodeBuddyEnvironment.IOA
              ? "使用企业 iOA API Key，环境会应用于模型列表与实际对话。Base URL 可留空使用该环境的默认地址。"
              : "选择 iOA（企业环境）可使用企业 API Key；跟随部署配置会沿用服务端的环境设置。"
          }
        >
          <Select options={codeBuddyEnvironmentOptions} />
        </Form.Item>
      )}
      <Form.Item
        name="baseUrl"
        label="Base URL"
        extra="切换服务地址后，请重新选择对应 API Key；同一站点内修改路径会保留选择。"
      >
        <Input
          onChange={(event) => {
            if (modelOriginChanged(baseUrl, event.target.value))
              form.setFieldValue("credentialId", undefined);
          }}
          placeholder={
            codeBuddy
              ? "可留空，使用所选 CodeBuddy 环境的默认地址"
              : "例如 https://api.openai.com/v1；SDK 可留空"
          }
        />
      </Form.Item>
      <Form.Item
        name="credentialId"
        label="API Key 凭证引用"
        extra="切换接入方式或 SDK 后，请重新选择对应的 API Key。"
      >
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          options={credentials.map((row) => ({
            value: row.id,
            label: row.name ?? row.id,
          }))}
        />
      </Form.Item>
      <Button type="link" onClick={onCreateCredential}>
        添加 API Key
      </Button>
      <Form.Item label="模型" required>
        <div className="model-discovery-input">
          <Form.Item
            name="model"
            noStyle
            rules={[{ required: true, message: "请选择或填写模型名称" }]}
          >
            <AutoComplete
              allowClear
              placeholder="选择或输入模型名称"
              aria-label="模型名称"
              options={models.map((model) => ({
                value: model.id,
                label: (
                  <div title={model.description}>
                    <span>{model.name}</span>
                    {model.name !== model.id && (
                      <span className="muted"> · {model.id}</span>
                    )}
                  </div>
                ),
                search: `${model.id} ${model.name}`,
              }))}
              filterOption={(input, option) =>
                String(option?.search ?? option?.value ?? "")
                  .toLowerCase()
                  .includes(input.toLowerCase())
              }
            />
          </Form.Item>
          {supportsList && (
            <Button
              loading={loading}
              icon={<ReloadOutlined />}
              onClick={() => void loadModels()}
            >
              获取模型列表
            </Button>
          )}
        </div>
        <p className="muted model-discovery-help">
          {supportsList
            ? loaded
              ? models.length
                ? isSDK
                  ? `已读取 ${models.length} 个模型；实际调用权限需通过对话验证，也可手动填写。`
                  : `已获取 ${models.length} 个模型；也可手动填写。模型能力仍需按实际支持情况设置。`
                : "服务未返回可选模型，仍可手动填写模型名称。"
              : isSDK
                ? codeBuddy
                  ? "读取所选环境的 SDK 模型目录；也可手动填写。"
                  : "读取 Claude SDK 的模型目录；实际调用权限需通过对话验证，也可手动填写。"
                : "按当前环境、地址与凭证获取可用模型，无需先保存配置；也可手动填写。"
            : "当前 SDK 暂不提供模型列表，请手动填写其支持的模型名称。"}
        </p>
        {error && (
          <Alert
            type="warning"
            showIcon
            message="获取模型列表失败"
            description={
              <>{error}。可检查环境、地址与凭证后重试，或继续手动填写。</>
            }
          />
        )}
      </Form.Item>
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
        {!isSDK && (
          <Form.Item name="maxTokens" label="最大输出 Token">
            <InputNumber min={1} max={32768} />
          </Form.Item>
        )}
      </div>
      {!isSDK && (
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
      )}
      <Space wrap>
        <Form.Item name={["capabilities", "tools"]} valuePropName="checked">
          <Checkbox>工具调用</Checkbox>
        </Form.Item>
        {!isSDK && (
          <>
            <Form.Item
              name={["capabilities", "stream"]}
              valuePropName="checked"
            >
              <Checkbox>流式输出</Checkbox>
            </Form.Item>
            <Form.Item
              name={["capabilities", "images"]}
              valuePropName="checked"
            >
              <Checkbox>图片输入</Checkbox>
            </Form.Item>
          </>
        )}
      </Space>
      {isSDK && (
        <p className="muted">
          SDK 提供流式输出和会话恢复；当前 SDK 仅支持文本输入。输出上限由
          SDK、模型和宿主的字节限制共同控制。
        </p>
      )}
      <p className="muted">
        能力需与所选模型一致。SDK 原生工具循环由 SDK 执行；模型 API
        的循环由本框架执行。
      </p>
    </>
  );
}
