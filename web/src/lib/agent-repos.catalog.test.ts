import { describe, expect, it } from "vitest";
import { buildRepoMachineTree, hasWorkspacePin, sessionWorkspace, showsWorktreeLevel, type RepoCatalogOptions, type RepoWorktree } from "./agent-repos";
import type { HostWorkspace } from "./workspace-catalog";
import type { Machine, Session } from "./types";

const machines: Machine[] = [{ id: "machine-a", name: "Computer A", online: true }, { id: "machine-b", name: "Computer B", online: true }];
const workspace = (id: string, patch: Partial<HostWorkspace> = {}): HostWorkspace => ({ id: `qualified-${id}`, machine_id: "machine-a", source: "orca", source_id: id, title: id, cwd: `/code/${id}`, project_id: "project-a", project_name: "Project A", ...patch });
const session = (id: string, workspaceId: string, patch: Partial<Session> = {}): Session => ({ session_id: id, machine_id: "machine-a", title: id, cwd: "/elsewhere", state: "idle", status: "idle", space_kind: "orca", space_id: workspaceId, ...patch });
const options = (workspaces: HostWorkspace[], patch: Partial<RepoCatalogOptions> = {}): RepoCatalogOptions => ({ owner: "credential-a", manager: "manager-a", workspaces, ...patch });
const flatten = (rows: RepoWorktree[]): RepoWorktree[] => rows.flatMap((w) => [w, ...flatten(w.children)]);
const rows = (all: Session[], opts: RepoCatalogOptions) => buildRepoMachineTree(all, machines, opts).flatMap((m) => m.repos.flatMap((r) => flatten(r.worktrees)));
const desktops = [["codex_app", "codex-project:home"], ["claude_desktop", "claude-environment:account"]] as const;

describe("catalog-backed By repo projection", () => {
  it("orders pinned workspaces by newest pin ahead of main and keeps equal paths as independent workspaces", () => {
    const catalog = [
      workspace("main", { source: "paseo", is_main: true, cwd: "/code/shared" }),
      workspace("older", { source: "paseo", pinned_at: "2026-11-15T10:00:00Z", cwd: "/code/shared" }),
      workspace("newer-b", { source: "paseo", pinned_at: "2026-11-16T10:00:00Z", title: "Same name", cwd: "/code/shared" }),
      workspace("newer-a", { source: "paseo", pinned_at: "2026-11-16T10:00:00Z", title: "Same name", cwd: "/code/shared" }),
      workspace("invalid", { source: "paseo", pinned_at: "not-a-date", cwd: "/code/shared" }),
    ];
    const ordered = rows([], options(catalog));
    expect(ordered.map((w) => w.workspace?.source_id)).toEqual(["newer-a", "newer-b", "older", "main", "invalid"]);
    expect(new Set(ordered.map((w) => w.key)).size).toBe(catalog.length);
    expect(ordered.every((w) => w.sessions.length === 0)).toBe(true);
    expect(rows([], options([...catalog].reverse())).map((w) => w.key)).toEqual(ordered.map((w) => w.key));
    expect(catalog.map((w) => w.source_id)).toEqual(["main", "older", "newer-b", "newer-a", "invalid"]);
  });

  it("lifts pinned descendants and their repository without changing parentage or pinning ancestors", () => {
    const catalog = [
      workspace("ordinary", { project_id: "alpha", project_name: "Alpha" }),
      workspace("main", { is_main: true, project_name: "Zeta" }),
      workspace("parent", { project_name: "Zeta" }),
      workspace("ordinary-child", { parent_source_id: "parent", is_main: true }),
      workspace("pinned-child", { parent_source_id: "parent", pinned_at: "2026-11-16T10:00:00Z" }),
    ];
    const tree = buildRepoMachineTree([], machines, options(catalog));
    expect(tree[0].repos.map((r) => r.name)).toEqual(["Zeta", "Alpha"]);
    const parent = tree[0].repos[0].worktrees[0];
    expect(parent.workspace?.source_id).toBe("parent");
    expect(parent.workspace?.pinned_at).toBeUndefined();
    expect(parent.children.map((w) => w.workspace?.source_id)).toEqual(["pinned-child", "ordinary-child"]);
    expect(tree[0].repos[0].worktrees[1].workspace?.source_id).toBe("main");
    expect(rows([], options(catalog, { search: "ordinary" })).map((w) => w.workspace?.source_id))
      .toEqual(["ordinary", "parent", "ordinary-child"]);
  });

  it("retains an offline computer's saved conversation only with an exact unarchived host pin", () => {
    const catalog = [workspace("pinned", { source: "paseo", pinned_at: "2026-11-16T10:00:00Z" })];
    const saved = session("saved", "project-a", { space_kind: "paseo", workspace_id: "pinned", state: "offline", status: "offline" });
    expect(hasWorkspacePin(saved, catalog)).toBe(true);
    for (const patch of [{ archived: true }, { workspace_id: undefined }, { space_kind: "herdr" }, { space_kind: undefined }, { machine_id: "machine-b" }, { manager: "other" }]) {
      expect(hasWorkspacePin({ ...saved, ...patch }, catalog, "manager-a")).toBe(false);
    }
    expect(hasWorkspacePin({ ...saved, workspace_id: undefined, space_id: "pinned" }, catalog)).toBe(false);
    const stopped = machines.map((m) => ({ ...m, online: false }));
    const tree = buildRepoMachineTree([saved, { ...saved, session_id: "archived", archived: true }], stopped, options(catalog));
    expect(tree[0].repos[0].worktrees[0].sessions).toEqual([saved]);
  });

  it("restores ordinary order on unpin while retaining exact identities across hosts and machines", () => {
    const catalog = [workspace("main", { source: "paseo", is_main: true }),
      workspace("shared", { source: "paseo", pinned_at: "2026-11-16T10:00:00Z" }),
      workspace("shared", { source: "herdr" }), workspace("shared", { source: "paseo", machine_id: "machine-b" })];
    const before = rows([], options(catalog));
    const after = rows([], options(catalog.map((w) => ({ ...w, pinned_at: undefined }))));
    expect(before.filter((w) => w.workspace?.pinned_at).map((w) => w.target)).toEqual([
      expect.objectContaining({ machineId: "machine-a", host: "paseo", workspaceId: "shared" }),
    ]);
    expect(after.filter((w) => w.workspace?.source === "paseo" && w.target?.machineId === "machine-a")
      .map((w) => w.workspace?.source_id)).toEqual(["main", "shared"]);
    expect(after.map((w) => w.key).sort()).toEqual(before.map((w) => w.key).sort());
  });

  const runtime = (id: string, patch: Partial<Session> = {}) => session(id, "/code/app", {
    source_kind: "runtime", space_kind: "agy", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app", workspace_cwd: "/code/app", ...patch,
  });
  const native = () => workspace("native", { source: "herdr", cwd: "/code/app", title: "app", project_id: undefined, project_name: undefined });

  it("groups native and logical-folder runtime Agents at one verified checkout exactly once", () => {
    const catalog = [native()];
    const codex = session("codex", "native", { space_kind: "herdr", workspace_id: "native", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app" });
    const agents = [codex, runtime("agy"), runtime("other-agy"), runtime("omp", { space_kind: "omp-history" })];
    const tree = buildRepoMachineTree(agents, machines, options(catalog));
    expect(tree[0].repos).toHaveLength(1);
    expect(tree[0].repos[0].count).toBe(4);
    const worktrees = tree[0].repos[0].worktrees;
    expect(worktrees).toHaveLength(1);
    expect(new Set(worktrees[0].sessions.map((s) => s.session_id))).toEqual(new Set(agents.map((s) => s.session_id)));
    expect(worktrees[0].navigationTree).toHaveLength(4);
    expect(worktrees[0].target?.workspaceId).toBe("native");
    expect(sessionWorkspace(agents[1], catalog, "manager-a")).toBeUndefined();
  });

  it("keeps runtime pinned navigation in the shared row without drawing or counting it twice", () => {
    const regular = runtime("regular"), pinned = runtime("pinned", { state: "needs_you", status: "blocked" });
    const tree = buildRepoMachineTree([regular], machines, options([native()], { navigationSessions: [regular, pinned], hideEmpty: true }));
    expect(tree[0].repos).toHaveLength(1); expect(tree[0].repos[0].count).toBe(1);
    const row = tree[0].repos[0].worktrees[0];
    expect(row.sessions).toEqual([regular]); expect(row.directSessions).toEqual([pinned, regular]);
    expect(row.navigationTree).toEqual([pinned, regular]);
    expect(rows([regular], options([native()], { search: "regular" }))[0].sessions).toEqual([regular]);
  });

  it("requires verified checkout metadata and preserves explicit native bindings", () => {
    const catalog = [native()];
    for (const agent of [runtime("unverified", { worktree: undefined }), runtime("explicit", { workspace_id: "missing" }),
      runtime("native-binding", { space_kind: "herdr", space_id: "missing" }), runtime("foreign-manager", { manager: "manager-b" }),
      runtime("clone", { cwd: "/other/app", repo_root: "/other/app", worktree: "/other/app" }), runtime("different-tree", { worktree: "/code/app/.worktrees/other" })]) {
      expect(rows([agent], options(catalog)).find((row) => row.workspace === catalog[0])?.sessions).toEqual([]);
    }
  });

  it("retains ambiguous host identities, including when one source is hidden", () => {
    const catalog = [native(), workspace("other", { cwd: "/code/app" })];
    for (const hiddenSources of [undefined, ["orca"]]) {
      const actual = rows([runtime("runtime")], options(catalog, { hiddenSources }));
      expect(actual.filter((row) => row.workspace).every((row) => row.sessions.length === 0)).toBe(true);
      expect(actual.filter((row) => !row.workspace).flatMap((row) => row.sessions).map((s) => s.session_id)).toEqual(["runtime"]);
    }
  });

  it("normalizes checkout paths while retaining computer partitions", () => {
    const catalog = [native(), { ...native(), id: "other-machine", machine_id: "machine-b" }];
    const actual = rows([runtime("local"), runtime("remote", { machine_id: "machine-b" })], options(catalog));
    expect(actual.filter((row) => row.workspace).map((row) => row.sessions.map((s) => s.session_id))).toEqual([["local"], ["remote"]]);
    const windows = workspace("windows", { cwd: "C:\\code\\app", source: "herdr" });
    expect(rows([runtime("windows", { cwd: "c:/code/app/src", worktree: "c:/code/app/", repo_root: "c:/code/app" })], options([windows]))[0].sessions).toHaveLength(1);
  });

  it("retains a runtime Agent when its catalog source is hidden among other visible sources", () => {
    const agent = runtime("runtime");
    const catalog = [native(), workspace("unrelated")];
    const tree = buildRepoMachineTree([agent], machines, options(catalog, { hiddenSources: ["herdr"], navigationSessions: [agent] }));
    expect(tree.flatMap((m) => m.repos.flatMap((r) => r.worktrees.flatMap((w) => w.sessions)))).toEqual([agent]);
  });

  it("preserves three empty ancestors, a standalone empty workspace, metadata and zero Agent counts", () => {
    const catalog = [workspace("root", { is_main: true }), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle", workspace_kind: "folder", head: "abc" }), workspace("standalone")];
    const tree = buildRepoMachineTree([], machines, options(catalog));
    expect(tree).toHaveLength(1);
    const repo = tree[0].repos[0];
    expect(repo.count).toBe(0);
    expect(flatten(repo.worktrees).map((w) => w.name)).toEqual(["root", "middle", "leaf", "standalone"]);
    expect(rows([], options(catalog)).find((w) => w.name === "leaf")?.workspace).toBe(catalog[2]);
    expect(repo.worktrees[0].childStates).toEqual({ needs_you: 0, working: 0, done: 0 });
    expect(showsWorktreeLevel(buildRepoMachineTree([], machines, options([catalog[0]]))[0].repos[0])).toBe(true);
  });

  it("follows a same-host parent in another project and retains the child's own project/main metadata", () => {
    const catalog = [workspace("root"), workspace("child", { parent_source_id: "root", project_id: "project-b", project_name: "Project B", is_main: true })];
    const tree = buildRepoMachineTree([session("agent", "child")], machines, options(catalog));
    expect(tree[0].repos).toHaveLength(1);
    expect(tree[0].repos[0].count).toBe(1);
    const root = tree[0].repos[0].worktrees[0];
    expect(root.children[0].workspace?.project_name).toBe("Project B");
    expect(root.children[0].main).toBe(true);
    expect(root.sessions).toEqual([]);
    expect(root.tree.map((s) => s.session_id)).toEqual(["agent"]);
  });

  it("never merges equal paths or equal source IDs across machines, hosts or owners", () => {
    const catalog = [workspace("shared", { cwd: "/code/shared" }), workspace("shared", { cwd: "/code/shared", source: "herdr" }), workspace("shared", { machine_id: "machine-b", cwd: "/code/shared" })];
    const agents = [session("same", "shared"), session("same", "shared", { space_kind: "herdr", workspace_id: "shared" }), session("same", "shared", { machine_id: "machine-b" })];
    const actual = rows(agents, options(catalog));
    expect(new Set(actual.map((w) => w.key)).size).toBe(3);
    expect(actual.every((w) => w.sessions.length === 1)).toBe(true);
    expect(rows(agents, options(catalog, { owner: "credential-b" })).every((w) => !actual.some((a) => a.key === w.key))).toBe(true);
    expect(rows(agents, options(catalog, { manager: "manager-b" })).every((w) => !actual.some((a) => a.key === w.key))).toBe(true);
  });

  it("uses explicit placement ahead of directory inference and refuses a foreign host's binding", () => {
    const catalog = [workspace("a", { cwd: "/code/a" }), workspace("b", { cwd: "/code/b" })];
    expect(sessionWorkspace(session("agent", "b", { cwd: "/code/a", workspace_id: "b" }), catalog)?.source_id).toBe("b");
    expect(sessionWorkspace(session("foreign", "b", { cwd: "/code/a", space_kind: "herdr", workspace_id: "b" }), catalog)).toBeUndefined();
    expect(sessionWorkspace(session("unknown", "missing", { cwd: "/code/a" }), catalog)).toBeUndefined();
    expect(sessionWorkspace(session("other-manager", "a", { manager: "manager-b" }), catalog, "manager-a")).toBeUndefined();
  });

  it.each(desktops)("keeps each %s project root visible and assigns Agents to the deepest matching root", (source, prefix) => {
    const project = `${prefix}:project-a`;
    const root = workspace("root", { source, source_id: project, cwd: "C:\\Projects\\App" });
    const child = { ...root, id: "child-root", title: "Child", cwd: "C:\\Projects\\App\\Packages\\Child" };
    const docs = { ...root, id: "docs-root", title: "Docs", cwd: "D:\\Docs" };
    const agents = [
      session("main", project, { space_kind: source, cwd: "c:/projects/app/src" }),
      session("child", project, { space_kind: source, cwd: "c:/projects/app/packages/child/src" }),
      session("docs", project, { space_kind: source, cwd: "d:/docs/pages" }),
    ];
    const actual = rows(agents, options([root, child, docs]));
    expect(actual).toHaveLength(3);
    expect(new Set(actual.map((w) => w.key)).size).toBe(3);
    for (const [w, id] of [[root, "main"], [child, "child"], [docs, "docs"]] as const) {
      expect(actual.find((entry) => entry.workspace === w)?.sessions.map((s) => s.session_id)).toEqual([id]);
    }
  });

  it.each(desktops)("uses a %s linked worktree's common Git root when its cwd is outside the project's roots", (source, prefix) => {
    const project = `${prefix}:project-a`;
    const app = workspace("app", { source, source_id: project, cwd: "C:\\Projects\\App" });
    const docs = { ...app, id: "docs-root", cwd: "D:\\Docs" };
    const agent = session("worktree", project, { space_kind: source, cwd: "C:\\Worktrees\\fix", repo_root: "c:/projects/app" });
    expect(sessionWorkspace(agent, [app, docs])).toBe(app);
    expect(sessionWorkspace({ ...agent, cwd: "d:/docs/pages" }, [app, docs])).toBe(docs);
    expect(sessionWorkspace({ ...agent, repo_root: undefined, workspace_cwd: "D:\\Docs" }, [app, docs])).toBe(docs);
    expect(sessionWorkspace({ ...agent, repo_root: undefined }, [app, docs])).toBeUndefined();
  });

  it.each(desktops)("never cross-binds saved %s projects that share a directory", (source, prefix) => {
    const a = workspace("a", { source, source_id: `${prefix}:a`, cwd: "C:\\Projects\\App" });
    const b = { ...a, id: "project-b-root", source_id: `${prefix}:b` };
    const otherRoot = { ...a, id: "project-a-docs", cwd: "D:\\Docs" };
    const agent = session("agent", a.source_id, { space_kind: source, cwd: "c:/projects/app/src" });
    expect(sessionWorkspace(agent, [a, b, otherRoot])).toBe(a);
    expect(sessionWorkspace({ ...agent, space_id: b.source_id }, [a, b, otherRoot])).toBe(b);
    expect(sessionWorkspace(agent, [b])).toBeUndefined();
    expect(sessionWorkspace({ ...agent, cwd: "C:/Unrelated" }, [a, b, otherRoot])).toBeUndefined();
    expect(sessionWorkspace(agent, [a, { ...a, id: "duplicate-root" }, otherRoot])).toBeUndefined();
  });

  it.each([["codex_app", "codex-project:"], ["claude_desktop", "codex-project:"], ["claude_desktop", "claude-project:"]])("matches directory-derived %s placement under %s only when a unique saved project contains its directory", (source, directoryPrefix) => {
    const project = workspace("app", { source, source_id: `${directoryPrefix}profile:saved`, cwd: "C:\\Projects\\App" });
    const inferred = session("unassigned", `${directoryPrefix}C:\\Projects\\App`, { space_kind: source, cwd: "c:/projects/app/src" });
    expect(sessionWorkspace(inferred, [project])).toBe(project);
    expect(sessionWorkspace({ ...inferred, cwd: "C:\\Users\\User\\.codex\\worktrees\\fix" }, [project])).toBe(project);
    expect(sessionWorkspace(inferred, [project, { ...project, id: "other", source_id: `${directoryPrefix}profile:other` }])).toBeUndefined();
  });

  it("keeps shared directory project ids bound to their exact desktop host and legacy namespace", () => {
    const projectID = "codex-project:C:/Projects/App";
    const codex = workspace("codex", { source: "codex_app", source_id: projectID, cwd: "C:/Projects/App" });
    const claude = { ...codex, id: "claude", source: "claude_desktop" };
    const legacy = { ...claude, id: "legacy", source_id: "claude-project:C:/Projects/App" };
    const catalog = [codex, claude, legacy];
    const chat = session("desktop", projectID, { cwd: "c:/projects/app/src" });
    expect(sessionWorkspace({ ...chat, space_kind: "codex_app" }, catalog)).toBe(codex);
    expect(sessionWorkspace({ ...chat, space_kind: "claude_desktop" }, catalog)).toBe(claude);
    expect(sessionWorkspace({ ...chat, space_kind: "claude_desktop", space_id: legacy.source_id }, catalog)).toBe(legacy);
    expect(sessionWorkspace({ ...chat, space_kind: "claude_desktop", space_id: "claude-environment:profile:missing" }, catalog)).toBeUndefined();
  });

  it.each(desktops)("limits an unavailable %s project's fallback to generic directory workspaces", (source, prefix) => {
    const native = workspace("native", { source: "herdr", cwd: "C:/Projects/App" });
    const directory = { ...native, id: "directory", source: "directory", source_id: "directory-project" };
    const chat = session("desktop", `${prefix}:missing`, { space_kind: source, cwd: "c:/projects/app/src" });
    expect(sessionWorkspace(chat, [native])).toBeUndefined();
    expect(sessionWorkspace(chat, [native, directory])).toBe(directory);
    expect(sessionWorkspace({ ...chat, workspace_id: "explicit-missing" }, [native, directory])).toBeUndefined();
  });

  it("binds a Paseo Agent to its workspace by id, not its project; an empty Paseo workspace is a row of the project", () => {
    const paseo = (id: string, patch: Partial<HostWorkspace> = {}) => workspace(id, { source: "paseo", project_id: "prj_app", project_name: "app", workspace_kind: "git", ...patch });
    const catalog = [paseo("wks_main", { cwd: "/code/app", is_main: true }), paseo("wks_fix", { cwd: "/paseo/worktrees/abc/fix", is_main: false, branch: "fix" })];
    const agent = session("paseo-1", "prj_app", { space_kind: "paseo", host: "paseo", source_kind: "runtime", workspace_id: "wks_fix", cwd: "/paseo/worktrees/abc/fix" });
    expect(sessionWorkspace(agent, catalog)?.source_id).toBe("wks_fix");
    expect(sessionWorkspace({ ...agent, workspace_id: undefined }, catalog)).toBeUndefined();
    const tree = buildRepoMachineTree([agent], machines, options(catalog));
    expect(tree[0].repos.map((r) => [r.name, r.count])).toEqual([["app", 1]]);
    expect(flatten(tree[0].repos[0].worktrees).map((w) => [w.name, w.sessions.length])).toEqual([["wks_main", 0], ["wks_fix", 1]]);
  });

  it("attributes unbound native Agents only to a unique deepest containing directory and respects worktree markers", () => {
    const a = workspace("a", { cwd: "/code/app" }), b = workspace("b", { cwd: "/code/app/child" });
    const native = session("native", "", { space_kind: undefined, cwd: "/code/app/child/src" });
    expect(sessionWorkspace(native, [a, b])).toBe(b);
    expect(sessionWorkspace(native, [a, b, { ...b, source_id: "other-id" }])).toBeUndefined();
    expect(sessionWorkspace(native, [a, b, { ...b, source: "herdr" }])).toBeUndefined();
    expect(sessionWorkspace({ ...native, space_id: "b" }, [a, b, { ...b, cwd: "/different", source: "herdr" }])).toBeUndefined();
    expect(sessionWorkspace({ ...native, cwd: "/tmp" }, [workspace("root", { cwd: "/" })])?.source_id).toBe("root");
    expect(sessionWorkspace({ ...native, cwd: "/code/app-copy/src" }, [a])).toBeUndefined();
    expect(sessionWorkspace({ ...native, cwd: "/code/app/.worktrees/independent" }, [a])).toBeUndefined();
    expect(sessionWorkspace({ ...native, cwd: "/outside", workspace_cwd: "/code/app/child" }, [a, b])).toBe(b);
    expect(sessionWorkspace({ ...native, machine_id: "machine-b" }, [a, b])).toBeUndefined();
  });

  it("keeps same-host orphans and cycles visible and never resolves parents across hosts or machines", () => {
    const catalog = [workspace("a", { parent_source_id: "b" }), workspace("b", { parent_source_id: "a" }), workspace("self", { parent_source_id: "self" }), workspace("orphan", { parent_source_id: "missing" }), workspace("foreign", { parent_source_id: "b", source: "herdr" }), workspace("elsewhere", { parent_source_id: "a", machine_id: "machine-b" })];
    const actual = rows([], options(catalog));
    expect(actual).toHaveLength(6);
    expect(actual.every((w) => w.children.length === 0)).toBe(true);
  });

  it("state/source filters retain only actual matches and their empty ancestors, including pinned direct navigation", () => {
    const catalog = [workspace("root"), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle" }), workspace("empty")];
    const pinned = session("pinned", "leaf", { state: "needs_you", status: "needs_you" });
    const actual = rows([], options(catalog, { navigationSessions: [pinned], hideEmpty: true }));
    expect(actual.map((w) => w.name)).toEqual(["root", "middle", "leaf"]);
    expect(actual.every((w) => w.sessions.length === 0)).toBe(true);
    expect(actual[0].directSessions).toEqual([]);
    expect(actual[2].directSessions).toEqual([pinned]);
    expect(rows([], options(catalog, { navigationSessions: [], hideEmpty: true }))).toEqual([]);
  });

  it("search preserves a matching empty workspace path or Agent path without retaining unrelated siblings", () => {
    const catalog = [workspace("root"), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle" }), workspace("unrelated")];
    expect(rows([], options(catalog, { search: "leaf" })).map((w) => w.name)).toEqual(["root", "middle", "leaf"]);
    expect(rows([session("needle", "leaf")], options(catalog, { search: "needle" })).map((w) => w.name)).toEqual(["root", "middle", "leaf"]);
  });

  it("navigates direct pinned Agents in display order without drawing them or counting them again", () => {
    const catalog = [workspace("root"), workspace("child", { parent_source_id: "root" })];
    const pinned = session("pinned", "root", { state: "needs_you", status: "needs_you" });
    const ordinary = session("ordinary", "root");
    const descendant = session("descendant", "child");
    const actual = rows([ordinary, descendant], options(catalog, { navigationSessions: [ordinary, pinned, descendant] }));
    expect(actual[0].directSessions?.map((s) => s.session_id)).toEqual(["pinned", "ordinary"]);
    expect(actual[0].sessions).toEqual([ordinary]);
    expect(actual[0].tree).toHaveLength(2);
  });

  it("keeps attribution ambiguous when a source is hidden and retains legacy pinned navigation without duplicate counts", () => {
    const a = workspace("a", { cwd: "/code/app" }), b = workspace("b", { cwd: "/code/app", source: "botmux" });
    const native = session("native", "", { space_kind: undefined, cwd: "/code/app" });
    const actual = rows([native], options([a, b], { hiddenSources: ["botmux"], navigationSessions: [native] }));
    expect(actual.find((w) => w.workspace === a)?.sessions).toEqual([]);
    expect(actual.some((w) => w.workspace === b)).toBe(false);
    const pinned = session("pinned", "project::/code/app");
    const legacy = rows([], options([], { navigationSessions: [pinned] }));
    expect(legacy[0].directSessions).toEqual([pinned]);
    expect(legacy[0].sessions).toEqual([]);
    expect(legacy[0].tree).toEqual([]);
  });

  it("draws one section per repository when a workspace without a project shares it with session-only rows", () => {
    const terminal = workspace("terminal", { source: "herdr", cwd: "/code/app", title: "app", project_id: undefined, project_name: undefined });
    const herdr = session("herdr", "terminal", { space_kind: "herdr", workspace_id: "terminal", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app" });
    const desktop = session("desktop", "project:/code/app", { space_kind: "claude_desktop", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app" });
    const linked = session("linked", "project:/code/app", { space_kind: "claude_desktop", cwd: "/code/app/.claude/worktrees/fix", repo_root: "/code/app", worktree: "/code/app/.claude/worktrees/fix", branch: "fix" });
    const tree = buildRepoMachineTree([herdr, desktop, linked], machines, options([terminal]));
    expect(tree[0].repos.map((r) => [r.name, r.count])).toEqual([["app", 3]]);
    const worktrees = tree[0].repos[0].worktrees;
    // herdr does not say which workspace is the main checkout; the one at the repository's own folder is (#13454), so it
    // comes first with the session-only main checkout, ahead of the linked tree.
    expect(worktrees.map((w) => w.sessions.map((s) => s.session_id))).toEqual([["herdr"], ["desktop"], ["linked"]]);
    expect(worktrees[0].workspace).toBe(terminal);
    expect(worktrees.map((w) => w.main)).toEqual([true, true, false]);
    expect(showsWorktreeLevel(tree[0].repos[0])).toBe(true);
    // An empty workspace joins the repository its own folder is; one whose Agents span repositories keeps its own section.
    expect(buildRepoMachineTree([desktop], machines, options([terminal]))[0].repos.map((r) => [r.name, r.count])).toEqual([["app", 1]]);
    const other = session("other", "terminal", { space_kind: "herdr", workspace_id: "terminal", cwd: "/code/app/vendor/lib", repo_root: "/code/app/vendor/lib", worktree: "/code/app/vendor/lib" });
    expect(buildRepoMachineTree([herdr, other, desktop], machines, options([terminal]))[0].repos.map((r) => [r.name, r.count]).sort()).toEqual([["app", 1], ["app", 2]]);
  });

  it.each([["codex_app", "codex-project:"], ["claude_desktop", "codex-project:"], ["claude_desktop", "claude-project:"]])("never attributes a %s Agent with Git metadata under %s to a workspace above its working tree", (source, prefix) => {
    const home = workspace("home", { source: "herdr", cwd: "/home/me", title: "me", project_id: undefined, project_name: undefined });
    const chat = session("chat", `${prefix}/code/app`, { space_kind: source, cwd: "/home/me/.codex/worktrees/1a2b/app", repo_root: "/code/app" });
    expect(sessionWorkspace(chat, [home])).toBeUndefined();
    expect(sessionWorkspace({ ...chat, repo_root: undefined }, [home])).toBe(home);
    const linked = workspace("linked", { source: "herdr", cwd: "/home/me/.codex/worktrees/1a2b/app", project_id: undefined, project_name: undefined });
    expect(sessionWorkspace(chat, [home, linked])).toBe(linked);
    const main = session("main", `${prefix}/code/app`, { space_kind: source, cwd: "/code/app/src", repo_root: "/code/app" });
    expect(sessionWorkspace(main, [workspace("code", { source: "herdr", cwd: "/code", project_id: undefined })])).toBeUndefined();
    expect(sessionWorkspace(main, [workspace("app", { source: "herdr", cwd: "/code/app", project_id: undefined })])?.source_id).toBe("app");
    const tree = buildRepoMachineTree([chat], machines, options([home]));
    expect(tree[0].repos.find((r) => r.name === "app")?.worktrees.flatMap((w) => w.sessions)).toEqual([chat]);
    expect(tree[0].repos.find((r) => r.name === "me")?.count ?? 0).toBe(0);
  });

  it("keeps Windows directory inference inside its verified Git checkout", () => {
    const home = workspace("home", { source: "directory", cwd: "C:/Users/User" });
    const checkout = workspace("checkout", { source: "directory", cwd: "C:/Users/User/.codex/worktrees/fix/App" });
    const chat = session("windows", "codex-project:C:/Work/App", {
      space_kind: "codex_app", cwd: "c:/users/user/.codex/worktrees/fix/app/src", repo_root: "C:/Work/App",
      worktree: "C:/Users/User/.codex/worktrees/fix/App",
    });
    expect(sessionWorkspace(chat, [home])).toBeUndefined();
    expect(sessionWorkspace(chat, [home, checkout])).toBe(checkout);
  });

  it("keeps an Agent inside its working tree when a symlinked folder leads to its cwd", () => {
    const herdr = { source: "herdr", project_id: undefined, project_name: undefined };
    const home = workspace("home", { ...herdr, cwd: "/home/me" });
    const repo = workspace("repo", { ...herdr, cwd: "/home/me/code/app" });
    const physical = workspace("physical", { ...herdr, cwd: "/data/home/me/code/app" });
    const chat = session("chat", "codex-project:/data/home/me/code/app", {
      space_kind: "codex_app", cwd: "/home/me/code/app/web", workspace_cwd: "/data/home/me/code/app/web", repo_root: "/data/home/me/code/app",
    });
    expect(sessionWorkspace(chat, [home, repo])).toBe(repo);
    expect(sessionWorkspace(chat, [physical])).toBe(physical);
    expect(sessionWorkspace(chat, [home])).toBeUndefined();
  });

  it("retains offline/empty folder workspaces and uses session-only fallback when discovery is absent", () => {
    const offline = workspace("folder", { workspace_kind: "folder", online: false, available: false });
    expect(rows([], options([offline]))[0].target?.workspaceId).toBe("folder");
    const agent = session("legacy", "", { cwd: "/code/legacy", space_kind: undefined });
    expect(buildRepoMachineTree([agent], machines, options([]))).toEqual(buildRepoMachineTree([agent], machines));
  });
});
