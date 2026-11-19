// Where the app opens (#9071, docs/web/web.md "Start page"): Persona's main chat, unless the user chose another page in
// Settings › General › Start page. It also carries the sidebar's arrangement and Overview's cards (#9745) to and from the hub (below). The hub keeps the choice (GET / PUT /api/v1/navigation, its default manager's): a
// hub has one owner, so every device of theirs follows. This browser keeps a copy, so an opening decides before the
// first paint without waiting for the hub, and keeps the choice itself where the hub has no such route (404). Only an
// opening at the root address goes there (a new tab, the installed app, the hub's address typed): a reload, Back /
// Forward, a link to a page and the sidebar's Persona row are left alone. A page that is not there (its extension
// removed or turned off, Messages or the workflow board switched off) opens the main chat instead; the choice stays,
// and Settings says so.
import { useEffect, useRef, useSyncExternalStore } from "react";
import { api, ApiError } from "./api";
import { stripBase } from "./base";
import { hasSlots } from "./extension-slots";
import { feedCacheScope } from "./feed-cache";
import { NAV_PAGES } from "./persona-nav";
import { adoptOverview, getOverviewPrefs, noteOverviewKept, onOverviewSaved, overviewChosen, overviewElsewhere, overviewKept, type OverviewEntry } from "./overview-cards";
import { adoptSidebarPrefs, chosen, getSidebarPrefs, onSidebarPrefsSaved } from "./sidebar-pages";
import { isResumed } from "./reconnect";
import { navigate, type Route } from "./router";
import { onHubEvent, useStore } from "./store";

const KEY = "persona.startPage";

const listeners = new Set<() => void>();
const changed = () => listeners.forEach((l) => l());
let current = "";
try {
  current = localStorage.getItem(KEY) ?? "";
} catch {
  // storage off: the main chat
}

/** The choice as this browser has it: "" for the main chat, else a page's key as the sidebar names it (lib/persona-nav.ts,
 *  `p:<extension>/<page>` for an extension's). */
export const startPage = () => current;

/** Keeps `key` here: the hub's answer or push, or the choice itself where the hub can't keep it. */
export function keepStartPage(key: string) {
  current = key;
  try {
    if (key) localStorage.setItem(KEY, key);
    else localStorage.removeItem(KEY);
  } catch {
    // storage off: this page only
  }
  changed();
}

// Whether the hub keeps the choice: null until its answer, false for a hub (or a manager) without the route.
let synced: boolean | null = null;
export const startPageSynced = () => synced;

/** Says the hub keeps the choice (true), or answered `e` (false for a 404 / 405: it has no such route). */
export function noteStartPageSynced(e?: unknown) {
  const was = synced;
  if (!e) synced = true;
  else if (e instanceof ApiError && (e.status === 404 || e.status === 405)) synced = false;
  if (synced !== was) changed();
}

/** The route a page's key opens; null for the main chat and for a key this version does not know. */
export function startRoute(key: string): Route | null {
  if (key.startsWith("p:")) {
    const at = key.indexOf("/");
    return at > 2 && at < key.length - 1 ? { name: "extension", extension: key.slice(2, at), ui: key.slice(at + 1) } : null;
  }
  return key !== "settings" && (NAV_PAGES as readonly string[]).includes(key) ? ({ name: key } as Route) : null;
}

// The start page this load opened on and its address, until it is known whether the page is there (useStartPageCheck).
let opened: { key: string; path: string } | null = null;

/** At load, before the first paint (App): an opening at the root address goes to the start page instead. */
export function openStartPage() {
  const to = startRoute(current);
  if (!to || stripBase(location.pathname) !== "/" || location.search || location.hash) return;
  const entry = performance.getEntriesByType?.("navigation")[0] as PerformanceNavigationTiming | undefined;
  if (entry && entry.type !== "navigate") return; // a reload or Back / Forward stays on the page it was on
  navigate(to, true);
  opened = { key: current, path: location.pathname };
}

/** Whether the start page `key` is gone, `listed` saying whether the sidebar lists it now: undefined while that can't be
 *  told yet (the hub has not answered which extension pages there are, or whether the workflow board is on). */
export function startPageGone(key: string, listed: boolean, known: { extensions: boolean; board: boolean }): boolean | undefined {
  if (listed) return false;
  return (key.startsWith("p:") ? known.extensions : key !== "workspace-board" || known.board) ? true : undefined;
}

// The hub has answered which extension pages there are: their list changed after the hello, which is when
// lib/extension-slots.ts asks for it.
let answered = false;

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};
export const useStartPage = () => useSyncExternalStore(subscribe, startPage);
export const useStartPageSynced = () => useSyncExternalStore(subscribe, startPageSynced);
export const useExtensionPagesKnown = () => useSyncExternalStore(subscribe, () => answered);

/** In the sidebar's pages (useNavPages, always drawn): once it can be told whether the page this load opened on is there,
 *  one that is not opens the main chat in its place, unless the user has gone elsewhere since. */
export function useStartPageCheck(listed: (key: string) => boolean, extensionPages: readonly unknown[], boardKnown: boolean) {
  const online = useStore((s) => s.conn === "online");
  const slots = useStore((s) => hasSlots(s.features));
  // this list as it was at the first render after the hello: each caller's own, as each memoizes its list
  const first = useRef<readonly unknown[] | null>(null);
  useEffect(() => {
    if (!online) return;
    if (!first.current) first.current = extensionPages;
    else if (extensionPages !== first.current && !answered) {
      answered = true;
      changed();
    }
    if (!opened) return;
    const gone = startPageGone(opened.key, listed(opened.key), { extensions: answered || !slots, board: boardKnown });
    if (gone === undefined) return;
    if (gone && location.pathname === opened.path) navigate({ name: "persona" }, true);
    opened = null;
  });
}

// The sidebar's arrangement (lib/sidebar-pages.ts) goes the same way: each change made here is sent; while one is on its
// way the hub's pushes of it are not taken (an older one would undo a newer); one the hub did not take goes again at the
// next hello instead of being replaced by the hub's.
let sending = 0;
let unsent = false;
onSidebarPrefsSaved((pages) => {
  if (synced === false) return; // this browser only
  sending++;
  api<{ pages?: unknown }>("/navigation", { method: "PUT", json: { pages } })
    .then(
      (r) => {
        noteStartPageSynced();
        unsent = false;
        if (sending === 1 && r.pages) adoptSidebarPrefs(r.pages);
      },
      (e) => {
        noteStartPageSynced(e);
        unsent = synced !== false;
      },
    )
    .finally(() => sending--);
});

// Overview's cards (lib/overview-cards.ts) go the same way, sent on their own. A default manager from before the field
// answers a change of only them with its own 400 (OLD_MANAGER), or takes the PUT and leaves them out of its answer: the
// arrangement then stays in this browser (Overview's editor says so) until the next hello asks again. Any other failure
// (403, 5xx, the network, another 400) keeps the change and sends it again at the next hello. One change is on its way at a
// time: one made meanwhile goes once that one is answered, the newest as this browser has it then, so the hub takes them in
// the order they were made and the last answer says whether the newest reached it. Pushes heard meanwhile, and until the
// hub's push of the last one taken comes (its answer can come first), are settled then (settleOverview), not dropped.
const OLD_MANAGER = ["start is required", "start or pages is required"]; // #9328, #9351
let sendingOverview = false;
let againOverview = false; // a change made while one was on its way
let unsentOverview = false;
let editsOverview = 0; // bumped by each change made here and each one sent: a hello's read that one overtook is not taken
let heardOverview: unknown[] = []; // the arrangements the hub pushed meanwhile, in its order
let answeredOverview: string[] = []; // its answers to our changes, in the same order, until it pushed the last of them
let helloOverview = 0; // bumped by each hello that did not resume: the hub's pushes from before it do not come again
// a change sent before such a hello was taken: its push may have gone with the old socket, so the pushes heard since are
// settled without it, the newest taken (they come in the hub's order; its own, if it comes, is taken in turn)
let looseOverview = false;
let pushedOverview = 0; // bumped by each push of it: a hello's read begun before one is older than what it pushed
// What else changed the arrangement since a read began: a push of it, or another tab's change to this sign-in's copy
// (review of #9778, P1): a read that one overtook is not taken.
const overviewVersion = () => pushedOverview + overviewElsewhere();
// The sign-in all of this belongs to (lib/feed-cache.ts feedCacheScope: the hub, the account, the device and its
// credentials, renewed at each sign-in and sign-out, a return to the same token too; review of #9778, P1). When it
// changes, everything on its way is retired: a change sent, queued or not taken under the earlier one, its
// answers and the pushes heard with them. An older request's answer, failure or end then changes nothing, and sends
// nothing under the new one, which starts as a fresh load does, its own hello reading its own arrangement.
let scopeOverview = feedCacheScope();
let ownerOverview = 0; // bumped at each such change: a request carries the one it was sent under
let helloScope: string | null | undefined; // the sign-in of the last hello: pushes before the next one's are the old socket's
function retireOverview() {
  ownerOverview++;
  scopeOverview = feedCacheScope();
  sendingOverview = againOverview = unsentOverview = looseOverview = false;
  heardOverview = [];
  answeredOverview = [];
  noteOverviewKept(null);
}
/** The sign-in's generation, the earlier one's work retired first where it changed since. */
function ownOverview() {
  if (feedCacheScope() !== scopeOverview) retireOverview();
  return ownerOverview;
}
// The hub passes a push on as it read it (internal/hub bus_broadcast.go), a card's keys in another order than its answer's:
// an arrangement is compared card by card.
const overviewKey = (v: unknown) =>
  JSON.stringify((Array.isArray(v) ? (v as Partial<OverviewEntry>[]) : []).map((c) => [c?.extension, c?.id, c?.hidden ?? false, c?.span ?? ""]));
function sendOverview(probe = false) {
  const owner = ownOverview();
  if (synced === false || (overviewKept() === false && !probe)) return; // this browser only
  if (sendingOverview) return void (againOverview = true);
  sendingOverview = true;
  editsOverview++;
  const hello = helloOverview;
  api<{ overview?: unknown }>("/navigation", { method: "PUT", json: { overview: getOverviewPrefs() } })
    .then(
      (r) => ({ ok: true as const, r }),
      (e: unknown) => ({ ok: false as const, e }),
    )
    .then((done) => {
      // the earlier sign-in's: its answer, failure and end change nothing here, nor is a change queued under it sent
      if (owner !== ownOverview()) return;
      try {
        if (done.ok) {
          const r = done.r;
          noteStartPageSynced();
          unsentOverview = false;
          noteOverviewKept(Array.isArray(r.overview));
          // its push is still to come, unless it went out before a hello that did not resume
          if (Array.isArray(r.overview) && hello === helloOverview) answeredOverview.push(overviewKey(r.overview));
          else if (Array.isArray(r.overview)) looseOverview = true;
        } else {
          const e = done.e;
          noteStartPageSynced(e);
          const old = e instanceof ApiError && e.status === 400 && OLD_MANAGER.includes(String(e.body?.error));
          if (old) noteOverviewKept(false);
          unsentOverview = !old && synced !== false;
        }
      } finally {
        sendingOverview = false;
        if (againOverview) {
          againOverview = false;
          sendOverview();
        }
        if (!sendingOverview) settleOverview();
      }
    });
}
onOverviewSaved(() => {
  editsOverview++;
  sendOverview();
});

/** Once the last change on its way is answered and the hub pushed it: the hub pushes every change, these too, in its
 *  order, to every app, so an arrangement pushed before the last of ours is older than ours and one pushed after it is
 *  newer and is taken; with none, this browser's copy (the newest change) stays, as does a change the hub did not take,
 *  which goes again at the next hello. Our answers are found among the pushes in the order they were made, each after the
 *  one before, so a later push with the value of an earlier change of ours (another device putting the cards back as they
 *  were) is not taken for ours. Until the last is found (its answer can come before its push), pushes wait with it. */
function settleOverview() {
  let last = -1;
  let next = 0;
  heardOverview.forEach((p, i) => {
    if (next < answeredOverview.length && overviewKey(p) === answeredOverview[next]) {
      next++;
      last = i;
    }
  });
  if (!unsentOverview && next < answeredOverview.length) return;
  const heard = heardOverview;
  const loose = looseOverview;
  heardOverview = [];
  answeredOverview = [];
  looseOverview = false;
  if (!unsentOverview && (last >= 0 || loose) && last < heard.length - 1) adoptOverview(heard[heard.length - 1]);
}

/** The hub's arrangement, read or pushed: taken at once, or settled with the change on its way or its push to come. A
 *  change the hub did not take stays over it until the next hello sends it again. */
function hubOverview(v: unknown[]) {
  noteOverviewKept(true);
  if (!sendingOverview && !answeredOverview.length && !unsentOverview) return adoptOverview(v);
  heardOverview.push(v);
  if (!sendingOverview) settleOverview();
}

// The hub's choices, read at every hello that did not resume (a resumed one gets the pushes it missed) and pushed on
// each change. A hub that never stored one is handed the one this browser kept while there was no record. A read a
// later hello or another sign-in overtook is left to that one, every field of it and any hand-over.
onHubEvent("hello", (msg) => {
  helloScope = feedCacheScope();
  if (isResumed(msg)) return;
  // what the hub pushed before it does not come again: its read stands for it; and another sign-in's work is over
  ownOverview();
  const hello = ++helloOverview;
  const scope = feedCacheScope();
  const overtaken = () => hello !== helloOverview || scope !== feedCacheScope();
  heardOverview = [];
  answeredOverview = [];
  const edits = sendingOverview ? -1 : editsOverview;
  const version = overviewVersion();
  api<{ start?: string; pages?: unknown; overview?: unknown }>("/navigation").then(
    (r) => {
      if (overtaken()) return;
      noteStartPageSynced();
      const handover: Record<string, unknown> = {};
      if (typeof r.start === "string") keepStartPage(r.start);
      else if (current) handover.start = current;
      if (r.pages && !unsent) adoptSidebarPrefs(r.pages);
      else if (chosen(getSidebarPrefs())) handover.pages = getSidebarPrefs();
      if (Object.keys(handover).length) void api("/navigation", { method: "PUT", json: handover }).then(() => (unsent = false), () => {});
      // ours, unless a change of it did not reach the hub or one was on its way or made since this read began, or the
      // hub pushed one or another tab changed it meanwhile (newer than its answer, review of #9778, P1); else handed
      // over (only while nothing newer came: the hub has one then), or asked again where it was refused
      const fresh = version === overviewVersion();
      if (Array.isArray(r.overview) && !unsentOverview && edits === editsOverview && fresh) hubOverview(r.overview);
      else if (unsentOverview || overviewKept() === false || (fresh && overviewChosen())) sendOverview(true);
    },
    (e) => void (overtaken() || noteStartPageSynced(e)), // the copies here stay
  );
});
onHubEvent("navigation", (msg) => {
  if (typeof msg.start === "string") keepStartPage(msg.start);
  if (msg.pages && !sending) adoptSidebarPrefs(msg.pages);
  // not one the earlier sign-in's socket passed on before this one's hello
  if (Array.isArray(msg.overview) && helloScope === feedCacheScope()) {
    pushedOverview++;
    hubOverview(msg.overview);
  }
});
