// Settings › Labs' terminal renderer as this browser keeps it (#9877, docs/web/web.md "Labs"). The Labs page shows it
// through the generic configuration form (lib/labs-terminal.ts turns it into a Document); Single pane terminals read it
// here, once, when they open (views/session/terminal/feed.ts).
//
// Scope: this browser only (localStorage). GPU support differs per browser and machine, so the choice never reaches the
// hub, another browser, another device or another user, and it grants nothing: it picks between two renderers the page
// already has.
//
// Storage: the key and its "1" are the Terminal settings switch's from before Labs, so a saved choice keeps working and
// an older build reads the same value back. Standard (the default) writes nothing. A value this build does not recognize
// reads as the default and is left in place until a choice replaces it.

export type TerminalRenderer = "standard" | "webgl";
export const TERMINAL_RENDERERS: readonly TerminalRenderer[] = ["standard", "webgl"];
export const TERMINAL_RENDERER_KEY = "persona.terminal.webgl";

/** Where the value read came from. "invalid": something this build does not recognize was saved (left in place);
 *  "unreadable": this browser does not let the page read its storage. Both read as the default. */
export type LabSource = "default" | "saved" | "invalid" | "unreadable";

export interface LabState<V> {
  value: V;
  source: LabSource;
  /** What is stored, for telling one saved state from another (null: nothing, or unreadable). */
  raw: string | null;
}

export type LabWrite = { ok: true } | { ok: false; reason: "invalid" | "storage" };

/** The renderer chosen, and where that came from. Read it once where a terminal opens. */
export function readTerminalRenderer(): LabState<TerminalRenderer> {
  let raw: string | null;
  try {
    raw = localStorage.getItem(TERMINAL_RENDERER_KEY);
  } catch {
    return { value: "standard", source: "unreadable", raw: null };
  }
  if (raw === null) return { value: "standard", source: "default", raw };
  return raw === "1" ? { value: "webgl", source: "saved", raw } : { value: "standard", source: "invalid", raw };
}

const listeners = new Set<() => void>();

/** Keeps a renderer: WebGL as "1", Standard as no key. A value that isn't one is refused, and a browser that does not
 *  keep it (storage off, full, or the write not read back) is reported, never taken as saved. */
export function writeTerminalRenderer(value: TerminalRenderer): LabWrite {
  if (!TERMINAL_RENDERERS.includes(value)) return { ok: false, reason: "invalid" };
  const raw = value === "webgl" ? "1" : null;
  try {
    if (raw === null) localStorage.removeItem(TERMINAL_RENDERER_KEY);
    else localStorage.setItem(TERMINAL_RENDERER_KEY, raw);
    if (localStorage.getItem(TERMINAL_RENDERER_KEY) !== raw) return { ok: false, reason: "storage" };
  } catch {
    return { ok: false, reason: "storage" };
  }
  listeners.forEach((l) => l());
  return { ok: true };
}

/** Called when the choice may have changed: a write here, or another tab of this browser. */
export function subscribeTerminalRenderer(l: () => void): () => void {
  listeners.add(l);
  const storage = (e: StorageEvent) => {
    if (e.key === null || e.key === TERMINAL_RENDERER_KEY) l();
  };
  if (typeof window !== "undefined") window.addEventListener("storage", storage);
  return () => {
    listeners.delete(l);
    if (typeof window !== "undefined") window.removeEventListener("storage", storage);
  };
}
