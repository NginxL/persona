// The owner's messages the assistant keeps after they could not go in (lib/undelivered.ts, #10194): the owner's own
// bubble, as a failed outbox one looks (OutboxBubbles), with a line under it. Waiting: why, and that it goes by itself
// (the caution color, Delete only). Given up: why in red, Retry and Delete. On every device, from the row. A message
// held for an Agent while its computer was away (#10661, `computer` set) looks the same, its line the manager's words
// as they come ("Will send when … is back", "Waits behind the message above", why it was not sent). A chunk of its own
// with its strings outside the startup catalog (#10357): only a conversation holding a kept message draws it, and
// ChatView shows the bare bubble until it is in. ChatView also takes the box's words for an Agent whose computer is away
// from here (heldPlaceholder), for the same reason.
import { useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import type { UndeliveredMessage } from "../../lib/types";
import { keptBubble } from "../../lib/undelivered";
import { deleteUndelivered, retryUndelivered } from "../../lib/undelivered-actions";
import { toast } from "../../ui/atoms";
import { UserBubble } from "./SegmentView";

/** The line under a kept message's bubble. */
function keptLine(m: UndeliveredMessage, failed: boolean): string {
  if (m.delete_only && m.error) return m.error;
  if (m.computer) return m.error || t(failed ? "chat-undeliveredbubbles.held_failed" : "chat-undeliveredbubbles.held_waiting", { computer: m.computer });
  return t(failed ? "chat-undeliveredbubbles.failed" : "chat-undeliveredbubbles.waiting", { reason: m.error || t("lib-outbox.delivery_unknown") });
}

/** What an Agent's message box says while its computer is away, on a hub that holds the messages sent meanwhile. */
export const heldPlaceholder = (computer: string): string => t("chat-undeliveredbubbles.held_placeholder", { computer });

export function UndeliveredBubble({ mid, sid, m }: { mid: string; sid: string; m: UndeliveredMessage }) {
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    try {
      await f();
    } catch (e) {
      toast(t("chat-undeliveredbubbles.action_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  const failed = m.state === "failed";
  return (
    <div data-undelivered={m.id} data-undelivered-state={m.state} data-undelivered-held={m.computer ? "" : undefined} className="flex flex-col gap-1.5">
      <UserBubble msg={keptBubble(m)} mid={mid} sid={sid} sendState="failed"
        onDelete={() => void run(() => deleteUndelivered(mid, sid, m))} />
      <div role={failed ? "alert" : "status"} className="flex flex-wrap items-center justify-end gap-x-2 gap-y-1 pl-[12%] text-submeta">
        <span className={`min-w-0 ${failed ? "text-red" : "text-orange"}`}>
          {keptLine(m, failed)}
          {m.images ? ` · ${t("chat-undeliveredbubbles.images", { count: m.images })}` : ""}
        </span>
        {failed && !m.delete_only && (
          <button type="button" disabled={busy} aria-busy={busy} onClick={() => void run(() => retryUndelivered(mid, sid, m))} className="whitespace-nowrap font-medium text-accent-ink hover:underline">
            {t("chat-outboxbubbles.retry")}
          </button>
        )}
        <button type="button" disabled={busy} onClick={() => void run(() => deleteUndelivered(mid, sid, m))} className="whitespace-nowrap text-ink-3 hover:text-ink-2">
          {t("chat-outboxbubbles.delete")}
        </button>
      </div>
    </div>
  );
}
