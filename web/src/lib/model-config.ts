import { t } from "../i18n";
import { agentName, isTraeCli, normalizeTraeCliName } from "./agent-kind";
import { MANAGER_HEADER } from "./manager-choice";
import { api, enc } from "./api";
import type { ProviderAPI, ProviderModel } from "./providers";
import type { ModelCandidates } from "./model-candidates";

export interface ModelBinding {
  provider?: string;
  model?: string;
  effort?: string;
  /** Launcher-backed purposes only: default, acceptEdits, plan, auto or bypassPermissions. */
  permission_mode?: string;
  /** Main chat only (#2455): how it runs Claude, "cli" or "agentsdk"; absent is the CLI. */
  runtime?: string;
  /** Main chat only (#4633): the Claude profile its new conversations start in, "legacy" (this computer's own); absent
   *  is Persona's own profile. */
  config_dir?: string;
}

export interface ModelProvider {
  id: string;
  name: string;
  kind: "api" | "launcher" | "plugin";
  /** Launcher provenance: "persona" for managed providers, else "zshrc" / "path" / "builtin" / runtime name. */
  source?: string;
  agent?: string;
  api?: ProviderAPI;
  /** A current hub accepts purpose effort on this Chat connection; absent on older hubs. */
  chat_effort?: boolean;
  /** The connection's saved default, inherited when a purpose effort is unset. */
  effort?: string;
  model?: string;
  models?: ProviderModel[];
  /** Legacy Chat tier aliases resolved to the stored model IDs whose capabilities apply. */
  model_aliases?: Record<string, string>;
  /** Explicit permission modes supported by a runtime launcher when not using the standard set. */
  permission_modes?: string[];
  machines?: string[];
  hidden?: boolean;
  has_key?: boolean;
}

export interface ModelPurpose {
  id: string;
  name: string;
  /** What the purpose is for, one sentence in the request's language (#5086, #5806); none from an older hub, and
   *  none for a task's own, an extension's or a voice plugin's purpose. */
  description?: string;
  group: string;
  kind: "api" | "launcher" | "plugin";
  /** Read-only execution form; absent for direct API/plugin purposes and older hubs. */
  launch?: "headless" | "engine";
  parent?: string;
  /** Owning category; a child with nonempty fields can still have its own editable record. */
  category_id?: string;
  /** A category's read-only list of what follows it; none on a use (#7980: how a use set apart is told from a category). */
  uses?: { name: string }[];
  machine_id?: string;
  override: ModelBinding;
  effective: ModelBinding;
  revision: number;
  inherited_from?: string;
  status?: string;
  fields?: string[];
  provider_ids?: string[];
  /** Candidate model settings are offered by an extension's own purpose. */
  candidates?: ModelCandidates;
  /** Main chat only: the runtimes it can run on (an older hub: none, no "Runtime" row). */
  runtimes?: string[];
  /** Main chat only (#4633): the profiles its new conversations can start in (an older hub: none, no row). */
  config_dirs?: string[];
  /** What its calls of the last 7 UTC days added up to, the list's only (#10498): none without calls, none from an older hub. */
  usage?: ModelUsage;
}

export interface ModelUsage {
  calls: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
}

/** The tokens Settings › Models shows for a purpose (#10498): input + output + cache writes. Cache reads cost a fraction
 *  and would swamp the number, so they are left out of it and listed apart. */
export function usageTokens(u: ModelUsage): number {
  return u.input_tokens + u.output_tokens + u.cache_creation_input_tokens;
}

export interface ModelConfigurationBody {
  purposes: ModelPurpose[];
  providers: ModelProvider[];
}
export interface ModelPurposeBody {
  purpose: ModelPurpose;
  providers: ModelProvider[];
}
export interface ModelDraft {
  provider: string;
  model: string;
  effort: string;
  /** Launcher-backed purposes only; "" inherits from parent or launcher default. */
  permission_mode?: string;
  /** "" when the purpose has no runtime choice. */
  runtime: string;
  /** Set only when the purpose offers a profile choice (#4633): "private" or "legacy". */
  config_dir?: string;
}

/** The runtimes this client can name, in the order "Runtime" offers them. */
export const RUNTIMES: { id: string; label: string }[] = [
  { id: "cli", label: "Claude Code CLI" },
  { id: "agentsdk", label: "Agent SDK" },
];

/** The agent CLI/runtime a provider runs ("claude" by default). */
export function providerAgent(provider?: ModelProvider, id?: string): string {
  if (provider?.agent) return provider.agent;
  const pid = provider?.id ?? id ?? "";
  if (pid === "launcher.codex") return "codex";
  if (pid === "launcher.agy") return "agy";
  if (pid === "launcher.omp") return "omp";
  if (pid === "launcher.traex") return "traex";
  return "claude";
}

/** A runtime's name; none is the CLI, one this client does not know shows its id. */
export function runtimeLabel(id: string | undefined, agent?: string): string {
  if (agent === "codex") return "Codex CLI";
  if (agent === "agy") return "Antigravity ACP";
  if (agent === "omp") return "omp CLI";
  if (isTraeCli(agent)) return agentName(agent);
  return RUNTIMES.find((r) => r.id === (id || "cli"))?.label ?? id!;
}

export const modelPurpose = (id: string, manager?: string) => api<ModelPurposeBody>(`/model-config/purposes/${enc(id)}`, { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });

/** A provider's name for the page: its own, or nothing for one of the hub's old built-in ones (`legacy.*`) that is not
 *  set up, whose id means nothing to a person (docs/assistant/onboarding.md 4.5). */
export function providerName(id: string | undefined, providers: ModelProvider[] = []): string | undefined {
  if (!id) return undefined;
  const name = providers.find((v) => v.id === id)?.name || "";
  return modelProviderTitle({ id, name }) || (id.startsWith("legacy.") ? undefined : id);
}

export function modelProviderTitle({ id, name }: Pick<ModelProvider, "id" | "name">): string {
  const launcher = id.startsWith("launcher.") ? id.slice("launcher.".length) : "";
  return isTraeCli(launcher) ? normalizeTraeCliName(name || launcher) : name;
}

export function modelSummary(p: ModelPurpose, providers: ModelProvider[] = []): string {
  const name = providerName(p.effective?.provider, providers);
  const effProvider = providers.find((v) => v.id === p.effective?.provider);
  const effAgent = providerAgent(effProvider, p.effective?.provider);
  const rt = p.effective?.runtime;
  const runtime = effAgent === "claude" && rt && rt !== "cli" ? runtimeLabel(rt) : ""; // the CLI is the default: not worth a word
  return [name, p.effective?.model, p.effective?.effort && t("lib-model-config.effort", { effort: p.effective.effort }), runtime].filter(Boolean).join(" · ") || t("lib-model-config.not_configured");
}

export { runtimeFallbackText } from "./runtime-fallback";
