// What the voice module draws in the chat slots (core/module-host.ts ChatSlotComponents, docs/web/web.md "Voice chat"):
// the composer's voice button and the dialog on why a call cannot start, the call's captions at the thread's end, its bar
// in the composer's place while it is live (the composer keeps its draft underneath) and the row after it ended by
// itself, and the mark on the card the call is asking about. The call belongs to the page (the same chat may be open
// twice, a page and a side panel): `owner` is the page's id, and the call ends as the page leaves the conversation.
import { useEffect, useLayoutEffect, useMemo } from "react";
import { t } from "../../../i18n";
import type { ChatSlotComponents } from "../../../core/module-host";
import { Icon } from "../../../ui/icons";
import { useVoiceAsking, useVoiceCall, useVoiceState, voiceCalls } from "../calls";
import { useVoiceEntry, VoiceBar, VoiceEnded, VoiceTail } from "./VoiceParts";

export const chat: ChatSlotComponents = {
  "composer.trailingAction": function VoiceEntry({ mid, sid, owner, session, label, canSend, items, offer }) {
    const o = useMemo(() => ({ mid, sid, owner }), [mid, sid, owner]);
    useEffect(() => () => voiceCalls.leave(o), [o]);
    const entry = useVoiceEntry({ ...o, session, items, label, off: !canSend });
    const e = entry.offer;
    useLayoutEffect(() => {
      offer(e && { icon: "voice", name: t("voice.voice_chat"), label: e.label, primary: e.mode === "start", onClick: e.onClick, attributes: { "data-testid": "composer-voice", "data-voice-mode": e.mode } });
    }, [e, offer]);
    useLayoutEffect(() => () => offer(undefined), [offer]);
    return entry.dialog;
  },
  "chat.tail": function VoiceCaptions({ mid, sid, owner, persona }) {
    const o = useMemo(() => ({ mid, sid, owner }), [mid, sid, owner]);
    return useVoiceState(o) ? <VoiceTail owner={o} persona={persona} /> : null;
  },
  "chat.bar": function VoiceCallBar({ mid, sid, owner, session, cards, label, persona, onStopAgent, place, take }) {
    const o = useMemo(() => ({ mid, sid, owner }), [mid, sid, owner]);
    const state = useVoiceState(o);
    const call = useVoiceCall(o);
    const live = state === "live";
    useLayoutEffect(() => {
      if (place === "composer") take(live);
    }, [place, live, take]);
    if (place === "dock") return state === "ended" ? <VoiceEnded owner={o} /> : null;
    return live && call ? (
      <div className="shrink-0">
        <VoiceBar call={call} session={session} cards={cards} label={label} persona={persona} onStopAgent={onStopAgent ?? (() => {})} />
      </div>
    ) : null;
  },
  // the card stays a quiet surface: a neutral chip, the voice's color only on its mark
  "interaction.asked": function AskedByVoice({ interaction }) {
    if (!useVoiceAsking(interaction)) return null;
    return (
      <span data-testid="voice-asking" className="ml-0.5 inline-flex min-w-0 items-center gap-1 rounded-full bg-inset px-1.5 py-px text-[10.5px] font-medium whitespace-nowrap text-ink-2 shadow-hairline">
        <Icon name="voice" size={10} stroke={2.4} className="shrink-0 text-ai" />
        <span className="truncate">{t("voice.asking_mark")}</span>
      </span>
    );
  },
};
