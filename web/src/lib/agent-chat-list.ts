// The Agents list's "Chat list view" (Filter menu): one choice per browser, shared by the complete list that draws it and
// by the sidebar's choice of list, which needs the complete one for it (the hub's paged list has no chat rows).
import { useSyncExternalStore } from "react";

const KEY = "persona.agentChatList";
const read = () => { try { return localStorage.getItem(KEY) === "1"; } catch { return false; } };
let value = read();
const listeners = new Set<() => void>();
const subscribe = (f: () => void) => { listeners.add(f); return () => { listeners.delete(f); }; };
const snapshot = () => value;

export function setChatList(next: boolean) {
  if (next === value) return;
  value = next;
  try { localStorage.setItem(KEY, next ? "1" : "0"); } catch { /* kept for this page */ }
  listeners.forEach((f) => f());
}

/** The stored choice (it is read again when the stored value changed behind this module's back, e.g. a test). */
export function useChatList(): boolean {
  value = read();
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
