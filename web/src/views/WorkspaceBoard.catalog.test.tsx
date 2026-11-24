// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setToken } from "../lib/api";
import { refresh, reset } from "../lib/store";
import { setRosterDefault } from "../lib/manager-choice";
import { useWorkspaceCatalog, type HostWorkspace } from "../lib/workspace-catalog";
import type { BoardMutation, BoardState } from "../lib/workspace-board";
import { WorkspaceBoard } from "./WorkspaceBoard";
import { t } from "../i18n";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
vi.mock("../lib/workspace-board-setting", () => ({ useWorkspaceBoardSetting: () => ({ enabled: true }) }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The same shared subscription used by Sidebar; keep it real so a stale cache cannot pass this regression.
function CatalogConsumer() {
  const { workspaces } = useWorkspaceCatalog(true);
  return <output data-catalog>{workspaces.map((w) => w.title).join(", ")}</output>;
}

const project: HostWorkspace = { id: "desktop-project", machine_id: "pc", source: "codex_app", source_id: "native-project", title: "Named project", cwd: "C:\\Projects\\Sample", online: true, available: true };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let root: Root, host: HTMLDivElement;
let inventory: HostWorkspace[], board: BoardState;
let reads: string[], acknowledge: (ok: boolean) => void;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const button = (key: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t(key))!;
const click = async (key: string) => { act(() => button(key).click()); await flush(); };
const input = (key: string, value: string) => act(() => {
  const el = host.querySelector<HTMLInputElement>(`input[aria-label="${t(key)}"]`)!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const catalogReads = () => reads.filter((path) => path.endsWith("/workspaces")).length;
const open = async () => {
  await act(() => refresh());
  act(() => root.render(<><CatalogConsumer /><WorkspaceBoard /></>));
  await flush();
  await click("workspaceboard.bind_workspace");
};

beforeEach(() => {
  localStorage.clear(); reset(); setToken("board-catalog-fixture");
  setRosterDefault("assistant");
  inventory = []; reads = [];
  board = { revision: 1, columns: [{ id: "todo", title: "Todo" }], cards: [], can_write: true };
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    const path = new URL(url, "http://fixture").pathname;
    if (path.endsWith("/workspace-board") && init?.method === "POST") {
      const mutation = JSON.parse(String(init.body)) as BoardMutation;
      if (mutation.desktop_app) return Promise.resolve(response({ error: "Native project creation is unavailable." }, 400));
      return new Promise<Response>((resolve) => { acknowledge = (ok) => {
        if (!ok) { resolve(response({ error: "Project could not be created." }, 400)); return; }
        const workspace = mutation.workspace_id ? inventory.find((w) => w.id === mutation.workspace_id)! : { ...project, source: "directory" };
        inventory = [workspace];
        board = { ...board, revision: 2, cards: [{ id: "created-card", machine_id: workspace.machine_id, workspace_id: workspace.id, source: workspace.source, source_id: workspace.source_id, cwd: workspace.cwd, title: workspace.title, column_id: "todo", pinned: false, archived: false }] };
        resolve(response(board));
      }; });
    }
    reads.push(path);
    return Promise.resolve(response(path.endsWith("/workspace-board") ? board
      : path.endsWith("/workspaces") ? { workspaces: inventory }
      : path.endsWith("/machines") ? { machines: [{ id: "pc", name: "Computer", online: true }] }
      : path.endsWith("/dirs") ? { path: "C:\\Projects", parent: "C:\\", entries: [] }
      : path.endsWith("/sessions") ? { sessions: [] }
      : path.endsWith("/todos") ? { todos: [] } : {}));
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); reset(); setToken(""); vi.unstubAllGlobals(); });

describe("workspace binding updates the board and shared sidebar catalog", () => {
  it("publishes a newly named Persona project after acknowledgment without waiting for polling", async () => {
    await open();
    await click("workspaceboard.mode_project");
    input("workspaceboard.project_name", project.title);
    input("workspaceboard.directory_label", project.cwd);
    const before = catalogReads();
    await click("workspaceboard.create_project");
    expect(catalogReads()).toBe(before);
    expect(host.querySelector("[data-catalog]")?.textContent).toBe("");
    await act(async () => acknowledge(true)); await flush();
    expect(catalogReads()).toBe(before + 2);
    expect(host.querySelector("[data-catalog]")?.textContent).toBe(project.title);
    const card = host.querySelector('[data-card-id="created-card"]');
    expect(card?.textContent).toContain(project.title);
    expect(card?.textContent).not.toContain(t("workspaceboard.space_not_found"));
    expect(host.querySelector('[role="dialog"]')).toBeNull();
  });

  it.each(["directory", "codex_app", "claude_desktop"])("refreshes both inventories after binding an existing %s workspace", async (source) => {
    if (source !== "directory") inventory = [{ ...project, source }];
    await open();
    if (source === "directory") {
      await click("workspaceboard.mode_directory");
      input("workspaceboard.directory_label", project.cwd);
    }
    const before = catalogReads();
    await click(source === "directory" ? "workspaceboard.bind_directory" : "workspaceboard.bind");
    expect(catalogReads()).toBe(before);
    await act(async () => acknowledge(true)); await flush();
    expect(catalogReads()).toBe(before + 2);
    expect(host.querySelector("[data-catalog]")?.textContent).toBe(project.title);
    expect(host.querySelector('[data-card-id="created-card"]')?.textContent).not.toContain(t("workspaceboard.space_not_found"));
  });

  it("retains a failed project draft and leaves both successful catalogs untouched", async () => {
    await open();
    await click("workspaceboard.mode_project");
    input("workspaceboard.project_name", project.title);
    input("workspaceboard.directory_label", project.cwd);
    const before = catalogReads();
    await click("workspaceboard.create_project");
    await act(async () => acknowledge(false)); await flush();
    expect(catalogReads()).toBe(before);
    expect(host.querySelector("[data-catalog]")?.textContent).toBe("");
    expect(host.querySelector('[data-card-id="created-card"]')).toBeNull();
    expect(host.querySelector('[role="dialog"]')).not.toBeNull();
    expect(host.querySelector<HTMLInputElement>(`input[aria-label="${t("workspaceboard.project_name")}"]`)?.value).toBe(project.title);
  });
});
