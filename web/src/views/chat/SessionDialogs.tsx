// The chat's ⋯ menu dialogs: session info, rename (the agent / its space), delete, close, and the header's permission mode.
// Session info and Rename / Rename space / Delete load as chunks of their own; what stays here is what the first screen uses.
import { useEffect, useRef, useState } from "react";
import { api, errorText, HubTimeoutError, pastHub, sessionPath } from "../../lib/api";
import { shortPath } from "../../lib/format";
import { BYPASS_MODE, renameBody } from "../../lib/session-actions";
import { bypassRestarts } from "../../lib/header-mode";
import { sessionLauncher } from "../../lib/model-switch";
import { getSession, refreshAssistant, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { cx, Switch, toast } from "../../ui/atoms";
import { removeAgentWorktree, worktreeLoss, worktreeStatus, type WorktreeStatus } from "../../lib/worktree";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { lazyDialog } from "../LazyOverlay";

// "Session info" (iOS SessionInfoSheet, SessionInfoDialog.tsx): its own chunk, loaded on the dialog's first open, so
// its fetch and rows are not the first screen's (#8261). The same component as before for the header that draws it.
export const SessionInfoDialog = lazyDialog(() => import("./SessionInfoDialog").then((m) => m.SessionInfoDialog));

// Rename, Rename space and Delete (SessionMenuDialogs.tsx): one chunk of their own, loaded on the first open or, sooner,
// when a menu that offers one opens (preloadSessionDialogs: the chat's ⋯ menu, an Agent row's, a space's), so the click
// draws the dialog at once. Off the first screen (#10382, docs/web/web.md "First screen").
const loadMenuDialogs = () => import("./SessionMenuDialogs");
export const RenameDialog = lazyDialog(() => loadMenuDialogs().then((m) => m.RenameDialog));
export const RenameSpaceDialog = lazyDialog(() => loadMenuDialogs().then((m) => m.RenameSpaceDialog));
export const DeleteSessionDialog = lazyDialog(() => loadMenuDialogs().then((m) => m.DeleteSessionDialog));
/** Starts loading the three, once (a failed load is asked again by the next menu or open). */
export function preloadSessionDialogs(): void {
  for (const d of [RenameDialog, RenameSpaceDialog, DeleteSessionDialog]) void d.preload().catch(() => {});
}

/** Saves a new title from the header's in-place rename (#5421, cindy's double-click on the title, which updates at once and
 *  puts the old title back if the save fails): only the title, the tab keeps its name. False when nothing changed or it
 *  failed (the error toasted). */
export async function renameTitle(session: Session, title: string): Promise<boolean> {
  const body = renameBody(session, "", title);
  if (!body?.title) return false;
  // the header and the list show the new name at once; the hub's push confirms it
  upsertSession({ ...session, title: body.title });
  try {
    await api(`${sessionPath(session.machine_id, session.session_id)}/rename`, { method: "POST", json: { title: body.title } });
    if (session.assistant) void refreshAssistant().catch(() => {});
    return true;
  } catch (e) {
    // back to the old title, unless something renamed it meanwhile
    const now = getSession(session.machine_id, session.session_id);
    if (!now || now.title === body.title) upsertSession({ ...(now ?? session), title: session.title });
    toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}

/** The hub gives a mode call 90 seconds. Five more lets its own refusal arrive first; a lost response still ends Apply. */
const MODE_CONFIRM_MS = pastHub(90_000);

function modeChangeError(e: unknown) {
  toast(e instanceof HubTimeoutError ? t("chat-sessiondialogs.change_unconfirmed") : t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
}

/** POST …/modes with a patch, drawn at once and put back if it is refused (the error toasted). A timeout may mean the
 *  change ran but its response was lost, so it stays drawn for the session push. `latest` is the session as it is now:
 *  a pick applies the model and then the effort, and the second must build on the session the first changed. */
export async function applyModes(latest: () => Session, patch: { permission_mode?: string; effort?: string; model?: string }): Promise<boolean> {
  const base = latest();
  const before = { permission_mode: base.permission_mode, effort: base.effort, model: base.model, bypass_pending: base.bypass_pending };
  // Full access the agent reaches only by a restart (#6476): drawn as waiting for it, in the mode it still runs in;
  // any other mode drops a restart that waits
  const restart = patch.permission_mode === BYPASS_MODE && bypassRestarts(base);
  const drawn: Partial<Session> = restart ? { ...patch, permission_mode: base.permission_mode, bypass_pending: true } : patch.permission_mode ? { ...patch, bypass_pending: undefined } : patch;
  upsertSession({ ...base, ...drawn });
  try {
    await api(`${sessionPath(base.machine_id, base.session_id)}/modes`, { method: "POST", json: patch, timeout: MODE_CONFIRM_MS });
    return true;
  } catch (e) {
    if (!(e instanceof HubTimeoutError)) upsertSession({ ...latest(), ...Object.fromEntries(Object.keys(drawn).map((k) => [k, before[k as keyof typeof before]])) });
    modeChangeError(e);
    return false;
  }
}

/** applyModes for one session: the composer's permission picker and model pill, the header's mode. */
export function useModeSet(session: Session) {
  const latest = useRef(session);
  latest.current = session;
  const set = (patch: { permission_mode?: string; effort?: string; model?: string }): Promise<boolean> => applyModes(() => latest.current, patch);
  /** POST …/modes {launcher, from, model?, effort?} (#5035): another launcher records a switch that the user's next
   *  message carries out, the session's own drops one; `from` is the launcher the pick was made against, so one made on
   *  a session that switched meanwhile is refused (`switch_stale`). The pending switch shows at once (the pill's clock)
   *  and goes back to what it was if the hub says no, with its words. `agent`: the engine a launcher of the other engine
   *  runs (#5054), drawn as the node will record it (`pending_switch.agent`); it is not sent. */
  const switchLauncher = async (step: { launcher: string; from?: string; model?: string; effort?: string }, agent?: string): Promise<boolean> => {
    const base = latest.current;
    const before = base.pending_switch;
    const pending = { launcher: step.launcher, model: step.model, effort: step.effort, ...(agent ? { agent } : {}), at: new Date().toISOString() };
    const next: Session = { ...base, pending_switch: step.launcher === sessionLauncher(base) ? undefined : pending };
    upsertSession(next);
    latest.current = next; // a step after this one builds on it before the push draws it
    try {
      await api(`${sessionPath(base.machine_id, base.session_id)}/modes`, { method: "POST", json: step, timeout: MODE_CONFIRM_MS });
      return true;
    } catch (e) {
      if (!(e instanceof HubTimeoutError)) upsertSession({ ...latest.current, pending_switch: before });
      modeChangeError(e);
      return false;
    }
  };
  return { set, latest, switchLauncher };
}

/** POST …/modes {service_tier}: Codex's speed (the Speed submenu, the model pill). Nothing is drawn ahead of it: the
 *  session's push after the agent took it owns what is shown (DESIGN_SYSTEM.md "Codex speed controls"). */
export async function setServiceTier(s: Session, tier: string): Promise<boolean> {
  try {
    await api(`${sessionPath(s.machine_id, s.session_id)}/modes`, { method: "POST", json: { service_tier: tier }, timeout: MODE_CONFIRM_MS });
    return true;
  } catch (e) {
    modeChangeError(e);
    return false;
  }
}

// ── the worktree a closed or deleted agent worked in (#6115) ──

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
