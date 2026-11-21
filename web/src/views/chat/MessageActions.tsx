// What you can do with a message (iOS MessageMenu.swift: "Copy" / "Select" / "Quote" / "Forward" / "Edit"): every item on
// right-click; on a touch screen, a long press opens them (and "Select" shows the text to select, since the press no longer
// selects). With a mouse, a small bar on hover too: beside your own bubbles (under one that leaves it no room), and
// under a turn's last reply cindy's bar
// (ReplyBar, #5314: Copy message, Start a new session, ⋯, the time); the other replies (narration in a work summary, words
// still streaming) have the menus only, and on a touch screen the last reply has a copy button and a ⋯ under it. "Quote" puts
// it as a quote in front of the draft, "Edit" (your own messages) puts it back in the composer to change and send again.
// "Forward" (#3899) opens Forward to: Persona or any Agent, the message over that chat's box with a note to type
// (lib/forward.ts); it took the place of "Hand to Persona". "Use as context" attaches it to this chat's next message. Sharing
// as a card is the phone's. "Start a new session" (#2137: a new conversation with what was said up to this reply) is there
// when the session can branch. Beside your own bubbles, Forward and Use as context live in ⋯ to keep the hover bar compact.
// The right-click / long-press menu ends in Select messages (#9469), after a line: picking starts with the message picked.
// While the chat picks messages to forward at once (#3838, Forward to's Select messages…), a message with a Forward has a
// checkbox over it (Picking.tsx PickRow, which comes with Forward to): a click anywhere on it picks it or unpicks it, the
// tick before it; its menus and bars are off. Not picking, the row is drawn as it always was.
import { lazy, Suspense, useRef, useState } from "react";
import { t } from "../../i18n";
import { clock } from "../../lib/format";
import { sendDraft, type QuotedTask } from "../../lib/drafts";
import type { ForwardPick } from "../../lib/forward";
import { addContext, newContext } from "../../lib/links";
import type { StarOrigin } from "../../lib/starred-rows";
import type { Item } from "../../lib/types";
import { copyText, cx, type MenuItem } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { NO_HOVER, useMedia } from "../../ui/media-query";
import { useChatActions, type ChatActions } from "./chat-actions";
import { pickParts } from "./picking-parts";
import { useIsLatestMessage } from "./latest-message-time";
import { useReplyTranslation, type ReplyTranslationSource } from "../../lib/reply-translation";
import { Markdown } from "../../ui/Markdown";
import { ReplyTranslationButton } from "./ReplyTranslationButton";
import { lazyDialog, lazyOverlay } from "../LazyOverlay";

// Hidden until the pointer comes, so it is not part of the first screen (startup-preload.ts): its row is kept while it
// loads, so nothing moves when it comes.
const replyBar = () => import("./ReplyBar");
const StarAvailability = lazy(() => replyBar().then((m) => ({ default: m.StarAvailability })));
const StarBadge = lazy(() => replyBar().then((m) => ({ default: m.StarBadge })));
const ReplyBar = lazy(() => replyBar().then((m) => ({ default: m.ReplyBar })));
// what the exchange cost, at the end of a touch screen's row (the reply bar has it on hover)
const TurnUsage = lazy(() => replyBar().then((m) => ({ default: m.TurnUsage })));
const SelectMessageText = lazyDialog(() => replyBar().then((m) => m.SelectMessageText));
const MessageMenu = lazyOverlay(() => replyBar().then((m) => m.MessageMenu));


/** The actions of one message; empty when there is nothing to act on. */
export interface Action extends Pick<MenuItem, "hint" | "disabled" | "danger" | "confirm" | "inline" | "children"> {
  key: string;
  label: string;
  icon: IconName;
  onSelect: () => void | Promise<unknown>;
}

function messageActions(chat: ChatActions | null, text: string, isUser: boolean, ts: string | undefined, task: QuotedTask | undefined, forward?: ForwardPick, editText?: string): Action[] {
  if (!text.trim()) return [];
  const out: Action[] = [{ key: "copy", label: t("chat-messageactions.copy"), icon: "copy", onSelect: () => void copyText(text) }];
  if (chat?.canCompose) {
    out.push({ key: "quote", label: t("chat-messageactions.quote"), icon: "quote", onSelect: () => void sendDraft(chat.draftKey, { type: "quote", text, from: isUser ? t("chat-messageactions.me") : chat.speaker, ts, task }) });
  }
  out.push(...messageContextActions(text, chat, forward));
  if (chat?.canCompose && isUser) out.push({ key: "edit", label: t("chat-messageactions.edit"), icon: "pencil", onSelect: () => void sendDraft(chat.draftKey, { type: "replace", text: editText ?? text }) });
  return out;
}

/** The message goes on to another chat (Forward, a message in the transcript: `forward` names it), or to its own
 *  conversation's next message (Android MessageMenu). */
export function messageContextActions(text: string, chat: ChatActions | null, forward?: ForwardPick): Action[] {
  if (!chat || !text.trim()) return [];
  const title = t("chat-messageactions.context_title", { title: chat.title });
  const out: Action[] = [];
  const send = chat.forward;
  if (send && forward) out.push({ key: "forward", label: t("chat-forward.forward"), icon: "forward", onSelect: () => send(forward) });
  if (chat.canCompose) out.push({
    key: "context", label: t("chat-messageactions.use_as_context"), icon: "clip",
    onSelect: () => addContext(chat.draftKey, newContext("context", title, "", text)),
  });
  return out;
}

/** Wraps a message: right-click / long press opens its menu. A hover bar goes beside it (`side`, your own bubbles: they had
 *  no row under them, so the layout does not move; the time leads that bar, #5285), over its top right corner (`corner`, a
 *  task's card: no row under it either), or under it (`bar`, a finished turn's last reply: ReplyBar). Without any of those
 *  it has the menus only. `task`: "Quote" quotes it as that task. The chat's last message keeps its time on screen
 *  (#5285): a last reply's in its bar (ReplyBar `pinned`) and the touch row, your own bubble's under it (UserBubble). */
export function MessageActions({
  text,
  editText,
  isUser,
  ts,
  side,
  corner,
  bar,
  task,
  itemId,
  messageId,
  images,
  photos,
  onDelete,
  usage,
  usageMs,
  translationSource,
  starSource,
  children,
  className,
}: {
  text: string;
  /** Explicit selections kept when editing or rewinding; Copy, Quote and Forward use visible words only. */
  editText?: string;
  isUser?: boolean;
  ts?: string;
  side?: boolean;
  corner?: boolean;
  bar?: boolean;
  task?: QuotedTask;
  /** The reply's transcript item: where "Start a new session" branches off, and what Forward forwards. */
  itemId?: string;
  /** Your message (UserMessage.id): where "Rewind to here" goes back to, and what Forward forwards. */
  messageId?: string;
  /** Your message's pictures, which go with a forward of it. */
  images?: Item[];
  photos?: string[];
  /** A message only on this device (a failed send): "Delete" ends its menus (#4626). */
  onDelete?: () => void;
  /** With `bar`: what the exchange used (lib/turn-usage.ts usageKey) and its work time, after the time (#5426). */
  usage?: string;
  usageMs?: number;
  /** A hub-paged reply must load all its words before translation, without changing its in-flight identity. */
  translationSource?: ReplyTranslationSource;
  /** The persisted source item, separate from the Forward / Rewind identifiers. */
  starSource?: StarOrigin;
  children: React.ReactNode;
  className?: string;
}) {
  const [selecting, setSelecting] = useState(false);
  const translation = useReplyTranslation(text, translationSource);
  const canTranslate = bar && !isUser && Boolean(text.trim());
  const touch = useMedia(NO_HOVER);
  const latest = useIsLatestMessage(ts);
  const forwarded = itemId ?? messageId;
  const pick: ForwardPick | undefined = forwarded ? { itemId: forwarded, text, isUser: Boolean(isUser), ts, images, photos } : undefined;
  const chat = useChatActions();
  const [starAvailable, setStarAvailable] = useState<"read" | "write" | null>(null);
  const actions = messageActions(chat, text, Boolean(isUser), ts, task, pick, editText);
  // the chat picks messages to forward (#3838): the checkbox over this one, when it is one to forward
  const picker = chat?.forward ? chat.pick : undefined;
  const picking = Boolean(picker);
  const PickRow = picker && pick && text.trim() ? pickParts.Row : null;
  // Right-click and the side bar's More are entries to the same overlay, never two menus.
  const [menu, setMenu] = useState<{ anchor: HTMLElement | { x: number; y: number }; more?: boolean } | null>(null);
  const moreAnchor = menu?.more;
  const canFork = Boolean(itemId && chat?.fork && !itemId.startsWith("line") && itemId.includes("#"));
  const hasMore = Boolean((starAvailable === "write" && starSource) || canFork || onDelete || ((side || isUser) && chat?.canCompose) ||
    (side && chat?.forward && pick) || (isUser && messageId && chat?.rewind && (editText ?? text).trim()));
  // Your bubble's hover bar sits to its left; in a narrow window a wide bubble leaves no room there and the bar fell
  // off the screen, its buttons still reached by Tab (audit item 73). Measured when the pointer comes or focus moves in
  // (not for every bubble as the chat opens): with no room, the same buttons sit under the bubble instead, so the one
  // focused stays focused and in sight.
  const barRef = useRef<HTMLDivElement>(null);
  const [below, setBelow] = useState(false);
  const placeBar = (row: HTMLElement) => {
    const bar = barRef.current;
    if (!side || !bar) return;
    // Below is safe while the intent-loaded placement code arrives; keep the same focused buttons.
    setBelow(true);
    void replyBar().then(({ placeSideBar }) => placeSideBar(row, bar, setBelow),
      () => {}); // A failed chunk leaves the working controls below; the next intent can try again.
  };
  const press = useRef<{ timer: number; x: number; y: number } | null>(null);
  const opened = useRef(0);

  const openAt = (x: number, y: number) => setMenu({ anchor: { x, y } });
  const close = () => setMenu(null);

  const cancelPress = () => {
    if (press.current) clearTimeout(press.current.timer);
    press.current = null;
  };

  if (!actions.length) return <div className={className}>{children}</div>;

  return (
    <div
      className={cx("group/msg", (side || corner || PickRow || starAvailable) && "relative", PickRow && (side ? "isolate" : "isolate pl-7"), className)}
      onPointerEnter={side ? (e) => e.pointerType !== "touch" && placeBar(e.currentTarget) : undefined}
      onFocus={side ? (e) => placeBar(e.currentTarget) : undefined}
    >
      {!picking && starSource && (
        <Suspense fallback={null}>
          <StarAvailability origin={starSource} chat={chat} report={setStarAvailable} />
          {starAvailable && <StarBadge origin={starSource} />}
        </Suspense>
      )}
      {PickRow && picker && pick && <PickRow pick={picker} message={pick} side={side} />}
      <div
        inert={picking || undefined}
        className="[@media(hover:none)]:select-none [@media(hover:none)]:[-webkit-touch-callout:none]"
        onContextMenu={(e) => {
          if (Date.now() - opened.current < 1000) {
            e.preventDefault(); // the long press already opened ours
            return;
          }
          // a selection inside the message (the browser's menu copies it), a link, a picture or code: the browser's own
          const sel = getSelection();
          if (sel && !sel.isCollapsed && sel.anchorNode && e.currentTarget.contains(sel.anchorNode)) return;
          if ((e.target as Element).closest?.("a, img, pre")) return;
          e.preventDefault();
          openAt(e.clientX, e.clientY);
        }}
        onPointerDown={(e) => {
          if (e.pointerType !== "touch") return;
          cancelPress();
          const x = e.clientX,
            y = e.clientY;
          press.current = {
            x,
            y,
            timer: window.setTimeout(() => {
              press.current = null;
              opened.current = Date.now();
              openAt(x, y);
            }, 450),
          };
        }}
        onPointerMove={(e) => {
          const p = press.current;
          if (p && Math.hypot(e.clientX - p.x, e.clientY - p.y) > 8) cancelPress();
        }}
        onPointerUp={cancelPress}
        onPointerCancel={cancelPress}
        // the finger lifting after the press opened the menu: no mouse events after it, whose mousedown would land
        // outside the menu and close it at once
        onTouchEnd={(e) => {
          if (Date.now() - opened.current < 1000) e.preventDefault();
        }}
      >
        {canTranslate && translation.shown ? (
          <div data-reply-translation className="[&_.prose-j]:text-[length:var(--chat-body)]! [&_.prose-j]:leading-[1.6]!">
            <Markdown text={translation.text!} lineBreaks />
          </div>
        ) : children}
      </div>
      {canTranslate && translation.error && <p role="alert" className="mt-1 text-xs text-red">{translation.error}</p>}
      {picking ? null : bar ? (
        <Suspense fallback={<div data-reply-bar="loading" className="mt-1.5 hidden h-6 [@media(hover:hover)]:block" />}>
          <ReplyBar text={text} ts={ts} pinned={latest} itemId={itemId} more={actions} star={starAvailable ? starSource : undefined} loadStar={translationSource?.load} usage={usage} usageMs={usageMs} persona={chat?.assistant} translation={canTranslate ? translation : undefined} />
        </Suspense>
      ) : (side || corner) && (
      <div
        ref={barRef}
        data-side-bar={side ? (below ? "below" : "beside") : undefined}
        className={cx(
          "hidden h-6 items-center gap-0.5 opacity-0 transition-opacity duration-200 group-hover/msg:opacity-100 focus-within:opacity-100 [@media(hover:hover)]:flex",
          moreAnchor && "opacity-100", // the ⋯ menu is open: its bar stays while the pointer is in the menu
          side
            ? below
              ? // over what comes next while shown only: hidden, it takes no clicks meant for that; a bridge over the gap
                // keeps the pointer on its way down from the bubble in the hover
                cx("absolute top-full right-0 z-10 mt-1 rounded-control bg-surface px-0.5 shadow-hairline before:absolute before:inset-x-0 before:-top-1 before:h-1",
                  moreAnchor ? "pointer-events-auto" : "pointer-events-none group-hover/msg:pointer-events-auto focus-within:pointer-events-auto")
              : "absolute right-full bottom-0.5 mr-1.5"
            : "absolute top-2 right-2 rounded-[8px] bg-surface px-0.5 shadow-hairline",
        )}
      >
        {side && ts && !latest && <span className="mr-1 text-[12px] whitespace-nowrap text-ink-3 tabular-nums">{clock(ts)}</span>}
        {actions
          .filter((a) => !(side && (a.key === "forward" || a.key === "context")))
          .map((a) => (
            <button
              key={a.key}
              type="button"
              aria-label={a.key === "copy" ? (isUser ? t("chat-messageactions.copy_message") : t("chat-messageactions.copy_reply")) : a.label}
              title={a.label}
              onClick={a.onSelect}
              className="flex size-6 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
            >
              <Icon name={a.icon} size={14} />
            </button>
          ))}
        {hasMore && (
          <button
            type="button"
            aria-label={t("chat-messageactions.more")}
            title={t("chat-messageactions.more")}
            aria-haspopup="menu"
            aria-expanded={Boolean(moreAnchor)}
            onClick={(e) => setMenu(moreAnchor ? null : { anchor: e.currentTarget, more: true })}
            className={cx("flex size-6 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2", moreAnchor && "bg-hover-2 text-ink-2")}
          >
            <Icon name="more" size={14} />
          </button>
        )}
      </div>
      )}
      {touch && bar && !picking && (
        <div data-reply-touch-bar className="mt-1 flex h-6 items-center gap-5">
          <button type="button" aria-label={t("chat-messageactions.copy_reply")} title={t("chat-messageactions.copy")} onClick={() => void copyText(text)} className="touch-hit flex size-6 items-center justify-center rounded-[6px] text-ink-3">
            <Icon name="copy" size={14} />
          </button>
          {canTranslate && <ReplyTranslationButton translation={translation} touch />}
          <button
            type="button"
            aria-label={t("chat-messageactions.more")}
            title={t("chat-messageactions.more")}
            aria-haspopup="menu"
            aria-expanded={Boolean(menu)}
            onClick={(e) => (menu ? close() : setMenu({ anchor: e.currentTarget }))}
            className="touch-hit flex size-6 items-center justify-center rounded-[6px] text-ink-3"
          >
            <Icon name="more" size={14} />
          </button>
          {latest && ts && <span data-latest-time className="-ml-3 text-[12px] whitespace-nowrap text-ink-3 tabular-nums">{clock(ts)}</span>}
          {usage && (
            <Suspense fallback={null}>
              <TurnUsage usage={usage} ms={usageMs} persona={chat?.assistant} touch />
            </Suspense>
          )}
        </div>
      )}
      {menu && !picking && <MessageMenu
        anchor={menu.anchor} onClose={close} primary={actions} more={menu.more}
        itemId={itemId} text={text} ts={ts} star={starAvailable ? starSource : undefined} loadStar={translationSource?.load} rewindText={editText ?? text} messageId={messageId} isUser={isUser} onDelete={onDelete} pick={pick}
        onSelectText={() => setSelecting(true)}
      />}
      <SelectMessageText open={selecting} onClose={() => setSelecting(false)} text={text} />
    </div>
  );
}
