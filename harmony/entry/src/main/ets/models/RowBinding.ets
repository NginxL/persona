let nextNativeBindingId: number = 0;

function nextNativeBindingKey(): string {
  nextNativeBindingId++;
  return 'row-binding.' + String(nextNativeBindingId);
}

/** Renderer-owned observable reference for an immutable model row. */
@ObservedV2
export class BoundRow<T> {
  readonly identity: string;
  readonly key: string;
  @Trace value: T;

  constructor(identity: string, value: T) { this.identity = identity; this.key = nextNativeBindingKey(); this.value = value; }
}

/**
 * Reconcile renderer wrappers to the current immutable model rows. A wrapper is
 * reused only inside the same owner and scope; duplicate model keys get a
 * distinct occurrence key so they never share one wrapper.
 */
export function reconcileBoundRows<T>(current: BoundRow<T>[], rows: T[], keyOf: (row: T) => string,
  currentOwner: Object | undefined, nextOwner: Object, currentScope: string, nextScope: string): BoundRow<T>[] {
  const previous: BoundRow<T>[] = currentOwner === nextOwner && currentScope === nextScope ? current : [];
  const available = new Map<string, BoundRow<T>[]>();
  previous.forEach((binding: BoundRow<T>) => {
    const bindings = available.get(binding.identity) || [];
    bindings.push(binding);
    available.set(binding.identity, bindings);
  });
  const occurrences = new Map<string, number>();
  const result: BoundRow<T>[] = [];
  rows.forEach((row: T) => {
    const stableKey = keyOf(row);
    const occurrence = occurrences.get(stableKey) || 0;
    occurrences.set(stableKey, occurrence + 1);
    const identity = JSON.stringify([stableKey, occurrence]);
    const matches = available.get(identity);
    const binding = matches && matches.length ? matches.shift() : undefined;
    if (binding) {
      binding.value = row;
      result.push(binding);
    } else {
      result.push(new BoundRow<T>(identity, row));
    }
  });
  return result;
}
