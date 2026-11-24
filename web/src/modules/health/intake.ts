// Health's water and protein records (docs/assistant/health.md, docs/protocol/protocol.md, #13175): the hub's
// routes under /assistant/health/intake, the feature that says it has them, and what the page checks before it asks.
//
// Every request names `space=health` and no manager: the health space stays the default manager's whichever assistant
// the Persona page shows (docs/assistant/managers.md "Clients"), and lib/manager-choice.ts `managerHeaderFor` adds the
// shown manager's header to an /assistant request unless it carries `space=`.
import { api, enc } from "../../lib/api";

/** The hello feature the default manager's records ride on: without it the hub has no intake, and the page shows none. */
export const INTAKE_FEATURE = "health_intake";
/** The WebSocket message that says a record was added or undone (no payload): read the day again. */
export const INTAKE_EVENT = "health_intake";

export type IntakeKind = "water" | "protein";
export const INTAKE_KINDS: readonly IntakeKind[] = ["water", "protein"];

/** A record, as the hub returns it (`deleted_at` only once undone). */
export interface IntakeEntry {
  id: string;
  kind: IntakeKind;
  amount: number;
  at: string;
  note: string;
  seq: number;
  created_at: string;
  deleted_at?: string;
}

/** Today's totals: millilitres of water, grams of protein. */
export interface IntakeTotals {
  water: number;
  protein: number;
}

/** GET: each kind's total and goal today (the page asks for one day, so `days` is not used). */
export interface IntakeToday {
  water: { today: number; goal: number };
  protein: { today: number; goal: number };
}

/** One record's limits: 1–3000 ml of water or 0.1–200 g of protein, one decimal at most. */
export const INTAKE_MIN: IntakeTotals = { water: 1, protein: 0.1 };
export const INTAKE_MAX: IntakeTotals = { water: 3000, protein: 200 };

const BASE = "/assistant/health/intake";
const SPACE = "space=health";

/** The day's totals and goals. */
export function readToday(): Promise<IntakeToday> {
  return api<IntakeToday & { days?: unknown }>(`${BASE}?${SPACE}&days=1`).then((r) => ({
    water: { today: r.water?.today ?? 0, goal: r.water?.goal ?? 0 },
    protein: { today: r.protein?.today ?? 0, goal: r.protein?.goal ?? 0 },
  }));
}

/** A record to make. `id` is the user's one record, made once: a retry sends the same one, and the hub keeps a single
 *  record for it. A cup of water has no `amount`: the hub uses the cup size its settings name. */
export interface IntakeDraft {
  id: string;
  kind: IntakeKind;
  amount?: number;
  at: string;
}

export function newDraft(kind: IntakeKind, amount?: number): IntakeDraft {
  return { id: crypto.randomUUID(), kind, amount, at: new Date().toISOString() };
}

export function postEntry(d: IntakeDraft): Promise<{ entry: IntakeEntry; today: IntakeTotals }> {
  return api(`${BASE}?${SPACE}`, { method: "POST", json: { id: d.id, kind: d.kind, ...(d.amount === undefined ? {} : { amount: d.amount }), at: d.at } });
}

/** Undoes a record, of any day; undoing it again is fine. */
export function deleteEntry(id: string): Promise<{ today: IntakeTotals }> {
  return api(`${BASE}/${enc(id)}?${SPACE}`, { method: "DELETE" });
}

/** An amount typed in a box: a number within the kind's limits, one decimal at most. null: not one. */
export function parseAmount(text: string, kind: IntakeKind): number | null {
  const s = text.trim().replace(",", ".");
  if (!/^\d+(\.\d)?$/.test(s)) return null;
  const n = Number(s);
  return n >= INTAKE_MIN[kind] && n <= INTAKE_MAX[kind] ? n : null;
}

/** A ratio of the goal, 0 to 1 (a day past its goal is a full ring). */
export function ratioOf(today: number, goal: number): number {
  return goal > 0 && today > 0 ? Math.min(1, today / goal) : 0;
}
