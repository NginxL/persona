// Assistant composer's @ / + panel wrapper (#12531): loads assistant-only mention sources
// (hub workspaces and bound repositories) on demand when the panel opens, keeping them off
// both the first-screen and idle tiers.
import { useEffect, useMemo, useState, useSyncExternalStore, type ComponentProps } from "react";
import { api, credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { workspaceDetail, workspaceMatches, workspaceTitle } from "../../lib/mentions";
import { loadMentionRepositories, repositoryMentionRows, type MentionRepository } from "../../lib/mentions-repositories";
import { machineWithHub } from "../../lib/mesh";
import { useStore } from "../../lib/store";
import type { Machine } from "../../lib/types";
import type { Workspace, WorkspaceList } from "../../lib/workspace-board";
import { ComposerPanel, type AssistantMentions, type MentionRow } from "./ComposerPanel";

type PanelProps = Omit<ComponentProps<typeof ComposerPanel>, "assistant">;
const NO_MACHINES: Machine[] = [];

/** The hub's workspaces, read once each time the panel opens (null while reading; empty when unavailable). */
function useWorkspaces(on: boolean): Workspace[] | null {
  const [list, setList] = useState<Workspace[] | null>(null);
  useEffect(() => {
    if (!on) return;
    const ac = new AbortController();
    api<WorkspaceList>("/workspaces", { signal: ac.signal })
      .then((l) => setList(l.workspaces ?? []))
      .catch(() => {
        if (!ac.signal.aborted) setList((cur) => cur ?? []);
      });
    return () => ac.abort();
  }, [on]);
  return list;
}

/** The assistant's bound repositories, read once per panel opening (null while reading; empty when unavailable). */
function useRepositories(repositories: string | boolean | undefined): MentionRepository[] | null {
  const enabled = Boolean(repositories);
  const manager = typeof repositories === "string" && repositories ? repositories : undefined;
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, () => "");
  const key = enabled ? `${owner}\n${manager ?? ""}` : "";
  const [state, setState] = useState<{ key: string; list: MentionRepository[] } | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const ac = new AbortController();
    loadMentionRepositories(manager, ac.signal)
      .then((list) => {
        if (!ac.signal.aborted) setState({ key, list });
      })
      .catch(() => {
        if (!ac.signal.aborted) setState((cur) => (cur?.key === key ? cur : { key, list: [] }));
      });
    return () => ac.abort();
  }, [enabled, key, manager]);
  if (!enabled) return null;
  return state?.key === key ? state.list : null;
}

export function AssistantComposerPanel(props: PanelProps) {
  const wantWorkspaces = Boolean(props.source?.workspaces);
  const wantRepositories = Boolean(props.source?.repositories);
  const machines = useStore((s) => (wantWorkspaces ? s.machines : NO_MACHINES));
  const workspaces = useWorkspaces(wantWorkspaces);
  const repositories = useRepositories(props.source?.repositories);

  const assistant = useMemo<AssistantMentions>(() => ({
    rows: (query: string) => {
      const out: MentionRow[] = [];
      if (wantWorkspaces) {
        const q = query.toLowerCase();
        const name = (id: string) => machineWithHub(id, machines);
        for (const w of workspaceMatches(workspaces ?? [], q, name)) {
          out.push({ kind: "workspace", id: `workspace:${w.id}`, name: workspaceTitle(w), detail: workspaceDetail(w, name), workspace: w });
        }
      }
      if (wantRepositories) {
        out.push(...repositoryMentionRows(repositories ?? [], query));
      }
      return out;
    },
    browsingRows: wantRepositories ? (query: string) => repositoryMentionRows(repositories ?? [], query) : undefined,
    loading: Boolean((wantWorkspaces && workspaces === null) || (wantRepositories && repositories === null)),
  }), [wantWorkspaces, wantRepositories, machines, workspaces, repositories]);

  return <ComposerPanel {...props} assistant={assistant} />;
}
