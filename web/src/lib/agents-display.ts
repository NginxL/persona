// One acknowledged display choice for the paired hub's default manager. Inventory
// and to-do attribution stay unchanged. The old local key is migration input only.
import { useSyncExternalStore } from "react";
import { api, credentialOwner, onCredentialChange } from "./api";

const LEGACY = "persona.agentHideBotmux";
const PATH = "/agents/display-settings";
export interface AgentsDisplay { show_botmux: boolean; configured: boolean; use_herdr_tab_name?: boolean }
type DisplayChange = { show_botmux: boolean } | { use_herdr_tab_name: boolean };
interface State { value: AgentsDisplay | null; saving: boolean; error?: unknown; change?: DisplayChange }
let state: State = { value: null, saving: false };
let revision = 0;
let reading: Promise<void> | undefined;
const listeners = new Set<() => void>();
const snapshot = () => state;
const subscribe = (f: () => void) => { listeners.add(f); return () => { listeners.delete(f); }; };
const set = (next: State) => { state = next; listeners.forEach((f) => f()); };
function checked(value: AgentsDisplay): AgentsDisplay {
  if (typeof value.show_botmux !== "boolean" || typeof value.configured !== "boolean" ||
    (value.use_herdr_tab_name !== undefined && typeof value.use_herdr_tab_name !== "boolean")) throw new Error("Invalid Agents display settings");
  return value;
}
function legacyHidden() { try { return localStorage.getItem(LEGACY) === "1"; } catch { return false; } }
function forgetLegacy() { try { localStorage.removeItem(LEGACY); } catch { /* a later read may retry */ } }

/** Called on hello and each Agents list refresh. Stale reads cannot undo a save or a new credential. */
export function refreshAgentsDisplay(): Promise<void> {
  credentialOwner();
  if (state.saving) return Promise.resolve();
  if (reading) return reading;
  const mine = revision;
  const work = (async () => {
    try {
      let value = checked(await api<AgentsDisplay>(PATH));
      if (mine !== revision) return;
      if (!value.configured && legacyHidden()) {
        value = checked(await api<AgentsDisplay>(PATH, { method: "PUT", json: { show_botmux: false, only_if_unset: true } }));
      }
      if (mine !== revision) return;
      if (value.configured) forgetLegacy();
      set(state.change === undefined ? { value, saving: false } : { ...state, value });
    } catch (error) {
      if (mine === revision && state.change === undefined) set({ ...state, error });
    }
  })();
  reading = work;
  void work.finally(() => { if (reading === work) reading = undefined; });
  return work;
}

export async function saveAgentsDisplay(input: boolean | DisplayChange): Promise<void> {
  credentialOwner();
  if (state.saving) return;
  const mine = ++revision;
  const change = typeof input === "boolean" ? { show_botmux: input } : input;
  set({ value: state.value, saving: true, change });
  try {
    const value = checked(await api<AgentsDisplay>(PATH, { method: "PUT", json: change }));
    if (mine !== revision) return;
    if (value.configured) forgetLegacy();
    set({ value, saving: false });
  } catch (error) {
    if (mine === revision) set({ ...state, saving: false, error });
  }
}

export function resetAgentsDisplay() {
  revision++;
  reading = undefined;
  set({ value: null, saving: false });
}
onCredentialChange(resetAgentsDisplay);
export function useAgentsDisplay() {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

export function retryAgentsDisplay(): Promise<void> {
  return state.change === undefined ? refreshAgentsDisplay() : saveAgentsDisplay(state.change);
}

export function useBotmuxHidden(): boolean {
  // An unavailable preference might hide these sessions. Keep the last acknowledged choice.
  return useAgentsDisplay().value?.show_botmux !== true;
}

export function useHerdrTabName(): boolean {
  return useAgentsDisplay().value?.use_herdr_tab_name === true;
}
