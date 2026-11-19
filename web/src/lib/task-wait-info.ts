import type { ParkContact, ParkEvidence } from "./supervision";
import { sentWaitContact, waitSource, waitStart } from "./task-wait";
import { openAsk, type PRFacts, type Todo } from "./todos";

export interface TaskWait {
  kind: ParkEvidence["kind"] | "legacy";
  target?: string;
  since?: number;
  sinceRequest: boolean;
  contact?: ParkContact;
  /** Whether a missing contact is worth saying. An external event or a date never carries one (park_evidence.go refuses
   *  it), so saying none was recorded tells the owner nothing; a legacy park or an implicit PR wait could have had one. */
  contactExpected: boolean;
  wake?: string;
  reason?: string;
  until?: number;
  overdue: boolean;
  canRemind: boolean;
  sources: string[];
}

/** Lazy display projection. The caller supplies only fresh, eligible merge waits; no lifecycle classification or write. */
export function taskWait(todo: Todo, now: number, merges: readonly PRFacts[] = []): TaskWait | undefined {
  if (todo.done_at) return undefined;
  const sup = todo.supervision;
  if (sup?.state === "needs_user" || sup?.state === "pending_review" || openAsk(todo)) return undefined;
  const p = sup?.state === "parked" ? sup.parked : undefined;
  if (!p && !merges.length) return undefined;
  const e = p?.evidence;
  const kinds: string[] = ["person_reply", "merge_review", "external_event", "date"];
  const kind = e && kinds.includes(e.kind) ? e.kind : p ? "legacy" : "merge_review";
  const contactKind = kind === "person_reply" || kind === "merge_review";
  const contact = contactKind ? sentWaitContact(e, now) : undefined;
  const deadline = p?.until ? Date.parse(p.until) : NaN;
  const until = Number.isFinite(deadline) && deadline > 0 ? deadline : undefined;
  const contactDue = contact ? Date.parse(contact.sent_at) + (kind === "merge_review" ? 72 : 24) * 3_600_000 : undefined;
  const start = waitStart(todo, now);
  return {
    kind,
    target: e?.target?.trim() || undefined,
    since: start.at,
    sinceRequest: start.request,
    contact,
    contactExpected: contactKind || kind === "legacy",
    wake: e?.wake?.trim() || undefined,
    reason: p?.reason,
    until,
    overdue: until !== undefined && until <= now || contactDue !== undefined && contactDue <= now,
    canRemind: contactDue !== undefined && contactDue <= now,
    // A task's other PRs are not evidence for this recorded wait.
    sources: [...new Set((e ? [e.url] : merges.map((pr) => pr.url)).map(waitSource).filter((url): url is string => !!url))],
  };
}
