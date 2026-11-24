// The Agents list's opt-in activity order (Settings › Agents › "Sort Agents by recent activity", the hub's
// `sort_by_activity`). Off (the default) the sidebar keeps the stable structural order of #11801: the hub's computer
// order, herdr's workspaces, tabs and panes. On, the rows go by time only and each grouping's sections (machines,
// spaces, repositories, tabs) by their latest agent, except that "By repo" keeps its machines in order. By status the
// sections are the states, so that grouping keeps its own order.
//
// Every helper takes the clock it reads an agent's time from (`ActivityDate`): the sidebar freezes it while the pointer
// is over the list or focus is in it (`useActivityDates`), so the order only moves once the person has looked away.
import { useMemo, useRef } from "react";
import type { Space } from "./agent-groups";
import type { RepoMachine, RepoWorktree } from "./agent-repos";
import { stateOf } from "./format";
import type { HerdrMachine, HerdrSpace } from "./herdr-tree";
import type { TodoTree, TreeAgent } from "./todo-tree";
import type { Session } from "./types";
import { byTime, timeMs } from "./times";
import { byWorkspacePin } from "./workspace-catalog";

/** An agent's time for ordering. */
export type ActivityDate = (s: Session) => string;

/** An agent's time: a working one by when it started working, so its every line does not move it. */
export function sortDate(s: Session): string {
  return (stateOf(s) === "working" ? s.state_since || s.updated_at : s.updated_at) ?? "";
}

const keyOf = (s: Session) => `${s.machine_id}\u0000${s.session_id}`;

/** The times as of the last render the list was not held still, and the clock that reads them. While `frozen` the
 *  order keeps those times (a session this clock has not seen yet reads its own), so state marks, counts and rows
 *  update in place and nothing moves; once released it reads the current times and the list reorders. */
export function useActivityDates(sessions: readonly Session[], frozen: boolean): ActivityDate {
  const live = useMemo(() => new Map(sessions.map((s): [string, string] => [keyOf(s), sortDate(s)])), [sessions]);
  const held = useRef(live);
  if (!frozen) held.current = live;
  const dates = frozen ? held.current : live;
  return useMemo(() => (s: Session) => dates.get(keyOf(s)) ?? sortDate(s), [dates]);
}

/** The helpers for one clock. */
export function activityOrder(dateOf: ActivityDate = sortDate) {
  /** The latest of the agents' times ("" for none). */
  const latestOf = (sessions: readonly Session[]): string => {
    let out = "";
    for (const s of sessions) {
      const d = dateOf(s);
      if (timeMs(d) > timeMs(out)) out = d;
    }
    return out;
  };

  /** The most recently updated first, then by id so equal times never swap between renders. */
  const byUpdated = (a: Session, b: Session): number =>
    byTime(dateOf(b), dateOf(a)) || a.session_id.localeCompare(b.session_id) || a.machine_id.localeCompare(b.machine_id);

  /** `list` with the most recent first (`date`), ties by `id`; a new array. */
  const latestFirst = <T,>(list: readonly T[], date: (x: T) => string, id: (x: T) => string): T[] =>
    list.map((x) => ({ x, d: date(x), k: id(x) }))
      .sort((a, b) => byTime(b.d, a.d) || a.k.localeCompare(b.k))
      .map(({ x }) => x);

  /** "By project": the spaces by their latest agent, the agents inside by time. `arrangeSpaces` still puts the pinned ones
   *  first and folds the quiet ones. */
  const spacesByUpdate = (spaces: readonly Space[]): Space[] =>
    latestFirst(spaces.map((sp) => ({ ...sp, sessions: [...sp.sessions].sort(byUpdated) })), (sp) => latestOf(sp.sessions), (sp) => sp.key);

  /** "By project", both halves: the spaces as `spacesByUpdate`, and "Other agents" (`loose`) by time too. */
  const projectsByUpdate = <T extends { spaces: readonly Space[]; loose: readonly Session[] }>(built: T): T =>
    ({ ...built, spaces: spacesByUpdate(built.spaces), loose: [...built.loose].sort(byUpdated) });

  const spaceByUpdate = (sp: HerdrSpace): HerdrSpace => {
    const tabs = latestFirst(sp.tabs, (tab) => latestOf(tab.sessions), (tab) => tab.key);
    const children = latestFirst(sp.children.map(spaceByUpdate), (c) => latestOf(c.tree), (c) => c.key);
    return { ...sp, tabs, children, loose: [...sp.loose].sort(byUpdated) };
  };

  /** "By machine": the machines, their spaces, nested spaces and tabs by their latest agent (pinned spaces still lead
   *  their machine); a tab's panes keep their layout order. */
  const machineTreeByUpdate = (tree: readonly HerdrMachine[]): HerdrMachine[] => {
    const machines = tree.map((m) => {
      const spaces = m.spaces.map(spaceByUpdate);
      const pinned = spaces.filter((sp) => sp.pinned);
      const rest = spaces.filter((sp) => !sp.pinned);
      const latest = (list: HerdrSpace[]) => latestFirst(list, (sp) => latestOf(sp.tree), (sp) => sp.key);
      return { ...m, spaces: [...latest(pinned), ...latest(rest)], other: [...m.other].sort(byUpdated) };
    });
    return latestFirst(machines, (m) => latestOf(m.sessions), (m) => m.key);
  };

  const repoWorktreeByUpdate = (w: RepoWorktree): RepoWorktree => {
    const children = latestFirst(w.children.map(repoWorktreeByUpdate), (c) => latestOf(c.tree), (c) => c.key).sort(byWorkspacePin);
    return { ...w, sessions: [...w.sessions].sort(byUpdated), children };
  };

  /** "By repo": each machine's repositories by their latest agent, including nested worktrees; the machines keep their
   *  order (a section does not jump when another computer's agent updates); a repository's main checkout still comes
   *  first, child worktrees sort by their latest descendant, and agents by time. */
  const repoMachinesByUpdate = (list: readonly RepoMachine[]): RepoMachine[] =>
    list.map((m) => ({
      ...m,
      repos: latestFirst(
        m.repos.map((r) => ({ ...r, worktrees: r.worktrees.map(repoWorktreeByUpdate) })),
        (r) => latestOf(r.worktrees.flatMap((w) => w.tree)),
        (r) => r.key,
      ).sort(byWorkspacePin),
    }));

  const agentsByUpdate = (agents: readonly TreeAgent[]): TreeAgent[] =>
    agents.map((a) => ({ ...a, children: agentsByUpdate(a.children) })).sort((a, b) => byUpdated(a.session, b.session));

  /** "By to-do": the to-dos keep their order (ranked by their own status), the agents under each by time. */
  const todoTreeByUpdate = (tree: TodoTree): TodoTree => ({
    projects: tree.projects.map((p) => ({ ...p, todos: p.todos.map((td) => ({ ...td, agents: agentsByUpdate(td.agents) })) })),
    todos: tree.todos.map((td) => ({ ...td, agents: agentsByUpdate(td.agents) })),
    loose: tree.loose.map((g) => ({ ...g, agents: agentsByUpdate(g.agents) })),
  });

  return { latestOf, byUpdated, latestFirst, spacesByUpdate, projectsByUpdate, machineTreeByUpdate, repoMachinesByUpdate, todoTreeByUpdate };
}

/** The default clock's helpers, for callers (and tests) that never hold the order still. */
export const { latestOf, byUpdated, projectsByUpdate, machineTreeByUpdate, repoMachinesByUpdate, todoTreeByUpdate, spacesByUpdate } = activityOrder();
