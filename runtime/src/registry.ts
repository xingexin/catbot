import type { ProviderName, SDKProvider } from "./contracts.js";
import { createCodeBuddyProvider } from "./providers/codebuddy.js";
import { createClaudeProvider } from "./providers/claude.js";
import { createCodexProvider } from "./providers/codex.js";

/** Construct once at startup; execution cannot mutate provider registration. */
export class ProviderRegistry {
  private readonly providers = new Map<string, SDKProvider>();

  constructor(providers: readonly SDKProvider[]) {
    for (const provider of providers) {
      if (this.providers.has(provider.name))
        throw new Error(`Duplicate Agent SDK provider: ${provider.name}`);
      this.providers.set(provider.name, provider);
    }
  }

  get(name: string): SDKProvider {
    const provider = this.providers.get(name);
    if (!provider) throw new Error("Unsupported Agent SDK");
    return provider;
  }

  names(): ProviderName[] {
    return [...this.providers.keys()] as ProviderName[];
  }
}

export const providerRegistry = new ProviderRegistry([
  createCodeBuddyProvider(),
  createClaudeProvider(),
  createCodexProvider(),
]);
