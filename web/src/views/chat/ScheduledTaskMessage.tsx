import { useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { sourceLabel } from "../../lib/message-source";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useChatContext } from "./chat-context";
import { useExpanded } from "./expanded";
import { useFullText } from "./FullText";
import { useReveal } from "./search-reveal";
import type { SourceProps } from "./SourceMessage";

/** A Codex-style automation input: two lines that cannot scroll, with the rest behind a disclosure. */
export default function ScheduledTaskMessage({ msg, mid, sid }: SourceProps) {
  const { scope } = useChatContext();
  const [open, setOpen] = useExpanded(`${scope}source:${msg.id}`);
  const item = msg.rawInput?.find((part) => part.id === msg.id);
  const full = useFullText(item, mid, sid);
  const text = item ? full.text : msg.text;
  const preview = useRef<HTMLDivElement>(null);
  const [overflow, setOverflow] = useState<boolean>();
  useLayoutEffect(() => {
    const el = preview.current;
    if (!el || open) return;
    const measure = () => {
      if (el.clientHeight > 0) setOverflow(el.scrollHeight > el.clientHeight + 1);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [text, open]);
  const long = open || !!full.more || (overflow ?? (text.length > 90 || text.split("\n").length > 2));
  useReveal(scope, [msg.id], () => {
    if (open) return;
    setOpen(true);
    return () => setOpen(false);
  });
  return (
    <div className="ml-auto flex w-fit max-w-[min(100%,488px)] min-w-0 flex-col items-end gap-1.5" data-message-source="scheduled_task" data-items={msg.id}>
      <span className="flex max-w-full items-center gap-1 text-right text-[length:var(--chat-secondary)] text-ink-3">
        <Icon name="clock" size={13} className="shrink-0" />
        {sourceLabel(msg.source!)}
      </span>
      <div className="min-w-0 rounded-card bg-neutral-bubble px-4 py-2.5 text-ink" data-disclosure-anchor>
        <div ref={preview} className={cx("text-[length:var(--chat-body)] leading-[1.6] whitespace-pre-wrap [overflow-wrap:anywhere]", !open && "line-clamp-2 overflow-clip")} data-scheduled-body data-source-folded={!open && long || undefined}>{text}</div>
        {open && full.more}
        {long && <button type="button" className="chat-source-toggle touch-hit mt-1.5 inline-flex items-center gap-1" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
          {t(open ? "chat-segmentview.collapse" : "chat-messages.show_more")}
          <Icon name="chevron" size={13} className={open ? "rotate-180" : undefined} />
        </button>}
      </div>
    </div>
  );
}
