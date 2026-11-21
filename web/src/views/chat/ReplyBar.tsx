// The bar under a finished turn's last reply (#5314), after cindy's MessageActionBar as it sits under an assistant
// message, at the owner's request (2026-11-12): Copy message (a ✓ once copied, the copy icon again on the next hover),
// Start a new session where the session can branch, ⋯ More message actions (Quote, Forward, Use as context), then when
// the reply was written ("5 minutes ago", the exact time as its tooltip). The pointer over the reply (the element the bar
// is in: MessageActions') or keyboard focus in the bar shows it; it stays REPLY_BAR_LINGER_MS after the pointer leaves,
// then fades (at once with reduced motion), and an open ⋯ menu keeps it. Narration inside a work summary and a reply
// still streaming have no bar: their right-click / long-press menu (MessageActions) has every action, and touch screens
// have MessageActions' copy + ⋯ row instead. The chat's last reply (`pinned`, #5285) keeps its time on screen in its
// place at the end of the bar; the buttons still come only with the pointer. After the time, what the exchange cost or its
// tokens, with cindy's usage card (TurnUsage, #5426): it comes with the pointer like the buttons (cindy shows it on hover),
// and an open card keeps the bar. All of it watches the pointer itself, so the reply around it draws nothing again, and
// none of it is on the first screen (MessageActions loads it lazily).
// MessageActions' menus and text selection share this chunk when opened.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { exactTime, relativeTime } from "../../lib/relative-time";
import { Button, copyText, cx, Dialog, Menu, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { REPLY_BAR_FADE_MS, REPLY_BAR_LINGER_MS } from "../../ui/motion";
import { isNoHover } from "../../ui/media-query";
import type { ForwardPick } from "../../lib/forward";
import type { Action } from "./MessageActions";
import { TurnUsage } from "./TurnUsage";
import type { ReplyTranslation } from "../../lib/reply-translation";
import { ReplyTranslationButton } from "./ReplyTranslationButton";
import { useChatActions, type ChatActions } from "./chat-actions";
import { starrable, useCanStar, useStarAccess, useStarMark, type StarOrigin, type StarSource } from "../../lib/starred-rows";
import { addStar, removeStar } from "../../lib/starred";
import { starUserText } from "./star-user-text";
import type { RewindRestore } from "../../lib/rewind";

export { TurnUsage };

/** The left edge of what shows `el`: the nearest box that clips it sideways (the chat's scroller), or the window's. */
function shownLeft(el: HTMLElement): number {
  for (let box = el.parentElement; box; box = box.parentElement) {
    const style = getComputedStyle(box);
    if (style.overflowX !== "visible" || /paint|strict|content/.test(style.contain)) return Math.max(0, box.getBoundingClientRect().left);
  }
  return 0;
}

/** Place the hover bar beside your bubble (0.375rem away) only when it still exists and fits inside the chat. */
export function placeSideBar(row: HTMLElement, bar: HTMLElement, setBelow: (below: boolean) => void): void {
  if (!row.isConnected || !bar.isConnected) return;
  const gap = 0.375 * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
  setBelow(!(row.getBoundingClientRect().left - gap - bar.offsetWidth >= shownLeft(row)));
}

/** "Start a new session" (cindy's words; "Start a new chat" in a Persona conversation, `chat`), on the reply bar and at the
 *  end of a reply's right-click menu, when the session can fork and the reply is a transcript line of its own
 *  (`<uuid>#<n>`; lines without a uuid are `line<seq>`, which the node cannot find). */
export function moreActions(itemId: string | undefined, fork: ((itemId: string) => void) | undefined, chat?: boolean): Action[] {
  if (!itemId || !fork || itemId.startsWith("line") || !itemId.includes("#")) return [];
  return [{ key: "fork", label: t(chat ? "chat-messageactions.fork_chat" : "chat-messageactions.fork"), icon: "fork", onSelect: () => fork(itemId) }];
}

/** Selection is opened from a message action, never drawn on the first screen. */
export function SelectMessageText({ open, onClose, text }: { open: boolean; onClose: () => void; text: string }) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("chat-messageactions.select_text")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("chat-messageactions.done")}</Button>
          <Button tone="primary" onClick={() => { void copyText(text); onClose(); }}>{t("chat-messageactions.copy_all")}</Button>
        </>
      }
    >
      <div className="max-h-[50vh] overflow-y-auto text-sm leading-relaxed whitespace-pre-wrap text-ink select-text [overflow-wrap:anywhere]">{text}</div>
    </Dialog>
  );
}

/** "Save as Prompt…" on a message of yours (#7329): the saved prompt editor over the page, filled with its words (a chunk of
 *  its own, settings/SavedPromptDialog). */
export function savePromptActions(text: string): Action[] {
  if (!text.trim()) return [];
  return [{
    key: "save-prompt", label: t("chat-messageactions.save_as_prompt"), icon: "book",
    onSelect: () => import("../settings/SavedPromptDialog").then(
      (m) => m.openSavedPromptDialog(text),
      (e: unknown) => toast(e instanceof Error ? e.message : String(e), "error"), // the chunk would not load
    ),
  }];
}

/** "Delete", last and in red, on a message only on this device (#4626, the apps' long press): in the right-click / long-press
 *  menu, and for your own bubbles in ⋯. */
export function deleteActions(onDelete: (() => void) | undefined): Action[] {
  return onDelete ? [{ key: "delete", label: t("chat-outboxbubbles.delete"), icon: "trash", danger: true, onSelect: onDelete }] : [];
}

type Rewind = NonNullable<ChatActions["rewind"]>;

/** "Rewind to here" (#2436) on a message of yours: "Conversation only" / "Code and conversation", the second asking again (it puts files back too). Off
 *  with the reason as its hint while the session cannot ("Claude only", "Working"…). Each choice waits for the node inside the
 *  menu (it drives Claude's own /rewind, up to a minute) and shows the failure there. */
export function rewindActions(messageId: string | undefined, text: string, rewind: { block: string; code?: boolean; run: Rewind["run"] } | undefined): Action[] {
  if (!messageId || !rewind || !text.trim()) return [];
  const go = (restore: RewindRestore) => () => rewind.run(messageId, text, restore);
  return [
    {
      key: "rewind",
      label: t("rewind.menu"),
      icon: "undo",
      hint: rewind.block || undefined,
      disabled: Boolean(rewind.block),
      onSelect: () => {},
      children: [
        { key: "rewind-conversation", label: t("rewind.conversation"), inline: true, onSelect: go("conversation") },
        ...(rewind.code === false ? [] : [{ key: "rewind-code", label: t("rewind.code_and_conversation"), danger: true, confirm: t("rewind.confirm_code"), inline: true, onSelect: go("code_and_conversation") }]),
      ],
    },
  ];
}

const NO_STORE = () => () => {};

/** `live`'s value while `open` (a menu that shows it), `closed` otherwise: a closed menu does not render again when it
 *  changes (why "Rewind to here" is off changes each time the session starts or stops working). */
function useWhileOpen(live: Rewind["block"] | undefined, open: boolean, closed: string): string {
  const on = open ? live : undefined;
  const get = on ? on.get : () => closed;
  return useSyncExternalStore(on ? on.subscribe : NO_STORE, get, get);
}

function starSource(origin: StarOrigin | undefined, chat: ChatActions | null): StarSource | undefined {
  if (!chat || !starrable(origin)) return undefined;
  return {
    machine_id: origin.mid, session_id: origin.sid, item_id: origin.item,
    conversation: chat.assistant ? "assistant" : "agent",
    source_fingerprint: origin.fingerprint,
  };
}

export function StarAvailability({ origin, chat, report }: { origin: StarOrigin; chat: ChatActions | null; report: (available: "read" | "write" | null) => void }) {
  const available = useStarAccess();
  useEffect(() => { report(available && chat && starrable(origin) ? available : null); }, [available, chat, origin, report]);
  return null;
}

export function StarBadge({ origin }: { origin: StarOrigin }) {
  const mark = useStarMark(starSource(origin, useChatActions()));
  if (!mark.id && !mark.pending) return null;
  return (
    <span className="pointer-events-none absolute -top-2 right-0 z-10 inline-flex items-center rounded-full bg-surface p-0.5 text-accent-ink" role="status"
      aria-label={t(mark.pending ? "starred.saving" : "starred.marked")} data-star-mark={mark.pending ?? "saved"}>
      {mark.pending ? <Spinner size={12} /> : <Icon name="star" className="fill-current" size={12} />}
    </span>
  );
}

function useStarAction(origin: StarOrigin | undefined, text: string, ts: string | undefined, isUser: boolean, load: (() => Promise<string>) | undefined): Action[] {
  const chat = useChatActions();
  const source = starSource(origin, chat);
  const mark = useStarMark(source);
  const allowed = useCanStar();
  if (!chat || !source || !allowed || !text.trim()) return [];
  return [{
    key: "star", label: t(mark.id ? "starred.unstar" : "starred.star"), icon: "star", disabled: Boolean(mark.pending),
    onSelect: () => {
      if (mark.id) void removeStar({ ...source, id: mark.id });
      else void addStar({
        ...source, role: isUser ? "user" : "assistant", source_manager: chat.sourceManager,
        title: chat.title, speaker: isUser ? "" : chat.speaker,
        written_at: ts && Number.isFinite(Date.parse(ts)) ? new Date(ts).toISOString() : undefined, text,
      }, load ?? starUserText(origin));
    },
  }];
}

/** Menu-only actions and their live disabled reasons load with the existing reply controls, not the transcript. */
export function MessageMenu({ anchor, onClose, primary: actions, itemId, text, rewindText, messageId, isUser, onDelete, more, onSelectText, pick, star, loadStar, ts }: {
  anchor: HTMLElement | { x: number; y: number };
  onClose: () => void;
  primary: Action[];
  itemId?: string;
  text: string;
  rewindText: string;
  messageId?: string;
  isUser?: boolean;
  onDelete?: () => void;
  more?: boolean;
  onSelectText: () => void;
  pick?: ForwardPick;
  star?: StarOrigin;
  loadStar?: () => Promise<string>;
  ts?: string;
}) {
  const chat = useChatActions();
  const rewind = isUser ? chat?.rewind : undefined;
  const block = useWhileOpen(rewind?.block, true, "");
  const primary = more ? actions.filter((a) => a.key === "forward" || a.key === "context") : [...actions];
  if (!more && isNoHover()) primary.splice(1, 0, {
    key: "select", label: t("chat-messageactions.select_text"), icon: "read", onSelect: onSelectText,
  });
  const extras = [
    ...useStarAction(star, text, ts, Boolean(isUser), loadStar),
    ...moreActions(itemId, chat?.fork, chat?.assistant),
    ...rewindActions(messageId, rewindText, rewind && { block, code: rewind.code, run: rewind.run }),
    ...(isUser && chat?.canCompose ? savePromptActions(text) : []),
    ...deleteActions(onDelete),
  ];
  const items = [...primary, ...extras].map((a, i) => ({ ...a, divider: i === primary.length && i > 0 }));
  const send = chat?.forward;
  if (!more && send && pick && !chat?.pick) items.push({
    key: "pick", label: t("chat-messageactions.pick"), icon: "check", divider: true, onSelect: () => send(pick, true),
  });
  return (
    <Menu
      anchor={anchor instanceof HTMLElement ? anchor : null}
      point={anchor instanceof HTMLElement ? undefined : anchor}
      onClose={onClose}
      items={items}
      side={more ? "top" : undefined}
      width={items.some((i) => i.hint) ? 200 : 168}
    />
  );
}

/** How often the time is worked out again while the pointer is over the reply (cindy's 30 s): only then, so a long chat
 *  holds no timer per reply. */
const TIME_TICK_MS = 30_000;

const BUTTON = "flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2";

/** Copies without a toast (the ✓ says it); without the async clipboard (plain http), copyText's way, which says it. */
async function copyQuietly(text: string): Promise<boolean> {
  const done = await navigator.clipboard?.writeText(text).then(() => true, () => false);
  return done || copyText(text);
}

/** `itemId`: where "Start a new session" branches, when the conversation can. `more`: the message's actions (Copy and
 *  Select have their own controls); no ⋯ without any others. `pinned`: the
 *  chat's last message, whose time stays shown without the pointer. `usage`: what the exchange used (lib/turn-usage.ts
 *  usageKey), `usageMs` its work time, `persona` a Persona conversation's plain cost. */
export function ReplyBar({ text, ts, itemId, more: actions, pinned, usage, usageMs, persona, translation, star, loadStar }: { star?: StarOrigin; loadStar?: () => Promise<string>; text: string; ts?: string; itemId?: string; more: Action[]; pinned?: boolean; usage?: string; usageMs?: number; persona?: boolean; translation?: ReplyTranslation }) {
  const chat = useChatActions();
  const branch = moreActions(itemId, chat?.fork, chat?.assistant)[0];
  const starActions = useStarAction(star, text, ts, false, loadStar);
  const more = [...actions.filter((a) => a.key !== "copy" && a.key !== "select"), ...starActions];
  const ref = useRef<HTMLDivElement>(null);
  const [hovered, setHovered] = useState(false);
  const [visible, setVisible] = useState(false);
  const [copied, setCopied] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [menu, setMenu] = useState<HTMLElement | null>(null);
  const [card, setCard] = useState(false);

  // the pointer over the reply: the element this bar is in
  useEffect(() => {
    const reply = ref.current?.parentElement;
    if (!reply) return;
    const enter = () => setHovered(true);
    const leave = () => setHovered(false);
    reply.addEventListener("mouseenter", enter);
    reply.addEventListener("mouseleave", leave);
    if (reply.matches(":hover")) enter(); // it came in while the pointer was there
    return () => {
      reply.removeEventListener("mouseenter", enter);
      reply.removeEventListener("mouseleave", leave);
    };
  }, []);

  useEffect(() => {
    if (!hovered) {
      // a quick way back in cancels this, so the bar does not flicker
      const linger = window.setTimeout(() => setVisible(false), REPLY_BAR_LINGER_MS);
      return () => clearTimeout(linger);
    }
    setVisible(true);
    // the ✓ goes on the way back in, not as the bar fades (it would snap back mid-fade)
    setCopied(false);
    setNow(Date.now());
    const tick = window.setInterval(() => setNow(Date.now()), TIME_TICK_MS);
    return () => clearInterval(tick);
  }, [hovered]);

  // a pinned reply's time is on screen without the pointer, so it keeps up by itself (one reply in a chat is pinned)
  useEffect(() => {
    if (!pinned) return;
    setNow(Date.now());
    const tick = window.setInterval(() => setNow(Date.now()), TIME_TICK_MS);
    return () => clearInterval(tick);
  }, [pinned]);

  const time = relativeTime(ts, now);
  // with the pointer, an open ⋯ menu or keyboard focus in it: the whole bar; otherwise nothing, or (`pinned`) the time only
  const fade = visible || menu || card ? "opacity-100" : pinned ? "pointer-events-none opacity-0 group-has-[:focus-visible]/bar:pointer-events-auto group-has-[:focus-visible]/bar:opacity-100" : "pointer-events-none opacity-0 has-[:focus-visible]:pointer-events-auto has-[:focus-visible]:opacity-100";
  const fadeStyle = { transitionDuration: `${REPLY_BAR_FADE_MS}ms` };
  const copyLabel = t("chat-messageactions.copy_message");
  const moreLabel = t("chat-replybar.more_actions");
  return (
    <div
      ref={ref}
      data-reply-bar
      // hidden on touch screens, which have the copy + ⋯ row (MessageActions); the keyboard shows it (:focus-visible,
      // so a click does not keep it after the pointer leaves)
      className={cx(
        "mt-1.5 hidden h-6 items-center gap-0.5 transition-opacity [@media(hover:hover)]:flex",
        pinned ? "group/bar" : fade,
      )}
      style={fadeStyle}
      onFocus={() => setNow(Date.now())}
    >
      <div className={cx("flex items-center gap-0.5", pinned && cx("transition-opacity", fade))} style={pinned ? fadeStyle : undefined}>
        <button
          type="button"
          aria-label={copyLabel}
          title={copyLabel}
          onClick={async () => {
            if (await copyQuietly(text)) setCopied(true);
          }}
          className={cx(BUTTON, !copied && "hover:text-ink")}
        >
          <Icon name={copied ? "check" : "copy"} size={14} />
        </button>
        {branch && (
          <button type="button" aria-label={branch.label} title={branch.label} onClick={() => void branch.onSelect()} className={cx(BUTTON, "hover:text-ink")}>
            <Icon name={branch.icon} size={14} />
          </button>
        )}
        {translation && <ReplyTranslationButton translation={translation} />}
        {more.length > 0 && (
          <button
            type="button"
            aria-label={moreLabel}
            title={moreLabel}
            aria-haspopup="menu"
            aria-expanded={Boolean(menu)}
            onClick={(e) => setMenu(menu ? null : e.currentTarget)}
            className={cx(BUTTON, "hover:text-ink", menu && "bg-hover-2 text-ink")}
          >
            <Icon name="more" size={14} />
          </button>
        )}
      </div>
      {time && (
        <time dateTime={ts} title={exactTime(ts)} className="flex h-6 cursor-default items-center text-[12px] whitespace-nowrap text-ink-3">
          {time}
        </time>
      )}
      {usage &&
        (pinned ? (
          // a pinned reply's time stays; what it cost comes with the pointer, as the buttons do
          <span data-usage-fade className={cx("flex items-center transition-opacity", fade)} style={fadeStyle}>
            <TurnUsage usage={usage} ms={usageMs} persona={persona} onOpen={setCard} />
          </span>
        ) : (
          <TurnUsage usage={usage} ms={usageMs} persona={persona} onOpen={setCard} />
        ))}
      {menu && <Menu anchor={menu} onClose={() => setMenu(null)} items={more} width={more.some((a) => a.hint) ? 200 : 184} />}
    </div>
  );
}
