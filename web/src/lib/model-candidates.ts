import { t } from "../i18n";
import type { ModelProvider, ModelPurpose } from "./model-config";
import { cleanModelId } from "./model-id";
import { typedModelProblem } from "./model-id-input";

export type CandidateMode = "extension" | "off" | "custom";
export interface CandidateView {
  model: string;
  provider?: string;
  provider_name?: string;
  available: boolean;
  problem?: string;
  configured_model?: string;
}
export interface ModelCandidates {
  mode: CandidateMode;
  modes: CandidateMode[];
  most: number;
  direct_provider_ids?: string[];
  custom: CandidateView[];
  requested: { models: CandidateView[]; last_used: string }[];
}
interface CandidateBinding { provider: string; model: string }
export interface CandidateDraft { mode: CandidateMode; custom: (CandidateBinding & { key: number })[] }
export type CandidateUpdate = { mode: "extension" | "off" } | { mode: "custom"; custom: CandidateBinding[] };

export function candidateDraft(purpose: ModelPurpose): CandidateDraft | undefined {
  const setting = purpose.candidates;
  if (!purpose.id.startsWith("extension.") || !setting) return;
  return { mode: setting.mode, custom: setting.custom.map((row, key) => ({
    key, provider: row.provider ?? "", model: row.configured_model ?? row.model,
  })) };
}

export function candidateProviders(p: ModelPurpose, providers: ModelProvider[]): ModelProvider[] {
  const direct = p.candidates?.direct_provider_ids;
  return providers.filter((v) => p.provider_ids?.includes(v.id) && (direct !== undefined
    ? direct.includes(v.id)
    // Older hubs include CLI-only connections and legacy IDs in the purpose's ordinary binding choices.
    : v.id.startsWith("launcher.") && !v.hidden && v.has_key && (v.kind === "api" || v.kind === "launcher" && (!v.api || v.api === "messages"))));
}

/** Omit an unchanged setting entirely. In particular a binding edit on an older hub must preserve its stored defaults. */
export function candidateUpdate(setting: ModelCandidates, draft: CandidateDraft): CandidateUpdate | undefined {
  if (draft.mode !== "custom") return draft.mode === setting.mode ? undefined : { mode: draft.mode };
  const custom = draft.custom.map(({ provider, model }) => ({ provider, model: cleanModelId(model) }));
  if (setting.mode === "custom" && custom.length === setting.custom.length && custom.every((row, i) => {
    const saved = setting.custom[i];
    return row.provider === saved.provider && row.model === (saved.configured_model ?? saved.model);
  })) return;
  return { mode: "custom", custom };
}

export function candidateProblem(setting: ModelCandidates, draft: CandidateDraft, providers: ModelProvider[]): string {
  if (draft.mode !== "custom") return "";
  if (!providers.length) return t("settings-modelconfiguration.candidate_no_providers");
  if (!draft.custom.length || draft.custom.length > setting.most) return t("settings-modelconfiguration.candidate_count", { most: setting.most });
  const seen = new Set<string>();
  for (const row of draft.custom) {
    if (!providers.some((p) => p.id === row.provider)) return t("settings-modelconfiguration.candidate_choose_provider");
    const model = cleanModelId(row.model);
    const problem = typedModelProblem(model);
    if (problem) return problem;
    const pair = JSON.stringify([row.provider, model]);
    if (seen.has(pair)) return t("settings-modelconfiguration.candidate_duplicate");
    seen.add(pair);
  }
  return "";
}
