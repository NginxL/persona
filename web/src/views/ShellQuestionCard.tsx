// The question a New Agent start's terminal tab stopped on, answered from here (#10365, lib/start-continue.ts): the
// question as the computer shows it, one button per answer it offers, until when the tab waits, and its last lines
// folded. An Agents-surface card in plain words, laid out as the design system's notice (ErrorNotice) with the
// terminal's mark instead of the red one. ShellQuestionCard takes how to send an answer; SessionShellQuestion is it
// for a session's own start, with the refused messages sent again after, TaskShellQuestion for a planned task's failed
// start (the hub starts the task again in that tab); the default export places them in the chat, on a New Agent
// launch's page and on a task's failed row, panel and to-do page, which load this file only for such a start.
import { useState } from "react";
import { t } from "../i18n";
import { errorText } from "../lib/api";
import { clock } from "../lib/format";
import { answerTaskStart, continueStart, openQuestion, resendRefused, shellQuestion, SHELL_WAITING, START_CONTINUE_FEATURE, TODO_START_ANSWER_FEATURE, type ShellQuestion } from "../lib/start-continue";
import { hubHas, useStore } from "../lib/store";
import type { PlanTask } from "../lib/feed";
import type { AgentLaunch } from "../lib/new-agent";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";
import { Button, useNow } from "../ui/atoms";
import { ErrorDetailsText, ErrorNotice } from "../ui/ErrorNotice";
import { Icon } from "../ui/icons";

/** The words on an answer's button: what it does where the question says, with the key it presses. */
export function choiceLabel(q: Pick<ShellQuestion, "kind" | "default">, id: string): string {
  switch (id) {
    case "enter":
      return q.default === "y" ? t("shellquestioncard.enter_yes") : q.default === "n" ? t("shellquestioncard.enter_no") : t("shellquestioncard.enter");
    case "cancel":
      return t("shellquestioncard.cancel");
    case "yes":
      return t("shellquestioncard.yes");
    case "no":
      return t("shellquestioncard.no");
  }
  const key = { key: id };
  switch (`${q.kind}:${id}`) {
    case "update:y":
      return t("shellquestioncard.update_yes", key);
    case "update:n":
      return t("shellquestioncard.update_no", key);
    case "compinit:y":
      return t("shellquestioncard.compinit_yes", key);
    case "compinit:n":
      return t("shellquestioncard.compinit_no", key);
    case "zsh_newuser:q":
      return t("shellquestioncard.newuser_quit", key);
    case "zsh_newuser:0":
      return t("shellquestioncard.newuser_empty", key);
    case "p10k_wizard:q":
      return t("shellquestioncard.p10k_quit", key);
    case "rvm_trust:n":
      return t("shellquestioncard.rvm_no", key);
    case "rvm_trust:c":
      return t("shellquestioncard.rvm_cancel", key);
  }
  if (id === "y") return t("shellquestioncard.key_yes", key);
  if (id === "n") return t("shellquestioncard.key_no", key);
  return t("shellquestioncard.key", key);
}

/** What helps, for the kind of question. */
function hint(q: ShellQuestion): string {
  if (q.secret) return t("shellquestioncard.hint_secret");
  if (q.kind === "ssh_host_key") return t("shellquestioncard.hint_ssh");
  return t("shellquestioncard.hint");
}

export function ShellQuestionCard({ question, answer, onAnswered, className }: {
  question: ShellQuestion;
  /** Sends one of question.choices; throws with the refusal when it was not taken. */
  answer: (choice: string) => Promise<unknown>;
  /** The answer was taken: the start goes on in that tab. */
  onAnswered?: (choice: string) => void;
  className?: string;
}) {
  const now = useNow(30_000);
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const q = openQuestion(question, now);
  if (!q) return null;
  const pick = async (choice: string) => {
    setBusy(choice);
    setError(undefined);
    try {
      await answer(choice);
      onAnswered?.(choice);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(undefined);
    }
  };
  const until = clock(q.keptUntil);
  return (
    <ErrorNotice
      data-testid="shell-question"
      aria-label={t("shellquestioncard.label")}
      className={className}
      icon={<span className="text-ink-2"><Icon name="terminal" size={16} /></span>}
      message={t("shellquestioncard.asking", { line: q.line })}
      description={
        <>
          {hint(q)}
          {until && <> {t("shellquestioncard.kept_until", { time: until })}</>}
          {error && <span role="alert" className="mt-1 block text-red">{error}</span>}
        </>
      }
      actions={q.choices.map((c) => (
        <Button key={c} size="sm" className="touch-hit" busy={busy === c} disabled={busy !== undefined && busy !== c} testId={`shell-choice-${c}`} onClick={() => void pick(c)}>
          {choiceLabel(q, c)}
        </Button>
      ))}
      details={q.screen ? <ErrorDetailsText text={q.screen} /> : undefined}
    />
  );
}

/** The card for session sid's own start on computer mid, from its start_error_args (or a refused message's details):
 *  an answer starts it again in that tab, and the messages that start refused are sent again. Nothing on a hub that
 *  cannot take the answer. */
export function SessionShellQuestion({ mid, sid, args, onAnswered, className }: {
  mid: string;
  sid: string;
  args: Record<string, unknown> | undefined;
  onAnswered?: () => void;
  className?: string;
}) {
  const can = useStore((s) => hubHas(s.features, START_CONTINUE_FEATURE));
  const question = shellQuestion(args);
  if (!can || !question) return null;
  return (
    <ShellQuestionCard
      key={`${question.line}\n${question.keptUntil ?? ""}`}
      question={question}
      className={className}
      answer={(choice) => continueStart(mid, sid, choice)}
      onAnswered={() => {
        void resendRefused(mid, sid);
        onAnswered?.();
      }}
    />
  );
}

/** The card for planned task `todo`'s failed start (`task`, its plan task: error_code shell_waiting, the question in
 *  error_args): an answer starts the task again in that tab, the to-do reopened. Nothing on a hub that cannot take it,
 *  where the task's Retry stays its way on. */
export function TaskShellQuestion({ todo, task, className }: { todo: Todo; task: PlanTask; className?: string }) {
  const can = useStore((s) => hubHas(s.features, TODO_START_ANSWER_FEATURE));
  const question = task.error_code === SHELL_WAITING ? shellQuestion(task.error_args) : undefined;
  if (!can || !question) return null;
  return (
    <ShellQuestionCard
      key={`${question.line}\n${question.keptUntil ?? ""}`}
      question={question}
      className={className}
      answer={(choice) => answerTaskStart(todo, choice)}
    />
  );
}

/** The card where a start stopped on its question (lazily loaded, the default export): a planned task's (its failed
 *  row, panel and page give `todo` and its plan task), a session's own (the chat's start error), or a New Agent
 *  launch's whose first message was refused with shell_waiting. */
export default function ShellQuestion({ session, launch, todo, task, className }: {
  session?: Session;
  launch?: AgentLaunch;
  todo?: Todo;
  task?: PlanTask;
  className?: string;
}) {
  return todo && task ? <TaskShellQuestion todo={todo} task={task} className={className} /> : <StartShellQuestion session={session} launch={launch} />;
}

/** A session's own start or a New Agent launch's, the started session's row saying the question as it stands now (a
 *  renewed tab), the refusal's own body until it does. */
function StartShellQuestion({ session, launch }: { session?: Session; launch?: AgentLaunch }) {
  const mid = session?.machine_id ?? launch?.machineId ?? "";
  const sid = session?.session_id ?? launch?.session?.session_id ?? "";
  const row = useStore((s) => (launch ? s.sessions[mid]?.find((x) => x.session_id === sid) : undefined));
  if (!sid) return null;
  const args = session ? session.start_error_args : row?.start_error_code === "shell_waiting" ? row.start_error_args : launch?.shellWaiting;
  return <SessionShellQuestion className={launch ? "w-full" : undefined} mid={mid} sid={sid} args={args} />;
}
