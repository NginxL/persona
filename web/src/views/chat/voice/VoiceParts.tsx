// The first screen's part of a voice call in the conversation's page (docs/assistant/voice.md 6.1, docs/web/web.md "Voice
// chat"): the composer's entry, and the call's own parts as stand-ins for VoiceCall.tsx, a chunk of its own: the bar, its
// captions at the thread's end, the row after a call ended by itself and the dialog that says why a call cannot start.
// That chunk loads with the call's code when the page's first call starts (voiceCalls.start waits for both, so the bar
// takes the composer's place at once), or as soon as the composer's button explains instead of starting; a first screen
// without a call never draws any of them.
import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../../i18n";
import { BASE } from "../../../lib/base";
import { lazyPage, type LazyPageComponent } from "../../../lib/lazy-page";
import { browserVoice, entryMode, evaluate, httpsAddress, type VoiceAvailability } from "../../../lib/voice/availability";
import { voiceCalls, type VoiceOwner } from "../../../lib/voice/calls";
import { helloAddresses, useVoiceProbe } from "../../../lib/voice/support";
import { recentLines } from "../../../lib/voice/wire";
import type { Item, Session } from "../../../lib/types";
import { lazyDialog } from "../../LazyOverlay";

/** What the composer offers: start a call, or say why it cannot start here. */
export interface VoiceOffer {
  mode: "start" | "explain";
  label: string;
  onClick: () => void;
}

/** Why the button explains (VoiceCall.tsx words it): the availability, and the hub's HTTPS address for this page. */
export interface VoiceUnavailableWhy {
  availability: VoiceAvailability;
  https: string | null;
}

const loadCall = () => import("./VoiceCall");
/** Drawn only while this page has a call, which starts once the chunk is in: in a Suspense of its own all the same. */
function callPart<P extends object>(Part: LazyPageComponent<P>) {
  return (props: P) => (
    <Suspense fallback={null}>
      <Part {...props} />
    </Suspense>
  );
}
const Bar = lazyPage(() => loadCall().then((m) => m.VoiceBar));
const Tail = lazyPage(() => loadCall().then((m) => m.VoiceTail));
const Ended = lazyPage(() => loadCall().then((m) => m.VoiceEnded));
const UnavailableDialog = lazyDialog(() => loadCall().then((m) => m.VoiceUnavailableDialog));
/** The bar (VoiceCall.tsx): the composer turns into it in place while the call is live. */
export const VoiceBar = callPart(Bar);
/** The call's captions at the end of the thread. */
export const VoiceTail = callPart(Tail);
/** Over the composer after a call ended by itself, until the page leaves the conversation. */
export const VoiceEnded = callPart(Ended);
/** Every part the page draws for a call, loaded with the call's code (voiceCalls.start `ui`). */
const preloadCall = () => Promise.all([Bar.preload(), Tail.preload(), Ended.preload(), UnavailableDialog.preload()]);

/**
 * The composer's voice entry for this page, and the dialog that explains why it cannot start (docs/assistant/voice.md 6.1).
 * `items`: the transcript as the page has it, read when the call starts (`start.recent`).
 */
export function useVoiceEntry(o: VoiceOwner & { session: Session | undefined; items: () => readonly Item[]; label: string; off?: boolean }): { offer: VoiceOffer | undefined; dialog: React.ReactNode } {
  const probe = useVoiceProbe();
  const [why, setWhy] = useState<VoiceUnavailableWhy | null>(null);
  const items = useRef(o.items);
  items.current = o.items;
  // whether it can send, and whether it is a meeting's room, is all that matters of the session here: the object changes
  // on every push
  const sendable = Boolean(o.session) && !o.off;
  const sessionRef = useRef(o.session);
  sessionRef.current = o.session;
  const availability = useMemo(
    () => evaluate({ session: sendable ? sessionRef.current : undefined, extensions: probe.extensions, info: probe.info, browser: browserVoice() }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sendable, probe, o.session?.source_kind, o.session?.capabilities?.send, o.session?.origin],
  );
  const mode = entryMode(availability);
  const extensionID = availability.kind === "available" ? availability.extensionID : "";
  const { mid, sid, owner, label } = o;
  const offer = useMemo<VoiceOffer | undefined>(() => {
    if (!mode) return undefined;
    if (mode === "start") {
      return {
        mode,
        label: t("voice.voice_chat_hint", { name: label }),
        // inside the click: the call makes its audio before anything is awaited; it is on once its parts are in too
        onClick: () => void voiceCalls.start({ mid, sid, owner, extensionID, recent: recentLines(items.current()), ui: preloadCall }),
      };
    }
    return {
      mode,
      label: t("voice.voice_unavailable_hint"),
      onClick: () => setWhy({ availability, https: httpsAddress(helloAddresses(), location, BASE) }),
    };
  }, [mode, extensionID, mid, sid, owner, label, availability]);
  // the button explains: what its click opens starts loading now, so the click draws it at once
  const explains = mode === "explain";
  useEffect(() => {
    if (explains) UnavailableDialog.preload().catch(() => {});
  }, [explains]);
  const dialog = <UnavailableDialog open={Boolean(why)} why={why} onClose={() => setWhy(null)} />;
  return { offer, dialog };
}
