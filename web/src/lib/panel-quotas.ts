// Quota answers for the unified model panel's rail (SessionModelPanel, AssistantModelPanel): each provider's gateway
// quota from GET /api/v1/launchers/{name}/quota, falling back to the live local subscription window (useUsageWindows)
// for plain local `codex` / `claude` launchers so the rail matches the sidebar's subscription bars.
import { useEffect, useMemo, useState } from "react";
import { api, enc, pastHub } from "./api";
import type { LauncherQuotaAnswer } from "./quota";
import { useStore } from "./store";
import { liveAccounts, useUsageWindows, type UsageAccount } from "./usage-windows";

export interface QuotaLauncher {
  name: string;
  base_url?: string;
}

/** Converts an unambiguous live local subscription account for `provider` ("codex" | "claude") on `machineName` into a
 *  `LauncherQuotaAnswer` for the model panel rail. Never guesses when multiple local accounts match. */
export function localSubscriptionQuota(accounts: readonly UsageAccount[], provider: string, machineName?: string): LauncherQuotaAnswer | undefined {
  if (provider !== "codex" && provider !== "claude") return undefined;
  const matched = accounts.filter(
    (a) => a.local && !a.stale && a.provider === provider && (!machineName || !a.machines?.length || a.machines.includes(machineName)),
  );
  if (matched.length !== 1) return undefined;
  const acct = matched[0];
  const budget = acct.windows
    .filter((w) => !w.model)
    .map((w) => ({
      label: w.label,
      used_ratio: Math.min(Math.max(w.used_percent, 0), 100) / 100,
      resets_at: w.resets_at,
    }));
  if (!budget.length) return undefined;
  return {
    checked_at: acct.sampled_at,
    quota: {
      gateway: acct.provider,
      gateway_name: acct.name,
      budget,
    },
  };
}

export function usePanelQuotas(mid: string, providers: readonly { id: string }[], launchers?: readonly QuotaLauncher[] | null): Readonly<Record<string, LauncherQuotaAnswer>> {
  const [fetched, setFetched] = useState<Readonly<Record<string, LauncherQuotaAnswer>>>({});
  const usage = useUsageWindows();
  const machineName = useStore((s) => s.machines.find((m) => m.id === mid)?.name);
  const providerIDs = JSON.stringify(providers.map((p) => p.id));

  useEffect(() => {
    if (!mid) return;
    let dead = false;
    setFetched({});
    for (const name of JSON.parse(providerIDs) as string[]) {
      // No refresh=1: the hub's one-minute answer is the cheap read; unsupported gateways simply draw no bar.
      api<LauncherQuotaAnswer>(`/launchers/${enc(name)}/quota?machine=${enc(mid)}`, { timeout: pastHub(45_000) })
        .then((answer) => {
          if (!dead) setFetched((was) => ({ ...was, [name]: answer }));
        })
        .catch(() => {});
    }
    return () => {
      dead = true;
    };
    // providerIDs stands for the launchers, not every model-list refresh.
  }, [mid, providerIDs]);

  return useMemo(() => {
    const live = liveAccounts(usage);
    if (!live.length) return fetched;
    const out: Record<string, LauncherQuotaAnswer> = { ...fetched };
    for (const { id } of providers) {
      if (out[id]?.quota) continue;
      const l = launchers?.find((x) => x.name === id);
      if (l?.base_url) continue;
      const local = localSubscriptionQuota(live, id, machineName);
      if (local) out[id] = local;
    }
    return out;
  }, [fetched, usage, providers, launchers, machineName]);
}
