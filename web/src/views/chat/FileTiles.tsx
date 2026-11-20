// The files picked for a message, above the input (#8361; iOS FileAttachmentsBar): the tile AttachmentTray draws for a
// file that is not a picture, in a row that scrolls sideways on a phone, with how far it got (or its size once there, Waiting, Failed), a thin bar while it
// uploads, and the tray's ✕. A click on a failed one tries again. Nothing of them goes into the text.
import { removeFile, retryFile, type ComposerFile } from "../../lib/composer-files";
import { bytes } from "../../lib/format";
import { cx, EASE } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

function status(f: ComposerFile): string {
  switch (f.state) {
    case "waiting":
      return t("chat-composer.file_waiting");
    case "uploading":
      return f.size ? `${bytes(f.sent)} / ${bytes(f.size)}` : bytes(f.sent);
    case "ready":
      return bytes(f.size);
    case "failed":
      return t("chat-composer.file_failed");
  }
}

export function FileTiles({ draftKey, files }: { draftKey: string; files: ComposerFile[] }) {
  if (!files.length) return null;
  return (
    // a row that scrolls sideways on a phone, as on iOS; rows that wrap where there is room
    <div className="flex flex-wrap gap-1.5 overflow-x-auto scrollbar-none max-sm:flex-nowrap" data-testid="composer-files">
      {files.map((f) => {
        const failed = f.state === "failed";
        const busy = f.state === "waiting" || f.state === "uploading";
        const body = (
          <>
            <Icon name={failed ? "warning" : "read"} size={16} className={cx("shrink-0", failed ? "text-red" : "text-ink-3")} />
            <span className="flex min-w-0 flex-col">
              <span className="min-w-0 truncate text-label text-ink-2">{f.name}</span>
              <span className={cx("truncate text-submeta tabular-nums", failed ? "text-red" : "text-ink-3")}>{status(f)}</span>
            </span>
          </>
        );
        return (
          <span
            key={f.id}
            title={failed ? f.error || f.name : f.name}
            data-state={f.state}
            className="group/att relative flex h-14 w-56 max-w-full shrink-0 items-center gap-2 overflow-hidden rounded-[8px] bg-field px-3 pr-7 shadow-hairline"
            style={{ animation: `pop-in 200ms ${EASE} both` }}
          >
            {failed ? (
              <button type="button" onClick={() => retryFile(draftKey, f.id)} aria-label={t("chat-composer.file_retry", { name: f.name })} className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left">
                {body}
              </button>
            ) : body}
            {busy && (
              <span className="absolute inset-x-2 bottom-1 h-0.5 overflow-hidden rounded-full bg-line" aria-hidden>
                <span className="block h-full rounded-full bg-accent" style={{ width: `${f.size ? Math.min(100, (f.sent / f.size) * 100) : 0}%` }} />
              </span>
            )}
            {/* the tray's ✕ (AttachmentTray), in a box of its own. The tile clips its tap area (the row clips it at the
                tile's top anyway, and past the tile's side it would lengthen the row's sideways scroll), so it takes the
                ✕'s column instead: down to 44 px and left to where a failed file's retry ends */}
            <span className="absolute top-0.5 right-0.5 flex">
              <button
                type="button"
                aria-label={busy ? t("chat-composer.file_cancel", { name: f.name }) : t("ui-attachmenttray.remove", { name: f.name })}
                onClick={() => removeFile(draftKey, f.id)}
                data-remove
                className="touch-hit-corner flex size-4.5 items-center justify-center rounded-full bg-black/60 text-white opacity-0 [--hit-in-x:8px] [--hit-in-y:24px] transition-opacity group-hover/att:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
              >
                <Icon name="x" size={10} stroke={2.6} />
              </button>
            </span>
          </span>
        );
      })}
    </div>
  );
}
