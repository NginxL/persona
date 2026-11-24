// A Settings category's URL (views/settings/nav.ts). Apart from nav.ts, whose category list is built with the words of
// every category, so a page that only links to Settings (the Persona page's extension link) keeps nav.ts and its words
// off the first screen.
import type { SettingsSection, Tab } from "./nav";

// The tab whose page opens one of its items, and the query parameter that names it.
export const SUB: Partial<Record<Tab, string>> = { models: "purpose", channels: "channel", extensions: "extension", connectors: "connector" };

/** The URL of a category (and its open item). "General" is plain /settings on a wide screen, where that opens it; on a
 *  phone /settings is the list, so it is named. */
export function settingsHref(tab: Tab, id: string | null, phone: boolean, manager?: string | null, section?: SettingsSection | null): string {
  const param = SUB[tab];
  const query = `/settings?tab=${tab}${id && param ? `&${param}=${encodeURIComponent(id)}` : ""}${manager ? `&manager=${encodeURIComponent(manager)}` : ""}`;
  return `${tab === "general" && !phone ? "/settings" : query}${section ? `#settings-${section}` : ""}`;
}
