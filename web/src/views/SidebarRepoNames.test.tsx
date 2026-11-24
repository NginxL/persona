// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { refresh, reset } from "../lib/store";
import { credentialOwner, setToken } from "../lib/api";
import { workspaceTarget, type HostWorkspace } from "../lib/workspace-catalog";
import { defaultManager } from "../lib/manager-choice";
import type { Machine, Session } from "../lib/types";
import { Icon } from "../ui/icons";
import { Sidebar } from "./Sidebar";
import { t } from "../i18n";
import type { WorkspaceTreeActions } from "./SidebarTree";

// By repo says a repository's name once, on its heading (#13454): a lone workspace named after its folder gives the
// heading its marks and menu instead of a row of its own, Agent rows leave the repository and the tree to the headings
// above them (DESIGN_SYSTEM 3.4), and the main checkout herdr does not name wears the home mark and comes first.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const machine: Machine = { id: "machine-a", name: "Computer A", online: true };
const herdr = (id: string, cwd: string, patch: Partial<HostWorkspace> = {}): HostWorkspace => ({ id: `qualified-${id}`, machine_id: machine.id, source: "herdr", source_id: id, cwd, title: cwd.split("/").pop()!, ...patch });
const agent = (id: string, title: string, workspace: string, cwd: string, patch: Partial<Session> = {}): Session => ({ machine_id: machine.id, session_id: id, title, cwd, space_kind: "herdr", workspace_id: workspace, status: "idle", state: "idle", ...patch });
let host: HTMLDivElement, root: Root;
let inventory: HostWorkspace[], sessions: Session[];
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const render = async (actions?: WorkspaceTreeActions) => {
  await act(() => refresh());
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} workspaceActions={actions} />));
  await flush();
};
const row = (id: string) => host.querySelector<HTMLAnchorElement>(`a[href="/s/${machine.id}/${id}"]`)!;
const sectionOf = (id: string) => row(id).closest("[data-sidebar-group]")!;
const headingOf = (id: string) => sectionOf(id).querySelector<HTMLElement>("[data-sidebar-group-heading]")!;
const words = (el: Element) => `${el.textContent ?? ""}\n${el.getAttribute("title") ?? ""}`;
let home = "";

beforeEach(() => {
  localStorage.clear(); reset(); setToken("repo-names-fixture");
  localStorage.setItem("persona.agentGrouping", "repo");
  inventory = []; sessions = [];
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://fixture").pathname;
    const body = path.endsWith("/workspaces") ? { workspaces: inventory.map((w) => ({ ...w })) }
      : path.endsWith("/machines") ? { machines: [machine] }
      : path.endsWith("/sessions") ? { sessions }
      : path.endsWith("/pins") ? { sessions: [], spaces: [] }
      : {};
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  }));
  history.replaceState(null, "", "/");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  // The home mark as Icon draws it, to find it in a row.
  const icon = document.createElement("div"); const iconRoot = createRoot(icon);
  act(() => iconRoot.render(<Icon name="home" />)); home = icon.querySelector("svg")!.innerHTML; act(() => iconRoot.unmount());
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); reset(); setToken(""); vi.unstubAllGlobals();
});

describe("By repo says a repository's name once", () => {
  it("gives a lone workspace's heading its marks and menu, and lists its Agents under it", async () => {
    inventory = [herdr("w-notes", "/code/notes", { pinned_at: "2026-11-16T10:00:00Z", available: false })];
    sessions = [agent("summary", "Write the summary", "w-notes", "/code/notes"), agent("links", "Check the links", "w-notes", "/code/notes")];
    const onWorkspaceMenu = vi.fn();
    await render({ onWorkspaceMenu });
    expect(host.querySelectorAll("[data-workspace-key]")).toHaveLength(0);
    const heading = headingOf("summary");
    expect(sectionOf("links")).toBe(sectionOf("summary"));
    expect(heading.textContent).toContain("notes");
    expect(heading.querySelector(`[title="${t("sidebar.pinned")}"]`)).toBeTruthy();
    expect(heading.querySelector(`[title="${t("workspace-tree.unavailable")}"]`)).toBeTruthy();
    // Its ⋯ and a right click on the heading both open the workspace's menu, for the workspace itself.
    const target = workspaceTarget(inventory[0]!, credentialOwner(), defaultManager());
    const button = heading.querySelector<HTMLButtonElement>(`button[aria-label="${t("workspace-tree.actions", { name: "notes" })}"]`)!;
    act(() => button.click());
    expect(onWorkspaceMenu).toHaveBeenLastCalledWith(target, button);
    const box = button.closest(".group\\/sec")!;
    act(() => box.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
    expect(onWorkspaceMenu).toHaveBeenCalledTimes(2);
    expect(onWorkspaceMenu.mock.calls[1]![0]).toEqual(target);
    expect(onWorkspaceMenu.mock.calls[1]![1]).toBe(box);
  });

  it("draws no menu on the heading of a section that is not a workspace", async () => {
    // Session-only rows (no host workspace): the heading has nothing to act on.
    sessions = [agent("summary", "Write the summary", "", "/code/notes", { space_kind: "claude_desktop", workspace_id: undefined })];
    await render({ onWorkspaceMenu: vi.fn() });
    expect(headingOf("summary").textContent).toContain("notes");
    expect(headingOf("summary").querySelector("[data-row-menu]")).toBeNull();
  });

  it("leaves the repository's and the tree's names out of Agent rows", async () => {
    inventory = [herdr("w-notes", "/code/notes"), herdr("w-zeta", "/code/zeta"), herdr("w-alpha", "/code/zeta/.worktrees/alpha")];
    sessions = [agent("summary", "Write the summary", "w-notes", "/code/notes"),
      agent("lead", "Plan the release", "w-zeta", "/code/zeta", { repo_root: "/code/zeta", worktree: "/code/zeta" }),
      agent("fix", "Fix the login", "w-alpha", "/code/zeta/.worktrees/alpha", { repo_root: "/code/zeta", worktree: "/code/zeta/.worktrees/alpha", branch: "alpha" })];
    await render();
    for (const id of ["summary", "lead", "fix"]) {
      expect(words(row(id))).not.toMatch(/notes|zeta|alpha|›/);
    }
  });

  it("lists the main checkout herdr does not name first, with the home mark", async () => {
    inventory = [herdr("w-alpha", "/code/zeta/.worktrees/alpha"), herdr("w-zeta", "/code/zeta")];
    sessions = [agent("lead", "Plan the release", "w-zeta", "/code/zeta", { repo_root: "/code/zeta", worktree: "/code/zeta" }),
      agent("fix", "Fix the login", "w-alpha", "/code/zeta/.worktrees/alpha", { repo_root: "/code/zeta", worktree: "/code/zeta/.worktrees/alpha", branch: "alpha" })];
    await render();
    const rows = [...host.querySelectorAll("[data-workspace-key]")];
    expect(rows.map((el) => JSON.parse(el.getAttribute("data-workspace-key")!)[4])).toEqual(["w-zeta", "w-alpha"]);
    const hasHome = (el: Element) => [...el.firstElementChild!.querySelectorAll("svg")].some((svg) => svg.innerHTML === home);
    expect(rows.map(hasHome)).toEqual([true, false]);
  });
});
