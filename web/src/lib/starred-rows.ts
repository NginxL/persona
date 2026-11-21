// The lazy message controls need the compact index. The sidebar's count and access have their own smaller summary in
// starred-summary.ts; requests and snapshot text remain in the on-demand starred.ts.
import { useSyncExternalStore } from "react";
import type { Item } from "./types";

/** Transcript location as the chat already holds it. */
export type StarOrigin = {
  mid: string;
  sid: string;
  item: string;
  fingerprint?: string;
  userInputs?: readonly Item[];
  merged?: boolean;
};

export interface StarSource {
  machine_id: string;
  session_id: string;
  item_id: string;
  conversation: "assistant" | "agent";
  source_fingerprint?: string;
}

export interface StarInput extends StarSource {
  role: "user" | "assistant";
  title: string;
  speaker: string;
  source_manager?: string;
  written_at?: string;
  text: string;
  omitted_chars?: number;
}

export interface Star extends StarInput {
  id: string;
  starred_at: string;
}

export const positionalStar = (item: string) => /^line[0-9]+#[0-9]+$/.test(item);
export const starrable = (source?: StarOrigin): source is StarOrigin =>
  !!source && (!positionalStar(source.item) || !!source.fingerprint);

export function starKey(s: StarSource & { id?: string }) {
  const proof = s.source_fingerprint || (s.id && positionalStar(s.item_id) ? `saved:${s.id}` : "");
  return JSON.stringify([s.machine_id, s.session_id, s.item_id, s.conversation, proof]);
}

type StarMark = { id?: string; pending?: "add" | "remove" };
const empty: StarMark = {};
let rows: ReadonlyMap<string, StarMark> = new Map();
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => (listeners.add(listener), () => void listeners.delete(listener));

export function setStarredRows(next: ReadonlyMap<string, StarMark>) {
  rows = new Map([...next].map(([key, mark]) => {
    const previous = rows.get(key);
    return [key, previous && previous.id === mark.id && previous.pending === mark.pending ? previous : mark];
  }));
  listeners.forEach((listener) => listener());
}

export function useStarMark(source: StarSource | undefined): StarMark {
  const key = source && starKey(source);
  return useSyncExternalStore(subscribe, () => (key && rows.get(key)) || empty, () => empty);
}

export { useCanStar, useStarAccess } from "./starred-summary";
