// Single-source-of-truth resolution for New Agent's "Follow model settings" state (#12379):
// keeps the Follow row summary, bottom model pill, dropdown Current/Default badges,
// sidebar Starting placeholder icon, and actual launch payload aligned on the same effective binding.
import { defaultModel, type LauncherDefault, type LauncherModels } from "./launcher-models";
import type { ModelBinding } from "./model-config";
import { modelTitle } from "./new-agent-panel";
import { launcherTitle, type Named } from "./provider-names";
import { effortTitle, modeTitle } from "./session-actions";
import type { Launcher } from "./types";

/** Normalizes a launcher ID from `/machines/{mid}/launchers`'s `default` into a `launcher.<name>` provider ID. */
function launcherProviderId(provider: string): string {
  return provider.startsWith("launcher.") ? provider : `launcher.${provider}`;
}

/**
 * Resolves the single effective Follow-mode binding for a computer's New Agent form.
 *
 * `GET /machines/{mid}/launchers` is routed by machine ownership and returns `default` from the exact
 * manager that will execute `POST /sessions`. When present, it is authoritative over the separate
 * `/model-config/purposes/agent.new` response so the Follow row, model pill, dropdown badges, and
 * startup handoff cannot disagree. Older hubs omit `default` (`undefined`) and fall back to
 * `configEffective`.
 */
export function resolveFollowBinding(
  hubDefault: LauncherDefault | null | undefined,
  configEffective: ModelBinding | null | undefined,
): ModelBinding | null {
  if (hubDefault && hubDefault.provider) {
    return {
      provider: launcherProviderId(hubDefault.provider),
      ...(hubDefault.model ? { model: hubDefault.model } : {}),
      ...(hubDefault.effort ? { effort: hubDefault.effort } : {}),
      ...(hubDefault.permission_mode ? { permission_mode: hubDefault.permission_mode } : {}),
    };
  }
  if (hubDefault === null) {
    return configEffective !== null && configEffective !== undefined ? {} : null;
  }
  return configEffective ?? null;
}

/** Formats the "Follow model settings" row's detail line from the exact same effective binding used by the pill and panel. */
export function formatFollowDetail(
  configured: ModelBinding | null,
  runs: string,
  list: LauncherModels | null | undefined,
  names: readonly Named[],
): string {
  if (!configured) return "";
  const provider = runs || (configured.provider?.startsWith("launcher.") ? configured.provider.slice("launcher.".length) : "");
  return [
    provider && launcherTitle(provider, names),
    configured.model && modelTitle(list, configured.model),
    configured.effort && effortTitle(configured.effort),
    configured.permission_mode && modeTitle(configured.permission_mode),
  ]
    .filter(Boolean)
    .join(" · ");
}

/**
 * Resolves the concrete model ID advertised for a new Agent start, so the newly opened session's
 * model pill keeps showing the resolved model instead of falling back to "Default (recommended)"
 * before the first transcript turn reports `session.model`.
 */
export function resolvedStartModel(o: {
  model?: string;
  followed?: ModelBinding;
  launcher?: Pick<Launcher, "model">;
  list?: LauncherModels | null;
}): string | undefined {
  if (o.model) return o.model;
  if (o.list?.locked) return o.launcher?.model || undefined;
  if (o.followed?.model) return o.followed.model;
  const d = o.list ? defaultModel(o.list.models) : undefined;
  if (d?.resolved) return d.resolved;
  if (d && d.id !== "default") return d.id;
  return o.launcher?.model || undefined;
}
