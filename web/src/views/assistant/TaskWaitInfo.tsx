import { useId, useState } from "react";
import { formatDate, t } from "../../i18n";
import { taskWait } from "../../lib/task-wait-info";
import { waitSource } from "../../lib/task-wait";
import { prBlockers } from "../../lib/todo-triage";
import type { PRFacts, Todo } from "../../lib/todos";
import { Button, copyText, cx, Dialog, useNow } from "../../ui/atoms";

function elapsed(since: number | undefined, now: number, request: boolean): string {
  if (since === undefined) return t("task-wait.age_unknown");
  const minutes = Math.max(0, Math.floor((now - since) / 60_000));
  const age = minutes < 1 ? t("task-wait.just_now") : minutes < 60 ? t("task-wait.minutes", { count: minutes }) :
    minutes < 1440 ? t("task-wait.hours", { count: Math.floor(minutes / 60) }) : t("task-wait.days", { count: Math.floor(minutes / 1440) });
  return t(request ? "task-wait.waiting_age" : "task-wait.parked_age", { age });
}

/** One projection for rows and task details. This reads recorded evidence; it does not watch events or send messages. */
export function TaskWaitInfo({ todo, merges = [], expanded = false }: { todo: Todo; merges?: readonly PRFacts[]; expanded?: boolean }) {
  const now = useNow(60_000);
  const info = taskWait(todo, now, merges);
  const [draft, setDraft] = useState<string>();
  const draftId = useId();
  if (!info) return null;
  const contact = info.contact;
  const contactTime = contact && formatDate(contact.sent_at, { dateStyle: "medium", timeStyle: "short" });
  const age = elapsed(info.since, now, info.sinceRequest);
  const target = info.target ?? t("task-wait.target_unknown");
  const contactURL = waitSource(contact?.url);
  const sourceLinks = info.sources.filter((url) => url !== contactURL || merges.some((pr) => waitSource(pr.url) === url));
  const extra = <>
    {info.reason && <p data-wait-reason>{info.reason}</p>}
    {contact && <p className="whitespace-pre-wrap" data-wait-request>{t("task-wait.request", { request: contact.request })}</p>}
    {info.until && <p>{t("task-wait.fallback", { time: formatDate(new Date(info.until).toISOString(), { dateStyle: "medium", timeStyle: "short" }) })}</p>}
    <p className="text-ink-3">{t(todo.supervision?.parked?.evidence ? "task-wait.evidence_note" : "task-wait.missing_evidence_note")}</p>
  </>;
  return <div data-task-wait-info={todo.id} data-wait-kind={info.kind} data-wait-overdue={info.overdue ? "true" : "false"}
    className="min-w-0 space-y-1 text-xs text-ink-2" onClick={(event) => {
      const target = event.target;
      // Plain waiting text still opens its task row; links, disclosure, actions and the portalled editor do not.
      if (!(target instanceof Element) || !event.currentTarget.contains(target) || target.closest("a, button, summary, input, textarea")) event.stopPropagation();
    }}>
    <div className={cx("flex flex-wrap items-baseline gap-x-2 gap-y-1", info.overdue && "text-red")}>
      <span data-wait-target className="min-w-0 break-words">{t(`task-wait.kind_${info.kind}`)}{t("task-wait.target", { target })}</span>
      <span data-wait-age className="min-w-0 break-words tabular-nums" title={info.since ? new Date(info.since).toISOString() : undefined}>{age}</span>
      {info.overdue && <span data-wait-due className="font-medium">{t("task-wait.follow_up_due")}</span>}
    </div>
    {contact ? <a data-wait-contact href={contact.url} target="_blank" rel="noreferrer noopener" className="block break-words text-accent-ink hover:underline pointer-coarse:min-h-11">
      {t("task-wait.last_contact", { sender: contact.sender, time: contactTime ?? contact.sent_at, channel: contact.channel })}
    </a> : info.contactExpected && <p data-wait-contact-missing className="text-ink-3">{t("task-wait.contact_unknown")}</p>}
    <p data-wait-wake className="break-words">{info.wake ? t("task-wait.wake", { wake: info.wake }) : t("task-wait.wake_unknown")}</p>
    {sourceLinks.length > 0 && <div className="space-y-1">{sourceLinks.map((url, i) => {
      const pr = merges.find((facts) => waitSource(facts.url) === url);
      return <div key={url} data-pr-waiting-url={pr ? url : undefined}>
        {url !== contactURL && <a data-wait-source href={url} target="_blank" rel="noreferrer noopener" title={url} className="block break-all text-accent-ink hover:underline pointer-coarse:min-h-11">{pr ? url : t("task-wait.source", { number: i + 1 })}</a>}
        {pr && <p>{prBlockers(todo, pr).map((key) => t(`assistant-tasks.pr_blocker_${key}`)).join(" · ")}</p>}
      </div>;
    })}</div>}
    {expanded ? <div className="space-y-1 break-words">{extra}</div> : <details className="break-words">
      <summary className="cursor-pointer text-ink-3 pointer-coarse:min-h-11">{t("task-wait.details")}</summary>
      <div className="mt-1 space-y-1">{extra}</div>
    </details>}
    {info.canRemind && <Button testId="wait-reminder" className="pointer-coarse:min-h-11" onClick={() => setDraft((previous) => previous ?? t("task-wait.reminder", { target, title: todo.title, request: contact!.request }))}>{t("task-wait.draft_reminder")}</Button>}
    {draft !== undefined && <Dialog open onClose={() => setDraft(undefined)} title={t("task-wait.draft_reminder")} footer={<>
      <Button className="pointer-coarse:min-h-11" onClick={() => setDraft(undefined)}>{t("common.cancel")}</Button>
      <Button className="pointer-coarse:min-h-11" tone="primary" onClick={() => {
        const text = draft!;
        void copyText(text).then((copied) => {
          // Dismiss after success, but keep a failed copy or a draft edited while clipboard permission was pending.
          if (copied) setDraft((current) => current === text ? undefined : current);
        });
      }}>{t("task-wait.copy_draft")}</Button>
    </>}>
      <p className="mb-3 text-sm text-ink-2">{t("task-wait.draft_note")}</p>
      <label htmlFor={draftId} className="mb-1 block text-xs text-ink-2">{t("task-wait.message")}</label>
      <textarea id={draftId} value={draft} rows={5} onChange={(event) => setDraft(event.target.value)}
        className="w-full resize-y rounded-control border border-line bg-canvas px-3 py-2 text-sm text-ink focus:border-accent" />
      {contact && <a data-reminder-source href={contact.url} target="_blank" rel="noreferrer noopener" className="mt-3 inline-block text-sm text-accent-ink hover:underline pointer-coarse:min-h-11">{t("task-wait.open_channel", { channel: contact.channel })}</a>}
    </Dialog>}
  </div>;
}
