// A session's context usage (#2430): how much of the model's window the latest reply used, read by the node off the
// transcript (Session.context_tokens / context_window, omitted while unknown) and shown as a second line in the sidebar's
// agent rows and as a thin bar below the composer. "Show agent details" turns on the sidebar line for this browser.
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
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

/** Tokens in the latest context, independent of the model's window and the cumulative session total. */
export function currentContextTokens(s: Pick<Session, "context_tokens"> | undefined): number {
  const tokens = s?.context_tokens ?? 0;
  return Number.isFinite(tokens) && tokens > 0 ? tokens : 0;
}

/** A window share needs both an observed context size and a known capacity. */
export function contextUsage(s: Pick<Session, "context_tokens" | "context_window"> | undefined): ContextUsage | null {
  const tokens = currentContextTokens(s);
  const window = s?.context_window ?? 0;
  if (!tokens || !Number.isFinite(window) || window <= 0) return null;
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

/** The explanation in the popover, tooltip and accessible names. */
export function contextTooltip(u: ContextUsage): string {
  return t("context.summary", { used: u.used, left: u.left });
}

/** The model as a row has room for it: "claude-opus-5-5" → "opus 5.5", "claude-sonnet-4-5-20250929" → "sonnet 4.5",
 *  "claude-opus-5-5[1m]" → "opus 5.5 1M"; ids that are not Claude's (gpt-5.5, a gateway's own) are kept as they are. */
export function modelShort(model: string | undefined): string {
  let id = (model ?? "").trim();
  if (!id) return "";
  const m = /^claude-([a-z]+)((?:-\d{1,2})*)(?:-\d{8})?(\[1m\])?$/i.exec(id);
  if (!m) return id;
  const version = m[2].split("-").filter(Boolean).join(".");
  id = [m[1].toLowerCase(), version, m[3] ? "1M" : ""].filter(Boolean).join(" ");
  return id;
}

/** One piece of a sidebar row's second line. */
export interface DetailPart {
  text: string;
  usage?: ContextUsage;
  /** The context share at the warning colour (orange; also set with `danger`). */
  warn?: boolean;
  /** The context share in the danger tier: 10% or less left. */
  danger?: boolean;
  title?: string;
}

/** The second line of an agent's sidebar row: context share · model · how long ago it moved · folder, most useful
 *  first — the row clips what does not fit at its end, and the folder is mostly the group's own name already. A piece
 *  the session has no value for is left out, so the line is empty (and not drawn) for a session that says nothing yet. */
export function rowDetail(s: Session, now = Date.now()): DetailPart[] {
  const parts: DetailPart[] = [];
  const u = contextUsage(s);
  if (u) parts.push({ text: contextLabel(u), usage: u, warn: contextWarn(u), danger: contextTier(u) === "danger", title: contextTooltip(u) });
  const model = modelShort(s.model);
  if (model) parts.push({ text: model, title: s.model });
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
