// Turning a module off in this browser (#13434, docs/web/web.md "Modules"): Settings › Labs › Features, drawn by the
// generic configuration form (core/module-labs.ts). Every module is on by default, so an existing browser sees no
// change; switching one off makes it absent here (core/module-host.ts) and is undone by switching it on again.
//
// Scope: this browser only (localStorage), as everything else on the Labs page. It grants nothing and reaches neither
// the hub nor another device: a module the hub does not serve stays absent whatever is stored here.
//
// Storage: `persona.modules.off`, a JSON array of the module ids switched off; nothing stored while every one is on.
// What this build cannot read is taken as nothing switched off and left until a choice replaces it.

export const MODULE_SWITCH_KEY = "persona.modules.off";

const listeners = new Set<() => void>();

function readOff(): string[] {
  try {
    const raw = localStorage.getItem(MODULE_SWITCH_KEY);
    if (raw === null) return [];
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

// read once per change, not per call: the router asks on every render
let cache: string[] | null = null;
const off = () => (cache ??= readOff());

/** Whether this browser switched the module off. */
export function moduleSwitchedOff(id: string): boolean {
  return off().includes(id);
}

/** Switches a module on or off here; false when the browser does not keep it. */
export function setModuleSwitchedOff(id: string, value: boolean): boolean {
  const next = value ? [...new Set([...off(), id])] : off().filter((x) => x !== id);
  try {
    if (next.length) localStorage.setItem(MODULE_SWITCH_KEY, JSON.stringify(next));
    else localStorage.removeItem(MODULE_SWITCH_KEY);
    cache = null;
    if (moduleSwitchedOff(id) !== value) return false;
  } catch {
    return false;
  }
  listeners.forEach((l) => l());
  return true;
}

/** Called when a switch may have changed: here, or in another tab of this browser. */
export function subscribeModuleSwitches(l: () => void): () => void {
  listeners.add(l);
  const storage = (e: StorageEvent) => {
    if (e.key === null || e.key === MODULE_SWITCH_KEY) {
      cache = null;
      l();
    }
  };
  if (typeof window !== "undefined") window.addEventListener("storage", storage);
  return () => {
    listeners.delete(l);
    if (typeof window !== "undefined") window.removeEventListener("storage", storage);
  };
}

/** Tests only: read storage again. */
export function forgetModuleSwitches(): void {
  cache = null;
}
