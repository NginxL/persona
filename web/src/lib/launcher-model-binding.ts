// Model and effort are edited together in settings and scheduled task forms. Keep this out of the chat's startup path.
import { useEffect } from "react";
import { ALL_EFFORTS, defaultModel, effortVariantBase, listedModel, modelName, type AgentModel, type LauncherModels } from "./launcher-models";
import type { ModelBinding } from "./model-config";
import { defaultVariant, groupVariants, variantName } from "./model-variant-groups";
import { CODEX_EFFORTS } from "./session-actions";

const levelOf = (model: AgentModel) => model.default_effort;
const baseOf = (model: AgentModel) => model.custom ? undefined : effortVariantBase(model.id, levelOf(model));
const recommended = (models: AgentModel[]) => defaultVariant(models, levelOf);
const levelModel = (models: AgentModel[], effort?: string) => effort ? models.find((model) => levelOf(model) === effort) : undefined;

export type LauncherModelOption = AgentModel & { variants?: true };

/** A folded row keeps a concrete ID for selection, with its siblings' IDs and aliases available to search. */
export function foldLauncherModels(models: AgentModel[]): LauncherModelOption[] {
  return groupVariants(models, baseOf).map((group) => {
    if (group.length === 1) return group[0];
    const { id } = recommended(group);
    const base = baseOf(group[0])!;
    const name = variantName(group.map((model) => model.name), base);
    const aliases = group.flatMap((model) => [model.id, ...(model.aliases ?? [])]);
    return { id, name: modelName({ id: base, name }), resolved: base, aliases, variants: true };
  });
}

function variantsOf(models: AgentModel[], selected: AgentModel | undefined): AgentModel[] | undefined {
  const base = selected && baseOf(selected);
  if (!base) return;
  const siblings = models.filter((model) => baseOf(model) === base);
  return siblings.length > 1 ? siblings : undefined;
}

type BindingPatch = { model: string; effort: string };
const asVariant = (model: AgentModel): BindingPatch => ({ model: model.id, effort: "" });
const effortOrder = CODEX_EFFORTS.map((effort) => effort.id);

/** The visible level and the edits it performs. A supported legacy effort wins over the saved variant's suffix. */
export function launcherBindingState(
  list: LauncherModels | null | undefined,
  binding: ModelBinding,
  followed: ModelBinding = {},
  { allowed = ALL_EFFORTS, allowUnknown = true }: { allowed?: readonly string[]; allowUnknown?: boolean } = {},
) {
  const models = list?.models ?? [];
  const model = binding.model ?? "";
  const effort = binding.effort ?? "";
  const id = model.trim() || followed.model;
  const selected = id ? listedModel(models, id) ?? models.find((row) => row.resolved === id) : defaultModel(models);
  const variant = !id && selected?.resolved ? listedModel(models, selected.resolved) ?? selected : selected;
  const variants = variantsOf(models, variant);
  const legacyChoice = variants && levelModel(variants, effort);
  const explicitVariant = model && variants ? legacyChoice ?? variant : undefined;
  const preferred = legacyChoice ?? explicitVariant;
  const legacy = variants && effort ? (preferred ? asVariant(preferred) : { model: "", effort: "" }) : undefined;
  const value = explicitVariant?.default_effort ?? effort;
  const defaultEffort = variants
    ? levelOf(model ? recommended(variants) : levelModel(variants, followed.effort) ?? variant!)
    : followed.effort || selected?.default_effort;
  const choices = variants
    ? effortOrder.filter((level) => levelModel(variants, level))
    : selected && !selected.custom
      ? (selected.efforts ?? []).filter((level) => allowed.includes(level))
      : allowUnknown ? allowed : [];
  return {
    model: model && legacy ? legacy.model : model,
    effectiveModel: legacy?.model || id || selected?.resolved || (selected?.id === "default" ? "" : selected?.id),
    value,
    defaultEffort,
    choices,
    folded: !!variants,
    legacy,
    pickModel: (next: string): Partial<BindingPatch> => {
      const group = variantsOf(models, listedModel(models, next));
      return group ? asVariant(levelModel(group, value) ?? recommended(group)) : { model: next };
    },
    pickEffort: (next: string): Partial<BindingPatch> => {
      if (!variants) return { effort: next };
      const choice = next ? levelModel(variants, next) ?? explicitVariant : model ? recommended(variants) : undefined;
      return choice ? asVariant(choice) : { effort: "" };
    },
  };
}

/** Rewrite a conflicting draft once its catalog is known. The form's Save still owns all persistence. */
export function useCanonicalLauncherBinding(patch: BindingPatch | undefined, onChange: (patch: BindingPatch) => void) {
  useEffect(() => {
    if (patch) onChange(patch);
  }, [patch, onChange]);
}
