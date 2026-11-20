import { useLayoutEffect, useRef } from "react";
import { t } from "../../i18n";
import { useCloseOnBack } from "../../lib/back-close";
import { enterSends, hasFinePointer, NARROW, useMedia } from "../../lib/viewport";
import { useComposition } from "../../lib/ime";
import { EASE } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useModalFocus } from "../../ui/overlay";

type ExpandedProps = {
  value: string;
  placeholder: string;
  selection: [number, number];
  onChange: (v: string) => void;
  onCollapse: (sel: [number, number]) => void;
  canSend: boolean;
  blocked: boolean;
  onSend: (sel: [number, number], repeat?: boolean) => void;
};

type SuggestionProps = { text: string; onFill: () => void; onSend?: () => void };

/** Both conditional composer overlays share one chunk and one first-screen loader. */
export function ComposerOverlay(props: ExpandedProps | SuggestionProps) {
  return "value" in props ? <ExpandedComposer {...props} /> : <SuggestionPill {...props} />;
}

/** The draft full screen on a phone (#5688): collapse at the top right, the text filling the visible height, send at the
 *  bottom right. It edits the box's own draft; closing hands back the caret so the box picks up where this left off. */
function ExpandedComposer({ value, placeholder, selection, onChange, onCollapse, canSend, blocked, onSend }: ExpandedProps) {
  const panel = useRef<HTMLDivElement>(null);
  const field = useRef<HTMLTextAreaElement>(null);
  const start = useRef(selection);
  const ime = useComposition();
  useModalFocus(panel);
  useLayoutEffect(() => {
    const el = field.current;
    if (!el) return;
    el.focus({ preventScroll: true });
    el.setSelectionRange(start.current[0], start.current[1]);
  }, []);
  const sel = (): [number, number] => [field.current?.selectionStart ?? value.length, field.current?.selectionEnd ?? value.length];
  // Back collapses it, as the collapse button does, and stays in the chat
  useCloseOnBack(true, () => onCollapse(sel()), useMedia(NARROW));
  return (
    <div
      ref={panel}
      role="dialog"
      aria-modal="true"
      aria-label={t("chat-composer.message")}
      tabIndex={-1}
      data-testid="composer-expanded"
      className="fixed inset-x-0 z-50 flex flex-col bg-surface outline-none"
      style={{ top: "var(--kb-top)", height: "var(--kb-h)", animation: `fade-in 160ms ease-out both` }}
      onKeyDown={(e) => {
        if (e.key === "Escape" && !e.nativeEvent.isComposing) {
          e.preventDefault();
          e.stopPropagation();
          onCollapse(sel());
        }
      }}
    >
      <div className="flex justify-end px-2 pt-[max(env(safe-area-inset-top),8px)]">
        <button
          type="button"
          data-testid="composer-collapse"
          aria-label={t("chat-composer.collapse")}
          title={t("chat-composer.collapse")}
          onClick={() => onCollapse(sel())}
          className="touch-hit flex size-9 items-center justify-center rounded-[10px] text-ink-2 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="collapse" size={18} stroke={2} />
        </button>
      </div>
      <textarea
        ref={field}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        {...ime.handlers}
        onBlur={ime.onBlur}
        onKeyDown={(e) => {
          if (ime.busy(e.nativeEvent)) return;
          // The full editor always keeps plain Enter as a newline; an explicit Mod+Enter sends on any pointer.
          if (!enterSends(e, false)) return;
          e.preventDefault();
          if (canSend || blocked) onSend(sel(), e.repeat);
        }}
        placeholder={placeholder}
        aria-label={t("chat-composer.message")}
        className="min-h-0 w-full flex-1 resize-none bg-transparent px-5 py-1 text-[16px] leading-[24px] text-ink outline-none [overflow-wrap:anywhere] placeholder:text-ink-3"
      />
      <div className="flex justify-end px-3 pt-2 pb-[max(env(safe-area-inset-bottom),12px)]">
        <button
          type="button"
          data-testid="composer-expanded-send"
          aria-label={t("chat-composer.send")}
          disabled={!canSend}
          onClick={() => onSend(sel())}
          className="touch-hit flex size-9 shrink-0 items-center justify-center rounded-full transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.94]"
          style={{ background: canSend ? "var(--ink)" : "var(--line-strong)", color: canSend ? "var(--surface)" : "var(--ink-2)" }}
        >
          <Icon name="up" size={18} stroke={2.4} />
        </button>
      </div>
    </div>
  );
}

/** The suggested next message over the box (iOS SuggestionPill): the row fills it in, the arrow sends it as it is; a
 *  risky one (no onSend) has no arrow (#4644). */
function SuggestionPill({ text, onFill, onSend }: SuggestionProps) {
  return (
    <div data-testid="composer-suggestion" className="mb-2 flex items-center gap-1 rounded-full bg-surface p-1 pl-1 shadow-card" style={{ animation: `fade-up 200ms ${EASE} both` }}>
      <button
        type="button"
        onMouseDown={(e) => e.preventDefault()}
        onClick={onFill}
        aria-label={t("chat-composer.suggestion_label", { suggestion: text })}
        title={hasFinePointer() ? t("chat-composer.suggestion_use_hint") : undefined}
        className="touch-hit flex min-w-0 flex-1 items-center gap-2 rounded-full py-1 pr-1 pl-2.5 text-left transition-colors hover:bg-hover"
      >
        <Icon name="sparkles" size={13} stroke={2} className="shrink-0 text-accent" />
        <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-2">{text}</span>
        <span className="shrink-0 whitespace-nowrap text-[12px] font-medium text-accent">{t("chat-composer.suggestion_use")}</span>
      </button>
      {onSend && (
        <button
          type="button"
          onMouseDown={(e) => e.preventDefault()}
          onClick={onSend}
          aria-label={t("chat-composer.suggestion_send")}
          title={t("chat-composer.suggestion_send")}
          className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-full text-ink-3 transition-[background-color,color,transform] duration-150 hover:bg-hover hover:text-ink active:scale-[0.94]"
        >
          <Icon name="send" size={14} stroke={2} />
        </button>
      )}
    </div>
  );
}
