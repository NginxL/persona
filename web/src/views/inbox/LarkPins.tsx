// The Pin list of a Feishu chat (iOS LarkChatPinsView, docs/extensions/lark-migration.md "Pinned messages in chats"), over the chat.
// Reading it marks the chat's Pins read in the Lark gateway, so it is read when opened, when a Pin changes and when the hub
// says the chat changed while it is open; nothing reads it ahead of time. Pages come in as the list nears its end.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { shortTime } from "../../lib/inbox";
import { olAccount } from "../../lib/lark-accounts";
import { useLarkDisplay, type LarkMessageDisplay } from "../../lib/lark-display";
import { canOpenPin, canRemovePin, ChatPinsModel, liveChatPinsDeps, pinSummary, type ChatPin, type ChatPinTarget } from "../../lib/lark-pins";
import { useItemManager } from "../../lib/manager-choice";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent } from "../../lib/store";
import { t } from "../../i18n";
import { Button, cx, Dialog, IconButton, Loading, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { LarkAvatar } from "./InboxParts";

export function LarkPinsDialog({ feed, topic, account, onClose, onOpen }: {
  feed: string;
  topic: boolean;
  /** The Feishu account the chat is in (`ol_account`); undefined: the hub's own. */
  account?: number;
  /** checked: the list was read cleanly, so an earlier unknown write is settled. */
  onClose: (checked: boolean) => void;
  onOpen: (target: ChatPinTarget) => void;
}) {
  const manager = useItemManager();
  const [model] = useState(() => new ChatPinsModel(liveChatPinsDeps(feed, topic, account, manager)));
  const display = useLarkDisplay(account);
  const s = useSyncExternalStore(model.subscribe, () => model.state);
  const [removing, setRemoving] = useState<string | null>(null);
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    model.start();
    void model.refresh();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const settle = () => {
      clearTimeout(timer);
      timer = setTimeout(() => void model.refresh(), 400);
    };
    const off = onHubEvent("lark", (msg) => {
      const ids = Array.isArray(msg.feed_ids) ? (msg.feed_ids as string[]) : [];
      if (ids.includes(feed) && olAccount(msg.ol_account) === account) settle();
    });
    // A reconnect the hub could not resume: the `lark` pushes missed meanwhile are not replayed.
    const offHello = onHubEvent("hello", (msg) => !isResumed(msg) && settle());
    return () => {
      off();
      offHello();
      clearTimeout(timer);
      model.stop();
    };
  }, [model, feed, account]);

  // The next page when the end of the list comes into view.
  useEffect(() => {
    const el = end.current;
    if (!el || !s.nextCursor || s.pageProblem) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && void model.more());
    io.observe(el);
    return () => io.disconnect();
  }, [model, s.nextCursor, s.pageProblem, s.loading, s.items.length]);

  const busy = s.loading || s.loadingMore || s.changing || s.locating !== undefined;
  const close = () => onClose(s.loaded && !s.problem && !s.requiresRecheck);
  const open = async (p: ChatPin) => {
    const target = await model.locate(p);
    if (target) onOpen(target);
  };

  return (
    <Dialog open onClose={close} title={t("inbox-larkpins.title")} width={520}>
      <div className="-mx-4 -mb-4 max-h-[min(62vh,560px)] overflow-y-auto border-t border-line">
        {s.problem && (
          <div className="flex items-start gap-3 px-4 py-3">
            <Icon name="info" size={14} className="mt-0.5 shrink-0 text-orange" />
            <p className="min-w-0 flex-1 text-[12.5px] leading-relaxed text-ink-2">{s.problem}</p>
            {!s.blocked && (
              <Button tone="secondary" icon="retry" busy={s.loading} disabled={busy} onClick={() => void model.refresh()}>
                {s.requiresRecheck ? t("inbox-larkpins.recheck") : t("inbox-larkpins.reload")}
              </Button>
            )}
          </div>
        )}
        {!s.items.length ? (
          s.loading ? (
            <div className="px-4 py-8"><Loading label={t("inbox-larkpins.loading")} /></div>
          ) : s.loaded && !s.problem ? (
            <div className="flex flex-col items-center gap-2 px-6 py-10 text-center">
              <span className="flex size-10 items-center justify-center rounded-full bg-field text-ink-3"><Icon name="pin" size={17} /></span>
              <p className="text-[13.5px] font-medium text-ink">{t("inbox-larkpins.empty_title")}</p>
              <p className="text-[12.5px] text-ink-3">{t("inbox-larkpins.empty_hint")}</p>
            </div>
          ) : null
        ) : (
          <>
            <p className="px-4 pt-2.5 pb-1 text-[11.5px] font-medium text-ink-3 tabular-nums">{t("inbox-larkpins.total", { count: s.total })}</p>
            <ul className="divide-y divide-line">
              {s.items.map((p) => (
                <li key={p.id}>
                  <PinRow
                    account={account}
                    display={display}
                    pin={p}
                    locating={s.locating === p.id}
                    disabled={busy || s.blocked}
                    removable={canRemovePin(p) && !s.requiresRecheck}
                    confirming={removing === p.id}
                    onOpen={() => void open(p)}
                    onRemove={() => setRemoving(p.id)}
                    onCancel={() => setRemoving(null)}
                    onConfirm={async () => {
                      setRemoving(null);
                      await model.change(p.message_id, p.id);
                    }}
                  />
                </li>
              ))}
            </ul>
            {s.pageProblem ? (
              <div className="flex items-center gap-3 px-4 py-3">
                <p className="min-w-0 flex-1 text-[12.5px] text-ink-2">{s.pageProblem}</p>
                <Button tone="secondary" icon="retry" disabled={busy} onClick={() => void model.more()}>{t("inbox-larkpins.retry")}</Button>
              </div>
            ) : s.nextCursor ? (
              <div ref={end} className="flex justify-center py-3"><Spinner /></div>
            ) : null}
          </>
        )}
      </div>
    </Dialog>
  );
}

function PinRow({ pin, account, display, locating, disabled, removable, confirming, onOpen, onRemove, onCancel, onConfirm }: {
  pin: ChatPin;
  account?: number;
  display: LarkMessageDisplay;
  locating: boolean;
  disabled: boolean;
  removable: boolean;
  confirming: boolean;
  onOpen: () => void;
  onRemove: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const m = pin.message;
  const name = m ? (m.is_mine ? t("inbox-larkpins.me") : m.sender_name || t("inbox-larkpins.unknown")) : "";
  const gone = !canOpenPin(pin);
  if (confirming) {
    return (
      <div className="flex items-center gap-3 bg-inset px-4 py-3">
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-medium text-ink">{t("inbox-larkpins.remove_confirm")}</p>
          <p className="mt-0.5 text-[11.5px] text-ink-3">{t("inbox-larkpins.remove_hint")}</p>
        </div>
        <Button tone="quiet" onClick={onCancel}>{t("common.cancel")}</Button>
        <Button tone="danger" icon="trash" onClick={onConfirm}>{t("inbox-larkpins.remove")}</Button>
      </div>
    );
  }
  return (
    <div className="group flex items-start gap-2 px-2 py-1">
      <button
        type="button"
        onClick={onOpen}
        disabled={disabled || gone}
        title={gone ? undefined : t("inbox-larkpins.view_in_chat")}
        className="flex min-w-0 flex-1 items-start gap-2.5 rounded-[10px] px-2 py-2 text-left transition-colors enabled:hover:bg-hover disabled:cursor-default"
      >
        {m ? <LarkAvatar name={name} size={28} avatarURL={m.sender_avatar_url} avatar={m.sender_avatar} account={account} /> : <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-field text-ink-3"><Icon name="pin" size={13} /></span>}
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-2">
            {name && <span className="truncate text-[12.5px] font-medium text-ink">{name}</span>}
            {m?.olk !== undefined && m?.olk !== null && (
              <span className="inline-flex shrink-0 items-center gap-1 rounded-[4px] bg-field px-1 text-[10.5px] whitespace-nowrap text-ink-3">
                <Icon name="lock" size={10} />{t("inbox-larkpins.encrypted")}
              </span>
            )}
            <span className="flex-1" />
            {locating ? <Spinner size={11} /> : <span className="shrink-0 text-[11px] text-ink-3 tabular-nums">{shortTime(pin.created_at)}</span>}
          </span>
          <span className={cx("mt-0.5 line-clamp-3 text-[13px] leading-relaxed break-words whitespace-pre-wrap", gone || m?.recalled ? "text-ink-3" : "text-ink")}>{pinSummary(pin, display)}</span>
          {pin.operator_name && <span className="mt-0.5 block truncate text-[11px] text-ink-3">{t("inbox-larkpins.added_by", { name: pin.operator_name })}</span>}
        </span>
      </button>
      {removable ? <IconButton icon="trash" label={t("inbox-larkpins.remove")} onClick={onRemove} disabled={disabled} className="mt-2" /> : <span aria-hidden className="w-7 shrink-0" />}
    </div>
  );
}
