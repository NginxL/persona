// Transcript notices, recaps and model errors. Background continuation belongs to the normal execution summary.
import { memo, Suspense, useMemo, useState } from "react";
import { lineItemIds } from "../../lib/chat-search";
import { useChatContext } from "./chat-context";
import { useExpanded } from "./expanded";
import { useReveal } from "./search-reveal";
import { ago, clock } from "../../lib/format";
import { retryable, type APIErrorInfo } from "../../lib/api-errors";
import type { RecoveryExhaustedView } from "../../lib/model-fallback";
import { runAction, turnErrorFix, type TurnFix } from "../../lib/readiness";
import { wakeSummary } from "../../lib/tasks";
import { type ChatLine, type OpenRecap } from "../../lib/turns";
import { sourceLabel } from "../../lib/message-source";
import type { Item } from "../../lib/types";
import { Button, copyText, cx, Dialog } from "../../ui/atoms";
import { ErrorDetailsText, ErrorNotice } from "../../ui/ErrorNotice";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { SegmentView } from "./SegmentView";
import { CompactDivider } from "./Steps";
import { t } from "../../i18n";
import { ScrollFold } from "./fold-scroll";
import { lazyPage } from "../../lib/lazy-page";

export const RefusalNotice = lazyPage(() => import("./RefusalNotice").then((m) => m.RefusalNotice));

export interface LineProps {
  line: ChatLine;
  mid: string;
  sid: string;
  streaming?: string;
  expandFold: (f: Item) => Promise<boolean | void>;
  /** A Persona conversation: a model error's text may name a fix in Persona's settings. */
  assistant?: boolean;
  /** The session's launcher keeps a text-derived fallback fix scoped when the hub has no turn_error_code. */
  launcher?: string;
  /** The hub's word on what to fix for the turn's model error (the session's turn_error_code): the chat's last line only,
   *  so its record reads as its banner did. */
  fix?: TurnFix | null;
  /** The session is waiting out a subscription usage limit: the last error card says that, not that the turn was interrupted. */
  limit?: string;
  /** The node's recovery chain exhausted every model (#7824): the last error card names the cause and reset time and
   *  offers Retry even when the parsed error would otherwise read as non-transient. */
  exhausted?: RecoveryExhaustedView | null;
  /** Inside an opened supervision trace, show complete injected inputs. */
  trace?: boolean;
  /** A finished exchange before the last two may skip drawing off screen (SegmentView `offscreen`). */
  offscreen?: (section: HTMLElement) => () => void;
  /** The chat's last exchange while the chat is still at work (SegmentView `busy`). */
  busy?: boolean;
}

export const ChatLineView = memo(function ChatLineView(props: LineProps) {
  // a box-less wrapper: the line's own element stays the chat's flex item; search finds the line by it (ChatSearch)
  return (
    <div className="contents" data-line={props.line.id}>
      <LineBody {...props} />
    </div>
  );
});

function LineBody({ line, mid, sid, streaming, expandFold, assistant, launcher, fix, limit, exhausted, trace, offscreen, busy }: LineProps) {
  switch (line.kind) {
    case "supervision":
      return <SupervisionLine line={line} mid={mid} sid={sid} streaming={streaming} expandFold={expandFold} assistant={assistant} />;
    case "segment":
      return (
        <SegmentView
          seg={line.seg}
          trace={trace}
          live={line.live}
          mid={mid}
          sid={sid}
          streaming={line.live ? streaming : undefined}
          batchedWords={!assistant}
          expandFold={expandFold}
          offscreen={offscreen}
          busy={busy}
        />
      );
    case "notice":
      return <LineNotice item={line.item} assistant={assistant} />;
    case "compact":
      return <CompactDivider item={line.item} />;
    case "recaps":
      return <RecapChip items={line.items} />;
    case "apiError":
      // a record in the chat, as cindy's ErrorMessageCard: Retry and Edit last message belong to the banner over the
      // composer (ErrorBanner), while this error is the one the chat stopped on
      if (limit) return <UsageLimitLine text={limit} />;
      // the fix points at Persona's model settings: only for Persona's own conversations
      return (
        <APIErrorCard
          info={line.info}
          count={line.count}
          at={line.item.ts}
          fix={assistant ? (fix ?? turnErrorFix(line.info.code, launcher)) : null}
          exhausted={assistant && (fix ?? turnErrorFix(line.info.code, launcher)) ? null : exhausted}
        />
      );
  }
}

/** One gray line for a whole triage round; expansion uses the actual transcript renderer, not a lossy summary. */
function SupervisionLine({ line, ...props }: LineProps & { line: Extract<ChatLine, { kind: "supervision" }> }) {
  const { scope } = useChatContext();
  const [open, setOpen] = useExpanded(`${scope}supervision:${line.id}`);
  const ids = useMemo(() => lineItemIds(line), [line]);
  useReveal(scope, ids, () => {
    if (open) return;
    setOpen(true);
    return () => setOpen(false);
  });
  return (
    <section className="flex min-w-0 flex-col gap-3" data-supervision={line.live ? "running" : "done"} data-background-round={line.source?.kind}>
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="touch-hit flex w-full min-w-0 items-center gap-1.5 rounded-control py-1 text-left text-[length:var(--chat-secondary)] whitespace-nowrap text-ink-3 hover:text-ink-2">
        <span className={cx("text-accent-ink", line.source ? "max-w-[60%] min-w-0 shrink-0 truncate" : "shrink-0")}>@{line.source ? sourceLabel(line.source) : t("chat-messages.source_supervision")} · </span>
        <span className="min-w-0 truncate">{t(line.source ? (line.live ? "chat-lines.background_running" : "chat-lines.background_done") : line.live ? "chat-lines.supervision_running" : "chat-lines.supervision_done")}</span>
        <Icon name="chevronRight" size={14} className="shrink-0" style={{ transform: open ? "rotate(90deg)" : undefined }} />
      </button>
      {open && <div className="flex min-w-0 flex-col gap-3 border-l-2 border-tool-rail pl-3" data-supervision-trace>
        {line.lines.map((child) => <LineBody key={child.id} {...props} line={child} trace />)}
      </div>}
    </section>
  );
}

/** A notice on a line of its own: a wake opening new work (a centered capsule), "Interrupted", a long local command's output. */
function LineNotice({ item, assistant }: { item: Item; assistant?: boolean }) {
  const [open, setOpen] = useState(false);
  if (item.model_refusal) return <Suspense fallback={null}><RefusalNotice item={item} plain={assistant} /></Suspense>;
  if (item.wake) {
    const w = wakeSummary(item);
    return (
      <div className="flex justify-center">
        <span className="flex max-w-full min-w-0 items-center gap-1.5 rounded-full bg-inset px-3 py-1 text-[12px] text-ink-2 shadow-hairline" title={clock(item.ts)}>
          <Icon name="bell" size={12} className={cx("shrink-0", w.failed ? "text-red" : "text-accent")} />
          <span className="min-w-0 truncate">{w.text}</span>
        </span>
      </div>
    );
  }
  const text = item.text ?? "";
  if (!text.includes("\n") && text.length <= 60) {
    return (
      <div className="flex justify-center">
        <span className="rounded-full bg-inset px-3 py-1 text-[12px] text-ink-2 shadow-hairline" title={clock(item.ts)}>
          {text}
        </span>
      </div>
    );
  }
  return (
    <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex w-full items-start gap-2 rounded-card bg-inset px-3 py-2 text-left shadow-hairline">
      <Icon name="info" size={13} className="mt-[3px] shrink-0 text-ink-3" />
      <span className={cx("min-w-0 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2", !open && "line-clamp-4")}>{text}</span>
    </button>
  );
}

// ── recaps ──

/** Claude Code's "while you were away" recap: one short line; the recap itself opens in a drawer. Several in a row are
 *  one line, the newest first in the drawer. */
function RecapChip({ items }: { items: Item[] }) {
  const [open, setOpen] = useState(false);
  const newest = items[items.length - 1];
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={items.length > 1 ? t("chat-lines.recap_open_many", { count: items.length }) : t("chat-lines.recap_open")}
        className="flex w-fit max-w-full items-center gap-1.5 rounded-full bg-inset px-3 py-1 text-[12px] whitespace-nowrap text-ink-2 shadow-hairline transition-colors hover:bg-hover-2"
      >
        <Icon name="retry" size={12} className="shrink-0 text-accent" />
        {t("chat-lines.recap")}
        {items.length > 1 && <span>{t("chat-lines.recap_times", { count: items.length })}</span>}
        {newest.ts && <span>· {ago(newest.ts)}</span>}
        <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
      </button>
      <RecapDrawer items={items} open={open} onClose={() => setOpen(false)} />
    </>
  );
}

function RecapDrawer({ items, open, onClose }: { items: Item[]; open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onClose={onClose} title={items.length > 1 ? t("chat-lines.recap_title_many", { count: items.length }) : t("chat-lines.recap")} width={600}>
      <div className="flex max-h-[65vh] flex-col gap-4 overflow-y-auto pr-1" data-scroll-box>
        {[...items].reverse().map((it, n) => (
          <section key={it.id} className={cx("flex flex-col gap-2", n > 0 && "border-t border-line pt-4")}>
            {it.ts && (
              <span className="text-[11.5px] text-ink-3">
                {t(n === 0 && items.length > 1 ? "chat-lines.recap_newest_written" : "chat-lines.recap_written", { ago: ago(it.ts) })}
              </span>
            )}
            <Markdown text={it.text ?? ""} lineBreaks />
          </section>
        ))}
      </div>
    </Dialog>
  );
}

/** The open recap's line, the chat's last (#1360): the older recaps folded with it as their chip, then the newest in
 *  full. In the flow with the agent's words: it scrolls with them, and the chat's end includes it. */
export function OpenRecapLine({ open, onClose }: { open: OpenRecap; onClose: () => void }) {
  return (
    <div className="flex flex-col gap-2" data-open-recap>
      {open.earlier.length > 0 && <RecapChip items={open.earlier} />}
      <RecapStrip item={open.newest} onClose={onClose} />
    </div>
  );
}

/** The newest recap in full when you come back to a chat; a click (or typing) closes it back to its chip. */
function RecapStrip({ item, onClose }: { item: Item; onClose: () => void }) {
  return (
    <div className="group/recap relative flex gap-2.5 rounded-card bg-accent-tint px-3 py-2.5" style={{ animation: "fade-up 250ms ease-out both" }}>
      <span aria-hidden className="w-[3px] shrink-0 rounded-full bg-accent" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="flex items-center gap-1.5 text-[12px] font-semibold whitespace-nowrap text-ink">
          <Icon name="retry" size={12} className="text-accent" />
          {t("chat-lines.recap")}
          {item.ts && <span className="font-normal text-ink-3">· {ago(item.ts)}</span>}
        </span>
        <ScrollFold className="max-h-[6lh] text-[13px] leading-relaxed text-ink-2">
          <Markdown text={item.text ?? ""} small lineBreaks />
        </ScrollFold>
      </div>
      <button type="button" onClick={onClose} aria-label={t("chat-lines.recap_close")} title={t("chat-lines.close")} className="flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink">
        <Icon name="x" size={13} />
      </button>
    </div>
  );
}

const CLOSED_RECAPS = "persona.closedRecaps";
const DISMISSED_ERRORS = "persona.dismissedErrors";

/** What was closed in one conversation (`key`), by id, kept in this browser: the newest 200 or so conversations. */
function closedIn(store: string, key: string): string | undefined {
  try {
    return (JSON.parse(localStorage.getItem(store) ?? "{}") as Record<string, string>)[key];
  } catch {
    return undefined;
  }
}

function closeIn(store: string, key: string, id: string) {
  let all: Record<string, string> = {};
  try {
    all = JSON.parse(localStorage.getItem(store) ?? "{}");
  } catch {
    /* start over */
  }
  delete all[key];
  all[key] = id;
  const keys = Object.keys(all);
  if (keys.length > 300) for (const k of keys.slice(0, keys.length - 200)) delete all[k];
  localStorage.setItem(store, JSON.stringify(all));
}

/** The recap closed per conversation (its id), kept in this browser. */
export const closedRecap = (key: string) => closedIn(CLOSED_RECAPS, key);
export const closeRecap = (key: string, id: string) => closeIn(CLOSED_RECAPS, key, id);

/** The error banner dismissed per conversation (its `errorKey`), kept in this browser: only the newest error can be
 *  dismissed, so one per conversation is all there is to keep. */
export const dismissedError = (key: string) => closedIn(DISMISSED_ERRORS, key);
export const dismissError = (key: string, error: string) => closeIn(DISMISSED_ERRORS, key, error);

// ── model API errors ──

type APIErrorLine = Extract<ChatLine, { kind: "apiError" }>;

/** Which error a banner is about: the same error again after a retry folds into its line (foldAPIErrors) with a higher
 *  count, and that is a new failure to say, so the count is part of it. */
export const errorKey = (line: APIErrorLine) => `${line.id}#${line.count}`;

/** The error the chat stopped on, over the composer (cindy's ErrorBanner, #5313): the card the chat would draw, with what
 *  can be done about it now (Fix it, Retry, Edit last message) and ✕. While it is here the chat leaves the line out. */
export function ErrorBanner({ line, assistant, launcher, limit, fix, exhausted, onRetry, onEditLastMessage, onDismiss }: {
  line: APIErrorLine;
  assistant?: boolean;
  /** The session's launcher keeps a text-derived fallback fix scoped when the hub has no turn_error_code. */
  launcher?: string;
  /** Waiting out a subscription usage limit: the banner says when it resets, as the line would. */
  limit?: string;
  /** The hub's word on what to fix for the turn's model error (the session's turn_error_code). */
  fix?: TurnFix | null;
  /** An exhausted recovery chain (#7824): Retry stays offered even for a parsed non-transient error such as a
   *  safeguards refusal, as long as the caller says the session is idle and can send. */
  exhausted?: RecoveryExhaustedView | null;
  /** Sends "continue"; offered only for an error that may pass by itself and needs no fix. */
  onRetry?: () => void;
  onEditLastMessage?: () => void;
  onDismiss: () => void;
}) {
  if (limit) return <UsageLimitLine text={limit} onDismiss={onDismiss} />;
  // the text-derived fix points at Persona's model settings: only for Persona's own conversations
  const f = assistant ? (fix ?? turnErrorFix(line.info.code, launcher)) : null;
  // A fix-type error (sign-in, model route) keeps its own fix card: an exhausted marker never overrides it (#7824).
  const shownExhausted = f ? null : exhausted;
  return (
    <APIErrorCard
      info={line.info}
      count={line.count}
      at={line.item.ts}
      fix={f}
      exhausted={shownExhausted}
      onRetry={onRetry && (f ? f.action === "retry" : shownExhausted || retryable(line.info)) ? onRetry : undefined}
      onEditLastMessage={!assistant && line.info.code === "safeguards" ? onEditLastMessage : undefined}
      onDismiss={onDismiss}
    />
  );
}

/** A subscription usage limit, where an error card would have said the turn was interrupted: the same notice, with
 *  when it resets or goes on (cindy draws the limit as its error notice too). */
function UsageLimitLine({ text, onDismiss }: { text: string; onDismiss?: () => void }) {
  return <ErrorNotice role="status" message={text} onDismiss={onDismiss} data-error-banner={onDismiss ? "" : undefined} />;
}

/** A model API error (cindy's error notice): why, in words, whether it passes by itself under it, then its actions; the
 *  ids, the status code, how many times in a row and the original are under "Error details". One the user must fix (not
 *  signed in, a wrong key, a model that is not there) says so instead, with "Fix it" (docs/assistant/onboarding.md 4.4). */
function APIErrorCard({ info, count, at, onRetry, onEditLastMessage, fix, onDismiss, exhausted }: { info: APIErrorInfo; count: number; at?: string; onRetry?: () => void; onEditLastMessage?: () => void; fix?: TurnFix | null; onDismiss?: () => void; exhausted?: RecoveryExhaustedView | null }) {
  const sub = fix ? fix.detail : info.transient === true ? t("chat-lines.api_transient") : info.transient === false ? t("chat-lines.api_permanent") : "";
  const reason = (fix ? fix.title : info.reason) || t("chat-lines.reply_failed");
  // An exhausted recovery chain says why every model failed and when the configured one recovers (#7824). The cause
  // and the reset time stay on the card's face; the tried-model list goes under "Error details" only.
  const exhaustedLines = exhausted
    ? [exhausted.cause, exhausted.reset ? t("chat-modelfallback.exhausted_resets", { time: exhausted.reset }) : ""].filter(Boolean)
    : [];
  const description = exhausted
    ? exhaustedLines.length
      ? (
        <span className="flex flex-col gap-1" data-testid="recovery-exhausted">
          {exhaustedLines.map((line) => <span key={line}>{line}</span>)}
        </span>
      )
      : (sub || undefined)
    : (sub || undefined);
  return (
    <ErrorNotice
      // the banner over the composer is the one with ✕ (ErrorBanner); it says the error out of the transcript, so it is a
      // status of its own, as the usage limit and the failure note are
      data-error-banner={onDismiss ? "" : undefined}
      role={onDismiss ? "status" : undefined}
      message={reason}
      description={description}
      onDismiss={onDismiss}
      actions={
        <>
          {fix && fix.action !== "retry" && (
            <Button tone="primary" size="sm" className="touch-hit" onClick={() => runAction(fix.action, { launcher: fix.launcher, onHelp: () => {}, onPickModel: fix.onPickModel })}>
              {t("chat-lines.fix_it")}
            </Button>
          )}
          {fix?.back && (
            <Button size="sm" className="touch-hit" onClick={fix.back.run}>
              {fix.back.label}
            </Button>
          )}
          {onRetry && (
            <Button icon="retry" size="sm" className="touch-hit" title={t("chat-lines.retry_title")} onClick={onRetry}>
              {t("chat-lines.retry")}
            </Button>
          )}
          {onEditLastMessage && (
            <Button icon="pencil" size="sm" className="touch-hit" onClick={onEditLastMessage}>
              {t("rewind.edit_last")}
            </Button>
          )}
        </>
      }
      details={
        <div className="flex flex-col gap-1.5">
          {info.requestId && <Field label={t("chat-lines.request_id")} value={info.requestId} />}
          {info.session && <Field label="Session" value={info.session} />}
          {at && <Field label={t("chat-lines.time")} value={clock(at)} copy={false} />}
          {info.status !== undefined && <Field label={t("chat-lines.status")} value={String(info.status)} copy={false} />}
          {count > 1 && <Field label={t("chat-lines.repeated")} value={t("chat-lines.api_repeated", { count })} copy={false} mono={false} />}
          {exhausted?.tried && (
            <div className="min-w-0 text-label leading-5 [overflow-wrap:anywhere] text-ink-2" data-testid="recovery-exhausted-tried">
              {t("chat-modelfallback.exhausted_tried", { models: exhausted.tried })}
            </div>
          )}
          <ErrorDetailsText text={info.raw} />
          <button type="button" onClick={() => copyText(info.raw)} className="touch-hit flex w-fit items-center gap-1 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline">
            <Icon name="copy" size={12} />
            {t("chat-lines.copy_raw")}
          </button>
        </div>
      }
    />
  );
}

function Field({ label, value, copy = true, mono = true }: { label: string; value: string; copy?: boolean; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-center gap-2 text-[12px] leading-5">
      <span className="w-20 shrink-0 truncate text-ink-3">{label}</span>
      <span className={cx("min-w-0 truncate text-ink-2", mono && "font-mono")}>{value}</span>
      {copy && (
        <button type="button" onClick={() => copyText(value)} aria-label={t("chat-lines.copy_field", { label })} className="flex size-5 shrink-0 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover-2 hover:text-ink">
          <Icon name="copy" size={11} />
        </button>
      )}
    </div>
  );
}
