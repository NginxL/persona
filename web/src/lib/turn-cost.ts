// What an exchange's tokens are worth and what its reply bar says about them (#5426), after cindy's MessageActionBar
// usage (buildTurnUsageTooltipLines, formatTurnCostMoney, the turn cost calculator's pricing): loaded with the reply bar,
// never on the first screen. A price is the hub's built-in catalog's reference price per million tokens (POST
// /api/v1/launchers/catalog), so a cost is always an estimate, never a bill. An exchange has one only when every model
// that used tokens has a price for every kind it used; otherwise the bar shows its tokens.

import { useEffect, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api } from "./api";
import { bareModel, replyModelName } from "./model-change";
import type { PanelPrice } from "./model-panel";
import { compactTokens } from "./tokens";
import { formatLatencyMs } from "./session-insights";
import type { TurnUsage } from "./turn-usage";
import type { TokenUsage } from "./types";
import { workDuration } from "./work-groups";

const KINDS = ["in", "out", "cache_read", "cache_write", "cache_write_1h"] as const;

/** One model's tokens in an exchange: `in` without the cache, `cache_write_1h` the part of `cache_write` kept an hour.
 *  `fast`: Claude Code's fast speed, apart from the same model's normal requests (it has no known price). `model` is
 *  the id without its wrappers (bareModel), "" when the transcript did not say. */
export interface ModelTokens {
  model: string;
  fast?: boolean;
  in: number;
  out: number;
  cache_read: number;
  cache_write: number;
  cache_write_1h: number;
}

/** An exchange's tokens by model, and whether it is the page's first round. */
export interface UsageSummary {
  /** In the order each model first used tokens. */
  models: ModelTokens[];
  first?: boolean;
}

const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0);

/** What an exchange's entries add up to by model and speed: a subagent's report (an `id`) once per model and speed,
 *  the largest count of each kind it was seen with; the rest summed. Models without a token left out. */
export function modelTokens(entries: readonly TokenUsage[]): ModelTokens[] {
  const reports = new Map<string, ModelTokens>();
  const models = new Map<string, ModelTokens>();
  const add = (into: Map<string, ModelTokens>, key: string, u: TokenUsage, model: string, max: boolean) => {
    let m = into.get(key);
    if (!m) into.set(key, (m = { model, ...(u.fast ? { fast: true } : {}), in: 0, out: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0 }));
    for (const k of KINDS) m[k] = max ? Math.max(m[k], num(u[k])) : m[k] + num(u[k]);
  };
  for (const u of entries) {
    const model = u.model ? bareModel(u.model) : "";
    const key = `${model}\n${u.fast ? 1 : 0}`;
    // its place in the order, whichever entry comes first: a report's counts are added below
    if (!models.has(key)) add(models, key, { fast: u.fast }, model, false);
    if (u.id) add(reports, `${u.id}\n${key}`, u, model, true);
    else add(models, key, u, model, false);
  }
  for (const [key, r] of reports) {
    const m = models.get(key.slice(key.indexOf("\n") + 1))!;
    for (const k of KINDS) m[k] += r[k];
  }
  return [...models.values()].filter((m) => KINDS.some((k) => m[k] > 0));
}

/** The usage a reply's props carry, summed by model; undefined when it used nothing. */
export function summarize(u: TurnUsage | undefined): UsageSummary | undefined {
  const models = u ? modelTokens(u.entries) : [];
  return models.length ? { models, ...(u?.first ? { first: true } : {}) } : undefined;
}

/** All of an exchange's tokens: `total` is input, output, cache reads and writes; `hit` the cache reads' share of all
 *  input (null without any input). */
export function usageTotals(models: readonly ModelTokens[]): Omit<ModelTokens, "model" | "fast"> & { total: number; hit: number | null } {
  const sum = { in: 0, out: 0, cache_read: 0, cache_write: 0, cache_write_1h: 0 };
  for (const m of models) for (const k of KINDS) sum[k] += m[k];
  const prompt = sum.in + sum.cache_read + sum.cache_write;
  return { ...sum, total: prompt + sum.out, hit: prompt > 0 ? sum.cache_read / prompt : null };
}

/** The usage a reply's props carry (lib/turn-usage.ts usageKey), summed by model; undefined for anything else. */
export function parseUsage(key: string | undefined): UsageSummary | undefined {
  if (!key) return undefined;
  try {
    const u = JSON.parse(key) as TurnUsage;
    return Array.isArray(u?.entries) ? summarize(u) : undefined;
  } catch {
    return undefined;
  }
}

// ── prices ──

/** A model's price, null when the catalog has none (or does not know the model). */
type Price = PanelPrice | null;

const prices = new Map<string, Price>();
const asking = new Set<string>();
/** Ids whose question failed, and when: asked again only after RETRY_MS, so a hub without the route is not asked
 *  on every reply. */
const failed = new Map<string, number>();
const RETRY_MS = 60_000;
/** The route answers at most this many ids at a time (catalogLookupMaxIDs). */
const BATCH = 100;
const waiting = new Set<string>();
let flushing = false;
const listeners = new Set<() => void>();
let version = 0;

function changed() {
  version++;
  for (const l of listeners) l();
}

async function ask(ids: string[]) {
  try {
    const r = await api<{ models?: { id: string; price?: PanelPrice }[] }>("/launchers/catalog", { method: "POST", json: { ids }, timeout: 15_000 });
    const found = new Map((r.models ?? []).map((m) => [m.id, m.price ?? null]));
    for (const id of ids) {
      prices.set(id, found.get(id) ?? null);
      failed.delete(id);
    }
  } catch {
    // an older hub, the manager away: no price this time (the bar shows the tokens), asked again later
    for (const id of ids) failed.set(id, Date.now());
  } finally {
    for (const id of ids) asking.delete(id);
    changed();
  }
}

/** Asks the catalog for the ids it has not answered yet, together with those other replies ask for at the same time. */
export function requestPrices(ids: Iterable<string>) {
  const now = Date.now();
  for (const id of ids) {
    if (!id || prices.has(id) || asking.has(id) || now - (failed.get(id) ?? -Infinity) < RETRY_MS) continue;
    asking.add(id);
    waiting.add(id);
  }
  if (!waiting.size || flushing) return;
  flushing = true;
  queueMicrotask(() => {
    flushing = false;
    const all = [...waiting];
    waiting.clear();
    for (let i = 0; i < all.length; i += BATCH) void ask(all.slice(i, i + BATCH));
  });
}

/** What the catalog said of an id: a price, null for none, undefined while it has not answered (or could not). */
export function priceOf(id: string): Price | undefined {
  return prices.get(id);
}

/** Whether the catalog is still being asked for any of `ids`. */
export const pricesPending = (ids: Iterable<string>) => [...ids].some((id) => asking.has(id));

/** For tests: forget every answer. */
export function forgetPrices() {
  prices.clear();
  asking.clear();
  failed.clear();
  waiting.clear();
  changed();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => void listeners.delete(l);
}

/** The prices of `ids` (bare model ids), asked for once; redrawn as answers come. `ready` is false while the first
 *  question is out, so a reply does not show its tokens and then its cost. */
export function usePrices(ids: readonly string[]): { ready: boolean; price: (id: string) => Price } {
  useSyncExternalStore(subscribe, () => version);
  const key = ids.join("\n");
  useEffect(() => {
    if (key) requestPrices(key.split("\n"));
  }, [key]);
  return { ready: !pricesPending(ids) && ids.every((id) => prices.has(id) || failed.has(id)), price: (id) => prices.get(id) ?? null };
}

// ── cost ──

/** A model's cost in USD at `price` (per million tokens), cindy's way: a 1-hour cache write at its own price, else the
 *  cache write's. Null when a kind it used has no price, or for the fast speed (no known price). */
export function modelCost(m: ModelTokens, price: Price): number | null {
  if (!price || m.fast) return null;
  const write1h = Math.min(m.cache_write_1h, m.cache_write);
  const write1hPrice = price.cache_write_1h ?? price.cache_write;
  if ((m.in > 0 && price.input === undefined) || (m.out > 0 && price.output === undefined) || (m.cache_read > 0 && price.cache_read === undefined) || (m.cache_write > 0 && price.cache_write === undefined)) return null;
  return (m.in * price.input + m.out * price.output + m.cache_read * (price.cache_read ?? 0) + (m.cache_write - write1h) * (price.cache_write ?? 0) + write1h * (write1hPrice ?? 0)) / 1e6;
}

export interface TurnCost {
  total: number;
  /** Each model's share, in the order they first used tokens (models without a cost of their own left out). */
  models: { model: string; cost: number }[];
}

/** The exchange's cost: only when every model that used tokens has one (a partial sum would read as the whole). */
export function turnCost(models: readonly ModelTokens[], price: (id: string) => Price): TurnCost | null {
  const out: { model: string; cost: number }[] = [];
  for (const m of models) {
    const cost = m.model ? modelCost(m, price(m.model)) : null;
    if (cost === null) return null;
    const same = out.find((x) => x.model === m.model);
    if (same) same.cost += cost;
    else out.push({ model: m.model, cost });
  }
  const total = out.reduce((n, m) => n + m.cost, 0);
  return { total, models: out.filter((m) => m.cost > 0) };
}

/** cindy's formatTurnCostMoney: two decimals, "<$0.01" under a cent. */
export function formatCost(usd: number): string {
  if (usd === 0) return "$0.00";
  return usd >= 0.01 ? `$${usd.toFixed(2)}` : "<$0.01";
}

/** cindy's formatPercent: whole when it is close to whole, else one decimal. */
export function formatPercent(v: number | null): string | null {
  if (v === null || !Number.isFinite(v)) return null;
  const pct = Math.min(100, Math.max(0, v * 100));
  if (Math.abs(pct - Math.round(pct)) < 0.05) return `${Math.round(pct)}%`;
  return `${pct.toFixed(1).replace(/\.0$/, "")}%`;
}

// ── what the card says ──

/** cindy's one suggestion: a lot of input that mostly missed the cache, which is worth knowing (the cache was broken:
 *  the context changed mid-session, a restart, a tool change). Never for a first round, which has nothing cached. */
const LOW_CACHE_MIN_INPUT = 50_000;
const LOW_CACHE_MAX_HIT = 0.2;

export function lowCache(usage: UsageSummary): boolean {
  if (usage.first) return false;
  const s = usageTotals(usage.models);
  return s.in + s.cache_read + s.cache_write >= LOW_CACHE_MIN_INPUT && s.hit !== null && s.hit < LOW_CACHE_MAX_HIT;
}

/** The model names the exchange used, each once ("" ones left out). */
function modelNames(models: readonly ModelTokens[]): string[] {
  return [...new Set(models.filter((m) => m.model).map((m) => replyModelName(m.model)))];
}

/** The card's lines, in cindy's order (buildTurnUsageTooltipLines): with a cost, its title, the value and, for two or
 *  more models, each one's; then the tokens, the cache, the time when known (`ms`, the work summary's "Worked for"),
 *  the model when there was no breakdown, the low-cache suggestion, and without a cost why there is none. */
export function usageLines(usage: UsageSummary, cost: TurnCost | null, ms?: number, ttftMs?: number): string[] {
  const lines: string[] = [];
  const s = usageTotals(usage.models);
  const priced = cost && cost.total > 0 ? cost : null;
  if (priced) {
    lines.push(t("chat-replybar.details_title"), t("chat-replybar.value_line", { cost: formatCost(priced.total) }));
  }
  const breakdown = priced && priced.models.length >= 2;
  if (breakdown) {
    lines.push(t("chat-replybar.by_model"));
    for (const m of priced.models) lines.push(t("chat-replybar.model_cost", { model: replyModelName(m.model), cost: formatCost(m.cost) }));
  }
  lines.push(t("chat-replybar.token_line", { total: compactTokens(s.total), input: compactTokens(s.in), output: compactTokens(s.out) }));
  const rate = formatPercent(s.hit);
  lines.push(rate ? t("chat-replybar.cache_line", { read: compactTokens(s.cache_read), write: compactTokens(s.cache_write), rate }) : t("chat-replybar.cache_line_no_rate", { read: compactTokens(s.cache_read), write: compactTokens(s.cache_write) }));
  if (ms !== undefined && ms > 0) lines.push(t("chat-replybar.time_line", { duration: workDuration(ms) }));
  if (ttftMs !== undefined && Number.isFinite(ttftMs) && ttftMs >= 0) lines.push(t("chat-replybar.ttft_line", { ttft: formatLatencyMs(ttftMs) }));
  if (!breakdown) {
    const names = modelNames(usage.models);
    if (names.length) lines.push(t("chat-replybar.model_line", { model: names.length === 1 ? names[0] : t("chat-replybar.models", { count: names.length }) }));
  }
  if (lowCache(usage)) lines.push(t("chat-replybar.suggestion_line", { suggestion: t("chat-replybar.low_cache") }));
  if (!priced) lines.push(t("chat-replybar.no_cost"));
  return lines;
}

/** A Persona conversation's card (super rules 0 and 4): what the reply cost in plain words, and each model's share when
 *  there were several; nothing about tokens or the cache. */
export function personaLines(cost: TurnCost): string[] {
  const lines = [t("chat-replybar.persona_cost", { cost: formatCost(cost.total) })];
  if (cost.models.length >= 2) {
    lines.push(t("chat-replybar.by_model"));
    for (const m of cost.models) lines.push(t("chat-replybar.model_cost", { model: replyModelName(m.model), cost: formatCost(m.cost) }));
  }
  return lines;
}
