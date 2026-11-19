// Active model-switcher providers without subscription usage windows yet, loaded after mount by SidebarUsage so the
// cold first screen stays within FIRST_SCREEN_BUDGET (#10082, #10349).
import { useEffect } from "react";
import { assistantLaunchers, readAssistantLaunchers, useModelLists } from "../lib/session-models";
import { useStore } from "../lib/store";
import { liveAccounts, type UnconnectedProvider, type UsageAccount } from "../lib/usage-windows";

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
  agy: "Antigravity",
};

/** Providers offered in the model switcher that do not yet have a live subscription account in `live`. */
export function unconnectedUsageProviders(launchers: readonly UsageProviderCandidate[], live: readonly UsageAccount[]): UnconnectedProvider[] {
  const out: UnconnectedProvider[] = [];
  const seen = new Set<string>();
  for (const l of launchers) {
    if (!l.name || seen.has(l.name)) continue;
    seen.add(l.name);
    const title = l.display_name?.trim() || BUILTIN_TITLES[l.name] || l.name;
    const agent = (l.agent_type || l.agent || "claude").toLowerCase();
    const idLower = l.name.toLowerCase();
    const titleLower = title.toLowerCase();
    const matchedByProvider =
      !l.base_url &&
      ((agent === "codex" && live.some((a) => a.provider === "codex")) ||
        (agent === "claude" && live.some((a) => a.provider === "claude")) ||
        ((agent === "agy" || agent === "antigravity") && live.some((a) => a.provider === "agy" || a.provider === "antigravity")));
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
  const mid = useStore((s) => s.assistant?.machine_id || s.machines.find((m) => m.online)?.id || s.machines[0]?.id || "");
  useEffect(() => {
    if (mid) void readAssistantLaunchers(mid);
  }, [mid]);
  const launchers = mid ? assistantLaunchers(mid).info?.launchers : undefined;
  useEffect(() => {
    onChange(launchers ? unconnectedUsageProviders(launchers, liveAccounts(accounts)) : []);
  }, [launchers, accounts, version, onChange]);
  return null;
}
