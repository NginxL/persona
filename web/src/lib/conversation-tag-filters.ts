// Shared presentation preference for the Assistant's tag-filter entries (#12018). Null/undefined is automatic;
// an array, including an empty one, is an exact user-customized set and order.
export const TAG_FILTERS_MAX = 50;

export function tagFilterEntries(inUse: readonly string[], chosen: readonly string[] | null | undefined): readonly string[] {
  return chosen == null ? inUse : chosen;
}
