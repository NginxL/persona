// Going from a Persona conversation to an Agent it handed work to, or a task: where it opens is the browser's setting
// (lib/agent-open.ts, #5424). By default the side panel beside the chat (TaskPanel / AgentPanel; on a phone-sized
// window it covers the chat). Set to "Full page" (#5096, after cindy's SessionHandoffCard): the Agent's own page, which
// says where it came from ("Came from … · click to return", ChatView's HandoffBack), and going back puts the
// conversation's chat where it was (ChatView takes the place `leaveChat` kept on the conversation's history entry).
// An Agent opened from inside another page (a to-do, a knowledge project, a meeting: `openFromPage`, #12055) says where
// it came from the same way, and the sidebar keeps that page's row (lib/persona-nav.ts sidebarRoute, #9971).
import { agentOpenMode } from "./agent-open";
import { onOverlayEntry } from "./back-close";
import { stripBase } from "./base";
import { REPIN_AT_BOTTOM } from "./disclosure";
import { openPanel, type Panel } from "./panel";
import { navigate, routeAt, type Route } from "./router";
import { getState, taskSessionLookup, type State } from "./store";
import { mostUrgentAgent } from "./todos";

const FROM = "handoff";
const PLACE = "chatPlace";
const PANEL = "sidePanel";

/** Where an Agent's page was opened from: the address in the app of the conversation or page it was opened from (the
 *  page names it, ChatView), and `page`, the first page of a chain when that address is itself an Agent's page opened
 *  from another (the sidebar keeps that one's row, #12055). */
export interface HandoffFrom {
  path: string;
  page?: string;
}

/** Where a chat was when the page left it: at its end, or the first line still in view and how far under the view's top
 *  it started (as search's "Back to where you were" keeps it). */
export interface ChatPlace {
  end: boolean;
  line?: string;
  offset: number;
}

function entry(state: unknown): Record<string, unknown> {
  return state && typeof state === "object" ? (state as Record<string, unknown>) : {};
}

const current = () => (typeof history !== "undefined" ? history.state : null);
const address = () => stripBase(location.pathname) + location.search;

// The place a hand-off kept when the conversation's entry could not carry it: left from an overlay's entry (a sheet, the
// task board), which the new page takes over, so Back lands on the entry under it. Used when Back reaches that address.
let left: { path: string; place: ChatPlace; back: boolean; panel?: Panel } | null = null;
if (typeof addEventListener !== "undefined") {
  // capture: before the router's listener draws the page that reads it
  addEventListener("popstate", () => {
    if (left) left.back = left.path === address();
  }, true);
  // after it: Back to a conversation left from its side panel (a task's or an Agent's "Open Agent page") opens that panel
  // again, as the chat's place is put back; the strip's "click to return" is this same Back
  addEventListener("popstate", () => {
    const p = panelIn();
    if (p) openPanel(p);
  });
}

/** The side panel a hand-off left this history entry's conversation with (only a task's or an Agent's), if any. */
export function panelIn(state: unknown = current()): Panel | undefined {
  const v = entry(entry(state)[PANEL]);
  if (v.kind === "task" && typeof v.todo === "string") return { kind: "task", todo: v.todo };
  if (v.kind === "agent" && typeof v.mid === "string" && typeof v.sid === "string") return { kind: "agent", mid: v.mid, sid: v.sid };
  return left?.back && left.path === address() ? left.panel : undefined;
}

/** The conversation or page the page now shown was opened from by a hand-off, if any. */
export function handoffFrom(state: unknown = current()): HandoffFrom | undefined {
  const v = entry(entry(state)[FROM]);
  if (!inApp(v.path)) return undefined;
  return inApp(v.page) ? { path: v.path, page: v.page } : { path: v.path };
}

const inApp = (path: unknown): path is string => typeof path === "string" && path.startsWith("/");

/** The page whose sidebar row stays lit on a page opened from another (#12055): the first page of the chain. */
export function sidebarPageOf(from: HandoffFrom | undefined): string | undefined {
  return from && (from.page ?? from.path);
}

/** What a page opened from the one showing now says about where it came from: this page's address and, when this page
 *  is itself an Agent's opened from another, the first page of that chain. */
export function fromHere(): HandoffFrom {
  const path = address();
  const here = routeAt(location.href);
  const page = here?.name === "session" || here?.name === "tab" ? sidebarPageOf(handoffFrom()) : undefined;
  return page ? { path, page } : { path };
}

/** The history state of a page opened from the one showing now (a link's `onLinkClick` state): `fromHere`. */
export function openedFromHere(): Record<string, unknown> {
  return { [FROM]: fromHere() satisfies HandoffFrom };
}

/**
 * Opens `to` (an Agent's page) from inside the page showing now: a drill-down (#9971, #12055), as a to-do's Agent rows,
 * a knowledge project's Agents and a meeting's hand-off open one. The new entry says where it came from, so the Agent's
 * page offers the way back ("Came from … · click to return") and the sidebar keeps that page's row; from a Persona
 * conversation the chat's place is kept too (`leaveChat`).
 */
export function openFromPage(to: Route) {
  leaveChat(to);
}

/** The place this history entry's chat was left at, if a hand-off kept one (on the entry, else for Back to the
 *  address it was left at). */
export function chatPlaceIn(state: unknown = current()): ChatPlace | undefined {
  return placeOf(entry(state)[PLACE]) ?? (left?.back && left.path === address() ? left.place : undefined);
}

function placeOf(value: unknown): ChatPlace | undefined {
  const v = entry(value);
  if (typeof v.end !== "boolean") return undefined;
  return { end: v.end, line: typeof v.line === "string" ? v.line : undefined, offset: typeof v.offset === "number" && Number.isFinite(v.offset) ? v.offset : 0 };
}

/** Takes the kept place off this entry once it was used: a reload later opens the chat at its end, as before. */
export function dropChatPlace() {
  if (left?.back) left = null;
  const { [PLACE]: _gone, ...rest } = entry(current());
  if (_gone !== undefined) history.replaceState(rest, "");
}

/** The first line of `sc`'s chat still in view and how far under the view's top it starts (search's "Back to where you
 *  were" keeps it too); the lines are in order, so it stops at the first. */
export function firstLineInView(sc: HTMLElement): { line?: string; offset: number } {
  const top = sc.getBoundingClientRect().top;
  for (const el of sc.querySelectorAll<HTMLElement>("[data-line]")) {
    const r = el.firstElementChild?.getBoundingClientRect();
    if (r && r.bottom > top) return { line: el.dataset.line, offset: r.top - top };
  }
  return { offset: 0 };
}

/** Where `sc`'s chat is now. */
export function chatPlaceOf(sc: HTMLElement): ChatPlace {
  if (sc.scrollHeight - sc.scrollTop - sc.clientHeight <= REPIN_AT_BOTTOM) return { end: true, offset: 0 };
  return { end: false, ...firstLineInView(sc) };
}

/** Where a task opens: its most pressing Agent's page, or the to-do's own page while no Agent is on it (one that did
 *  not start has no page to show). */
export function taskRoute(id: string, s: State = getState()): Route {
  const todo = s.todos.find((x) => x.id === id);
  const lead = todo ? mostUrgentAgent({ ...todo, agents: todo.agents?.filter((a) => !a.start_error) }, taskSessionLookup(s))?.agent : undefined;
  return lead ? { name: "session", mid: lead.machine_id, sid: lead.session_id } : { name: "todos", id };
}

/** Opens a task from a conversation: in the side panel (its Agent's chat, TaskPanel), or as a page, its most pressing
 *  Agent's, or the to-do's while no Agent is on it. */
export function openTask(id: string) {
  if (agentOpenMode() === "panel") openPanel({ kind: "task", todo: id });
  else leaveChat(taskRoute(id));
}

/** Opens an Agent from a conversation: in the side panel (AgentPanel), or as its page. */
export function openAgent(mid: string, sid: string) {
  if (agentOpenMode() === "panel") openPanel({ kind: "agent", mid, sid });
  else leaveChat({ name: "session", mid, sid });
}

/**
 * Opens `to` as a page of its own. With `from` the new page's entry says which conversation or page it came from
 * (`fromHere`); left from an assistant conversation, the conversation's history entry also keeps where its chat is (Back
 * puts it there again). `panel`: the side panel it was left from (a task's or an Agent's), opened again when Back
 * returns to it.
 */
export function leaveChat(to: Route, from = true, panel?: Panel) {
  const here = typeof location !== "undefined" ? routeAt(location.href) : null;
  const state = from ? openedFromHere() : null;
  if (here?.name !== "persona") return navigate(to, false, state);
  // the main chat's own scroller, not the side panel's chat (which also has one, after it)
  const sc = [...document.querySelectorAll<HTMLElement>("[data-chat-scroll]")].find((el) => !el.closest("aside")) ?? null;
  const path = address();
  const kept = panel && (panel.kind === "task" || panel.kind === "agent") ? panel : undefined;
  const place = sc ? chatPlaceOf(sc) : undefined;
  // an overlay's entry is taken over by the new page (router.ts navigate): Back lands on the conversation's own
  left = onOverlayEntry() && (place || kept) ? { path, place: place ?? { end: true, offset: 0 }, back: false, panel: kept } : null;
  if (!left) {
    // a panel an earlier hand-off kept here goes either way: Back opens only the one this one left from
    const { [PANEL]: old, ...rest } = entry(history.state);
    if (place || kept || old !== undefined) history.replaceState({ ...rest, ...(place ? { [PLACE]: place } : {}), ...(kept ? { [PANEL]: kept } : {}) }, "");
  }
  navigate(to, false, state);
}
