// The worktree a closed or deleted agent worked in (#6115). Only the intent-loaded CloseAgentDialog and
// SessionMenuDialogs import this module; do not re-export it from the first-screen SessionDialogs module.
import { useEffect, useState } from "react";
import { errorText } from "../../lib/api";
import { shortPath } from "../../lib/format";
import type { Session } from "../../lib/types";
import { removeAgentWorktree, worktreeLoss, worktreeStatus, type WorktreeStatus } from "../../lib/worktree";
import { t } from "../../i18n";
import { cx, Switch, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

/** What to do with it: keep it (the default), or remove it, its branch too if asked; `confirmed` that what it loses goes. */
export interface Cleanup {
  remove: boolean;
  deleteBranch: boolean;
  confirmed: boolean;
}

export const KEEP: Cleanup = { remove: false, deleteBranch: false, confirmed: false };

/** The worktree the agent's folder is, when Persona made it; null otherwise (and while the computer is asked). */
export function useAgentWorktree(s: Session, open: boolean): WorktreeStatus | null {
  const [st, setSt] = useState<WorktreeStatus | null>(null);
  useEffect(() => {
    setSt(null);
    if (!open || !s.cwd) return;
    const c = new AbortController();
    // asked of the engine it ran in, which made it (a Paseo agent's is Paseo's)
    void worktreeStatus(s.machine_id, s.cwd, c.signal, s.host).then((x) => {
      if (!c.signal.aborted) setSt(x?.ours && x.path ? x : null);
    });
    return () => c.abort();
  }, [open, s.machine_id, s.cwd, s.host]);
  return st;
}

/** Removing it loses something the user has not confirmed yet: the button waits. */
export function cleanupReady(st: WorktreeStatus | null, c: Cleanup): boolean {
  if (!st || !c.remove) return true;
  const loss = worktreeLoss(st, c.deleteBranch);
  return c.confirmed || (loss.dirty === 0 && loss.unmerged === 0);
}

/** After the agent is closed or deleted: removes its worktree when asked, forced only for what the user confirmed, by
 *  the engine the agent ran in (kind: its host). */
export async function runCleanup(mid: string, st: WorktreeStatus | null, c: Cleanup, kind?: string) {
  if (!st?.path || !c.remove) return;
  const loss = worktreeLoss(st, c.deleteBranch);
  try {
    const r = await removeAgentWorktree(mid, st.path, { deleteBranch: c.deleteBranch, force: loss.dirty > 0 || loss.unmerged > 0, kind });
    toast(t(r.branch_deleted ? "chat-sessiondialogs.wt_removed_branch" : "chat-sessiondialogs.wt_removed"), "ok");
  } catch (e) {
    toast(t("chat-sessiondialogs.wt_remove_failed", { error: errorText(e) }), "error");
  }
}

/** Keep or remove the worktree, and what removing it loses (said with the counts, and to be confirmed). */
export function WorktreeCleanup({ st, value, onChange }: { st: WorktreeStatus; value: Cleanup; onChange: (c: Cleanup) => void }) {
  const loss = worktreeLoss(st, value.deleteBranch);
  const losing = value.remove && (loss.dirty > 0 || loss.unmerged > 0);
  const pick = (remove: boolean) => onChange({ ...value, remove, confirmed: false });
  return (
    <div data-worktree-cleanup className="mt-3 rounded-control bg-inset px-3 py-2.5">
      <div className="flex items-center gap-2 text-[12.5px] font-medium text-ink">
        <Icon name="branch" size={13} stroke={2.2} className="shrink-0 text-ink-2" />
        <span className="min-w-0 flex-1 truncate">{t("chat-sessiondialogs.wt_title")}</span>
      </div>
      <p className="mt-0.5 truncate pl-[21px] font-mono text-[11.5px] text-ink-3" title={st.path}>
        {st.branch ? `${st.branch} · ` : ""}
        {shortPath(st.path ?? "")}
      </p>
      <div role="radiogroup" aria-label={t("chat-sessiondialogs.wt_title")} className="mt-2 flex gap-1.5 pl-[21px]">
        {([false, true] as const).map((remove) => (
          <button
            key={String(remove)}
            type="button"
            role="radio"
            aria-checked={value.remove === remove}
            onClick={() => pick(remove)}
            className={cx("h-7 rounded-full px-3 text-[12px] font-medium whitespace-nowrap transition-colors", value.remove === remove ? "bg-surface text-ink shadow-btn" : "text-ink-2 hover:bg-hover")}
          >
            {t(remove ? "chat-sessiondialogs.wt_remove" : "chat-sessiondialogs.wt_keep")}
          </button>
        ))}
      </div>
      {value.remove && (
        <div className="mt-2 flex flex-col gap-1.5 pl-[21px]">
          {st.branch && (
            <div className="flex items-center gap-2 text-[12px] text-ink-2">
              <span className="min-w-0 flex-1">{t("chat-sessiondialogs.wt_delete_branch", { branch: st.branch })}</span>
              <Switch on={value.deleteBranch} onChange={(on) => onChange({ ...value, deleteBranch: on, confirmed: false })} label={t("chat-sessiondialogs.wt_delete_branch", { branch: st.branch })} />
            </div>
          )}
          {loss.dirty > 0 && <p className="text-[11.5px] leading-snug text-red" data-wt-dirty>{t("chat-sessiondialogs.wt_dirty", { count: loss.dirty })}</p>}
          {loss.unmerged > 0 && <p className="text-[11.5px] leading-snug text-red" data-wt-unmerged>{t("chat-sessiondialogs.wt_unmerged", { count: loss.unmerged, main: st.main_branch || "main" })}</p>}
          {losing && (
            <div className="flex items-center gap-2 text-[12px] font-medium text-ink">
              <span className="min-w-0 flex-1">{t("chat-sessiondialogs.wt_confirm_loss")}</span>
              <Switch on={value.confirmed} onChange={(on) => onChange({ ...value, confirmed: on })} label={t("chat-sessiondialogs.wt_confirm_loss")} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
