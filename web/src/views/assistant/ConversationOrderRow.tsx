// The edit-mode row shared by the sidebar and All chats (#12017). Normal rows are untouched; while arranging, the
// content cannot navigate accidentally, a grip accepts HTML drags, and visible buttons provide the same move by keyboard/touch.
import { useState, type DragEvent, type ReactNode } from "react";
import { cx, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

const DRAG_TYPE = "application/x-persona-conversation-order";

export function ConversationOrderRow({ id, title, index, count, onMove, onStep, children }: {
  id: string; title: string; index: number; count: number;
  onMove: (from: string, to: string) => void;
  onStep: (id: string, by: -1 | 1) => void;
  children: ReactNode;
}) {
  const [dragging, setDragging] = useState(false);
  const [over, setOver] = useState(false);
  const start = (event: DragEvent<HTMLElement>) => {
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData(DRAG_TYPE, id);
    event.dataTransfer.setData("text/plain", id);
    setDragging(true);
  };
  return (
    <div
      data-order-row={id}
      onDragOver={(event) => {
        if (!event.dataTransfer.types.includes(DRAG_TYPE)) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = "move";
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(event) => {
        event.preventDefault();
        const from = event.dataTransfer.getData(DRAG_TYPE);
        if (from && from !== id) onMove(from, id);
        setOver(false);
        setDragging(false);
      }}
      className={cx("flex items-center gap-1 rounded-control", dragging && "opacity-50", over && !dragging && "shadow-[inset_0_2px_0_var(--accent)]")}
    >
      <span
        draggable
        onDragStart={start}
        onDragEnd={() => { setDragging(false); setOver(false); }}
        title={t("conversation-order.drag_conversation")}
        aria-hidden
        className="flex size-7 shrink-0 cursor-grab items-center justify-center text-ink-3 active:cursor-grabbing"
      >
        <Icon name="menu" size={14} />
      </span>
      <div inert className="conversation-order-content min-w-0 flex-1">{children}</div>
      <div className="flex shrink-0 items-center">
        <IconButton icon="up" label={t("conversation-order.move_up", { title })} disabled={index === 0} onClick={() => onStep(id, -1)} size={26} iconSize={13} className="touch-hit" />
        <IconButton icon="arrowDown" label={t("conversation-order.move_down", { title })} disabled={index === count - 1} onClick={() => onStep(id, 1)} size={26} iconSize={13} className="touch-hit" />
      </div>
    </div>
  );
}
