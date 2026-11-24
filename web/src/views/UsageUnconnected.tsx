// Active model-switcher providers without subscription usage windows yet, loaded after mount by SidebarUsage so the
// cold first screen stays within FIRST_SCREEN_BUDGET (#10082, #10349).
import { useEffect } from "react";
import { assistantLaunchers, readAssistantLaunchers, useModelLists } from "../lib/session-models";
import { useStore } from "../lib/store";
import { canonicalUsageProvider, liveAccounts, useHiddenUsageProviders, useUsageWindowsReady, type UnconnectedProvider, type UsageAccount } from "../lib/usage-windows";

export interface UsageProviderCandidate {
  name: string;
  display_name?: string;
  agent?: string;
  agent_type?: string;
  base_url?: string;
}

const BUILTIN_TITLES: Readonly<Record<string, string>> = {
  claude: "Claude Code",
  codex: "Codex",
  antigravity: "Antigravity",
};

/** Providers offered in the model switcher that do not yet have a live subscription account in `live`, leaving out the
 *  ones the owner hid from the bars. A relay launcher matches by name or source. */
export function unconnectedUsageProviders(launchers: readonly UsageProviderCandidate[], live: readonly UsageAccount[], hidden: readonly string[] = []): UnconnectedProvider[] {
  const out: UnconnectedProvider[] = [];
  const seen = new Set<string>();
  const hiddenProviders = new Set(hidden.map(canonicalUsageProvider));
  const connectedProviders = new Set(live.map((a) => canonicalUsageProvider(a.provider)));
  for (const l of launchers) {
    if (!l.name || seen.has(l.name)) continue;
    seen.add(l.name);
    const launcherProvider = canonicalUsageProvider(l.name);
    const title = l.display_name?.trim() || BUILTIN_TITLES[launcherProvider] || l.name;
    const agent = (l.agent_type || l.agent || "claude").toLowerCase();
    const provider = canonicalUsageProvider(agent);
    if (hiddenProviders.has(launcherProvider) || hiddenProviders.has(canonicalUsageProvider(title)) ||
        (!l.base_url && hiddenProviders.has(provider))) continue;
    const idLower = l.name.toLowerCase();
    const titleLower = title.toLowerCase();
    const matchedByProvider = !l.base_url && Object.hasOwn(BUILTIN_TITLES, provider) && connectedProviders.has(provider);
    const matchedByName = live.some(
      (a) =>
        a.name.toLowerCase() === titleLower ||
        a.name.toLowerCase() === idLower ||
        a.sources?.some((s) => s.toLowerCase() === titleLower || s.toLowerCase() === idLower),
    );
    if (matchedByProvider || matchedByName) continue;
    out.push({ id: l.name, title, agent });
  }
  return out;
}

export function UsageUnconnected({ accounts, onChange }: { accounts: UsageAccount[]; onChange: (next: readonly UnconnectedProvider[]) => void }) {
  const version = useModelLists();
  const usageReady = useUsageWindowsReady();
  const hidden = useHiddenUsageProviders();
  const mid = useStore((s) => s.assistant?.machine_id || s.machines.find((m) => m.online)?.id || s.machines[0]?.id || "");
  useEffect(() => {
    if (mid) void readAssistantLaunchers(mid);
  }, [mid]);
  const launchers = mid ? assistantLaunchers(mid).info?.launchers : undefined;
  useEffect(() => {
    onChange(usageReady && launchers ? unconnectedUsageProviders(launchers, liveAccounts(accounts), hidden) : []);
  }, [usageReady, launchers, accounts, hidden, version, onChange]);
  return null;
}
