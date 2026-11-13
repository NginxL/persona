// A question's option, as the question card and "Change answer" (Delegated.tsx) draw it.
import { optionDisplay } from "../../lib/tool-detail";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { RecommendedTag } from "../../ui/RecommendedTag";

/** A digit the card's own keys pick (lib/card-keys.ts), drawn on an option — but only while the card holds keyboard
 *  focus, so a card at rest looks as it always did. Same look as a terminal dialog's key badge. */
export function DigitHint({ n }: { n: number }) {
  return <span aria-hidden data-key-hint className="mt-[2px] flex h-4.5 w-4.5 shrink-0 items-center justify-center rounded-[5px] bg-field font-mono text-[10.5px] text-ink-2 shadow-hairline">{n}</span>;
}

export function Choice({ on, multi, label, desc, pick, hint, disabled = false, onClick }: { on: boolean; multi?: boolean; label: string; desc?: string; /** The assistant recommends it (its `delegate.recommend`, #544). */ pick?: boolean; /** The digit that picks this option, while the card has keyboard focus. */ hint?: number; disabled?: boolean; onClick: () => void }) {
  const shown = optionDisplay(label);
  return (
    <button type="button" aria-pressed={on} disabled={disabled} onClick={onClick} className="touch-hit -mx-1.5 flex items-start gap-2 rounded-control px-1.5 py-1.5 text-left transition-colors duration-100 enabled:hover:bg-hover disabled:opacity-40">
      {hint !== undefined && <DigitHint n={hint} />}
      <span
        className={cx(
          "mt-[2px] flex size-4 shrink-0 items-center justify-center transition-colors duration-200",
          multi ? "rounded-[5px]" : "rounded-full",
          on ? "bg-ink text-canvas" : "text-transparent shadow-[inset_0_0_0_1.5px_var(--line-strong)]",
        )}
      >
        {multi ? <Icon name="check" size={11} stroke={3} /> : <span className="size-1.5 rounded-full bg-canvas transition-transform duration-200" style={{ transform: on ? "scale(1)" : "scale(0)" }} />}
      </span>
      <span className="min-w-0">
        <span className={cx("block text-[13px] transition-colors", on ? "text-ink" : "text-ink-2")}>
          {shown.text}
          {shown.recommended && <RecommendedTag />}
          {pick && <RecommendedTag assistant />}
        </span>
        {desc && <span className="block text-[12px] leading-snug text-ink-3">{desc}</span>}
      </span>
    </button>
  );
}
