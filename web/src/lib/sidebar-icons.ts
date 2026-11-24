// Settings › General › Appearance › "Sidebar icons" (#10822): Monochrome, the default, draws every sidebar page's line
// icon in the neutral ink, as it always was; Colored gives each built-in page its own named color and an extension's
// nav_page the `icon_color` its manifest names (schema Color: the --hue-* tokens a row's icon_color is drawn in). The
// open page's icon keeps the open row's ink either way. Kept in this browser only, as the theme color and the text size
// are (ui/look.ts), and other tabs follow at once; Monochrome stores nothing, so the page is what it was before this
// existed (docs/design/DESIGN_SYSTEM.md "Sidebar icon colors").
import { useSyncExternalStore } from "react";
import type { UIColor } from "./extension-ui-v6";
import type { NAV_PAGES } from "./persona-nav";

export const SIDEBAR_ICON_MODES = ["monochrome", "colored"] as const;
export type SidebarIconMode = (typeof SIDEBAR_ICON_MODES)[number];
const KEY = "persona.sidebar-icons";

/**
 * Each built-in page's color under Colored, one per page so no two look alike, each picked for what the page is (an
 * amber Overview, an orange Inbox, blue To-dos …). Red is left out: on these pages it would read as an error. Every
 * named color reads at 3:1 on the sidebar and its hover fill, light and dark (ui/theme-contrast.test.ts). History
 * belongs to Agents and is not one of the pages (SidebarParts.tsx useNavPages). A feature module's page names its own
 * (core/module-host.ts ModuleNavItem.color; the health module's is pink).
 */
export const BUILTIN_PAGE_COLORS: Record<Exclude<(typeof NAV_PAGES)[number], "terminal" | "starred">, UIColor> = {
  home: "amber",
  inbox: "orange",
  messages: "green",
  "workspace-board": "yellow",
  todos: "blue",
  routines: "purple",
  kb: "indigo",
  machines: "teal",
  settings: "gray",
};

const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

export function sidebarIconMode(): SidebarIconMode {
  try {
    return localStorage.getItem(KEY) === "colored" ? "colored" : "monochrome";
  } catch {
    return "monochrome";
  }
}

export function setSidebarIconMode(mode: SidebarIconMode) {
  try {
    if (mode === "colored") localStorage.setItem(KEY, mode);
    else localStorage.removeItem(KEY);
  } catch {
    // not kept: the next page starts with the default
  }
  notify();
}

// Another tab of this browser changed it (or its storage was cleared): this one follows, as with the theme color.
if (typeof window !== "undefined")
  addEventListener("storage", (e) => {
    if (e.key === null || e.key === KEY) notify();
  });

// the same on a server render (the palette's tests draw to a string): Monochrome without a browser's storage
export const useSidebarIconMode = () => useSyncExternalStore(subscribe, sidebarIconMode, sidebarIconMode);
