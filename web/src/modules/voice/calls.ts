// The page's voice call: at most one at a time, bound to the conversation page that started it (docs/assistant/voice.md
// 6.3, 6.5; docs/web/web.md "Voice chat"). A new call ends the one before; leaving the conversation (or closing the tab)
// ends it; a hidden tab keeps it (desktop multitasking) unless the browser suspends its audio. ChatView reads only whether
// a call is on here; the bar and the thread's tail read the call itself.
//
// This is the first screen's part: the store, its hooks and what the page's buttons call. The call's own code (the
// conversation, its audio, uplink and resampling: call-stack.ts) is a chunk of its own, imported when the page's first
// call starts, together with the page's parts for it (`ui`); until then the call's types are all this module takes from it.
import { useSyncExternalStore } from "react";
import { reportBoundary } from "../../lib/error-reports";
import { closeContexts, openContexts, type VoiceContexts } from "./audio-contexts";
import type { VoiceConversation, VoiceDeps, VoiceSnapshot } from "./conversation";
import { DEFAULT_OUTPUT_RATE, type VoiceLine } from "./wire";

type CallStack = typeof import("./call-stack");

/** Which conversation page a call belongs to: the conversation, and the page that started it (the same conversation
 *  can be open twice, as a page and in a side panel; leaving one must not end the other's call). */
export interface VoiceOwner {
  mid: string;
  sid: string;
  owner: string;
}

interface Current {
  call: VoiceConversation;
  owner: string;
  /** Whether it ended: changes only as it ends or is picked up again, so the page swaps the bar and the composer
   *  without drawing again on every caption. */
  over: boolean;
}

let current: Current | null = null;
/** The call's code once it is in; `loading` while it comes (a failed load is not kept: the next start asks again). */
let stack: CallStack | null = null;
let loading: Promise<CallStack> | null = null;
const listeners = new Set<() => void>();
/** Told of every change of the current call too (its captions, runs, the card it asks about). */
const deep = new Set<() => void>();
let unwatch: (() => void) | null = null;

function emit() {
  listeners.forEach((l) => l());
}

function setCurrent(next: Current | null) {
  unwatch?.();
  unwatch = null;
  current = next;
  if (next) {
    unwatch = next.call.subscribe(() => {
      deep.forEach((l) => l());
      if (current?.call !== next.call || current.over === next.call.over) return;
      current = { ...current, over: next.call.over };
      // a call that ended by the user's own doing leaves nothing behind (the call's code is in: it made this call)
      const phase = next.call.getSnapshot().phase;
      if (phase.kind === "ended" && stack?.endedByUser(phase.end)) setCurrent(null);
      emit();
    });
  }
  emit();
}

/** `ui`: the page's own code for a call (its bar, chat/VoiceCall.tsx), loaded with the call's: the first call
 *  is on only once both are in, so the bar draws at once in the composer's place. */
type Start = VoiceOwner & { extensionID: string; recent: VoiceLine[]; deps?: VoiceDeps; ui?: () => Promise<unknown> };

function loadStack(ui?: () => Promise<unknown>): Promise<CallStack> {
  return (loading ??= Promise.all([import("./call-stack"), ui?.()]).then(
    ([m]) => (stack = m),
    (e: unknown) => {
      loading = null;
      throw e;
    },
  ));
}

/** The page's first start while the call's code loads: what was tapped, and the audio contexts the click made. */
interface Pending {
  start: Start;
  contexts?: VoiceContexts;
  done: Promise<VoiceConversation | null>;
}

let pending: Pending | null = null;

/** The pending start goes (the page left, the call was ended, another start took its place): it starts nothing. */
function dropPending() {
  if (pending?.contexts) closeContexts(pending.contexts);
  pending = null;
}

function startNow(s: CallStack, o: Start, contexts?: VoiceContexts): VoiceConversation {
  if (current) {
    const c = current.call;
    if (c.mid === o.mid && c.sid === o.sid && c.extensionID === o.extensionID && current.owner === o.owner) {
      if (c.over) void c.restart();
      return c;
    }
    c.end({ kind: "replaced" });
    setCurrent(null);
  }
  const call = new s.VoiceConversation({ mid: o.mid, sid: o.sid, extensionID: o.extensionID, recent: o.recent, deps: o.deps ?? s.browserDeps(o.mid, o.sid, o.extensionID, contexts) });
  setCurrent({ call, owner: o.owner, over: false });
  void call.begin();
  return call;
}

export const voiceCalls = {
  /** Starts a call on `o`'s conversation (synchronously up to its audio: call it inside the click). The same page's
   *  ended call is picked up again; any other call ends first. The page's first call waits for the call's code: the
   *  click makes the audio contexts (a browser starts audio only from a gesture) and the call starts with them once the
   *  code is in; a second tap meanwhile is the same start. Settles with the call, or null when none started (the page
   *  left, another start took its place, or the code would not load: reported, and the next tap tries again). */
  start(o: Start): Promise<VoiceConversation | null> {
    if (stack) return Promise.resolve(startNow(stack, o));
    if (pending && samePage(pending.start, o) && pending.start.extensionID === o.extensionID) return pending.done;
    dropPending();
    const mine: Pending = { start: o, contexts: o.deps ? undefined : openContexts(DEFAULT_OUTPUT_RATE), done: Promise.resolve(null) };
    pending = mine;
    mine.done = loadStack(o.ui).then(
      (s) => {
        if (pending !== mine) return null;
        pending = null;
        return startNow(s, o, mine.contexts);
      },
      (e: unknown) => {
        if (pending !== mine) return null;
        dropPending();
        // the chunk would not load (offline, or the hub was redeployed under the page): a warning, as a lazy overlay's
        reportBoundary(e, "voice_call", true);
        return null;
      },
    );
    return mine.done;
  },

  /** End tapped: ends the call, whichever page shows it. */
  end() {
    dropPending();
    current?.call.end({ kind: "user" });
    setCurrent(null);
  },

  /** Continue / Retry on the ended call of this page (inside the click: it makes the audio again). */
  resume(o: VoiceOwner) {
    if (current && bound(current, o) && current.call.over) void current.call.restart();
  },

  /** The page left the conversation (another one, another page, the tab closing): its call ends, and its Continue row
   *  goes with it; a first call still waiting for its code never starts. */
  leave(o: VoiceOwner) {
    if (pending && samePage(pending.start, o)) dropPending();
    if (!current || !bound(current, o)) return;
    current.call.end({ kind: "left" });
    setCurrent(null);
  },

  /** The call bound to this page, live or ended. */
  callOn(o: VoiceOwner): VoiceConversation | null {
    return current && bound(current, o) ? current.call : null;
  },

  /** Any call now (the tests, and the tab closing). */
  get current(): VoiceConversation | null {
    return current?.call ?? null;
  },
};

function bound(c: Current, o: VoiceOwner): boolean {
  return c.call.mid === o.mid && c.call.sid === o.sid && c.owner === o.owner;
}

function samePage(a: VoiceOwner, b: VoiceOwner): boolean {
  return a.mid === b.mid && a.sid === b.sid && a.owner === b.owner;
}

if (typeof window !== "undefined") {
  // closing or reloading the tab: hang up properly rather than leave the extension to notice a dead socket
  window.addEventListener("pagehide", () => {
    dropPending();
    current?.call.end({ kind: "left" });
  });
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** Whether this page has a call: "live", "ended" (its Continue row), or null. Changes only as a call starts, ends or
 *  picks up again. */
export function useVoiceState(o: VoiceOwner): "live" | "ended" | null {
  const get = () => (current && bound(current, o) ? (current.over ? "ended" : "live") : null);
  return useSyncExternalStore(subscribe, get, get);
}

/** The call bound to this page (changes as calls come and go, not as they talk). */
export function useVoiceCall(o: VoiceOwner): VoiceConversation | null {
  const get = () => (current && bound(current, o) ? current.call : null);
  return useSyncExternalStore(subscribe, get, get);
}

const NONE = { subscribe: () => () => {}, getSnapshot: () => null };

/** What the call says now (every caption, run and state): only the bar and the thread's tail read it. */
export function useVoiceSnapshot(call: VoiceConversation | null): VoiceSnapshot | null {
  const src = call ?? NONE;
  return useSyncExternalStore(src.subscribe, src.getSnapshot, src.getSnapshot);
}

/** Whether the voice is asking this card's question now (the `ask` frame): the card says so, so a click and an answer
 *  said aloud are not taken for two questions (docs/assistant/voice.md 1.2 H). */
export function useVoiceAsking(ix: { id: string; machine_id: string; session_id: string }): boolean {
  const get = () => {
    const call = current?.call;
    if (!call || call.over || call.mid !== ix.machine_id || call.sid !== ix.session_id) return false;
    return call.getSnapshot().askingID === ix.id;
  };
  return useSyncExternalStore(subscribeDeep, get, get);
}

function subscribeDeep(l: () => void) {
  listeners.add(l);
  deep.add(l);
  return () => {
    listeners.delete(l);
    deep.delete(l);
  };
}
