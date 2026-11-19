// A session's context usage (#2430): how much of the model's window the latest reply used, read by the node off the
// transcript (Session.context_tokens / context_window, omitted while unknown) and shown as a second line in the sidebar's
// agent rows and as a thin bar below the composer. "Show agent details" turns on the sidebar line for this browser.
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import type { MenuItem } from "../ui/menu-model";
import { formatTokens } from "./compaction";
import { ago, baseName } from "./format";
import type { Session } from "./types";

/** At or past this share of the window used, the numbers turn to the warning colour (orange). */
export const CONTEXT_WARN_USED = 80;
/** At or past this share used (10% or less left), the context is nearly full. */
export const CONTEXT_DANGER_USED = 90;

/** How full the window is: nothing to say, close to the limit, or about to run out. */
export type ContextTier = "ok" | "warn" | "danger";

export interface ContextUsage {
  /** Whole percent of the window used, 0–100. */
  used: number;
  /** Whole percent left, 100 − used. */
  left: number;
  tokens: number;
  window: number;
}

/** The session's context usage, or null while the node cannot tell (no reply yet, a model whose window it does not know, an older node). */
export function contextUsage(s: Pick<Session, "context_tokens" | "context_window"> | undefined): ContextUsage | null {
  const tokens = s?.context_tokens ?? 0;
  const window = s?.context_window ?? 0;
  if (!(tokens > 0) || !(window > 0)) return null;
  const used = Math.min(100, Math.max(0, Math.round((tokens / window) * 100)));
  return { used, left: 100 - used, tokens, window };
}

export function contextTier(u: ContextUsage): ContextTier {
  return u.used >= CONTEXT_DANGER_USED ? "danger" : u.used >= CONTEXT_WARN_USED ? "warn" : "ok";
}

/** Whether the numbers are past the warning line (the danger tier is past it too). */
export function contextWarn(u: ContextUsage): boolean {
  return contextTier(u) !== "ok";
}

/** The composer and sidebar use the same short label for the used share. */
export function contextLabel(u: ContextUsage): string {
  return t("context.label", { percent: u.used });
}

/** What happens when the window fills, and what to do once it is close (#3668): the header's tooltip under the share. */
export function contextHint(u: ContextUsage): string {
  return contextWarn(u) ? t("context.hint_warn") : t("context.hint_ok");
}

/** The header's popover (#4054), the tooltip's words for a screen without hover: the share with the token count beside
 *  it, then what happens when the window fills (and, once close, what to do). */
export function contextNotes(u: ContextUsage): MenuItem[] {
  const none = () => {};
  return [
    { key: "share", note: true, label: contextTooltip(u), hint: t("context.tokens", { used: formatTokens(u.tokens), window: formatTokens(u.window) }), onSelect: none },
    { key: "hint", note: true, label: contextHint(u), onSelect: none },
  ];
}

/** The explanation in the popover, tooltip and accessible names. */
export function contextTooltip(u: ContextUsage): string {
  return t("context.summary", { used: u.used, left: u.left });
}

/** The model as a row has room for it: "claude-opus-5-5" → "opus 5.5", "claude-sonnet-4-5-20250929" → "sonnet 4.5",
 *  "claude-opus-5-5[1m]" → "opus 5.5 1M"; ids that are not Claude's (gpt-5.5, a gateway's own) are kept as they are. */
export function modelShort(model: string | undefined): string {
  let id = (model ?? "").trim();
  if (!id) return "";
  // A gateway routes a model under a prefix: the model after the last "/" is the one that answers.
  if (id.includes("/")) id = id.slice(id.lastIndexOf("/") + 1) || id;
  const m = /^claude-([a-z]+)((?:-\d{1,2})*)(?:-\d{8})?(\[1m\])?$/i.exec(id);
  if (!m) return id;
  const version = m[2].split("-").filter(Boolean).join(".");
  id = [m[1].toLowerCase(), version, m[3] ? "1M" : ""].filter(Boolean).join(" ");
  return id;
}

/** One piece of a sidebar row's second line. */
export interface DetailPart {
  text: string;
  /** The context share at the warning colour (orange; also set with `danger`). */
  warn?: boolean;
  /** The context share in the danger tier: 10% or less left. */
  danger?: boolean;
  title?: string;
}

/** The second line of an agent's sidebar row: model · context share · how long ago it moved · folder, most useful
 *  first — the row clips what does not fit at its end, and the folder is mostly the group's own name already. A piece
 *  the session has no value for is left out, so the line is empty (and not drawn) for a session that says nothing yet. */
export function rowDetail(s: Session, now = Date.now()): DetailPart[] {
  const parts: DetailPart[] = [];
  const model = modelShort(s.model);
  if (model) parts.push({ text: model, title: s.model });
  const u = contextUsage(s);
  if (u) parts.push({ text: contextLabel(u), warn: contextWarn(u), danger: contextTier(u) === "danger", title: contextTooltip(u) });
  const age = s.updated_at ? ago(s.updated_at, now) : "";
  if (age) parts.push({ text: age });
  const folder = baseName(s.cwd);
  if (folder) parts.push({ text: folder, title: s.cwd });
  return parts;
}

// ── sidebar details preference (this browser only) ──

const listeners = new Set<() => void>();

function flag(key: string) {
  const get = (): boolean => {
    try {
      return localStorage.getItem(key) === "1";
    } catch {
      return false;
    }
  };
  const set = (on: boolean): void => {
    try {
      if (on) localStorage.setItem(key, "1");
      else localStorage.removeItem(key);
    } catch {
      // storage unavailable: nothing is kept, the default (off) stays
    }
    listeners.forEach((l) => l());
  };
  const subscribe = (l: () => void) => {
    listeners.add(l);
    if (typeof window !== "undefined") window.addEventListener("storage", l);
    return () => {
      listeners.delete(l);
      if (typeof window !== "undefined") window.removeEventListener("storage", l);
    };
  };
  const use = (): boolean => useSyncExternalStore(subscribe, get, get);
  return { get, set, use };
}

const details = flag("agent.details");

/** Settings › Appearance › "Show agent details": the sidebar rows' second line. */
export const setAgentDetails = details.set;
export const useAgentDetails = details.use;
