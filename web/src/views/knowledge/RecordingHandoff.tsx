// Handing a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting to an Agent
// (#8852)"): the recording's page offers "Hand to Agent…" once the meeting is over and has something to hand, and only
// to someone with an Agent that can take it. The hub puts the meeting's name, time and minutes (the transcript too when
// asked) before the owner's line and sends it to the Agent picked; each try is kept in `detail.meta.handoffs`, the
// last one shown on the page as a row: where it went (open its conversation), or why it didn't (try again).
import { useMemo, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, enc, errorText } from "../../lib/api";
import { sessionTitle, stateOf } from "../../lib/format";
import { machineWithHub } from "../../lib/mesh";
import { openFromPage } from "../../lib/handoff";
import { BYPASS_MODE } from "../../lib/session-actions";
import { useStore } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { Button, Dialog } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { StatusMark } from "../../ui/StatusMark";
import { handOffAgents, Target } from "../inbox/HandOffTargets";
import { Card } from "./KBParts";

/** The most of the owner's line the hub takes (its maxMeetingTextRunes). */
const TEXT_MAX = 8000;

/** One hand-off as the hub keeps it (`detail.meta.handoffs`, the call's `handoff`). */
export interface MeetingHandoff {
  id: string;
  at: string;
  machine_id: string;
  session_id: string;
  /** The Agent's title when it was handed (absent: the app showed none). */
  title?: string;
  /** The transcript went too. */
  transcript?: boolean;
  state: "sent" | "failed";
  /** Why a failed one didn't go: `not_agent`, `not_allowed`, `unavailable`, `send_failed`. */
  problem?: string;
}

/** The Agents a meeting can be handed to: Hand to AI's (not Persona's own, on computers online now, not offline), less
 *  those the hub refuses outright as attending's rules do: a subagent, one that takes no messages, one closed or
 *  interrupted, and one in Allow All, which nothing would ever ask first. A work computer is the hub's to say. */
export function meetingAgents(sessions: Record<string, Session[]>, machines: Machine[]): Session[] {
  return handOffAgents(sessions, machines).filter((s) =>
    !s.is_subagent && s.permission_mode !== BYPASS_MODE && s.status !== "interrupted" && (!s.source_kind || s.capabilities?.send === true));
}

function isHandoff(v: unknown): v is MeetingHandoff {
  const h = v as MeetingHandoff | null;
  return !!h && typeof h === "object" && typeof h.machine_id === "string" && typeof h.session_id === "string" &&
    (h.state === "sent" || h.state === "failed");
}

/** The meeting's latest hand-off (`detail.meta.handoffs`, oldest first); null without one. */
export function lastHandoff(meta: Record<string, unknown> | null | undefined): MeetingHandoff | null {
  const list = Array.isArray(meta?.handoffs) ? meta.handoffs.filter(isHandoff) : [];
  return list.at(-1) ?? null;
}

/** Why a hand-off didn't go, in the owner's words. */
export function handoffProblem(problem: string | undefined): string {
  switch (problem) {
    case "not_agent": return t("knowledge-handoff.problem_not_agent");
    case "not_allowed": return t("knowledge-handoff.problem_not_allowed");
    case "unavailable": return t("knowledge-handoff.problem_unavailable");
    default: return t("knowledge-handoff.problem_send_failed");
  }
}

/** Why the hub took no hand-off at all: the meeting still running, nothing to hand, or its own words. */
function callError(err: unknown): string {
  if (err instanceof ApiError && err.code === "meeting_running") return t("knowledge-handoff.running");
  if (err instanceof ApiError && err.code === "no_material") return t("knowledge-handoff.no_material");
  return errorText(err);
}

const agentName = (h: Pick<MeetingHandoff, "title">) => h.title?.trim() || t("knowledge-handoff.agent");

/** Picks an Agent, an optional line and whether the transcript goes too, then hands the meeting over. Opened again from
 *  a failed hand-off's Try again: its Agent and transcript choice come back picked. */
export function RecordingHandoffDialog({
  id,
  hasTranscript,
  auth,
  again,
  onClose,
  onDone,
}: {
  id: string;
  /** The recording still has its transcript (not trimmed, not empty): the box is offered. */
  hasTranscript: boolean;
  auth: { headers: Record<string, string> } & Record<string, unknown>;
  /** A failed hand-off tried again. */
  again?: MeetingHandoff | null;
  onClose: () => void;
  /** The hub kept the try (sent, or failed and why): the page shows it at once (its row, the only word of it) and reads
   *  the detail again. */
  onDone: (h: MeetingHandoff) => void;
}) {
  const sessions = useStore((s) => s.sessions);
  const machines = useStore((s) => s.machines);
  // Read once when it opens: an Agent coming or going while the owner writes doesn't move the list under them.
  const [agents] = useState(() => meetingAgents(sessions, machines).slice(0, 8));
  const [target, setTarget] = useState<Session | undefined>(() =>
    (again && agents.find((s) => s.machine_id === again.machine_id && s.session_id === again.session_id)) || agents[0]);
  const [text, setText] = useState("");
  const [transcript, setTranscript] = useState(hasTranscript && !!again?.transcript);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const title = target ? sessionTitle(target) : "";

  const send = async () => {
    if (!target || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await api<{ handoff?: MeetingHandoff }>(`/recordings/${enc(id)}/handoff`, {
        method: "POST",
        json: { machine_id: target.machine_id, session_id: target.session_id, title, text: text.trim(), transcript },
        ...auth,
      });
      const h = isHandoff(r.handoff) ? r.handoff : null;
      if (h) onDone(h);
      if (h?.state === "failed") {
        // kept as failed: said here, where another Agent can be picked
        setError(handoffProblem(h.problem));
        setBusy(false);
        return;
      }
      // No toast: the page's row says where it went, with Open.
      onClose();
    } catch (e) {
      setError(callError(e));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={() => !busy && onClose()}
      title={t("knowledge-handoff.title")}
      width={460}
      footer={
        <>
          {/* The transcript stays home unless asked for (long, and others' own words): beside Send, as Hand to AI's
              "Also mark as done". */}
          {hasTranscript && (
            <label className="mr-auto flex cursor-pointer items-center gap-2 text-label whitespace-nowrap text-ink-2">
              <input type="checkbox" checked={transcript} onChange={(e) => setTranscript(e.target.checked)} className="size-3.5 accent-[var(--accent)]" data-testid="handoff-transcript" />
              {t("knowledge-handoff.transcript")}
            </label>
          )}
          <Button tone="quiet" disabled={busy} onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="accent" icon="send" busy={busy} disabled={!target} onClick={() => void send()} testId="handoff-send">
            {t("knowledge-handoff.send")}
          </Button>
        </>
      }
    >
      <p className="mb-1.5 text-label font-medium text-ink-2">{t("knowledge-handoff.pick")}</p>
      <div role="radiogroup" aria-label={t("knowledge-handoff.pick")} className="max-h-[300px] divide-y divide-line overflow-y-auto rounded-card bg-surface shadow-card">
        {agents.map((s) => (
          <Target
            key={`${s.machine_id}/${s.session_id}`}
            on={!!target && target.machine_id === s.machine_id && target.session_id === s.session_id}
            onPick={() => setTarget(s)}
            title={sessionTitle(s)}
            sub={[machineWithHub(s.machine_id, machines), s.space_name].filter(Boolean).join(" › ")}
            icon={<span className="flex size-6 items-center justify-center"><StatusMark state={stateOf(s)} /></span>}
          />
        ))}
      </div>
      <textarea
        value={text}
        maxLength={TEXT_MAX}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing || e.keyCode === 229) return;
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            void send();
          }
        }}
        rows={2}
        placeholder={t("knowledge-handoff.ask_placeholder")}
        aria-label={t("knowledge-handoff.ask_label")}
        className="mt-3 block w-full resize-none rounded-control bg-field px-3 py-2 text-meta leading-relaxed text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)]"
      />
      <p className="mt-2 text-submeta leading-relaxed text-ink-3">{t("knowledge-handoff.gets")}</p>
      {error && <p role="alert" className="mt-2 text-label text-red">{error}</p>}
    </Dialog>
  );
}

/** The meeting's latest hand-off, at the top of its page: where it went, opening that Agent's conversation; or why it
 *  didn't, with Try again while there is an Agent to try. */
export function RecordingHandoffRow({ handoff, onRetry }: { handoff: MeetingHandoff; onRetry?: () => void }) {
  const failed = handoff.state === "failed";
  const name = agentName(handoff);
  return (
    <section data-testid="handoff-row">
      <Card>
        <div className="flex items-center gap-2.5 px-4 py-2.5 text-meta">
          <Icon name={failed ? "warning" : "send"} size={14} className={failed ? "shrink-0 text-red" : "shrink-0 text-ink-3"} />
          <span className="min-w-0 flex-1">
            <span className="block truncate font-medium text-ink">
              {failed ? t("knowledge-handoff.row_failed", { name }) : t("knowledge-handoff.row_sent", { name })}
            </span>
            {failed && <span className="block truncate text-label text-ink-3">{handoffProblem(handoff.problem)}</span>}
          </span>
          {failed ? (
            onRetry && <Button tone="secondary" onClick={onRetry}>{t("knowledge-handoff.retry")}</Button>
          ) : (
            <Button tone="secondary" onClick={() => openFromPage({ name: "session", mid: handoff.machine_id, sid: handoff.session_id })}>
              {t("knowledge-handoff.open")}
            </Button>
          )}
        </div>
      </Card>
    </section>
  );
}

/** The page's hand-off state: the Agents on offer, the latest try (one just made shows before the detail is read
 *  again), and the dialog. */
export function useRecordingHandoff(meta: Record<string, unknown> | null | undefined) {
  const sessions = useStore((s) => s.sessions);
  const machines = useStore((s) => s.machines);
  const hasAgents = useMemo(() => meetingAgents(sessions, machines).length > 0, [sessions, machines]);
  const [made, setMade] = useState<MeetingHandoff | null>(null);
  const kept = lastHandoff(meta);
  // the newer of the one made here and the detail's
  const latest = made && (!kept || Date.parse(made.at) >= Date.parse(kept.at)) ? made : kept;
  return { hasAgents, latest, setMade };
}
