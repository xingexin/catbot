export type ProviderName = "claude" | "codebuddy" | "codex";

export enum CodeBuddyEnvironment {
  Unknown = 0,
  Inherit = 1,
  IOA = 2,
  CN = 3,
  International = 4,
}

export enum ModelListSource {
  Unknown = 0,
  Live = 1,
}

export interface ModelDiscoveryRequest {
  config: {
    provider: ProviderName;
    baseUrl?: string;
    codebuddyEnvironment?: CodeBuddyEnvironment;
  };
  apiKey?: string;
}

export interface ModelOption {
  id: string;
  name: string;
  description?: string;
}

export interface ModelDiscoveryContext {
  home: string;
  cwd: string;
  signal: AbortSignal;
  deferCleanup?: (cleanup: Promise<void>) => void;
}

export interface RunRequest {
  runId: string;
  sessionId: string;
  config: {
    provider: ProviderName;
    model: string;
    baseUrl?: string;
    codebuddyEnvironment?: CodeBuddyEnvironment;
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
  context?: string;
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
  discoverModels?(
    input: ModelDiscoveryRequest,
    context: ModelDiscoveryContext,
  ): Promise<ModelOption[]>;
}

export type SDKQuery<Options> = (request: {
  prompt: string;
  options: Options;
}) => AsyncIterable<unknown>;
