// Settings-only presentation stays out of the shared API module and first-screen catalog.
import { t } from "../i18n";
import { SUPERVISE_PURPOSE } from "./background-supervisor";
import { api, enc } from "./api";
import { MANAGER_HEADER } from "./manager-choice";
import { modelSummary, providerAgent, providerName, RUNTIMES, runtimeLabel, type ModelConfigurationBody, type ModelDraft, type ModelProvider, type ModelPurpose, type ModelPurposeBody } from "./model-config";
import type { Machine } from "./types";

/** Older hubs omit launch; keep their known launcher purposes readable without using inheritance-group names. */
export function purposeLaunch(p: ModelPurpose): ModelPurpose["launch"] {
  if (p.kind !== "launcher") return undefined;
  if (p.launch) return p.launch;
  if (p.id === "inbox.agent" || p.id === "lark.agent") return "headless";
  return [p.id, p.parent].some((id) => id === "assistant" || id === "schedule" || id?.startsWith("task.")) ? "headless" : "engine";
}

function executionLabel(form: string): string {
  return form === "api" ? t("settings.direct_api") : form === "plugin" ? t("settings-nav.tab_extensions") : t(`settings-modelconfiguration.${form}`);
}

export function executionForm(p: ModelPurpose, providers: ModelProvider[] = [], machines: Machine[] = []): { label: string; backend: string } {
  const provider = providers.find((v) => v.id === p.effective?.provider);
  const launch = purposeLaunch(p);
  const agent = providerAgent(provider, p.effective?.provider);
  let backend: string;
  if (p.kind === "plugin") backend = providerName(p.effective?.provider, providers) ?? "";
  // A launcher picked for an API purpose runs Claude Code once per request on the main chat's computer, unless it is a
  // managed one with a Messages gateway, called over HTTP (#5823; the hub decides by protocol, a key marks a managed one).
  else if (p.kind === "api" && provider?.kind === "launcher" && ((provider.api && provider.api !== "messages") || !provider.has_key)) backend = "claude -p";
  else if (p.kind === "api") backend = { messages: "Messages", responses: "Responses", chat: "Chat" }[provider?.api || "messages"];
  else if (launch === "headless") {
    backend = agent === "codex" ? "codex app-server"
      : agent === "claude" ? (p.effective?.runtime === "agentsdk" ? "Agent SDK" : "claude -p")
      : runtimeLabel("cli", agent);
  } else {
    const machine = p.machine_id ? machines.find((m) => m.id === p.machine_id) : undefined;
    backend = `${machine ? machine.engine_default || machine.engine || "herdr" : t("lib-agent-groups.by_machine")} · ${agent}`;
  }
  return { label: executionLabel(launch || p.kind), backend };
}

/** What a purpose is for, in a sentence under its name: the hub's (`description`, #5086, in the request's language),
 *  else this page's own for the background supervisor (#5082), which a hub before #5086 sends without one. Nothing for
 *  any other purpose the hub describes nothing for. */
export function purposeDescription(p: Pick<ModelPurpose, "id" | "description">): string {
  return p.description?.trim() || (p.id === SUPERVISE_PURPOSE ? t("settings-modelconfiguration.supervise_description") : "");
}

/** Whether the hub's catalog lists purpose `id` itself (the background supervisor's since #5806): then the page asks
 *  nothing more for it. */
export function catalogLists(catalog: ModelConfigurationBody, id: string): boolean {
  return catalog.purposes.some((p) => p.id === id);
}

/** The catalog with purpose `id` right after `after` (the background supervisor's after Main chat, which it follows;
 *  #5082, #5806, docs/assistant/model-config.md "The background supervisor in the catalog"). A hub since #5806 lists
 *  the row last among its built-in purposes, so the apps that append their own copy keep their order: here it is
 *  moved up to its parent. A hub before that leaves it out and answers it by its own id: `fetched` is that answer, put
 *  in. Unchanged when there is no such row (an older hub still), when it is in place already, or without an `after` row. */
export function withPurposeAfter(catalog: ModelConfigurationBody, id: string, after: string, fetched?: ModelPurpose): ModelConfigurationBody {
  const at = catalog.purposes.findIndex((p) => p.id === id);
  const row = at >= 0 ? catalog.purposes[at] : fetched?.id === id ? fetched : undefined;
  const anchor = catalog.purposes.findIndex((p) => p.id === after);
  if (!row || anchor < 0 || at === anchor + 1) return catalog;
  const rest = catalog.purposes.filter((p) => p.id !== id);
  const to = rest.findIndex((p) => p.id === after) + 1;
  return { ...catalog, purposes: [...rest.slice(0, to), row, ...rest.slice(to)] };
}

export function purposeGroups(purposes: ModelPurpose[], query: string, providers: ModelProvider[] = []): [string, ModelPurpose[]][] {
  const q = query.trim().toLocaleLowerCase();
  return [[t("sidebar.persona_chats"), "headless"], [t("shellswitch.agents"), "engine"], [t("settings-modelconfiguration.processing"), "api"], ["", "plugin"]].map(([name, key]): [string, ModelPurpose[]] => {
    const group = [name, executionLabel(key)].filter(Boolean).join(" · ");
    return [group, purposes.filter((p) =>
      (p.id.startsWith("extension.") || p.kind === "plugin" ? "plugin" : purposeLaunch(p) || "api") === key
      && (!q || [p.name, p.group, p.id, group, modelSummary(p, providers)].some((s) => s.toLocaleLowerCase().includes(q))))];
  }).filter(([, rows]) => rows.length);
}

// What only Model settings does with a purpose (its editor's draft, what it sends, the catalog): here, so the first
// screen's lib/model-config carries none of it (startup-preload's budget).

/** "Runtime"'s options: only the ones the hub offers for this purpose (and this client knows). */
export function runtimeChoices(p: ModelPurpose, agent?: string): { id: string; label: string }[] {
  if (!p.runtimes?.length) return [];
  if (agent && agent !== "claude") return [{ id: "cli", label: runtimeLabel("cli", agent) }];
  return RUNTIMES.filter((r) => p.runtimes?.includes(r.id));
}

export const modelConfiguration = (manager?: string) => api<ModelConfigurationBody>("/model-config", { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
export function draftFor(p: ModelPurpose): ModelDraft {
  const runtime = runtimeChoices(p).length ? p.override?.runtime || p.effective?.runtime || "cli" : "";
  return { provider: p.override?.provider ?? "", model: p.override?.model ?? "", effort: p.override?.effort ?? "", runtime };
}

export function chooseProvider(draft: ModelDraft, provider: string): ModelDraft {
  return draft.provider === provider ? draft : { provider, model: "", effort: "", runtime: draft.runtime };
}

export function supportsField(p: ModelPurpose, field: Exclude<keyof ModelDraft, "runtime">): boolean {
  return (p.fields ?? ["provider", "model"]).includes(field);
}

/** A use of Agents the hub lets the user set apart (Lark /agent, the AI twin): it says so with fields of its own, and
 * follows Agents while they are empty. Other uses of a category have no fields and stay read-only. */
export function agentsOverride(p: ModelPurpose): boolean {
  return p.parent === "agent.new" && (p.fields ?? []).some((field) => field === "provider" || field === "model" || field === "effort");
}

export function modelPatch(p: ModelPurpose, draft: ModelDraft): Record<string, string | number | null> {
  const patch: Record<string, string | number | null> = { revision: p.revision };
  for (const field of ["provider", "model", "effort"] as const) {
    if (supportsField(p, field)) patch[field] = draft[field].trim() || null;
  }
  if (runtimeChoices(p).length && draft.runtime) patch.runtime = draft.runtime;
  return patch;
}

export function saveModelPurpose(p: ModelPurpose, draft: ModelDraft, manager?: string): Promise<ModelPurposeBody> {
  return api(`/model-config/purposes/${enc(p.id)}`, { method: "PATCH", json: modelPatch(p, draft), headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
}

export function providersFor(p: ModelPurpose, providers: ModelProvider[]): ModelProvider[] {
  const candidates = providers.filter((provider) =>
    p.provider_ids ? p.provider_ids.includes(provider.id) : provider.kind === p.kind,
  );
  return candidates.filter((provider) => !provider.hidden || provider.id === p.override?.provider || provider.id === p.effective?.provider);
}
