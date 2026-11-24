// The Agents list's filters (#6589): the Filter menu's choices and the words ⌘K put on the list with "Filter Agents
// list". The list has no search field of its own any more, so the palette sets its query here, and the title row's
// Filter button and the chips above the list read the same choices. Kept for this page's life, not stored.
import { useSyncExternalStore } from "react";
import { showFilterNow } from "./agents-display";
import { DEFAULT_DIMENSION_FILTERS, type AgentDimensionFilters } from "./agent-chatlist-filter";

let filters: AgentDimensionFilters = DEFAULT_DIMENSION_FILTERS;
const subscribers = new Set<() => void>();

const subscribe = (f: () => void) => {
  subscribers.add(f);
  return () => void subscribers.delete(f);
};
export const getAgentListFilters = () => filters;

export function setAgentListFilters(next: AgentDimensionFilters) {
  if (next === filters) return;
  filters = next;
  subscribers.forEach((f) => f());
}

/** ⌘K's "Filter Agents list": what was typed narrows the list, its operators as the field took them. */
export function filterAgentList(query: string) {
  if (!showFilterNow()) return; // the Filter is a Settings switch (Agents): off, nothing shows what narrows the list
  setAgentListFilters({ ...filters, query: query.trim() });
}

/** Back to nothing filtered (a new login, a test). */
export function resetAgentListFilters() {
  setAgentListFilters(DEFAULT_DIMENSION_FILTERS);
}

export function useAgentListFilters(): AgentDimensionFilters {
  return useSyncExternalStore(subscribe, getAgentListFilters, getAgentListFilters);
}

/** What the hub's paged list can narrow by, as its filter parameters, for the Filter's choices: the words go to the hub as
 *  `q` and one computer as `machine_id`. Null when a choice has no such parameter (a model, a harness, a project, Pinned,
 *  a search scope, a time window, Search as Code, several computers: the hub counts and filters none of them by these
 *  names), so the complete list, which applies every one, answers instead. */
export function pagedFilterOf(f: AgentDimensionFilters): Partial<Record<"machine_id", string>> | null {
  if (f.harnesses.length || f.projects.length || f.models.length || f.machines.length > 1 || f.pinnedOnly || f.scope !== "all" || f.since || f.searchAsCode) return null;
  return f.machines.length ? { machine_id: f.machines[0]! } : {};
}
