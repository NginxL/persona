// The steps of a short process, one per line, with the one under way: done ones ticked in green, the current one turning
// (crossed in red when it stopped there), the rest numbered and quiet. The alert sheet's Redeploy and a switch to
// Persona Link (views/machines/AlertDeploy.tsx, LinkSwitch.tsx) draw their progress with it.
import { cx, Spinner } from "./atoms";
import { Icon } from "./icons";

export type StepsOutcome = "running" | "done" | "failed";

export function Steps({ steps, step, outcome, label, testId }: {
  steps: readonly { key: string; label: string }[];
  /** the step under way (or reached) */
  step: number;
  outcome: StepsOutcome;
  /** what the list is, for assistive technology */
  label: string;
  testId?: string;
}) {
  return (
    <ol className="space-y-1.5" data-testid={testId} aria-label={label}>
      {steps.map((s, i) => {
        const done = i < step || (outcome === "done" && i === step);
        const current = i === step && outcome !== "done";
        const failed = current && outcome === "failed";
        return (
          <li key={s.key} className="flex items-center gap-2 text-meta" data-step={s.key} data-state={failed ? "failed" : done ? "done" : current ? "current" : "todo"} aria-current={current ? "step" : undefined}>
            <span className={cx("flex size-5 shrink-0 items-center justify-center rounded-full", failed ? "bg-red-tint text-red" : done ? "bg-green-tint text-green" : current ? "bg-field text-ink" : "bg-field text-ink-3")}>
              {failed ? <Icon name="x" size={11} /> : done ? <Icon name="check" size={11} /> : current ? <Spinner size={10} /> : <span className="text-submeta tabular-nums">{i + 1}</span>}
            </span>
            <span className={cx(current || done ? "text-ink" : "text-ink-3", failed && "text-red")}>{s.label}</span>
          </li>
        );
      })}
    </ol>
  );
}
