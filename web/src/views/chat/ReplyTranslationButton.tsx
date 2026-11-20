import type { ReplyTranslation } from "../../lib/reply-translation";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export function ReplyTranslationButton({ translation, touch }: { translation: ReplyTranslation; touch?: boolean }) {
  return (
    <button
      type="button"
      aria-label={translation.label}
      title={translation.label}
      aria-pressed={translation.shown}
      aria-busy={translation.pending}
      disabled={translation.pending}
      onClick={translation.toggle}
      className={cx("flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink disabled:opacity-50", touch && "touch-hit", translation.shown && "text-accent")}
    >
      <Icon name="translate" size={14} />
    </button>
  );
}
