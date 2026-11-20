// The eager shell only needs the request channel; creation transport loads with its dialog.
import type { MenuNode } from "./node-actions";
import type { WorkspaceTarget } from "./workspace-target";
import type { WorktreeCreated, WorktreeTarget } from "./worktree";

export interface WorktreeRequest {
  target: WorktreeTarget;
  /** The computer's tree changed (the new workspace is in it): read it again. */
  changed: () => void;
  source?: WorkspaceTarget;
  /** Undefined follows the source; null requests a top-level workspace. */
  parent?: WorkspaceTarget | null;
  onCreated?: (created: WorktreeCreated, target: WorkspaceTarget) => void;
}

const openers = new Set<(r: WorktreeRequest) => void>();

/** Opens "New worktree…" (the app shell listens) for a workspace node. */
export function openNewWorktree(node: Pick<MenuNode, "mid" | "workspaceId" | "host" | "label">, changed: () => void = () => {}) {
  if (!node.workspaceId) return;
  const request = { target: { mid: node.mid, workspaceId: node.workspaceId, host: node.host, label: node.label }, changed };
  openers.forEach((l) => l(request));
}

/** Delivers an already assembled creation request to the app shell. */
export function emitWorktreeRequest(request: WorktreeRequest) {
  openers.forEach((l) => l(request));
}

export function onOpenNewWorktree(l: (r: WorktreeRequest) => void) {
  openers.add(l);
  return () => {
    openers.delete(l);
  };
}
