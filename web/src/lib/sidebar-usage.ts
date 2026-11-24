// "Launcher quota in the sidebar": whether the usage card at the bottom of the sidebar (views/UsageBars.tsx) shows.
// On unless turned off (Settings › General › Appearance). It lives in this browser only, as the other Appearance
// choices do; turning it back on stores nothing, so an untouched browser is exactly as before the setting existed.
import { useSyncExternalStore } from "react";

export const SIDEBAR_USAGE_KEY = "persona.sidebar-usage";

const listeners = new Set<() => void>();
function subscribe(l: () => void) {
  listeners.add(l);
  window.addEventListener("storage", l);
  return () => {
    listeners.delete(l);
    window.removeEventListener("storage", l);
  };
}

/** Whether the sidebar shows the quota card (the default). */
export function sidebarUsageShown(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_USAGE_KEY) !== "0";
  } catch {
    return true;
  }
}

export function setSidebarUsageShown(on: boolean): void {
  try {
    if (on) localStorage.removeItem(SIDEBAR_USAGE_KEY);
    else localStorage.setItem(SIDEBAR_USAGE_KEY, "0");
  } catch {
    // storage unavailable: nothing is kept, the default (shown) stays
  }
  listeners.forEach((l) => l());
}

export const useSidebarUsageShown = () => useSyncExternalStore(subscribe, sidebarUsageShown, () => true);
