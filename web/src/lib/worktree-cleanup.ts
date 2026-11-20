// Closing an Agent reads/removes its worktree without loading creation-form guidance.
import { api, ApiError, enc } from "./api";

/** What worktree/status says of a folder (server/internal/proto/panes.go WorktreeStatus). `path` is set for any folder
 *  in a repository (its top level), `worktree` only for a linked worktree, `ours` only for one Persona made. */
export interface WorktreeStatus {
  worktree: boolean;
  ours: boolean;
  path?: string;
  branch?: string;
  root?: string;
  main_branch?: string;
  /** Where a new agent's worktree of this repository goes (newer nodes). */
  checkouts?: string;
  dirty: number;
  unmerged: number;
}

/** Null when the computer cannot say (an older node, offline): the switch and the cleanup then stay out of the way.
 *  kind: the engine the agent opens in (New Agent) or ran in (closing it: its `host`, which made its worktree; Paseo
 *  makes its own); none, the computer's default. */
export async function worktreeStatus(mid: string, cwd: string, signal?: AbortSignal, kind?: string): Promise<WorktreeStatus | null> {
  try {
    return await api<WorktreeStatus>(`/machines/${enc(mid)}/worktree/status`, { method: "POST", json: { cwd, ...(kind ? { kind } : {}) }, signal });
  } catch {
    return null;
  }
}

/** What removing a worktree would lose: changed files, and with the branch, its commits the main branch lacks. */
export function worktreeLoss(st: WorktreeStatus, deleteBranch: boolean): { dirty: number; unmerged: number } {
  return { dirty: st.dirty, unmerged: deleteBranch ? st.unmerged : 0 };
}

/** Removes it once its agent is closed: the node may still list the agent for a moment (worktree_in_use), so a few
 *  tries a second apart. `force` only when the user confirmed what is lost; `kind` the engine the agent ran in (its
 *  `host`). */
export async function removeAgentWorktree(mid: string, path: string, o: { deleteBranch: boolean; force: boolean; kind?: string }, tries = 6, wait = 1000): Promise<{ removed: boolean; branch_deleted: boolean }> {
  for (let i = 1; ; i++) {
    try {
      return await api<{ removed: boolean; branch_deleted: boolean }>(`/machines/${enc(mid)}/worktree/remove`, {
        method: "POST",
        json: { path, ...(o.deleteBranch ? { delete_branch: true } : {}), ...(o.force ? { force: true } : {}), ...(o.kind ? { kind: o.kind } : {}) },
      });
    } catch (e) {
      if (i >= tries || !(e instanceof ApiError) || e.code !== "worktree_in_use") throw e;
      await new Promise((r) => setTimeout(r, wait));
    }
  }
}
