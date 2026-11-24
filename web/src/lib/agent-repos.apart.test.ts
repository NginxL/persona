import { describe, expect, it } from "vitest";
import { buildRepoMachineTree, repoOf, type RepoCatalogOptions, type RepoMachine, type RepoTree } from "./agent-repos";
import type { HostWorkspace } from "./workspace-catalog";
import type { Machine, Session } from "./types";

// A folder of trees kept apart from its repository (herdr's ~/.herdr/worktrees/<repository>/<tree>) names that
// repository only by its name. A tree there that Git no longer places must not make a second section with that name.
const machines: Machine[] = [{ id: "machine-a", name: "Computer A", online: true, home: "/home/me" }, { id: "machine-b", name: "Computer B", online: true, home: "/home/me" }];
const trees = "/home/me/.herdr/worktrees/app";
const session = (id: string, cwd: string, patch: Partial<Session> = {}): Session => ({
  session_id: id, machine_id: "machine-a", title: id, cwd, state: "idle", status: "idle", updated_at: "2026-11-22T10:00:00Z", ...patch,
});
/** As the node reports a session it reads Git for (gitplace.go): the main checkout, the tree, the branch. */
const placed = (id: string, cwd: string, repo: string, worktree: string, patch: Partial<Session> = {}) =>
  session(id, cwd, { repo_root: repo, worktree, branch: worktree === repo ? "main" : "feature", ...patch });
const herdr = (id: string, cwd: string, patch: Partial<HostWorkspace> = {}): HostWorkspace => ({
  id: `qualified-${id}`, machine_id: "machine-a", source: "herdr", source_id: id, title: cwd.split("/").pop()!, cwd, ...patch,
});
const options = (workspaces: HostWorkspace[]): RepoCatalogOptions => ({ owner: "credential-a", manager: "manager-a", workspaces });
const sections = (tree: RepoMachine[]) => tree.flatMap((m) => m.repos.map((r) => ({ machine: m.machine.id, name: r.name, count: r.count, rows: r.worktrees.map((w) => w.name) })));

describe("a folder of trees kept apart from its repository", () => {
  it("is what path inference names when Git does not place the tree", () => {
    expect(repoOf(`${trees}/fix-login`)).toEqual({ repo: trees, name: "app", worktree: `${trees}/fix-login`, main: false, apart: true });
    expect(repoOf("/home/me/.worktrees/app/fix-login").apart).toBe(true);
    // A tree kept inside its repository names the checkout itself.
    expect(repoOf("/code/app/.worktrees/fix-login").apart).toBeUndefined();
    expect(repoOf("/code/app/.claude/worktrees/fix-login").apart).toBeUndefined();
    // Claude Code keeps a repository's trees inside it, so a folder in one of them is never a tree kept apart.
    expect(repoOf("/code/app/.claude/worktrees/feature/src").apart).toBeUndefined();
    expect(repoOf("/code/app").apart).toBeUndefined();
  });

  it("keeps a removed tree's Agents in their repository's section beside its herdr workspace", () => {
    // The screenshot of the bug: the main checkout's herdr workspace with live Agents, and history sessions from two trees
    // under herdr's folder of trees that were removed once their work merged (no Git placement any more).
    const main = herdr("w-main", "/code/app");
    const live = [placed("cli", "/code/app", "/code/app", "/code/app", { space_kind: "herdr", workspace_id: "w-main" }), placed("cleanup", "/code/app", "/code/app", "/code/app", { space_kind: "herdr", workspace_id: "w-main" })];
    const history = [session("integration", `${trees}/launch`), session("lead", `${trees}/launch`), session("control", `${trees}/control`, { agent: "agy" })];
    const tree = buildRepoMachineTree([...live, ...history], machines, options([main]));
    expect(sections(tree)).toEqual([{ machine: "machine-a", name: "app", count: 5, rows: ["app", "control", "launch"] }]);
    const rows = tree[0]!.repos[0]!.worktrees;
    expect(rows.map((w) => w.sessions.map((s) => s.session_id).sort())).toEqual([["cleanup", "cli"], ["control"], ["integration", "lead"]]);
    expect(rows[0]!.workspace).toBe(main);
    expect(new Set(rows.map((w) => w.key)).size).toBe(3);
  });

  it("joins the same way without a workspace catalog", () => {
    const tree = buildRepoMachineTree([placed("main", "/code/app/web", "/code/app", "/code/app"), session("old", `${trees}/fix`)], machines);
    expect(sections(tree)).toEqual([{ machine: "machine-a", name: "app", count: 2, rows: ["main", "fix"] }]);
  });

  it("puts an empty herdr workspace in that folder under its repository", () => {
    const tree = buildRepoMachineTree([placed("main", "/code/app", "/code/app", "/code/app")], machines, options([herdr("w-fix", `${trees}/fix`)]));
    expect(sections(tree)).toEqual([{ machine: "machine-a", name: "app", count: 1, rows: ["main", "fix"] }]);
  });

  it("follows Git when it placed another tree of the folder, even beside another repository of that name", () => {
    const tree = buildRepoMachineTree([
      placed("live", `${trees}/live`, "/code/app", `${trees}/live`),
      placed("other", "/work/app", "/work/app", "/work/app"),
      session("removed", `${trees}/removed`),
    ], machines);
    expect(sections(tree)).toEqual([
      { machine: "machine-a", name: "app", count: 2, rows: ["feature", "removed"] },
      { machine: "machine-a", name: "app", count: 1, rows: ["main"] },
    ]);
    expect(tree[0]!.repos[0]!.repo).toBe("/code/app");
  });

  it("keeps its own section rather than guess between two repositories", () => {
    // Two repositories of that name on the computer, and Git placed none of the folder's trees.
    const byName = buildRepoMachineTree([placed("a", "/code/app", "/code/app", "/code/app"), placed("b", "/work/app", "/work/app", "/work/app"), session("old", `${trees}/fix`)], machines);
    expect(sections(byName).map((s) => s.count)).toEqual([1, 1, 1]);
    expect(byName[0]!.repos.find((r) => r.apart)?.repo).toBe(trees);
    // Git placed the folder's trees in two repositories.
    const byGit = buildRepoMachineTree([
      placed("one", `${trees}/one`, "/code/app", `${trees}/one`),
      placed("two", `${trees}/two`, "/work/app", `${trees}/two`),
      session("old", `${trees}/fix`),
    ], machines);
    expect(sections(byGit).map((s) => s.count)).toEqual([1, 1, 1]);
  });

  it("names its own section after the repository when nothing else on the computer has that name", () => {
    const tree = buildRepoMachineTree([session("old", `${trees}/fix`), placed("notes", "/code/notes", "/code/notes", "/code/notes", { machine_id: "machine-b" })], machines);
    expect(sections(tree)).toEqual([
      { machine: "machine-a", name: "app", count: 1, rows: ["fix"] },
      { machine: "machine-b", name: "notes", count: 1, rows: ["main"] },
    ]);
  });

  it("never takes a folder in a repository's own tree for a tree of another repository", () => {
    // Without Git metadata, `/code/app/.worktrees/feature/src` has the shape of `~/.worktrees/<repository>/<tree>`, but it
    // is not at the computer's home: it is a folder in the tree `feature` of `/code/app`, and `/code/feature` is unrelated.
    const feature = placed("feature", "/code/feature", "/code/feature", "/code/feature");
    for (const cwd of ["/code/app/.worktrees/feature/src", "/code/app/.claude/worktrees/feature/src", "/home/me/.claude/worktrees/feature/src"]) {
      for (const tree of [
        buildRepoMachineTree([feature, session("nested", cwd)], machines),
        buildRepoMachineTree([feature, session("nested", cwd)], machines, options([herdr("w-feature", "/code/feature")])),
      ]) {
        const repos = tree[0]!.repos;
        const ids = (r?: RepoTree) => r?.worktrees.flatMap((w) => w.sessions.map((s) => s.session_id));
        expect(ids(repos.find((r) => r.repo === "/code/feature"))).toEqual(["feature"]);
        expect(ids(repos.find((r) => r.repo !== "/code/feature"))).toEqual(["nested"]);
        expect(repos.map((r) => r.count)).toEqual([1, 1]);
        expect(repos.some((r) => r.apart)).toBe(false);
      }
    }
  });

  it("keeps its own section on a computer whose home is not known", () => {
    const tree = buildRepoMachineTree([placed("main", "/code/app", "/code/app", "/code/app"), session("old", `${trees}/fix`)], [{ id: "machine-a", name: "Computer A", online: true }]);
    expect(sections(tree).map((s) => s.count)).toEqual([1, 1]);
  });

  it("never joins a repository on another computer", () => {
    const tree = buildRepoMachineTree([placed("main", "/code/app", "/code/app", "/code/app"), session("old", `${trees}/fix`, { machine_id: "machine-b" })], machines);
    expect(sections(tree)).toEqual([
      { machine: "machine-a", name: "app", count: 1, rows: ["main"] },
      { machine: "machine-b", name: "app", count: 1, rows: ["fix"] },
    ]);
  });
});
