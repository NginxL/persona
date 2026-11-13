// An inbox item as an event (docs/assistant/inbox.md "Events and handling chain", docs/web/web.md "Inbox"): the row's mark ("In progress"'s state
// word, "AI is preparing a suggestion", the suggestion's kind and first line) and the detail's body — ✦ summary, recap, the
// suggestion card
// with its buttons right under it, then the handling chain folded to its newest entry, each entry going where its link says.
// Drawn only for an item that carries this phase's fields (lib/inbox-events.ts `hasEvent`).
import { useEffect, useRef, useState } from "react";
import {
  canExpand, chainRows, chainTap, chainTime, firstLine, handlingFailed, handlingWord, preparing, suggestionButtons, suggestionTitle,
  type ChainEntry, type ChainTap, type InboxSuggestion,
} from "../../lib/inbox-events";
import { conversationRoute, docChangeLine, isDone, itemKey, itemRoute, suggestionAct, useInbox, type InboxItem } from "../../lib/inbox";
import { otherManager } from "../../lib/manager-choice";
import { openLink } from "../../lib/links";
import { type Route } from "../../lib/router";
import { openFromInbox } from "../../lib/inbox-nav";
import { useStore } from "../../lib/store";
import { t } from "../../i18n";
import { Button, cx, Shimmer, Spinner } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { useFoldScroll } from "../chat/fold-scroll";
import { InboxLine } from "./InboxLine";
import { Glyph, Spark } from "./InboxParts";

/** Where a tapped `source` entry (or "Original") opens the item's own page: at a Feishu position, a mail's uid, or a URL. */
export type SourceAt = { position?: string; uid?: number; url?: string };

/** The processing conversation's route: the assistant's conversations open in Persona, the rest as agents. */
function useConversation(): (mid: string, sid: string) => Route {
  const sessions = useStore((s) => s.sessions);
  const assistant = useStore((s) => s.assistant);
  return (mid, sid) => conversationRoute(mid, sid, sessions, assistant);
}

// ── the row ──────────────────────────────────────────────

/** Under a row: what the AI is doing ("In progress"), "AI is preparing a suggestion" (urgent), a failed handling, or the suggestion's kind + first line. */
export function EventMark({ it }: { it: InboxItem }) {
  if (isDone(it)) {
    // filed by the manager after it looked (done_by manager): say who
    if (it.done_by !== "manager") return null;
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap text-ink-3" data-event-mark="manager-done">
        <Icon name="check" size={12} stroke={2.4} className="shrink-0" />
        <span className="truncate">{t("inbox-inboxevent.manager_done")}</span>
      </p>
    );
  }
  const s = it.suggestion;
  if (s) {
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap" data-event-mark="suggestion">
        <span className="shrink-0 font-medium text-accent-ink"><Spark />{suggestionTitle(s)}</span>
        <span className="min-w-0 truncate text-ink-2">{firstLine(s.text)}</span>
      </p>
    );
  }
  if (preparing(it)) {
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap" data-event-mark="preparing">
        <Spinner size={11} />
        <Shimmer className="truncate">{t("inbox-inboxevent.preparing")}</Shimmer>
      </p>
    );
  }
  if (handlingFailed(it)) {
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap text-red" data-event-mark="failed">
        <Icon name="warning" size={12} className="shrink-0" />
        <span className="truncate">{t("inbox-inboxevent.failed_short")}</span>
      </p>
    );
  }
  const word = handlingWord(it);
  if (!word) return null;
  const waiting = it.handling?.state === "waiting";
  return (
    <p className="flex min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap text-ink-2" data-event-mark="working">
      {waiting ? <Icon name="clock" size={12} className="shrink-0 text-ink-3" /> : <Spinner size={11} />}
      <span className="truncate">{word}</span>
    </p>
  );
}

// ── the detail ───────────────────────────────────────────

/**
 * The detail's body for an event: summary, recap, how the handling goes, the suggestion, handling chain, "Related" and "Trail".
 * `embedded`: above the item's own page (a Feishu chat or mail, #3262 / #5471), as tall as it needs up to a share of the pane, then scrolling.
 */
export function EventBody({ item, onSource, embedded = false }: { item: InboxItem; onSource: (at: SourceAt) => void; embedded?: boolean }) {
  const done = isDone(item);
  // capped above the source: its bottom fades while there is more below, so a cut-off row reads as more to scroll to
  const box = useRef<HTMLDivElement>(null);
  const fold = useFoldScroll(box, embedded, [item]);
  const body = (
    <div ref={box} {...fold} className={embedded ? "min-h-0 overflow-y-auto" : "min-h-0 flex-1 overflow-y-auto"} data-event-body={embedded ? "embedded" : ""}>
      <div className={cx("mx-auto flex max-w-[760px] flex-col px-4 sm:px-6", embedded ? "gap-3 py-3" : "gap-4 py-4")}>
        {(item.summary || item.recap) && (
          <section className="flex flex-col gap-2">
            {item.summary && (
              <p className="text-[13.5px] leading-relaxed font-medium text-ink">
                <Spark />
                {item.summary}
              </p>
            )}
            {item.recap && (
              <div>
                <p className="text-[11.5px] font-medium text-ink-3">{t("inbox-inboxevent.recap")}</p>
                <p className="mt-0.5 text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2" data-event-recap>{item.recap}</p>
              </div>
            )}
          </section>
        )}
        {!done && <HandlingLine item={item} />}
        {done && item.done_by === "manager" && (
          <p className="flex items-center gap-2 text-[12.5px] text-ink-2" data-event-manager-done>
            <Icon name="check" size={13} stroke={2.4} className="shrink-0 text-green" />
            {t("inbox-inboxevent.manager_done")}
          </p>
        )}
        {!done && item.suggestion && <SuggestionCard key={`${item.id}@${item.suggestion.version}`} item={item} suggestion={item.suggestion} />}
        <Chain item={item} onSource={onSource} />
        <Related item={item} />
        <InboxLine item={item} />
      </div>
    </div>
  );
  // the line under it outside the fade, which would wash it out
  return embedded ? <div className="flex max-h-[40%] shrink-0 flex-col border-b border-line">{body}</div> : body;
}

/** "In progress"'s state with a way into the processing conversation, or why the AI did not get through it. */
function HandlingLine({ item }: { item: InboxItem }) {
  const conversation = useConversation();
  const h = item.handling;
  if (!h) return null;
  const word = handlingWord(item);
  const to = h.machine_id && h.session_id ? conversation(h.machine_id, h.session_id) : undefined;
  const open = to && (
    <button type="button" onClick={() => openFromInbox(to)} className="-mx-1 inline-flex h-6 shrink-0 items-center gap-1 rounded-[6px] px-1 text-[12px] font-medium whitespace-nowrap text-accent-ink transition-colors hover:bg-hover">
      {t("inbox-inboxevent.open_conversation")}
      <Icon name="chevronRight" size={11} stroke={2.4} />
    </button>
  );
  // urgent item still being looked at: where the suggestion will be, "AI is preparing a suggestion"
  if (preparing(item)) {
    return (
      <div className="flex min-w-0 items-center gap-2 rounded-[10px] bg-inset p-3 text-[12.5px] shadow-hairline" data-event-preparing>
        <Spinner size={12} />
        <Shimmer className="min-w-0 flex-1 truncate">{t("inbox-inboxevent.preparing")}</Shimmer>
        {open}
      </div>
    );
  }
  if (h.state === "failed") {
    return (
      <div className="flex items-start gap-2 rounded-[10px] bg-red-tint p-3 text-[12.5px] text-red" role="status" data-event-failed>
        <Icon name="warning" size={14} className="mt-px shrink-0" />
        <span className="min-w-0 flex-1 leading-relaxed">{h.error ? t("inbox-inboxevent.failed_with", { error: h.error }) : t("inbox-inboxevent.failed")}</span>
        {open}
      </div>
    );
  }
  if (!word) return null; // done: the chain's manager-handling entry opens the conversation
  return (
    <div className="flex min-w-0 items-center gap-2 text-[12.5px] text-ink-2" data-event-handling={h.state}>
      {h.state === "waiting" ? <Icon name="clock" size={13} className="shrink-0 text-ink-3" /> : <Spinner size={12} />}
      <span className="min-w-0 truncate">{word}</span>
      <span className="flex-1" />
      {open}
    </div>
  );
}

/**
 * The suggestion and its buttons: reply "Send" · "Edit" · "Reject"; agent "Start" · "Edit" · "Reject"; conclusion
 * "Got it" · "Reject"; one that needs judgment (handback) "Confirm" · "Edit" · "Reject". "Edit" opens the text to edit
 * ("Confirm" runs the edited one) and "Say something", which goes
 * back to the processing conversation for a new suggestion instead.
 */
export function SuggestionCard({ item, suggestion: s }: { item: InboxItem; suggestion: InboxSuggestion }) {
  const busy = useInbox((st) => st.sending.includes(itemKey(item)));
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(s.text);
  const [say, setSay] = useState("");
  const [acting, setActing] = useState<"" | "confirm" | "reject" | "instruct">("");
  useEffect(() => setText(s.text), [s.text]);
  const b = suggestionButtons(s);
  const act = async (action: "confirm" | "reject" | "instruct", body?: string) => {
    setActing(action);
    const ok = await suggestionAct(item, action, body);
    setActing("");
    if (ok) setEditing(false);
  };
  const edited = text.trim() && text !== s.text ? text : undefined;
  return (
    <section className="rounded-[14px] bg-surface p-3.5 shadow-card" data-suggestion={s.kind} data-handback={s.handback ? "" : undefined}>
      <p className="flex items-center gap-1.5 text-[11.5px] font-medium whitespace-nowrap text-accent-ink">
        <span><Spark />{suggestionTitle(s)}</span>
        {s.handback && <span className="text-ink-3">· {t("inbox-inboxevent.handback")}</span>}
      </p>
      {editing ? (
        <div className="mt-2 flex flex-col gap-2">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            aria-label={t("inbox-inboxevent.edit_text")}
            rows={Math.min(8, Math.max(3, text.split("\n").length + 1))}
            className="w-full resize-y rounded-control bg-field px-2.5 py-2 text-[13px] leading-relaxed text-ink outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]"
            data-suggestion-editor
          />
          <label className="flex h-8 items-center gap-2 rounded-control bg-field px-2.5 focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
            <Icon name="chat" size={13} className="shrink-0 text-ink-3" />
            <input
              value={say}
              onChange={(e) => setSay(e.target.value)}
              placeholder={t("inbox-inboxevent.say_placeholder")}
              aria-label={t("inbox-inboxevent.say")}
              className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3"
              data-suggestion-say
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.nativeEvent.isComposing && say.trim()) void act("instruct", say.trim());
              }}
            />
          </label>
          <div className="flex flex-wrap justify-end gap-2">
            <Button tone="quiet" disabled={busy} onClick={() => { setEditing(false); setText(s.text); setSay(""); }}>{t("common.cancel")}</Button>
            <Button tone={say.trim() ? "accent" : "secondary"} icon="spark" disabled={!say.trim() || (busy && acting !== "instruct")} busy={acting === "instruct"} onClick={() => void act("instruct", say.trim())}>
              {t("inbox-inboxevent.say")}
            </Button>
            <Button tone={say.trim() ? "secondary" : "accent"} icon={s.kind === "reply" && !s.handback ? "send" : "check"} disabled={!text.trim() || (busy && acting !== "confirm")} busy={acting === "confirm"} onClick={() => void act("confirm", edited)}>
              {b.confirm}
            </Button>
          </div>
        </div>
      ) : (
        <>
          <p className="mt-1.5 text-[13px] leading-relaxed whitespace-pre-wrap text-ink" data-suggestion-text>{s.text}</p>
          {(s.changes ?? []).map((c) => (
            <p key={`${c.ref}|${c.field}`} className="mt-1 flex items-center gap-1.5 truncate text-[11.5px] text-ink-2">
              <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3" />
              <span className="truncate">{docChangeLine(c, item.doc)}</span>
            </p>
          ))}
          {s.note && <p className="mt-1.5 text-[12px] leading-relaxed text-ink-3">{s.note}</p>}
          <div className="mt-3 flex flex-wrap justify-end gap-2">
            <Button tone="quiet" disabled={busy && acting !== "reject"} busy={acting === "reject"} onClick={() => void act("reject")}>{t("inbox-inboxevent.reject")}</Button>
            {b.edit && <Button tone="secondary" icon="pencil" disabled={busy} onClick={() => setEditing(true)}>{t("inbox-inboxevent.edit")}</Button>}
            <Button tone="accent" icon={s.kind === "reply" && !s.handback ? "send" : "check"} disabled={busy && acting !== "confirm"} busy={acting === "confirm"} onClick={() => void act("confirm")}>
              {b.confirm}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

const CHAIN_ICON: Record<string, { icon?: IconName; glyph?: "reply" | "question" }> = {
  received: { icon: "inbox" },
  triaged: { icon: "filter" },
  handling: { icon: "spark" },
  sent: { icon: "send" },
  agent: { icon: "agent" },
  confirmed: { icon: "check" },
  rejected: { icon: "x" },
  instructed: { icon: "chat" },
  self_replied: { glyph: "reply" },
  failed: { icon: "warning" },
  done: { icon: "check" },
  concluded: { icon: "read" },
};

function ChainIcon({ kind }: { kind: string }) {
  const c = CHAIN_ICON[kind] ?? { icon: "info" as const };
  const tone = kind === "failed" ? "text-red" : kind === "handling" || kind === "concluded" ? "text-accent" : "text-ink-3";
  return (
    <span className={cx("flex size-5 shrink-0 items-center justify-center rounded-full bg-field", tone)} aria-hidden>
      {c.glyph ? <Glyph name={c.glyph} size={11} stroke={2.2} /> : <Icon name={c.icon!} size={11} stroke={2.2} />}
    </span>
  );
}

/** The handling chain: folded to the newest entry, "Show all (N)" shows them all, oldest first. */
export function Chain({ item, onSource }: { item: InboxItem; onSource: (at: SourceAt) => void }) {
  const [open, setOpen] = useState(false);
  const conversation = useConversation();
  const all = item.chain ?? [];
  if (!all.length) return null;
  const rows = chainRows(all, open);
  const tap = (x: ChainTap) => {
    if (x.kind === "source") {
      if (x.url) {
        openLink(x.url, undefined, item.manager);
        return;
      }
      onSource({ position: x.position, uid: x.uid });
    } else if (x.kind === "route") openFromInbox(x.route);
  };
  return (
    <section data-event-chain>
      <div className="flex items-center gap-2 pb-1">
        <p className="min-w-0 flex-1 text-[11.5px] font-medium whitespace-nowrap text-ink-3">{t("inbox-inboxevent.chain")}</p>
        {all.length > 1 && (
          <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="h-6 shrink-0 rounded-[6px] px-1.5 text-[11.5px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink" data-chain-toggle>
            {open ? t("inbox-inboxevent.chain_collapse") : t("inbox-inboxevent.chain_expand", { count: all.length })}
          </button>
        )}
      </div>
      <ol className="flex flex-col">
        {rows.map((e) => <ChainRow key={e.id} e={e} tap={chainTap(e, conversation, otherManager(item.manager))} onTap={tap} />)}
      </ol>
    </section>
  );
}

function ChainRow({ e, tap, onTap }: { e: ChainEntry; tap: ChainTap; onTap: (x: ChainTap) => void }) {
  const [open, setOpen] = useState(false);
  const expandable = canExpand(e);
  const goes = tap.kind === "source" || tap.kind === "route";
  const press = () => (goes ? onTap(tap) : expandable && setOpen((o) => !o));
  return (
    <li data-chain-entry={e.kind}>
      <div className="flex min-w-0 items-center gap-1">
        <button
          type="button"
          onClick={press}
          disabled={!goes && !expandable}
          aria-expanded={!goes && expandable ? open : undefined}
          className={cx("flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1.5 text-left text-[12.5px]", goes || expandable ? "text-ink hover:bg-field" : "cursor-default text-ink")}
        >
          <span className="min-w-[34px] shrink-0 text-[11.5px] whitespace-nowrap text-ink-3 tabular-nums">{chainTime(e.at)}</span>
          <ChainIcon kind={e.kind} />
          <span className="min-w-0 flex-1 truncate">{e.text}</span>
          {goes && <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3" />}
          {!goes && expandable && <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(90deg)" : undefined }} />}
        </button>
        {/* an entry that goes somewhere and also has words to read: those open beside it */}
        {goes && expandable && (
          <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} aria-label={open ? t("inbox-inboxevent.entry_collapse") : t("inbox-inboxevent.entry_expand")} title={open ? t("inbox-inboxevent.entry_collapse") : t("inbox-inboxevent.entry_expand")} className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink">
            <Icon name="chevron" size={12} stroke={2.4} className="transition-transform duration-200" style={{ transform: open ? "rotate(180deg)" : undefined }} />
          </button>
        )}
      </div>
      {open && (
        <div className="mx-1.5 mb-1 flex flex-col gap-1 rounded-[10px] bg-inset px-3 py-2 text-[12.5px] leading-relaxed text-ink-2" data-chain-detail>
          {e.detail && <p className="whitespace-pre-wrap">{e.detail}</p>}
          {(e.messages ?? []).map((m, i) => (
            <p key={i} className="min-w-0">
              {m.sender && <span className="font-medium text-ink">{t("inbox-inboxevent.message_sender", { sender: m.sender })}</span>}
              <span className="whitespace-pre-wrap">{m.text}</span>
              {m.at && <span className="ml-1.5 text-[11px] text-ink-3 tabular-nums">{chainTime(m.at)}</span>}
            </p>
          ))}
        </div>
      )}
    </li>
  );
}

/** "Related": other items this one touches; each goes its own way, a tap opens it. */
function Related({ item }: { item: InboxItem }) {
  const list = item.related ?? [];
  if (!list.length) return null;
  return (
    <section data-event-related>
      <p className="pb-1 text-[11.5px] font-medium text-ink-3">{t("inbox-inboxevent.related")}</p>
      <ul className="flex flex-col gap-0.5">
        {list.map((r) => (
          <li key={r.id}>
            <button type="button" onClick={() => openFromInbox(itemRoute({ id: r.id, manager: item.manager }))} className="flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left text-[12.5px] whitespace-nowrap text-ink hover:bg-field">
              <Icon name={r.source === "mail" ? "mail" : "link"} size={12} className="shrink-0 text-ink-3" />
              <span className="truncate">{r.title}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
