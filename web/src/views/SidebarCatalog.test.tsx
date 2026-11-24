// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const previews = vi.hoisted(() => new Map<string, number>());
vi.mock("../lib/format", async (load) => {
  const real = await load<typeof import("../lib/format")>();
  return { ...real, sessionPreview: (s: Session) => {
    previews.set(s.session_id, (previews.get(s.session_id) ?? 0) + 1);
    return real.sessionPreview(s);
  } };
});
import { getState, refresh, reset } from "../lib/store";
import { credentialOwner, setToken } from "../lib/api";
import { refreshWorkspaceCatalog, workspaceTarget, type HostWorkspace } from "../lib/workspace-catalog";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";
import { t } from "../i18n";
import type { WorkspaceTreeActions } from "./SidebarTree";
import { defaultManager } from "../lib/manager-choice";
import { selectWorkspace } from "../lib/workspace-selection";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const machine: Machine = { id: "machine-a", name: "Computer A", online: true };
const workspace = (id: string, patch: Partial<HostWorkspace> = {}): HostWorkspace => ({ id: `qualified-${id}`, machine_id: machine.id, source: "orca", source_id: id, cwd: `/code/${id}`, title: id, project_id: "project-a", project_name: "Project A", ...patch });
const agent = (id: string, space: string, patch: Partial<Session> = {}): Session => ({ machine_id: machine.id, session_id: id, title: id, cwd: `/code/${space}`, space_kind: "orca", space_id: space, status: "idle", state: "idle", ...patch });
let host: HTMLDivElement, root: Root;
let inventory: HostWorkspace[], sessions: Session[], pinned: string[];
let request: ReturnType<typeof vi.fn>;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const render = async (actions?: WorkspaceTreeActions) => {
  await act(() => refresh());
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} workspaceActions={actions} />));
  await flush();
};
const title = (name: string) => [...host.querySelectorAll<HTMLAnchorElement | HTMLButtonElement>("[data-workspace-key] > div > a, [data-workspace-key] > div > button[aria-pressed]")].find((el) => el.firstElementChild?.textContent === name)!;
const disclosure = (name: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("workspace-tree.disclosure", { name })}"]`)!;
const click = (el: HTMLElement) => act(() => el.click());

beforeEach(() => {
  previews.clear(); localStorage.clear(); reset(); setToken("catalog-fixture");
  localStorage.setItem("persona.agentGrouping", "repo");
  inventory = [workspace("root", { is_main: true }), workspace("middle", { parent_source_id: "root" }), workspace("leaf", { parent_source_id: "middle", project_id: "project-b", project_name: "Project B" }), workspace("empty")];
  sessions = [agent("Leaf Agent", "leaf")]; pinned = [];
  request = vi.fn((url: string) => {
    const path = new URL(url, "http://fixture").pathname;
    const body = path.endsWith("/workspaces") ? { workspaces: inventory.map((w) => ({ ...w })) }
      : path.endsWith("/machines") ? { machines: [machine] }
      : path.endsWith("/sessions") ? { sessions }
      : path.endsWith("/pins") ? { sessions: pinned, spaces: [] }
      : {};
    return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
  });
  vi.stubGlobal("fetch", request);
  history.replaceState(null, "", "/");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); reset(); setToken(""); vi.unstubAllGlobals();
});

describe("By repo catalog integration", () => {
  it("keeps saved offline Agents in their exact pinned workspace beyond Recent's limit", async () => {
    inventory = [workspace("pinned", { source: "paseo", pinned_at: "2026-11-16T10:00:00Z" }),
      workspace("sibling", { source: "paseo", cwd: "/code/pinned" })];
    const saved = agent("Saved conversation", "project-a", { space_kind: "paseo", host: "paseo", workspace_id: "pinned", state: "offline", status: "offline", updated_at: "2020-01-01T00:00:00Z" });
    sessions = [...Array.from({ length: 45 }, (_, i) => agent(`Recent ${i}`, "sibling", { state: "offline", status: "offline", updated_at: "2026-11-16T10:00:00Z" })),
      saved, { ...saved, session_id: "archived", title: "Archived conversation", archived: true },
      { ...saved, session_id: "history", title: "Archived history", workspace_id: undefined, space_id: "pinned" }];
    await render();
    const holder = title("pinned").closest("[data-workspace-key]")!;
    expect([...holder.querySelectorAll("a")].map((el) => el.textContent)).toHaveLength(1);
    expect(holder.querySelector("a")!.textContent).toContain("Saved conversation");
    expect([...host.querySelectorAll("a")].filter((el) => el.textContent?.includes("Saved conversation"))).toHaveLength(1);
    expect(title("sibling").closest("[data-workspace-key]")!.querySelectorAll("a")).toHaveLength(0);
    inventory = inventory.map((w) => ({ ...w, pinned_at: undefined }));
    await act(() => refreshWorkspaceCatalog()); await flush();
    expect(holder.querySelectorAll("a")).toHaveLength(0);
  });

  it("does not use a workspace pin to override an explicit state filter", async () => {
    inventory = [workspace("pinned", { source: "paseo", pinned_at: "2026-11-16T10:00:00Z" })];
    sessions = [agent("Saved conversation", "project-a", { space_kind: "paseo", workspace_id: "pinned", state: "offline", status: "offline" })];
    localStorage.setItem("persona.agentFilter", "working");
    await render();
    expect(title("pinned")).toBeUndefined();
    expect([...host.querySelectorAll("a")].some((el) => el.textContent?.includes("Saved conversation"))).toBe(false);
  });

  it("refreshes host pins and unpins without changing selection, folds or Persona session pins", async () => {
    inventory = [workspace("main", { source: "paseo", is_main: true, cwd: "/code/shared" }),
      workspace("pinned-empty", { source: "paseo", cwd: "/code/shared", pinned_at: "2026-11-16T10:00:00Z" }),
      workspace("local")];
    sessions = [agent("Local pin", "local")]; pinned = [`${machine.id}/Local pin`];
    const workspaceOrder = () => [...host.querySelectorAll("[data-workspace-key]")]
      .map((el) => JSON.parse(el.getAttribute("data-workspace-key")!)[4]);
    await render();
    expect(workspaceOrder().filter((id) => id !== "local")).toEqual(["pinned-empty", "main"]);
    const before = title("pinned-empty");
    const holder = before.closest("[data-workspace-key]")!;
    expect(holder.querySelector(`[title="${t("sidebar.pinned")}"]`)).toBeTruthy();
    expect(title("main").closest("[data-workspace-key]")!.querySelector(`[title="${t("sidebar.pinned")}"]`)).toBeNull();
    click(before);
    expect(before.getAttribute("aria-pressed")).toBe("true");
    expect(disclosure("pinned-empty").getAttribute("aria-expanded")).toBe("false");
    inventory = inventory.map((w) => ({ ...w, pinned_at: undefined }));
    await act(() => refreshWorkspaceCatalog()); await flush();
    expect(workspaceOrder().filter((id) => id !== "local")).toEqual(["main", "pinned-empty"]);
    expect(title("pinned-empty")).toBe(before);
    expect(holder.querySelector(`[title="${t("sidebar.pinned")}"]`)).toBeNull();
    expect(before.getAttribute("aria-pressed")).toBe("true");
    expect(disclosure("pinned-empty").getAttribute("aria-expanded")).toBe("false");
    expect([...host.querySelectorAll("a")].filter((el) => el.textContent?.includes("Local pin"))).toHaveLength(1);
    expect(getState().pins.sessions).toEqual(pinned);
    expect(request.mock.calls.some(([, init]) => init && (init as RequestInit).method === "PUT")).toBe(false);
  });

  it("renders native and logical runtime sessions under one checkout with one row per Agent", async () => {
    inventory = [workspace("native", { source: "herdr", title: "app", cwd: "/code/app", project_id: undefined, project_name: undefined })];
    sessions = [agent("codex", "native", { space_kind: "herdr", workspace_id: "native", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app" }),
      ...["agy-one", "agy-two", "omp"].map((id) => agent(id, "/code/app", { space_kind: "agy", source_kind: "runtime", cwd: "/code/app", repo_root: "/code/app", worktree: "/code/app" }))];
    await render();
    // The lone workspace is named after its folder, the repository's, so its Agents are listed under the repository's
    // heading without a row of their own (#13454).
    expect(host.querySelectorAll("[data-workspace-key]")).toHaveLength(0);
    const section = host.querySelector(`a[href="/s/${machine.id}/codex"]`)!.closest("[data-sidebar-group]")!;
    expect(section.querySelector("[data-sidebar-group-heading]")!.textContent).toContain("app");
    for (const session of sessions) {
      expect(section.querySelectorAll(`a[href="/s/${machine.id}/${session.session_id}"]`)).toHaveLength(1);
      expect(host.querySelectorAll(`a[href="/s/${machine.id}/${session.session_id}"]`)).toHaveLength(1);
    }
  });

  it("does not rebuild Agent rows on an unchanged catalog refresh", async () => {
    await render();
    const before = title("root"); previews.clear();
    await act(() => refreshWorkspaceCatalog()); await flush();
    expect(title("root")).toBe(before);
    expect([...previews.keys()]).toEqual([]);
  });

  it("loads independent of the board, exposes empty ancestry and lets titles and arrows toggle the same disclosure", async () => {
    const onSelectWorkspace = vi.fn();
    await render({ onSelectWorkspace });
    expect(inventory.every((w) => title(w.title))).toBe(true);
    expect(title("leaf").textContent).toBe("leafProject B");
    expect(request.mock.calls.some(([url]) => String(url).endsWith("/workspace-board"))).toBe(false);
    expect(getState().sessions[machine.id]).toHaveLength(1);
    expect(disclosure("root").getAttribute("aria-expanded")).toBe("true");
    click(title("root"));
    expect(title("root").getAttribute("aria-pressed")).toBe("true");
    expect(location.pathname).toBe("/");
    expect(disclosure("root").getAttribute("aria-expanded")).toBe("false");
    expect(onSelectWorkspace).toHaveBeenCalledWith(expect.objectContaining({ machineId: machine.id, host: "orca", workspaceId: "root", cwd: "/code/root" }));
    click(disclosure("root"));
    expect(disclosure("root").getAttribute("aria-expanded")).toBe("true");
    expect(onSelectWorkspace).toHaveBeenCalledTimes(1);
    for (const name of ["root", "middle", "leaf", "empty"]) {
      click(title(name));
      expect(disclosure(name).getAttribute("aria-expanded")).toBe("false");
      click(title(name));
      expect(disclosure(name).getAttribute("aria-expanded")).toBe("true");
    }
  });

  it("reveals an externally selected parent without opening it and rejects an old connection's selection", async () => {
    await render();
    click(disclosure("middle")); click(disclosure("root"));
    const parent = workspaceTarget(inventory[1]!, credentialOwner(), defaultManager());
    act(() => selectWorkspace(parent));
    expect(disclosure("root").getAttribute("aria-expanded")).toBe("true");
    expect(disclosure("middle").getAttribute("aria-expanded")).toBe("false");
    expect(title("middle").getAttribute("aria-pressed")).toBe("true");
    expect(title("middle").closest("[inert]")).toBeNull();
    act(() => setToken("replacement-connection")); await flush();
    act(() => selectWorkspace(parent));
    expect(title("middle").getAttribute("aria-pressed")).toBe("false");
  });

  it("retires the old credential's outstanding discovery read while a mounted sidebar switches owners", async () => {
    let complete!: (response: Response) => void;
    const oldRead = new Promise<Response>((done) => { complete = done; });
    const original = request.getMockImplementation()! as (url: string) => Promise<Response>;
    let reads = 0;
    request.mockImplementation((url: string) => String(url).endsWith("/workspaces") && ++reads === 1 ? oldRead : original(url));
    await render();
    inventory = [workspace("replacement")];
    act(() => setToken("catalog-fixture-next")); await flush();
    expect(title("replacement")).toBeTruthy();
    await act(async () => { complete(new Response(JSON.stringify({ workspaces: [workspace("obsolete")] }), { headers: { "Content-Type": "application/json" } })); });
    await flush();
    expect(title("replacement")).toBeTruthy();
    expect(title("obsolete")).toBeUndefined();
  });

  it("keeps workspace titles as disclosures and opens the exact Agent from its own row", async () => {
    sessions.push(agent("Pinned Agent", "root", { state: "needs_you", status: "needs_you" }));
    pinned = [`${machine.id}/Pinned Agent`];
    await render();
    expect([...host.querySelectorAll("a")].filter((el) => el.textContent?.includes("Pinned Agent"))).toHaveLength(1);
    click(title("root"));
    expect(location.pathname).toBe("/");
    expect(disclosure("root").getAttribute("aria-expanded")).toBe("false");
    click(title("root"));
    expect(disclosure("root").getAttribute("aria-expanded")).toBe("true");
    const pinnedRow = [...host.querySelectorAll("a")].find((el) => el.textContent?.includes("Pinned Agent"))!;
    click(pinnedRow);
    expect(decodeURIComponent(location.pathname)).toContain("Pinned Agent");
  });

  it("passes the same exact target to pointer and keyboard-accessible menu entry points", async () => {
    const onWorkspaceMenu = vi.fn(); await render({ onWorkspaceMenu });
    const button = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("workspace-tree.actions", { name: "empty" })}"]`)!;
    click(button);
    expect(onWorkspaceMenu).toHaveBeenCalledWith(expect.objectContaining({ host: "orca", workspaceId: "empty" }), button);
    const target = onWorkspaceMenu.mock.calls[0][0];
    act(() => button.parentElement!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
    expect(onWorkspaceMenu.mock.calls[1][0]).toEqual(target);
  });

  it("refreshes a renamed workspace without changing identity, selection or the remembered disclosure", async () => {
    await render();
    click(title("empty"));
    const key = title("empty").closest("[data-workspace-key]")!.getAttribute("data-workspace-key");
    inventory = inventory.map((w) => w.source_id === "empty" ? { ...w, title: "Renamed" } : w);
    await act(() => refreshWorkspaceCatalog()); await flush();
    expect(title("Renamed").getAttribute("aria-pressed")).toBe("true");
    expect(title("Renamed").closest("[data-workspace-key]")!.getAttribute("data-workspace-key")).toBe(key);
    expect(disclosure("Renamed").getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps matching Agent ancestors and hides standalone empties under a state filter", async () => {
    sessions[0] = { ...sessions[0], status: "needs_you", state: "needs_you" };
    localStorage.setItem("persona.agentFilter", "needs");
    await render();
    expect(title("root")).toBeTruthy(); expect(title("middle")).toBeTruthy();
    expect(disclosure("leaf")).toBeTruthy(); expect(title("empty")).toBeUndefined();
    expect(localStorage.getItem("persona.agentGrouping")).toBe("repo");
  });
});
