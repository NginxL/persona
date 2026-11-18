// What the main conversation still needs, above its composer (docs/assistant/onboarding.md 4.4): the first missing step as a card
// (a title, one line why, one button), the "chat only" note, a refused start or send as a card with the original text under
// "Error details" (4.6), and the help those buttons and the local example questions open in place (ReadinessHelp.tsx, a chunk of
// its own). "Add computer" and "Confirm computer" open the
// machines page's own flow (views/machines/AddComputer.tsx) in place where the hub has it (machines_add, machine_trust).
import { useEffect, useState } from "react";
import { canAddComputer, canConfirmComputer, pendingToConfirm } from "../../lib/add-computer";
import { mainConversationMachine } from "../../lib/feed-cache";
import { requestModelPicker } from "../../lib/model-switch";
import {
  actionLabel, blockingStep, knownAction, readinessChanged, refreshReadiness, retryWhileBlocked, runAction, sendLabel, stepActions, useReadiness, type CodedFailure,
  type HelpTopic, type Readiness, type ReadinessStep,
} from "../../lib/readiness";
import { navigate } from "../../lib/router";
import { getSession, getState, useStore } from "../../lib/store";
import { assistantPickerKey } from "../chat/ModelPill";
import type { PendingComputer } from "../machines/AddComputer";
import { lazyDialog, lazyOverlay } from "../LazyOverlay";
import { Button, copyText, cx } from "../../ui/atoms";
import { ErrorDetailsText, ErrorNotice } from "../../ui/ErrorNotice";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";

// the machines page's dialog loads on its first open, not with the main conversation
const AddComputer = lazyDialog(() => import("../machines/AddComputer").then((m) => m.AddComputer));

/** The help a card's button or a local example question opens (ReadinessHelp.tsx): its chunk loads once something that
 *  opens it is on screen (useReadinessGate, FailureCard), so the tap draws it at once; elsewhere on the first open. */
export const HelpDialog = lazyOverlay(() => import("./ReadinessHelp").then((m) => m.HelpDialog));
const preloadHelp = () => void HelpDialog.preload().catch(() => {});

// ── the gate ────────────────────────────────────────────

export interface ReadinessGate {
  readiness: Readiness | null;
  /** The step the card shows; null: nothing missing (or a hub without readiness). */
  step: ReadinessStep | null;
  /** The send button's stand-in while a step is missing: its words and what it opens. Nothing is queued (4.4).
   *  `onNudge`: a send was tried anyway (Enter): the card draws the eye to what is missing. */
  blocked?: { label: string; onClick?: () => void; onNudge?: () => void };
  /** How many times a send was tried while blocked: the card replays its nudge on each. */
  nudged: number;
  help: (topic: HelpTopic) => void;
  /** "Confirm computer": the confirm screen of the computer waiting to be confirmed (the machines page on an older hub). */
  confirm: () => void;
  /** "Try it" is running (the hub gives it up to PROBE_HUB_MS: the one-word check, then the self-check). */
  probing: boolean;
  /** The last "Try it" got no answer: why ("" none). It stands in for the try's result. */
  probeError: string;
  /** Another model was picked for the main chat since its model step said it can't run that one: "Try it" leads (stepActions). */
  picked?: boolean;
  /** "Choose another model": opens the main chat's own model panel; false when it has none (runAction then opens Settings › Models). */
  onPickModel?: () => boolean;
  /** The help dialog and the add / confirm computer flow, to render once beside the card. */
  dialog: React.ReactNode;
}

/** The main conversation's gate: the readiness, its card's step, the send button's stand-in and the help dialog. */
export function useReadinessGate(on = true): ReadinessGate {
  const { readiness, probing, probeError } = useReadiness(on);
  const [topic, setTopic] = useState<HelpTopic | null>(null);
  // the add computer flow: a new one ({}), or the one waiting to be confirmed
  const [computer, setComputer] = useState<{ confirm?: PendingComputer } | null>(null);
  const [nudged, setNudged] = useState(0);
  const canAdd = useStore((s) => canAddComputer(s.features));
  const canConfirm = useStore((s) => canConfirmComputer(s.features));
  const r = on ? readiness : null;
  const step = blockingStep(r);
  const machine = r?.machine?.name ?? "";
  // "Add computer" is the in-app flow where a signed-in device can add one; the how-to pointing at /admin on an older hub
  const help = (h: HelpTopic) => (h === "add_computer" && canAdd ? setComputer({}) : setTopic(h));
  // the machine list is read on the click, not subscribed to: every chat and the Persona page hold this gate, and a
  // machines push or refresh replaces the whole list
  const confirm = () => {
    const m = canConfirm ? pendingToConfirm(getState().machines, step?.machine) : undefined;
    if (m) setComputer({ confirm: { id: m.id, name: m.name } });
    else navigate("/machines");
  };
  // the main chat's own model panel, its pill's (read on the click, as the machine list is)
  const onPickModel = () => {
    const a = getState().assistant;
    return Boolean(a?.main) && requestModelPicker(assistantPickerKey(mainConversationMachine(a)!, a!.main!));
  };
  const act = (id: string) => runAction(id, { launcher: step?.launcher, onHelp: help, onConfirm: confirm, onPickModel });
  // A model the main chat can't run: a pick of another one (here, in its model error's Fix it, in its pill) is what "Try it"
  // checks next. The pick it had when the step came up, or when its last try ended, is the one already tried.
  const route = step?.code === "model_route";
  const pick = useStore((s) => (route && s.assistant?.main ? JSON.stringify(getSession(mainConversationMachine(s.assistant)!, s.assistant.main)?.model_pick) : ""));
  const [[wasRoute, wasProbing, tried], setTried] = useState([route, probing, pick] as const);
  if (wasRoute !== route || wasProbing !== probing) setTried([route, probing, probing && wasRoute === route ? tried : pick]);
  const picked = route && pick !== tried;
  const [main] = step ? stepActions(step, picked) : [];
  // a step can be done without this page hearing of it: ask again while it holds the button, and at once when the user tries
  const isBlocked = Boolean(step);
  useEffect(() => (isBlocked ? retryWhileBlocked() : undefined), [isBlocked]);
  // something that opens the help is on screen: a step's card, the chat-only note, a local example question
  const helpNear = isBlocked || r?.mode === "chat_only" || Boolean(r?.suggestions?.some((x) => x.local));
  useEffect(() => (helpNear ? preloadHelp() : undefined), [helpNear]);
  const blocked = step
    ? {
        label: sendLabel(step, main),
        onClick: () => {
          void refreshReadiness({ force: true });
          if (main) act(main);
        },
        onNudge: () => {
          void refreshReadiness({ force: true });
          setNudged((n) => n + 1);
        },
      }
    : undefined;
  return {
    readiness: r,
    step,
    blocked,
    nudged,
    help,
    confirm,
    probing: on && probing,
    probeError: on ? probeError : "",
    picked,
    onPickModel,
    dialog: (
      <>
        {topic && <HelpDialog topic={topic} machine={machine} step={step} onClose={() => setTopic(null)} onAction={act} />}
        {on && (
          <AddComputer
            open={!!computer}
            confirm={computer?.confirm}
            onClose={() => {
              setComputer(null);
              void readinessChanged(); // added, confirmed or removed: the card follows now
            }}
          />
        )}
      </>
    ),
  };
}

// ── the card ────────────────────────────────────────────

function stepIcon(step: ReadinessStep): IconName {
  if (step.id === "computer") return "machine";
  if (step.id === "claude") return "terminal";
  return "key";
}

/** The first missing step: its title and line from the hub, its button and the other ways (alt_actions). */
export function ReadinessCard({ gate }: { gate: ReadinessGate }) {
  const step = gate.step;
  if (!step) return null;
  const act = (id: string) => runAction(id, { launcher: step.launcher, onHelp: gate.help, onConfirm: gate.confirm, onPickModel: gate.onPickModel });
  const [main, alts] = stepActions(step, gate.picked);
  const problem = step.state === "problem";
  const probe = gate.readiness?.probe;
  return (
    <div
      // a new key replays the nudge: Enter while blocked lights the card up (and gives it a small shake)
      key={gate.nudged}
      role="status"
      data-testid="readiness-card"
      data-step={step.id}
      data-code={step.code}
      data-nudged={gate.nudged || undefined}
      className="flex items-start gap-3 rounded-card bg-surface px-3.5 py-3 shadow-card"
      style={{ animation: gate.nudged ? "ready-nudge 700ms ease-out both" : "fade-up 220ms ease-out both" }}
    >
      <span className={cx("flex size-8 shrink-0 items-center justify-center rounded-control", problem ? "bg-orange-tint text-orange" : "bg-accent-tint text-accent-ink")}>
        <Icon name={stepIcon(step)} size={15} stroke={2} />
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-[13.5px] font-semibold text-ink">{step.title || t("assistant-readiness.one_more_step")}</span>
        {step.detail && <span className="text-[12.5px] leading-relaxed text-ink-2">{step.detail}</span>}
        {gate.probeError && !gate.probing ? (
          <span data-testid="readiness-probe" data-failed="" className="text-[12.5px] leading-relaxed text-red">
            {t("assistant-readiness.probe_failed", { error: gate.probeError })}
          </span>
        ) : (
          probe &&
          !gate.probing && (
            <span data-testid="readiness-probe" className={cx("text-[12.5px] leading-relaxed", probe.ok ? "text-ink-2" : "text-red")}>
              {probe.ok ? t("assistant-readiness.probe_ok", { answer: probe.answer ?? "" }) : t("assistant-readiness.probe_failed", { error: probe.error ?? "" })}
            </span>
          )
        )}
        {(main || alts.length > 0) && (
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {main && (
              <Button tone="primary" busy={main === "test_assistant" && gate.probing} onClick={() => act(main)}>
                {actionLabel(main)}
              </Button>
            )}
            {alts.map((id) => (
              <Button key={id} tone="quiet" busy={id === "test_assistant" && gate.probing} onClick={() => act(id)}>
                {actionLabel(id)}
              </Button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

/** "Chat only" (P1, 4.4): the hub answers by itself without a computer; sending stays as it is. */
export function ChatOnlyNote({ gate }: { gate: ReadinessGate }) {
  if (gate.readiness?.mode !== "chat_only") return null;
  return (
    <div role="status" className="flex min-h-9 items-center gap-2 rounded-[12px] bg-accent-tint px-3 py-1.5 text-[12.5px] text-ink-2">
      <Icon name="chat" size={13} className="shrink-0 text-accent-ink" />
      <span className="min-w-0 flex-1">{t("assistant-readiness.chat_only")}</span>
      <Button tone="quiet" onClick={() => gate.help("add_computer")}>
        {actionLabel("add_computer")}
      </Button>
    </div>
  );
}

// ── a refused start or send ─────────────────────────────

/** A start or a send that did not go: the hub's words, a button for its code, the original under "Error details" (4.6).
 *  A send's own Retry is its failed bubble's, in the chat above: the card has none, so there is one way to send again. */
export function FailureCard({ failure, onClose, onHelp, onConfirm }: { failure: CodedFailure; onClose: () => void; onHelp: (topic: HelpTopic) => void; onConfirm?: () => void }) {
  const action = failure.action && knownAction(failure.action) ? failure.action : undefined;
  useEffect(() => (action ? preloadHelp() : undefined), [action]);
  const raw = [failure.detail, failure.requestId && t("assistant-readiness.request_id", { id: failure.requestId })].filter(Boolean).join("\n");
  // the same error notice as a model error over the composer (#5313): the hub's sentence, its buttons, the original
  // folded under Error details, and ✕
  return (
    <ErrorNotice
      role="alert"
      data-testid="readiness-failure"
      data-code={failure.code}
      message={failure.error}
      onDismiss={onClose}
      actions={
        action && (
          <Button tone="primary" size="sm" className="touch-hit" onClick={() => runAction(action, { launcher: failure.launcher, onHelp, onConfirm })}>
            {actionLabel(action)}
          </Button>
        )
      }
      details={
        raw ? (
          <div className="flex flex-col gap-1.5">
            <ErrorDetailsText text={raw} />
            <button type="button" onClick={() => void copyText(raw)} className="touch-hit flex w-fit items-center gap-1 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline">
              <Icon name="copy" size={12} />
              {t("assistant-readiness.copy_details")}
            </button>
          </div>
        ) : undefined
      }
    />
  );
}
