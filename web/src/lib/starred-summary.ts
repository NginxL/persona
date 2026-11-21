// Only the sidebar needs the count and access. Message identity and per-message marks stay with the chat's lazy
// controls in starred-rows.ts, so the shell does not carry the whole compact index on its first screen.
import { useEffect, useSyncExternalStore } from "react";

let count: number | null | undefined;
let writable = false;
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => (listeners.add(listener), () => void listeners.delete(listener));
let chunk: Promise<typeof import("./starred")> | undefined;

function starredModule() {
  chunk ??= import("./starred").catch((error: unknown) => {
    chunk = undefined;
    throw error;
  });
  return chunk;
}

export function setStarredSummary(total: number | null | undefined, canWrite: boolean) {
  count = total;
  writable = canWrite;
  listeners.forEach((listener) => listener());
}

/** Undefined while availability is unknown, null when unavailable, or the confirmed count. */
export function useStarredCount(enabled: boolean) {
  useEffect(() => {
    if (enabled) void starredModule().then((module) => module.startStarred(), () => {});
  }, [enabled]);
  return useSyncExternalStore(subscribe, () => enabled ? count : null, () => undefined);
}

export function useStarAccess() {
  return useSyncExternalStore(subscribe, () => writable ? "write" : count != null ? "read" : null, () => null);
}

export function useCanStar() {
  return useStarAccess() === "write";
}
