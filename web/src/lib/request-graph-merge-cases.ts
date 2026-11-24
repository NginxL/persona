// Test table, shared by lib/request-graph.test.ts (buildGraph) and views/TodoRequestGraph.test.tsx (the drawn block):
// every way a request's root to-do and its one same-name piece can stand, and whether the piece's card alone stands for
// both (mergesWithRoot in lib/request-graph.ts). Each case is one root to-do and one piece (`part_of` the root), both
// titled the same, so only their states decide. Nothing imports this outside tests.
import type { Supervision } from "./supervision";
import type { Todo } from "./todos";
import type { Session } from "./types";
import type { NodeState } from "./request-graph";

type At = (minutes: number) => string;

/** A to-do's state as fields: stored closed or open, and what draws it differently from that. */
interface Variant {
  name: string;
  patch: (at: At) => Partial<Todo>;
  /** Sessions its Agent needs (a working piece). */
  sessions?: (at: At) => Session[];
}

const accepted = (at: At): Supervision => ({ supervisor: "main/s0", state: "accepted", reviews: [{ at: at(20), verdict: "accepted" }] });

export const ROOTS = {
  open: { name: "open", patch: () => ({}) },
  reopened: { name: "reopened (done, then reopened)", patch: (at) => ({ reopened_at: at(10) }) },
  done: { name: "done by the owner", patch: (at) => ({ done_at: at(5) }) },
  auto_unseen: { name: "auto-done, not looked at", patch: (at) => ({ done_at: at(5), done_by: "auto", done_reason: "finished" }) },
  failed: { name: "did not start (start_error, done_at set)", patch: (at) => ({ done_at: at(5), start_error: "no such directory" }) },
} satisfies Record<string, Variant>;

export const PIECES = {
  open: { name: "open, nobody on it", patch: () => ({}) },
  working: {
    name: "open, its Agent working",
    patch: (at) => ({ agents: [{ machine_id: "mac", session_id: "work", linked_at: at(30) }] }),
    sessions: () => [{ machine_id: "mac", session_id: "work", title: "work", cwd: "/code/app", status: "working", state: "working" } as Session],
  },
  done: { name: "done by the owner", patch: (at) => ({ done_at: at(4) }) },
  review_done: { name: "closed by its supervisor's acceptance", patch: (at) => ({ done_at: at(4), done_by: "review", supervision: accepted(at) }) },
  cancelled: {
    name: "cancelled by its supervisor",
    patch: (at) => ({ done_at: at(4), done_by: "review", supervision: { supervisor: "main/s0", state: "cancelled", reviews: [{ at: at(20), verdict: "cancelled" }] } }),
  },
  auto_seen: { name: "auto-done, looked at", patch: (at) => ({ done_at: at(4), done_by: "auto", done_reason: "finished", seen_at: at(2) }) },
  auto_unseen: { name: "auto-done, not looked at (drawn review)", patch: (at) => ({ done_at: at(4), done_by: "auto", done_reason: "finished" }) },
  reopened: { name: "reopened", patch: (at) => ({ reopened_at: at(3) }) },
  accepted_at_rest: { name: "accepted, reopened, nothing running (drawn done, stored open)", patch: (at) => ({ supervision: accepted(at) }) },
  failed: { name: "did not start (drawn failed)", patch: (at) => ({ done_at: at(4), start_error: "no such directory" }) },
} satisfies Record<string, Variant>;

export type RootKind = keyof typeof ROOTS;
export type PieceKind = keyof typeof PIECES;

export interface MergeCase {
  root: RootKind;
  piece: PieceKind;
  /** The piece as drawn. */
  state: NodeState;
  /** One card for both. */
  merged: boolean;
  /** Filed under Done. */
  done: boolean;
  /** Filed under Cancelled instead of active or Done. */
  cancelled?: boolean;
}

/** Every root × piece, with what it must be. `merged` holds only while both are closed or both open as stored, and a
 *  card drawn done never stands for a root still open. */
export const MERGE_CASES: MergeCase[] = [
  // the root open: stored open, so the piece must be stored open and not drawn done
  ...(["open", "reopened", "failed"] as const).flatMap((root): MergeCase[] => [
    { root, piece: "open", state: "ready", merged: true, done: false },
    { root, piece: "working", state: "working", merged: true, done: false },
    { root, piece: "done", state: "done", merged: false, done: false },
    { root, piece: "review_done", state: "done", merged: false, done: false },
    { root, piece: "cancelled", state: "cancelled", merged: false, done: false },
    { root, piece: "auto_seen", state: "done", merged: false, done: false },
    { root, piece: "auto_unseen", state: "review", merged: false, done: false },
    { root, piece: "reopened", state: "ready", merged: true, done: false },
    { root, piece: "accepted_at_rest", state: "done", merged: false, done: false },
    { root, piece: "failed", state: "failed", merged: true, done: false },
  ]),
  // the root closed: the piece must be stored closed
  ...(["done", "auto_unseen"] as const).flatMap((root): MergeCase[] => [
    { root, piece: "open", state: "ready", merged: false, done: false },
    { root, piece: "working", state: "working", merged: false, done: false },
    { root, piece: "done", state: "done", merged: true, done: true },
    { root, piece: "review_done", state: "done", merged: true, done: true },
    { root, piece: "cancelled", state: "cancelled", merged: true, done: false, cancelled: true },
    { root, piece: "auto_seen", state: "done", merged: true, done: true },
    { root, piece: "auto_unseen", state: "review", merged: true, done: false },
    { root, piece: "reopened", state: "ready", merged: false, done: false },
    { root, piece: "accepted_at_rest", state: "done", merged: false, done: true },
    { root, piece: "failed", state: "failed", merged: false, done: false },
  ]),
];

export const caseKey = (c: Pick<MergeCase, "root" | "piece">) => `${c.root}-${c.piece}`;
export const caseName = (c: Pick<MergeCase, "root" | "piece">) => `root ${ROOTS[c.root].name} × piece ${PIECES[c.piece].name}`;

/** The case's two to-dos (root `r-<key>`, piece `p-<key>`) and the sessions they need; `base` makes a to-do. */
export function caseTodos(c: Pick<MergeCase, "root" | "piece">, at: At, base: (id: string, patch: Partial<Todo>) => Todo): { todos: Todo[]; sessions: Session[] } {
  const k = caseKey(c);
  const title = `Ship ${k}`;
  const root = ROOTS[c.root] as Variant;
  const piece = PIECES[c.piece] as Variant;
  return {
    todos: [base(`r-${k}`, { title, ...root.patch(at) }), base(`p-${k}`, { title, part_of: `r-${k}`, ...piece.patch(at) })],
    sessions: [...(root.sessions?.(at) ?? []), ...(piece.sessions?.(at) ?? [])],
  };
}
