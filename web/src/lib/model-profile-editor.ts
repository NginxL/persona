// Pure draft state, validation, and payload serialization for editing a ModelProfile
// (server/internal/assistant/model_profiles_edit.go). Kept separate from lib/model-profiles.ts
// so only the lazy-loaded editor chunk carries it.
import { t } from "../i18n";
import { EFFORT_LEVELS } from "./providers";
import { PERMISSION_MODES } from "./session-actions";
import { validModelId } from "./model-id";
import type { ModelBinding } from "./model-config";
import {
  LAUNCHER_MODEL_FIELDS,
  MAX_MODEL_TIERS,
  MAX_PROFILE_DESCRIPTION,
  MAX_PROFILE_NAME,
  MAX_PROFILE_VALUE,
  MAX_TIER_DESCRIPTION,
  PROFILE_CATEGORY_IDS,
  TIER_NAME_RE,
  type LauncherModelField,
  type ModelProfile,
  type ModelProfileLauncherModels,
  type ModelProfileSaveInput,
  type ModelProfileTier,
} from "./model-profiles";

export interface PurposeDraftEntry {
  included: boolean;
  provider: string;
  model: string;
  effort: string;
  permission_mode: string;
}

/** Tri-state per launcher field: undefined = leave unchanged on switch; "" = clear to launcher default; non-empty = set. */
export type LauncherDraftEntry = Partial<Record<LauncherModelField, string>>;

export interface TierDraftEntry {
  /** Stable React list key so renaming `name` keeps focus. */
  key: string;
  /** Original tier name when loaded from the profile, for preserving legacy unlisted model IDs unchanged. */
  originalName?: string;
  originalModel?: string;
  name: string;
  launcher: string;
  model: string;
  effort: string;
  permission_mode: string;
  speed: string;
  description: string;
}

export interface ProfileDraft {
  name: string;
  description: string;
  purposes: Record<string, PurposeDraftEntry>;
  launchers: Record<string, LauncherDraftEntry>;
  tiers: TierDraftEntry[];
}

export function categoryIdsForProfile(profile: Pick<ModelProfile, "purposes">, catalogPurposeIds?: readonly string[]): string[] {
  const ordered: string[] = [...PROFILE_CATEGORY_IDS];
  for (const id of catalogPurposeIds ?? []) {
    if ((PROFILE_CATEGORY_IDS as readonly string[]).includes(id) && !ordered.includes(id)) ordered.push(id);
  }
  for (const id of Object.keys(profile.purposes ?? {})) {
    if (!ordered.includes(id)) ordered.push(id);
  }
  return ordered;
}

export function profileToDraft(profile: ModelProfile, catalogPurposeIds?: readonly string[]): ProfileDraft {
  const purposes: Record<string, PurposeDraftEntry> = {};
  for (const id of categoryIdsForProfile(profile, catalogPurposeIds)) {
    const has = !!profile.purposes && Object.prototype.hasOwnProperty.call(profile.purposes, id);
    const b = has ? profile.purposes![id] : undefined;
    purposes[id] = {
      included: has,
      provider: b?.provider ?? "",
      model: b?.model ?? "",
      effort: b?.effort ?? "",
      permission_mode: b?.permission_mode ?? "",
    };
  }
  const launchers: Record<string, LauncherDraftEntry> = {};
  for (const [name, fields] of Object.entries(profile.launchers ?? {})) {
    const entry: LauncherDraftEntry = {};
    for (const field of LAUNCHER_MODEL_FIELDS) {
      if (Object.prototype.hasOwnProperty.call(fields, field) && fields[field] !== undefined) {
        entry[field] = fields[field];
      }
    }
    launchers[name] = entry;
  }
  const tiers: TierDraftEntry[] = Object.entries(profile.tiers ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, tier], index) => ({
      key: `tier-${index}-${name}`,
      originalName: name,
      originalModel: tier.model ?? "",
      name,
      launcher: tier.launcher ?? "",
      model: tier.model ?? "",
      effort: tier.effort ?? "",
      permission_mode: tier.permission_mode ?? "",
      speed: tier.speed ?? "",
      description: tier.description ?? "",
    }));
  return {
    name: profile.name ?? "",
    description: profile.description ?? "",
    purposes,
    launchers,
    tiers,
  };
}

function runeLength(value: string): number {
  return [...value].length;
}

function validProfileValue(value: string): boolean {
  return value === value.trim() && !/[\r\n\t\0]/.test(value) && new TextEncoder().encode(value).length <= MAX_PROFILE_VALUE;
}

const VALID_SPEEDS = new Set(["", "standard", "fast", "ultrafast"]);
const VALID_PERMISSION_MODES = new Set<string>(PERMISSION_MODES.map((m) => m.id));

/** Client-side checks mirroring server/internal/assistant/model_profiles_edit.go so users see clear inline reasons before saving. */
export function profileDraftProblems(draft: ProfileDraft, launcherKinds?: Readonly<Record<string, "launcher" | "api" | undefined>>): string[] {
  const problems: string[] = [];
  const name = draft.name.trim();
  if (!name || runeLength(name) > MAX_PROFILE_NAME || /[\r\n\0]/.test(name)) {
    problems.push(t("settings-modelprofiles.err_name", { max: MAX_PROFILE_NAME }));
  }
  const desc = draft.description.trim();
  if (runeLength(desc) > MAX_PROFILE_DESCRIPTION || desc.includes("\0")) {
    problems.push(t("settings-modelprofiles.err_description", { max: MAX_PROFILE_DESCRIPTION }));
  }
  for (const [id, p] of Object.entries(draft.purposes)) {
    if (!p.included) continue;
    for (const [field, raw] of [["provider", p.provider], ["model", p.model]] as const) {
      const value = raw.trim();
      if (value && !validProfileValue(value)) {
        problems.push(t("settings-modelprofiles.err_value", { where: id, field }));
      }
    }
    const permissionMode = p.permission_mode.trim();
    if (permissionMode && (!validProfileValue(permissionMode) || !VALID_PERMISSION_MODES.has(permissionMode))) {
      problems.push(t("settings-modelprofiles.err_value", { where: id, field: "permission_mode" }));
    }
  }
  for (const [launcherName, fields] of Object.entries(draft.launchers)) {
    const isApi = launcherKinds?.[launcherName] === "api";
    for (const field of LAUNCHER_MODEL_FIELDS) {
      const raw = fields[field];
      if (raw === undefined) continue;
      const value = raw.trim();
      if (field === "model" && !value) {
        problems.push(t("settings-modelprofiles.err_launcher_model", { launcher: launcherName }));
        continue;
      }
      if (!validProfileValue(value)) {
        problems.push(t("settings-modelprofiles.err_value", { where: launcherName, field }));
        continue;
      }
      if (field === "effort" && value && !EFFORT_LEVELS.includes(value)) {
        problems.push(t("settings-modelprofiles.err_effort", { where: launcherName, effort: value }));
      }
      if (isApi && field !== "model" && value) {
        problems.push(t("settings-modelprofiles.err_launcher_api", { launcher: launcherName, field }));
      }
    }
  }
  if (draft.tiers.length > MAX_MODEL_TIERS) {
    problems.push(t("settings-modelprofiles.err_tiers_limit", { max: MAX_MODEL_TIERS }));
  }
  const seenTiers = new Set<string>();
  for (const tier of draft.tiers) {
    const tierName = tier.name.trim();
    if (!TIER_NAME_RE.test(tierName)) {
      problems.push(t("settings-modelprofiles.err_tier_name", { tier: tierName || "?" }));
    } else if (seenTiers.has(tierName)) {
      problems.push(t("settings-modelprofiles.err_tier_duplicate", { tier: tierName }));
    } else {
      seenTiers.add(tierName);
    }
    const launcher = tier.launcher.trim();
    if (!launcher) {
      problems.push(t("settings-modelprofiles.err_tier_launcher", { tier: tierName || "?" }));
    } else if (!validProfileValue(launcher) || launcher.includes(" ")) {
      problems.push(t("settings-modelprofiles.err_value", { where: tierName || "?", field: "launcher" }));
    }
    const model = tier.model.trim();
    if (model) {
      const unchangedLegacy = tier.originalName === tierName && tier.originalModel === model;
      if (!validProfileValue(model) || model.includes(" ") || (!unchangedLegacy && !validModelId(model))) {
        problems.push(t("settings-modelprofiles.err_tier_model", { tier: tierName || "?", model }));
      }
    }
    const effort = tier.effort.trim();
    if (effort && !EFFORT_LEVELS.includes(effort)) {
      problems.push(t("settings-modelprofiles.err_effort", { where: tierName || "?", effort }));
    }
    const permissionMode = tier.permission_mode.trim();
    if (permissionMode && (!validProfileValue(permissionMode) || !VALID_PERMISSION_MODES.has(permissionMode))) {
      problems.push(t("settings-modelprofiles.err_value", { where: tierName || "?", field: "permission_mode" }));
    }
    const speed = tier.speed.trim();
    if (!VALID_SPEEDS.has(speed)) {
      problems.push(t("settings-modelprofiles.err_tier_speed", { tier: tierName || "?", speed }));
    }
    const tierDesc = tier.description.trim();
    if (runeLength(tierDesc) > MAX_TIER_DESCRIPTION || /[\r\n\0]/.test(tierDesc)) {
      problems.push(t("settings-modelprofiles.err_tier_description", { tier: tierName || "?", max: MAX_TIER_DESCRIPTION }));
    }
  }
  return problems;
}

/** Converts the editor draft into the PUT /api/v1/model-profiles/{id} body. Never emits credentials or config_dir. */
export function draftToProfileInput(draft: ProfileDraft, revision: number): ModelProfileSaveInput {
  const purposes: Record<string, ModelBinding> = {};
  for (const [id, entry] of Object.entries(draft.purposes)) {
    if (!entry.included) continue;
    const binding: ModelBinding = {};
    const provider = entry.provider.trim();
    const model = entry.model.trim();
    const effort = entry.effort.trim();
    const permissionMode = entry.permission_mode.trim();
    if (provider) binding.provider = provider;
    if (model) binding.model = model;
    if (effort) binding.effort = effort;
    if (permissionMode) binding.permission_mode = permissionMode;
    purposes[id] = binding;
  }
  const launchers: Record<string, ModelProfileLauncherModels> = {};
  for (const [name, fields] of Object.entries(draft.launchers)) {
    const out: ModelProfileLauncherModels = {};
    let any = false;
    for (const field of LAUNCHER_MODEL_FIELDS) {
      if (fields[field] === undefined) continue;
      const value = fields[field]!.trim();
      if (field === "model" && !value) continue;
      out[field] = value;
      any = true;
    }
    if (any) launchers[name] = out;
  }
  const tiers: Record<string, ModelProfileTier> = {};
  for (const entry of draft.tiers) {
    const tierName = entry.name.trim();
    if (!tierName) continue;
    const out: ModelProfileTier = { launcher: entry.launcher.trim() };
    const model = entry.model.trim();
    const effort = entry.effort.trim();
    const permissionMode = entry.permission_mode.trim();
    const speed = entry.speed.trim();
    const description = entry.description.trim();
    if (model) out.model = model;
    if (effort) out.effort = effort;
    if (permissionMode) out.permission_mode = permissionMode;
    if (speed) out.speed = speed;
    if (description) out.description = description;
    tiers[tierName] = out;
  }
  const out: ModelProfileSaveInput = {
    revision,
    name: draft.name.trim(),
    description: draft.description.trim(),
  };
  if (Object.keys(purposes).length > 0) out.purposes = purposes;
  if (Object.keys(launchers).length > 0) out.launchers = launchers;
  if (Object.keys(tiers).length > 0) out.tiers = tiers;
  return out;
}

export function isProfileDraftDirty(draft: ProfileDraft, original: ProfileDraft): boolean {
  return JSON.stringify(draftToProfileInput(draft, 0)) !== JSON.stringify(draftToProfileInput(original, 0));
}
