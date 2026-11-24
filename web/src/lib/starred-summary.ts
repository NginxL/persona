// Only the sidebar needs the count and access. Message identity and per-message marks stay with the chat's lazy
// controls in starred-rows.ts, so the shell does not carry the whole compact index on its first screen.
import { useEffect, useSyncExternalStore } from "react";

const KEY = "persona.starredMessages";
const switchListeners = new Set<() => void>();

/** Whether this browser shows Starred (Settings › General › Appearance): off unless turned on. Off only hides the sidebar
 *  page, the message menu's Star / Unstar and the saved marker; the saved messages stay on the hub. */
export function starredEnabled(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export function setStarredEnabled(on: boolean): void {
  try {
    if (on) localStorage.setItem(KEY, "1");
    else localStorage.removeItem(KEY);
  } catch {
    // storage unavailable: nothing is kept, the default (off) stays
  }
  switchListeners.forEach((l) => l());
}

function subscribeSwitch(l: () => void) {
  switchListeners.add(l);
  window.addEventListener("storage", l);
  return () => {
    switchListeners.delete(l);
    window.removeEventListener("storage", l);
  };
}

export function useStarredSetting(): boolean {
  return useSyncExternalStore(subscribeSwitch, starredEnabled, () => false);
}

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
export function useStarredCount(supported: boolean) {
  const enabled = useStarredSetting() && supported;
  useEffect(() => {
    if (enabled) void starredModule().then((module) => module.startStarred(), () => {});
  }, [enabled]);
  return useSyncExternalStore(subscribe, () => enabled ? count : null, () => undefined);
}

export function useStarAccess() {
  const on = useStarredSetting();
  const access = useSyncExternalStore(subscribe, () => writable ? "write" : count != null ? "read" : null, () => null);
  return on ? access : null;
}

export function useCanStar() {
  return useStarAccess() === "write";
}
