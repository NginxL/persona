// The current assistant (docs/assistant/managers.md "Clients"): with several managers, each a whole assistant, the sidebar's
// switcher picks the one the whole app shows. The pick is kept in this browser (per hub: the page's origin and proxy
// prefix) across reloads; a manager that left the roster goes back to the default. The assistant's requests name the
// chosen one (`X-Persona-Manager`, docs/protocol/protocol.md "Per-manager routing"); with the default chosen none is sent,
// so a hub with one manager gets the same requests as before. Live updates of another manager's assistant (they carry
// `manager`; none means the hub's own, `assistant`) are left out. No imports from api.ts: it reads this.
import { createContext, useContext, useSyncExternalStore } from "react";
import { BASE } from "./base";

export const MANAGER_HEADER = "X-Persona-Manager";
/** The id of the assistant the hub runs itself: what a message without `manager` comes from. */
export const BUILTIN_MANAGER = "assistant";

/** Where this browser keeps the pick: one hub per origin, and per proxy prefix where several share an origin. */
export const CHOICE_KEY = `persona.currentManager${BASE ? `:${BASE}` : ""}`;

function storedChoice(): string | null {
  try {
    return localStorage.getItem(CHOICE_KEY) || null;
  } catch {
    return null; // no storage (a private window, a test without a DOM): the pick lasts the page
  }
}

function storeChoice(id: string | null) {
  try {
    if (id) localStorage.setItem(CHOICE_KEY, id);
    else localStorage.removeItem(CHOICE_KEY);
    // a name is the pick's: a new pick (or none) has none until the roster gives it
    localStorage.removeItem(PICKED_NAME_KEY);
  } catch {
    // as above
  }
}

/** Where this browser keeps the pick's name as the roster last gave it: the sidebar's head shows it before the roster
 *  is read again (a reload), rather than the hub's own name for a moment. */
export const PICKED_NAME_KEY = `persona.pickedAssistantName${BASE ? `:${BASE}` : ""}`;

function storedName(): string | null {
  try {
    return localStorage.getItem(PICKED_NAME_KEY) || null;
  } catch {
    return null;
  }
}

// Only another manager than the default is kept: following the default is the absence of a pick, so a hub with one
// manager stores and sends nothing. Before the roster is read the kept pick already names its manager on requests.
let chosen: string | null = storedChoice();
let chosenName: string | null = chosen ? storedName() : null;
let rosterDefault = BUILTIN_MANAGER;
// Whether rosterDefault is the roster's (read once, or the hub has none to read): until then it is a placeholder, and
// what goes by the default (the pages without a switcher, an item's requests) waits for it or is evaluated again.
let defaultSettled = false;
// Whether the hub returned a roster with a default manager (false after a failed/404 roster read or reset).
let defaultConfirmed = false;
const listeners = new Set<() => void>();
const defaultListeners = new Set<(was: { id: string; settled: boolean }) => void>();

const emit = () => listeners.forEach((l) => l());

/** The manager picked in the switcher; null: the default one. */
export function chosenManager(): string | null {
  return chosen;
}

/** The picked manager's name as the roster last gave it (null: no pick, or not known yet). */
export function pickedName(): string | null {
  return chosen ? chosenName : null;
}

/** The roster names the picked manager name (lib/managers.ts): kept with the pick for the next reload. */
export function notePickedName(name: string | null) {
  if (!chosen || name === chosenName) return;
  chosenName = name;
  try {
    if (name) localStorage.setItem(PICKED_NAME_KEY, name);
    else localStorage.removeItem(PICKED_NAME_KEY);
  } catch {
    // as storeChoice
  }
}

/** The assistant identity the sidebar last showed, for that same head while the roster loads again. Unlike the pick,
 *  this includes the default and is display-only: it never chooses a manager or adds a routing header or query. */
export interface ShownIdentity {
  id: string;
  name: string;
}

export const DISPLAY_IDENTITY_KEY = `persona.shownAssistant${BASE ? `:${BASE}` : ""}`;

function storedShownIdentity(): ShownIdentity | null {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(DISPLAY_IDENTITY_KEY) ?? "null");
    if (!value || typeof value !== "object") return null;
    const { id, name } = value as Record<string, unknown>;
    return typeof id === "string" && id && typeof name === "string" && name ? { id, name } : null;
  } catch {
    return null; // unavailable storage or malformed JSON: the in-memory identity still lasts this page
  }
}

let rememberedIdentity: ShownIdentity | null = storedShownIdentity();
const identityListeners = new Set<() => void>();

/** The last displayable current assistant, independent of the pick and its request-routing state. */
export function shownIdentity(): ShownIdentity | null {
  return rememberedIdentity;
}

/** Records (or clears) the identity for the sidebar's head. Null is for a fresh roster with no installed current
 *  assistant, a genuinely old hub, and test cleanup; merely reloading or failing to read a roster does not call it. */
export function noteShownIdentity(identity: ShownIdentity | null) {
  const unchanged = rememberedIdentity?.id === identity?.id && rememberedIdentity?.name === identity?.name;
  if (unchanged && identity) return;
  rememberedIdentity = identity;
  try {
    if (identity) localStorage.setItem(DISPLAY_IDENTITY_KEY, JSON.stringify(identity));
    else localStorage.removeItem(DISPLAY_IDENTITY_KEY);
  } catch {
    // as storedChoice: no storage still leaves the identity available for this page
  }
  if (!unchanged) identityListeners.forEach((l) => l());
}

const subscribeShownIdentity = (l: () => void) => {
  identityListeners.add(l);
  return () => void identityListeners.delete(l);
};

/** The remembered head identity, re-rendering when a fresh roster changes or invalidates it. */
export function useShownIdentity(): ShownIdentity | null {
  return useSyncExternalStore(subscribeShownIdentity, shownIdentity, shownIdentity);
}

/** The roster's default manager: whose an item without `manager` is. */
export function defaultManager(): string {
  return rosterDefault;
}

/** The current assistant, whose things the app shows: the chosen one, else the roster's default. */
export function shownManager(): string {
  return chosen ?? rosterDefault;
}

/** Pick the current assistant; the default (or null) goes back to sending no header. Kept across reloads. */
export function chooseManager(id: string | null) {
  const next = id === null || id === rosterDefault ? null : id;
  if (next === chosen) return;
  chosen = next;
  chosenName = null;
  storeChoice(chosen);
  emit();
}

/** The roster's default changed (GET /managers `default`): picking it means no header. */
export function setRosterDefault(id: string) {
  const was = { id: rosterDefault, settled: defaultSettled };
  defaultSettled = true;
  defaultConfirmed = Boolean(id);
  if (!id || id === rosterDefault) {
    if (!was.settled) defaultListeners.forEach((l) => l(was));
    return;
  }
  const shown = shownManager();
  rosterDefault = id;
  if (shown === id && chosen) {
    chosen = null;
    chosenName = null;
    storeChoice(null);
  }
  emit();
  defaultListeners.forEach((l) => l(was));
}

/** The default manager confirmed by a live roster response, or undefined before the roster loaded or when unavailable. */
export function confirmedDefaultManager(): string | undefined {
  return defaultConfirmed ? rosterDefault : undefined;
}

/** Clears the confirmed-roster marker (tests resetting the roster). */
export function clearConfirmedDefault() {
  defaultConfirmed = false;
}

/** The hub has no roster (from before it), or it could not be read: the default stays the hub's own assistant, and
 *  what waited for the roster goes on. A roster read later still moves it (setRosterDefault). */
export function settleRosterDefault() {
  if (defaultSettled) return;
  defaultSettled = true;
  defaultListeners.forEach((l) => l({ id: rosterDefault, settled: false }));
}

/** Whether the roster's default is known (see settleRosterDefault); before, defaultManager() is a placeholder. */
export function defaultKnown(): boolean {
  return defaultSettled;
}

/** Called when the default becomes known or moves, with what it was: what was read, filtered or cached under the old
 *  one (or the placeholder) is evaluated again. */
export function onDefaultManager(l: (was: { id: string; settled: boolean }) => void): () => void {
  defaultListeners.add(l);
  return () => void defaultListeners.delete(l);
}

const defaultSnapshot = () => (defaultSettled ? rosterDefault : "");
const subscribeDefault = (l: () => void) => onDefaultManager(() => l());

/** The roster's default once known, "" before; re-renders when it becomes known or moves. A view about one manager's
 *  thing waits for it, so nothing it asks goes by the placeholder. */
export function useKnownDefault(): string {
  return useSyncExternalStore(subscribeDefault, defaultSnapshot, defaultSnapshot);
}

// The things a merged list showed with their manager (an inbox item), and the conversations each manager's page listed,
// by their path: requests for one name its manager, as the hub asks ("When a list item carries manager, include it").
// Every stamp is kept; whether it needs a header is decided when asked, against the default then (the roster may move
// it meanwhile).
const itemManagers = new Map<string, string>();

/** A merged list showed the thing at path (e.g. `/inbox/x`) as manager's (none: forget it). */
export function noteItemManager(path: string, manager: string | undefined) {
  if (manager) itemManagers.set(path, manager);
  else itemManagers.delete(path);
}

/** The manager a page noted for conversation id (noteConversation), undefined when none (the default's, or unknown). */
export function conversationManager(id: string): string | undefined {
  const m = itemManagers.get(`/assistant/conversations/${encodeURIComponent(id)}`);
  return m && m !== rosterDefault ? m : undefined;
}

/** A manager's page listed conversation id (or this page started it there): its requests go to that manager. A
 *  conversation of the default (undefined: the page asked the default, whichever that turns out to be) is not noted: with
 *  no header the hub sends it to the conversation's own manager, so a placeholder default from before the roster
 *  loaded is never sent. */
export function noteConversation(id: string, manager: string | undefined) {
  if (id) noteItemManager(`/assistant/conversations/${encodeURIComponent(id)}`, manager);
}

// The manager a conversation's address names (`?manager=`): the last fallback for conversationHeaders, after what a page
// noted and the store's row, for an address whose row is not found (its computer moved to another assistant, or the
// address was edited).
const routedManagers = new Map<string, string>();

/** A page opened conversation id by an address naming manager (none: forget it). */
export function noteRoutedConversation(id: string, manager: string | undefined) {
  if (!id) return;
  if (manager) routedManagers.set(id, manager);
  else routedManagers.delete(id);
}

/** The manager the address of conversation id named, undefined when none or the default's. */
export function routedConversationManager(id: string): string | undefined {
  const m = routedManagers.get(id);
  return m && m !== rosterDefault ? m : undefined;
}

// Pins: while a view about one manager's thing is open (an extension's details or pages), the requests under its
// prefix name that manager; the latest pin of a prefix wins, and each pin goes with its own view (released by the pin
// itself, so two views of the same manager don't take each other's). Views about an inbox item don't pin: they name the
// item's manager on each request they make (ItemManager below), which a request deferred past the view still carries.
type Pin = { manager: string };
const pins = new Map<string, Pin[]>();

/** Requests under prefix (e.g. `/extensions/voice`) go to manager until the returned function is called. */
export function pinManager(prefix: string, manager: string | undefined): () => void {
  if (!manager) return () => {};
  const pin: Pin = { manager };
  pins.set(prefix, [...(pins.get(prefix) ?? []), pin]);
  return () => {
    const left = (pins.get(prefix) ?? []).filter((x) => x !== pin);
    if (left.length) pins.set(prefix, left);
    else pins.delete(prefix);
  };
}

/** pinManager for several prefixes at once; the returned function releases them all. */
export function pinManagerAt(prefixes: string[], manager: string | undefined): () => void {
  const offs = prefixes.map((p) => pinManager(p, manager));
  return () => offs.forEach((off) => off());
}

/** The manager of the inbox item a view shows (its merged list's stamp; none: the default's). Its chat, mail and
 *  connector thread are that manager's: every request the view makes names it (itemHeaders / withManager). */
export const ItemManager = createContext<string | undefined>(undefined);

export function useItemManager(): string | undefined {
  return useContext(ItemManager);
}

/** The manager of the chat (an agent's, a conversation) or the opened document a link is in: a document or post the
 *  link opens in the page is read through it (docs/assistant/managers.md "Grouping"). Where none is provided, an inbox
 *  item's (ItemManager), else the default's. */
export const LinkManager = createContext<string | undefined>(undefined);

export function useLinkManager(): string | undefined {
  const chat = useContext(LinkManager);
  const item = useContext(ItemManager);
  return chat ?? item;
}

/** The header naming manager on a request about one of its things; none for the default's, or none known. */
export function itemHeaders(manager: string | undefined): Record<string, string> | undefined {
  return manager && manager !== rosterDefault ? { [MANAGER_HEADER]: manager } : undefined;
}

/** The header naming manager on a change to a thing a list read from each manager stamped with it (several managers),
 *  even when it is the default: the default may have changed since the list was read. None for an unstamped thing (one
 *  manager). */
export function stampedHeaders(manager: string | undefined): Record<string, string> | undefined {
  return manager ? { [MANAGER_HEADER]: manager } : undefined;
}

/** url (an /api/v1 path or a full /api/ URL) with `manager=` for another manager's thing, as an <img> or a link
 *  can't send a header (the hub reads the query too); as it is for the default's, and for anything not the hub's (a
 *  picture's https: or blob: URL). */
export function withManager(url: string, manager: string | undefined): string {
  if (!manager || manager === rosterDefault || !url.startsWith("/") || url.startsWith("//") || /[?&]manager=/.test(url)) return url;
  const hash = url.indexOf("#");
  const [head, tail] = hash < 0 ? [url, ""] : [url.slice(0, hash), url.slice(hash)];
  return `${head}${head.includes("?") ? "&" : "?"}manager=${encodeURIComponent(manager)}${tail}`;
}

/** The manager a pin or a noted stamp gives path, or the one under it; undefined when none or when it is the default. */
function itemManager(path: string): string | undefined {
  const bare = path.split("?")[0];
  for (let p = bare; p.length > 1; p = p.slice(0, p.lastIndexOf("/"))) {
    const m = pins.get(p)?.at(-1)?.manager ?? itemManagers.get(p);
    if (m) return m === rosterDefault ? undefined : m;
  }
  return undefined;
}

const CONVERSATION = /^\/assistant\/conversations\/[^/?]+/;
// The routes that are the current assistant's as a whole: its page, conversations, scheduled tasks and memory
// (`/assistant…`), its knowledge base with the recordings it keeps there, and its computers' token history (the
// new-chat page's calendar, #9506; the island's `/usage` stays the default's).
const CURRENT = /^\/(?:assistant|kb|kb-search|recordings|usage\/days|usage\/hours)(?=$|[/?])/;

/** The header value for a request to path (under /api/v1), or undefined.
 *  - One conversation's routes name its manager when a page listed it, else none: the hub then sends them to the
 *    conversation's manager itself (ownership.go byConversation), and naming the page's pick would override that.
 *  - The assistant's other routes and its knowledge base name the current assistant, but not the health space's
 *    (`?space=`, docs/assistant/health.md: that page stays the default manager's).
 *  - Anything else: a pin or a thing a merged list showed as another manager's names that one. */
export function managerHeaderFor(path: string): string | undefined {
  if (CONVERSATION.test(path)) return itemManager(path);
  if (chosen && CURRENT.test(path) && !/[?&]space=/.test(path)) return chosen;
  return itemManager(path);
}

/** The manager a hub message is from: its `manager`, else the hub's own assistant (docs/protocol/protocol.md). */
export function messageManager(msg: Record<string, unknown>): string {
  return typeof msg.manager === "string" && msg.manager ? msg.manager : BUILTIN_MANAGER;
}

/** Whether a hub message is the default manager's, whose settings the pages without a switcher show. */
export function fromDefaultManager(msg: Record<string, unknown>): boolean {
  return messageManager(msg) === rosterDefault;
}

/** Whether a hub message is the shown manager's: one without `manager` is the hub's own assistant's. */
export function fromShownManager(msg: Record<string, unknown>): boolean {
  return messageManager(msg) === shownManager();
}

/** The header naming owner (a thing's `manager`; none: the default's) on a request that would otherwise go by the
 *  assistant page's pick: none when nothing was picked and the thing is the default's (it names none, or names the
 *  default: a hub with one manager stamps every item `assistant`), so one manager sees no change. */
export function ownerHeaders(owner: string | undefined): Record<string, string> | undefined {
  if ((!owner || owner === rosterDefault) && !chosen) return undefined;
  return { [MANAGER_HEADER]: owner || rosterDefault };
}

/** The header naming the current assistant on a request that makes a thing of its own (a new to-do): none while the
 *  default is the current one, so a hub with one manager gets the same request as before. */
export function currentHeaders(): Record<string, string> | undefined {
  return chosen ? { [MANAGER_HEADER]: chosen } : undefined;
}

/** owner when it is another manager than the default, else undefined: what a page notes for a conversation it started
 *  there (noteConversation) and the item routes carry (`@manager`). */
export function otherManager(owner: string | undefined): string | undefined {
  return owner && owner !== rosterDefault ? owner : undefined;
}

/** A settings page's route for a thing of manager: `<id>@<manager>` for another manager than the default's (the same id
 *  may be under several: each one's built-ins), the id alone for the default's. */
export function ownedSub(id: string, manager: string | undefined): string {
  const m = otherManager(manager);
  return m ? `${id}@${m}` : id;
}

/** What ownedSub wrote: the id, and the manager when it names one. */
export function parseOwnedSub(sub: string): { id: string; manager?: string } {
  const at = sub.indexOf("@");
  return at > 0 && at < sub.length - 1 ? { id: sub.slice(0, at), manager: sub.slice(at + 1) } : { id: sub };
}

/** Whether a and b (two things' `manager`; none: the default's) are the same manager's. */
export function sameManager(a: string | undefined, b: string | undefined): boolean {
  return (a || rosterDefault) === (b || rosterDefault);
}

export function onManagerChoice(l: () => void): () => void {
  listeners.add(l);
  return () => void listeners.delete(l);
}

/** The shown manager's id, re-rendering when it changes. */
export function useShownManager(): string {
  return useSyncExternalStore(onManagerChoice, shownManager, shownManager);
}

/** The picked manager (null: the default), re-rendering when it changes: what the assistant's requests name. */
export function useChosenManager(): string | null {
  return useSyncExternalStore(onManagerChoice, chosenManager, chosenManager);
}

/** Tests: back to the default, the hub's own assistant, not known yet, with no remembered head identity. */
export function resetManagerChoice() {
  chosen = null;
  chosenName = null;
  storeChoice(null);
  itemManagers.clear();
  pins.clear();
  rosterDefault = BUILTIN_MANAGER;
  defaultSettled = false;
  defaultConfirmed = false;
  emit();
  // Choice listeners may note the roster's current identity; tests still end with every remembered display gone.
  noteShownIdentity(null);
}
