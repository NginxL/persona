import { withoutEffortQualifier } from "./launcher-models";

/** Keep catalog order, putting siblings at the position of their first variant. */
export function groupVariants<M>(models: readonly M[], baseOf: (model: M) => string | undefined): M[][] {
  const groups: M[][] = [];
  const byBase = new Map<string, M[]>();
  for (const model of models) {
    const base = baseOf(model);
    const group = base ? byBase.get(base) : undefined;
    if (group) {
      group.push(model);
    } else {
      const own = [model];
      groups.push(own);
      if (base) byBase.set(base, own);
    }
  }
  return groups;
}

export function variantName(names: string[], base: string): string {
  const plain = names.map(withoutEffortQualifier);
  return plain.every((name) => name === plain[0]) ? plain[0] || base : base;
}

/** Medium when listed, otherwise the first variant in the catalog. */
export function defaultVariant<M>(models: readonly M[], effortOf: (model: M) => string | undefined): M {
  return models.find((model) => effortOf(model) === "medium") ?? models[0];
}
