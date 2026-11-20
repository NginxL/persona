import { cx, EASE } from "./atoms";
import { Icon } from "./icons";
import { t } from "../i18n";

export function AttachmentTray({ attachments, onRemove, onOpen, openLabel }: {
  attachments: { id: string; name: string; url?: string }[];
  onRemove?: (id: string) => void;
  /** Click a picture to open it (the composer marks it up before sending). */
  onOpen?: (id: string) => void;
  /** A read-only opener's spoken label; without it, opening retains the composer's Edit wording. */
  openLabel?: (name: string) => string;
}) {
  if (!attachments.length) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {attachments.map((a) => (
        <span key={a.id} title={a.name} className={cx("group/att relative rounded-[8px] bg-field shadow-hairline", a.url ? "size-14" : "flex h-14 max-w-full items-center gap-2 px-3 pr-7")} style={{ animation: `pop-in 200ms ${EASE} both` }}>
          {a.url ? (
            onOpen ? (
              <button type="button" aria-label={openLabel?.(a.name) ?? t("ui-attachmenttray.edit", { name: a.name })} title={openLabel?.(a.name) ?? t("ui-attachmenttray.edit_hint")} onClick={() => onOpen(a.id)} className="block size-full cursor-pointer overflow-hidden rounded-[8px]">
                <img src={a.url} alt={a.name} className="size-full object-cover" />
              </button>
            ) : (
              <img src={a.url} alt={a.name} className="size-full rounded-[8px] object-cover" />
            )
          ) : <><Icon name="read" size={16} className="shrink-0 text-ink-3" /><span className="min-w-0 truncate text-[12px] text-ink-2">{a.name}</span></>}
          {/* in a box of its own: touch-hit-corner makes the button relative; the tile is not overflow-hidden, so its tap
              area reaches out past the corner. Only a little into a picture, whose middle opens it; down a file's ✕ column
              (pr-7) */}
          {onRemove && <span className="absolute top-0.5 right-0.5 flex">
            <button type="button" aria-label={t("ui-attachmenttray.remove", { name: a.name })} onClick={() => onRemove(a.id)} className={cx("touch-hit-corner flex size-4.5 items-center justify-center rounded-full bg-black/60 text-white opacity-0 transition-opacity group-hover/att:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-100", !a.url && "[--hit-in-x:8px] [--hit-in-y:24px]")}>
              <Icon name="x" size={10} stroke={2.6} />
            </button>
          </span>}
        </span>
      ))}
    </div>
  );
}
