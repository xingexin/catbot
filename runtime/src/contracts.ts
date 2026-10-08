export type ProviderName = "claude" | "codebuddy" | "codex";

export interface RunRequest {
  runId: string;
  sessionId: string;
  config: {
    provider: ProviderName;
    model: string;
    baseUrl?: string;
    maxSteps: number;
    timeoutSec?: number;
  };
  persona: {
    systemPrompt: string;
    preferences?: string;
    examples?: { role: string; content: string }[];
  };
  history: { role: string; content: string }[];
  prompt: string;
  nativeId?: string;
  apiKey?: string;
  gatewayUrl: string;
  gatewayToken: string;
}
export interface RunEvent {
  type: string;
  data: Record<string, unknown>;
}
export type Sink = (event: RunEvent) => Promise<void>;

export interface ExecutionContext {
  home: string;
  cwd: string;
  controller: AbortController;
  emit: Sink;
}

/** Providers own SDK-specific options and event translation. */
export interface SDKProvider {
  readonly name: ProviderName;
  execute(input: RunRequest, context: ExecutionContext): Promise<void>;
}

export type SDKQuery<Options> = (request: {
  prompt: string;
  options: Options;
}) => AsyncIterable<unknown>;
