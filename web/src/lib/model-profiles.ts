import { MANAGER_HEADER } from "./manager-choice";
import { api, enc } from "./api";
import type { ModelBinding } from "./model-config";

/** The six model categories a profile can bind (server/internal/assistant/models.go modelCategoryIDs). */
export const PROFILE_CATEGORY_IDS = ["assistant", "agent.new", "kb", "inbox.triage", "agent.delegate", "translate"] as const;
export type ProfileCategoryId = (typeof PROFILE_CATEGORY_IDS)[number];

/** The model fields of a managed launcher a profile can set (server/internal/assistant/model_profiles.go LauncherModels). */
export const LAUNCHER_MODEL_FIELDS = [
  "model",
  "small_model",
  "subagent_model",
  "opus_model",
  "sonnet_model",
  "haiku_model",
  "fable_model",
  "effort",
] as const;
export type LauncherModelField = (typeof LAUNCHER_MODEL_FIELDS)[number];

/** Optional tier/helper model fields on a managed launcher (every model field other than the main model and effort). */
export const LAUNCHER_EXTRA_MODEL_FIELDS = [
  "small_model",
  "subagent_model",
  "opus_model",
  "sonnet_model",
  "haiku_model",
  "fable_model",
] as const;

export const MAX_MODEL_TIERS = 12;
export const MAX_PROFILE_NAME = 40;
export const MAX_PROFILE_DESCRIPTION = 300;
export const MAX_TIER_DESCRIPTION = 100;
export const MAX_PROFILE_VALUE = 200;
export const TIER_NAME_RE = /^[a-z][a-z0-9_-]{0,23}$/;
export const TIER_SPEEDS = ["", "fast", "ultrafast"] as const;

export type ModelProfileLauncherModels = Partial<Record<LauncherModelField, string>>;

export interface ModelProfileTier {
  launcher: string;
  model?: string;
  effort?: string;
  permission_mode?: string;
  speed?: string;
  description?: string;
}

export interface ModelProfile {
  id: string;
  name: string;
  description?: string;
  purposes?: Record<string, ModelBinding>;
  launchers?: Record<string, ModelProfileLauncherModels>;
  /** The Agent presets (#2225): what agent_start / task_plan's tier and "New agent"'s presets run on, and when to use each. */
  tiers?: Record<string, ModelProfileTier>;
  active?: boolean;
  changes: string[];
}

export interface ModelProfiles {
  revision?: number;
  active?: string;
  profiles: ModelProfile[];
  rollback?: { to?: string; changes: string[] };
}

export interface ModelProfileSaveInput {
  revision: number;
  name: string;
  description?: string;
  purposes?: Record<string, ModelBinding>;
  launchers?: Record<string, ModelProfileLauncherModels>;
  tiers?: Record<string, ModelProfileTier>;
  capture?: boolean;
}

type RequestContext = { token: string; tokenOwner: string; isAuthContextCurrent: () => boolean; signal?: AbortSignal };

export const modelProfiles = (manager?: string, context?: RequestContext) =>
  api<ModelProfiles>("/model-profiles", { ...context, headers: manager ? { [MANAGER_HEADER]: manager } : undefined });

export const saveModelProfile = (id: string, input: ModelProfileSaveInput, manager?: string, context?: RequestContext) =>
  api<ModelProfiles>(`/model-profiles/${enc(id)}`, {
    ...context,
    method: "PUT",
    json: input,
    headers: manager ? { [MANAGER_HEADER]: manager } : undefined,
  });

export const switchModelProfile = (id: string, manager?: string, context?: RequestContext) =>
  api<ModelProfiles>(`/model-profiles/${enc(id)}/apply`, { ...context, method: "POST", headers: manager ? { [MANAGER_HEADER]: manager } : undefined });

export const rollbackModelProfile = (manager?: string, context?: RequestContext) =>
  api<ModelProfiles>("/model-profiles/rollback", { ...context, method: "POST", headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
