import { useLayoutEffect, useState, type RefObject } from "react";
import { parseQueued } from "../../lib/queue";
import type { Session } from "../../lib/types";

export interface QueueEntry {
  id: string;
  raw: string;
  images?: { mime: string; data: string }[];
}

export interface QueueCandidate extends QueueEntry {
  /** Own queue at dispatch. Undefined while waiting behind another POST. */
  before?: string[];
}

function content(raw: string, images = 0) {
  const { text, photos, files, contexts } = parseQueued(raw, images);
  return {
    words: JSON.stringify([text.replace(/\s+/g, " ").trim(), files.map(({ kind, path }) => [kind, path]), contexts]),
    photos: photos.length + images,
  };
}

function matches(raw: string, local: QueueCandidate): boolean {
  const sent = content(local.raw, local.images?.length);
  const queued = content(raw);
  if (!local.raw.trim() && !queued.photos) return false;
  return sent.words === queued.words && (!queued.photos || sent.photos === queued.photos);
}

interface QueueEdit {
  before: QueueEntry[];
  kept: QueueEntry[];
}

export interface QueueSnapshot {
  source?: string[];
  own: number;
  rows: QueueEntry[];
  next: number;
  accepted: string[];
  edit?: QueueEdit;
}

function matchesRows(raw: string[], rows: QueueEntry[]): boolean {
  return raw.length === rows.length && raw.every((text, i) => text === rows[i].raw);
}

/** The hub publishes strings, so existing occurrences get first claim on the new snapshot. Only a new occurrence
 *  can accept a local send, and it accepts at most one. Keep the local key and photos until that row leaves. */
function reconcile(previous: QueueSnapshot, source: string[] | undefined, own: number, pending: QueueCandidate[]): QueueSnapshot {
  const rawRows = (source ?? []).slice(0, own);
  const candidates = pending.filter((p) => p.before);
  const edit = previous.edit;
  // When our edit is reflected in the push, prefer the exact retained occurrences over the wire's identical strings.
  const unchanged = edit && matchesRows(rawRows, edit.before);
  const applied = edit && !unchanged && matchesRows(rawRows, edit.kept);
  const available = (applied ? edit.kept : previous.rows).slice();
  const accepted: string[] = [];
  let next = previous.next;
  const rows = rawRows.map((raw) => {
    const old = available.findIndex((r) => r.raw === raw);
    if (old !== -1) return available.splice(old, 1)[0];
    const match = candidates.findIndex((p) => p.raw === raw && (raw || p.images?.length));
    const i = match !== -1 ? match : candidates.findIndex((p) => matches(raw, p));
    if (i === -1) return { id: `server:${next++}`, raw };
    const [local] = candidates.splice(i, 1);
    accepted.push(local.id);
    return { id: local.id, raw, images: local.images };
  });
  return { source, own, rows, next, accepted, edit: unchanged ? edit : undefined };
}

function initial(pending: QueueCandidate[]): QueueSnapshot {
  const before = pending.find((p) => p.before)?.before;
  const rows = (before ?? []).map((raw, i) => ({ raw, id: `server:${i}` }));
  return { source: before, own: rows.length, rows, next: rows.length, accepted: [] };
}

export function useQueueEntries(session: Session, pending: QueueCandidate[], memory?: RefObject<QueueSnapshot | undefined>, onAccepted?: (ids: string[]) => void) {
  const [previous, setPrevious] = useState(() => memory?.current ?? initial(pending));
  const source = session.queued;
  const count = source?.length ?? 0;
  const own = count - Math.min(Math.max(session.queued_relayed ?? 0, 0), count);
  let snapshot = previous;
  if (source !== previous.source || own !== previous.own) {
    snapshot = reconcile(previous, source, own, pending);
    setPrevious(snapshot);
  }
  const { rows, accepted } = snapshot;
  useLayoutEffect(() => {
    if (memory) memory.current = snapshot;
  }, [memory, snapshot]);
  useLayoutEffect(() => {
    if (accepted.length) onAccepted?.(accepted);
  }, [accepted, onAccepted]);

  // A local edit tells us exactly which occurrence survived, even if equal strings changed places.
  const planEdit = (keep: number[]) => {
    const edit = { before: rows, kept: keep.map((i) => rows[i]) };
    setPrevious((s) => ({ ...s, edit }));
    return (ok: boolean) => setPrevious((s) => {
      if (s.edit !== edit) return s;
      if (!ok) return { ...s, edit: undefined };
      if (matchesRows(s.rows.map((r) => r.raw), edit.kept)) return { ...s, rows: edit.kept, edit: undefined };
      return s; // the successful edit's push is still on the way
    });
  };
  return { rows, accepted, planEdit };
}
