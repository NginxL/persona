/** Reconcile immutable JSON into a retained native-observable presentation tree. */
function record(value: Object | undefined): Record<string, Object> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, Object> : undefined;
}

/** Match the native extension renderer's keys; display values are not general identities. */
function identity(value: Object, index: number, property: string, parent: string): string {
  const item = record(value);
  if (item) {
    const name = property === 'choices' || property === 'items' && parent === 'tabs' ? 'value' :
      property === 'entries' ? 'path' : property === 'comments' ? 'id' :
      ['sections', 'items', 'tiles'].includes(property) ? 'key' : '';
    const key = name ? item[name] : property === 'buttons' || property === 'actions' && parent !== 'tiles' ? record(item['action'])?.['id'] : undefined;
    if (typeof key === 'string' && key.length > 0) return '@key:' + key;
  }
  return '@index:' + String(index);
}

function safeName(name: string): boolean { return name !== '__proto__' && name !== 'prototype' && name !== 'constructor'; }

function reconcileValue(previous: Object | undefined, incoming: Object, property: string, parent: string): Object {
  if (Array.isArray(incoming)) {
    const target: Object[] = Array.isArray(previous) ? previous as Object[] : [];
    const available = new Map<string, Object[]>();
    target.forEach((value: Object, index: number): void => {
      const key = identity(value, index, property, parent), values = available.get(key) || [];
      values.push(value); available.set(key, values);
    });
    const ordered = (incoming as Object[]).map((value: Object, index: number): Object =>
      reconcileValue(available.get(identity(value, index, property, parent))?.shift(), value, property, parent));
    ordered.forEach((value: Object, index: number): void => { if (target[index] !== value) target[index] = value; });
    if (target.length !== ordered.length) target.length = ordered.length;
    return target;
  }
  const next = record(incoming);
  if (next) {
    const target = record(previous) || {};
    reconcileRecord(target, next, property);
    return target;
  }
  return incoming;
}

function reconcileRecord(target: Record<string, Object>, incoming: Record<string, Object>, parent: string): void {
  const incomingKeys = Object.keys(incoming), names = new Set<string>(incomingKeys);
  for (const key of Object.keys(target)) if (!safeName(key) || !names.has(key)) delete target[key];
  for (const key of incomingKeys) if (safeName(key)) {
    const value = reconcileValue(target[key], incoming[key], key, parent);
    if (target[key] !== value) target[key] = value;
  }
}


function bounded(value: Object, depth: number, budget: number[]): boolean {
  if (depth > 64 || ++budget[0] > 50000) return false;
  if (Array.isArray(value)) return (value as Object[]).every((item: Object): boolean => bounded(item, depth + 1, budget));
  const object = record(value);
  return !object || Object.keys(object).every((key: string): boolean => !safeName(key) || bounded(object[key], depth + 1, budget));
}

export function reconcileJsonProjection(target: Record<string, Object>, incoming: Record<string, Object>): void {
  if (!bounded(incoming, 0, [0])) throw new Error('projection_limit');
  reconcileRecord(target, incoming, '');
}
