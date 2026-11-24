// The files a finished Agent turn changed (cindy's TurnChangesCard, at the owner's request, 2026-11-12; docs/web/web.md
// "Changed files"): a bordered card at the end of the exchange with "Changed 3 files", the lines added and removed,
// Review, and the files, three of them, then "Show N more files". What it counts is lib/turn-changes.ts. Never in a
// Persona conversation, never while the turn runs. cindy's Undo and Reapply are not here: nothing in Persona applies
// or reverts a turn's patch.
import { memo, useState } from "react";
import { below } from "../../lib/files";
import { openPanel } from "../../lib/panel";
import { hubHas, useStore } from "../../lib/store";
import type { ChangedFile, TurnChangeSet } from "../../lib/turn-changes";
import { REVIEW_FEATURE } from "../../lib/types";
import { Button } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

/** Files listed before "Show N more files" (cindy's MAX_VISIBLE_FILES). */
export const VISIBLE_FILES = 3;

/** The card's height as drawn (Chrome, 2026-11-12), for an exchange's estimate before it is first drawn: its 4 px
 *  margins, border, the 64 px head, and the files' padding, rows and Show more row. */
export function turnChangesHeight(c: TurnChangeSet): number {
  const rows = Math.min(c.files.length, VISIBLE_FILES);
  return 8 + 2 + 64 + 1 + 16 + rows * 36 + (c.files.length > VISIBLE_FILES ? 32 : 0);
}

/** "Show N more files" / "Show less": a row of its own, a finger tall on a touch screen like the files above it. */
const LINK_ROW = "flex h-8 items-center gap-1 rounded-control px-2 text-[13px] whitespace-nowrap text-ink-2 transition-colors hover:bg-hover hover:text-ink [@media(pointer:coarse)]:h-11";

export const TurnChanges = memo(function TurnChanges({ changes, mid, sid, cwd }: { changes: TurnChangeSet; mid: string; sid: string; cwd?: string }) {
  // the session's review panel, where the hub has it (as the chat's menu and the Git panel decide)
  const reviews = useStore((s) => hubHas(s.features, REVIEW_FEATURE)) && Boolean(cwd);
  const [expanded, setExpanded] = useState(false);
  const { files } = changes;
  const visible = expanded ? files : files.slice(0, VISIBLE_FILES);
  const hidden = files.length - visible.length;
  // the branch's review: an Agent often commits its edits before its turn ends, and Uncommitted would then be empty
  const review = (path?: string) => cwd && openPanel({ kind: "review", mid, sid, cwd, scope: "branch", ...(path ? { at: { path, file: true } } : {}) });
  // a file in the session's folder opens the review at it (the file itself when the review does not list it); one
  // outside it, or without a review, the file itself
  const open = (f: ChangedFile) => (reviews && cwd && below(cwd, f.path) ? review(f.path) : openPanel({ kind: "file", mid, path: f.path, cwd, sid }));
  return (
    <section data-turn-changes={files.length} className="my-1 overflow-hidden rounded-xl border border-line bg-surface">
      <div className="flex min-h-16 items-center gap-3 px-4 py-3">
        <div className="flex size-9 shrink-0 items-center justify-center rounded-control bg-field text-ink-2">
          <Icon name="fileDiff" size={18} />
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[14px] font-medium text-ink">{t("chat-turnchanges.title", { count: files.length })}</div>
          {changes.exact && (
            <div className="mt-0.5 font-mono text-[12px] tabular-nums">
              <span className="text-diff-add-fg">+{changes.add}</span> <span className="text-diff-del-fg">-{changes.del}</span>
            </div>
          )}
        </div>
        {reviews && (
          <Button className="touch-hit" onClick={() => review()}>
            {t("chat-turnchanges.review")}
          </Button>
        )}
      </div>
      <div className="border-t border-line px-3 py-2">
        {visible.map((f) => (
          <button key={f.path} type="button" title={f.path} onClick={() => open(f)} className="flex h-9 w-full items-center gap-2 rounded-control px-2 text-left transition-colors hover:bg-hover [@media(pointer:coarse)]:h-11" data-changed-file={f.shown}>
            <Icon name="file" size={15} className="shrink-0 text-ink-2" />
            <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{f.shown}</span>
            {f.exact && (f.add > 0 || f.del > 0) && (
              <span className="shrink-0 font-mono text-[12px] whitespace-nowrap tabular-nums">
                {f.add > 0 && <span className="text-diff-add-fg">+{f.add}</span>}
                {f.add > 0 && f.del > 0 && " "}
                {f.del > 0 && <span className="text-diff-del-fg">-{f.del}</span>}
              </span>
            )}
          </button>
        ))}
        {!expanded && hidden > 0 && (
          <button type="button" onClick={() => setExpanded(true)} aria-expanded={false} className={LINK_ROW}>
            {t("chat-turnchanges.show_more", { count: hidden })}
            <Icon name="chevron" size={14} />
          </button>
        )}
        {expanded && files.length > VISIBLE_FILES && (
          <button type="button" onClick={() => setExpanded(false)} aria-expanded className={LINK_ROW}>
            {t("chat-turnchanges.show_less")}
            <Icon name="chevron" size={14} style={{ transform: "rotate(180deg)" }} />
          </button>
        )}
      </div>
    </section>
  );
});
