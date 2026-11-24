// The Health home's "Today" state: the hub's totals plus what this page has sent and not yet heard back about, so a
// press moves the ring at once (docs/design/PRINCIPLES.md 7) and the hub's own total settles it.
//
// - A press adds its amount locally (`pending`); the hub's answer carries today's total, which replaces the local sum, and
//   a failure takes the amount back out.
// - Writes go one at a time, in the order they were made, so the totals they answer with are in that order too.
// - A read (the hub's `health_intake` push, a hello, the page coming back) waits for the writes in flight: what it would
//   show may be older than what they answer. One that came meanwhile is run once they are done.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { hubHas, onHubEvent, useStore } from "../../lib/store";
import { deleteEntry, INTAKE_EVENT, INTAKE_FEATURE, postEntry, ratioOf, readToday, type IntakeDraft, type IntakeEntry, type IntakeKind, type IntakeToday, type IntakeTotals } from "./intake";

/** What a cup of water is until the hub has answered one: its default (cup_ml). */
const DEFAULT_CUP_ML = 250;

interface Pending {
  key: string;
  kind: IntakeKind;
  delta: number;
}

export interface Ring {
  /** Today's amount, with what is on its way. */
  today: number;
  goal: number;
  ratio: number;
}

export interface HealthIntake {
  /** Whether the hub has intake at all: the page shows nothing otherwise. */
  enabled: boolean;
  /** Null until the first read has come back. */
  rings: Record<IntakeKind, Ring> | null;
  /** The first read's failure, while there is nothing to show. */
  error: string;
  reload: () => void;
  /** Makes the record; resolves with the hub's record, or throws what the hub said (the amount is already taken back out). */
  log: (draft: IntakeDraft) => Promise<IntakeEntry>;
  /** Undoes a record that was logged (`amount` is what it added, for the ring to move at once). */
  undo: (id: string, kind: IntakeKind, amount: number) => Promise<void>;
}

export function useHealthIntake(): HealthIntake {
  const enabled = useStore((s) => hubHas(s.features, INTAKE_FEATURE));
  const [server, setServer] = useState<IntakeToday | null>(null);
  const [pending, setPending] = useState<Pending[]>([]);
  const [error, setError] = useState("");
  const cup = useRef(DEFAULT_CUP_ML);
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const writing = useRef(0);
  const wrote = useRef(0);
  const read = useRef(0);
  const stale = useRef(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const load = useCallback(async function loadToday() {
    if (!alive.current) return;
    if (writing.current > 0) {
      stale.current = true;
      return;
    }
    const epoch = wrote.current;
    const sequence = ++read.current;
    const accept = () => {
      if (!alive.current || sequence !== read.current) return false;
      if (writing.current > 0 || wrote.current !== epoch) {
        // A write invalidated this read. If it already settled, there is no later drain to consume stale.
        stale.current = true;
        if (writing.current === 0) {
          stale.current = false;
          void loadToday();
        }
        return false;
      }
      return true;
    };
    try {
      const r = await readToday();
      if (!accept()) return;
      setServer(r);
      setError("");
    } catch (e) {
      if (accept()) setError(errorText(e));
    }
  }, []);

  const settled = useCallback(() => {
    if (writing.current === 0 && stale.current) {
      stale.current = false;
      void load();
    }
  }, [load]);

  useEffect(() => {
    if (!enabled) return;
    void load();
    // the hub says a record was added or undone (here or elsewhere); a hello is a hub back from away or with another feature set
    const stops = [onHubEvent(INTAKE_EVENT, () => void load()), onHubEvent("hello", () => void load())];
    // a page left open across midnight, or in a tab the socket missed: back in view, read the day again
    const visible = () => { if (document.visibilityState === "visible") void load(); };
    document.addEventListener("visibilitychange", visible);
    return () => {
      read.current++; // Results from the previous subscription are no longer current.
      stops.forEach((stop) => stop());
      document.removeEventListener("visibilitychange", visible);
    };
  }, [enabled, load]);

  /** One write, after the ones before it; `local` is on the rings until it is answered. */
  const write = useCallback(<T extends { today: IntakeTotals }>(local: Pending, send: () => Promise<T>): Promise<T> => {
    setPending((p) => [...p, local]);
    wrote.current++;
    writing.current++;
    const run = queue.current.then(send);
    queue.current = run.catch(() => undefined);
    return run.then(
      (r) => {
        // the hub's total replaces the local sum for this kind; the other kind's is untouched by it
        writing.current--;
        if (alive.current) {
          setServer((s) => (s ? { water: { ...s.water, today: r.today.water }, protein: { ...s.protein, today: r.today.protein } } : s));
          setPending((p) => p.filter((x) => x.key !== local.key));
        }
        settled();
        return r;
      },
      (e) => {
        writing.current--;
        if (alive.current) setPending((p) => p.filter((x) => x.key !== local.key));
        settled();
        throw e;
      },
    );
  }, [settled]);

  const log = useCallback(async (draft: IntakeDraft) => {
    const delta = draft.amount ?? (draft.kind === "water" ? cup.current : 0);
    const r = await write({ key: draft.id, kind: draft.kind, delta }, () => postEntry(draft));
    // a cup the user did not size: the hub's answer says what its cup is, for the next one's local sum
    if (draft.kind === "water" && draft.amount === undefined && r.entry.amount > 0) cup.current = r.entry.amount;
    return r.entry;
  }, [write]);

  const undo = useCallback(async (id: string, kind: IntakeKind, amount: number) => {
    await write({ key: `undo:${id}`, kind, delta: -amount }, () => deleteEntry(id));
  }, [write]);

  const rings = useMemo(() => {
    if (!server) return null;
    const ring = (kind: IntakeKind): Ring => {
      const sum = pending.filter((p) => p.kind === kind).reduce((n, p) => n + p.delta, 0);
      const today = Math.max(0, Math.round((server[kind].today + sum) * 10) / 10);
      return { today, goal: server[kind].goal, ratio: ratioOf(today, server[kind].goal) };
    };
    return { water: ring("water"), protein: ring("protein") };
  }, [server, pending]);

  return { enabled, rings, error: server ? "" : error, reload: () => void load(), log, undo };
}
