// The owner's messages the assistant keeps after they could not go in (lib/undelivered.ts, #10194): the owner's own
// bubble, as a failed outbox one looks (OutboxBubbles), with a line under it. Waiting: why, and that it goes by itself
// (the caution color, Delete only). Given up: why in red, Retry and Delete. On every device, from the row. A chunk of
// its own with its strings outside the startup catalog (#10357): only a conversation holding a kept message draws it,
// and ChatView shows the bare bubble until it is in.
import { useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import type { UndeliveredMessage } from "../../lib/types";
import { keptBubble } from "../../lib/undelivered";
import { deleteUndelivered, retryUndelivered } from "../../lib/undelivered-actions";
import { toast } from "../../ui/atoms";
import { UserBubble } from "./SegmentView";

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
  const reason = m.error || t("lib-outbox.delivery_unknown");
  return (
    <div data-undelivered={m.id} data-undelivered-state={m.state} className="flex flex-col gap-1.5">
      <UserBubble msg={keptBubble(m)} mid={mid} sid={sid} sendState="failed"
        onDelete={() => void run(() => deleteUndelivered(mid, sid, m.id))} />
      <div role={failed ? "alert" : "status"} className="flex flex-wrap items-center justify-end gap-x-2 gap-y-1 pl-[12%] text-submeta">
        <span className={`min-w-0 ${failed ? "text-red" : "text-orange"}`}>
          {t(failed ? "chat-undeliveredbubbles.failed" : "chat-undeliveredbubbles.waiting", { reason })}
          {m.images ? ` · ${t("chat-undeliveredbubbles.images", { count: m.images })}` : ""}
        </span>
        {failed && (
          <button type="button" disabled={busy} aria-busy={busy} onClick={() => void run(() => retryUndelivered(mid, sid, m.id))} className="whitespace-nowrap font-medium text-accent-ink hover:underline">
            {t("chat-outboxbubbles.retry")}
          </button>
        )}
        <button type="button" disabled={busy} onClick={() => void run(() => deleteUndelivered(mid, sid, m.id))} className="whitespace-nowrap text-ink-3 hover:text-ink-2">
          {t("chat-outboxbubbles.delete")}
        </button>
      </div>
    </div>
  );
}
