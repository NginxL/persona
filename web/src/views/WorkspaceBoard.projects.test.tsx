// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BindWorkspace, boardAgentPreset } from "./WorkspaceBoard";
import type { BoardCard, BoardMutation } from "../lib/workspace-board";
import { t } from "../i18n";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
const mutations: BoardMutation[] = [];
const closed = vi.fn();
const board = { revision: 1, columns: [{ id: "todo", title: "Todo" }], cards: [] };
const machines = [{ id: "pc", name: "Computer", online: true }];
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const button = (key: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t(key))!;
const input = (key: string, value: string) => act(() => {
  const el = host.querySelector<HTMLInputElement>(`input[aria-label="${t(key)}"]`)!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const select = (key: string, value: string) => act(() => {
  const el = host.querySelector<HTMLSelectElement>(`select[aria-label="${t(key)}"]`)!;
  el.value = value;
  el.dispatchEvent(new Event("change", { bubbles: true }));
});
const render = (extra: { busy?: boolean; disabled?: boolean; failure?: string } = {}) => act(() => root.render(<BindWorkspace column="todo" board={board} workspaces={[]} machines={machines} busy={false} disabled={false} {...extra} onClose={closed} onBind={async (m) => { mutations.push(m); return true; }} />));

beforeEach(() => {
  mutations.length = 0;
  closed.mockClear();
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ path: "C:\\Projects", parent: "C:\\", entries: [] }), { headers: { "Content-Type": "application/json" } })));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host); render();
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });

describe("project creation and existing desktop project binding", () => {
  it("creates a named Persona project with a directory, without launching a conversation", async () => {
    act(() => button("workspaceboard.mode_project").click()); await flush();
    expect(button("workspaceboard.create_project").disabled).toBe(true);
    input("workspaceboard.project_name", "Sample project");
    input("workspaceboard.directory_label", "C:\\Projects\\Sample");
    await act(async () => button("workspaceboard.create_project").click());
    expect(mutations).toEqual([{ action: "add_card", column_id: "todo", machine_id: "pc", cwd: "C:\\Projects\\Sample", title: "Sample project", create_directory: true }]);
    expect(closed).toHaveBeenCalledOnce();
  });

  it.each(["codex_app", "claude_desktop"])("keeps %s creation visible but unavailable without submitting a misleading Persona fallback", async (source) => {
    act(() => button("workspaceboard.mode_project").click()); await flush();
    input("workspaceboard.project_name", "Sample project");
    input("workspaceboard.directory_label", "C:\\Projects\\Sample");
    const picker = host.querySelector<HTMLSelectElement>(`select[aria-label="${t("workspaceboard.project_app")}"]`)!;
    expect(picker.querySelector<HTMLOptionElement>(`option[value="${source}"]`)?.disabled).toBe(true);
    expect(host.textContent).toContain(t("workspaceboard.project_desktop_unavailable"));
    // A stale selection or synthetic change must not bypass the unavailable option via Enter/form submission.
    select("workspaceboard.project_app", source);
    expect(button("workspaceboard.create_project").disabled).toBe(true);
    await act(async () => { host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(mutations).toEqual([]);
    expect(closed).not.toHaveBeenCalled();
  });

  it("keeps ordinary directory binding free of project creation options", async () => {
    act(() => button("workspaceboard.mode_directory").click()); await flush();
    input("workspaceboard.directory_label", "C:\\Projects\\Existing");
    await act(async () => button("workspaceboard.bind_directory").click());
    expect(mutations).toEqual([{ action: "add_card", column_id: "todo", machine_id: "pc", cwd: "C:\\Projects\\Existing" }]);
  });

  it("keeps the draft and displays a failed creation in its dialog", async () => {
    act(() => button("workspaceboard.mode_project").click()); await flush();
    input("workspaceboard.project_name", "Keep this draft");
    input("workspaceboard.directory_label", "C:\\Projects\\Sample");
    render({ failure: "The parent folder is missing." });
    expect(host.querySelector('[role="alert"]')?.textContent).toBe("The parent folder is missing.");
    expect(host.querySelector<HTMLInputElement>(`input[aria-label="${t("workspaceboard.project_name")}"]`)?.value).toBe("Keep this draft");
  });

  it("preselects a project's desktop launcher without an engine workspace id", () => {
    const card = { machine_id: "pc", cwd: "C:\\Projects\\Sample", source: "codex_app" } as BoardCard;
    expect(boardAgentPreset(card)).toEqual({ machineId: "pc", cwd: card.cwd, launcher: "codex-app" });
    expect(boardAgentPreset({ ...card, source: "claude_desktop" }).launcher).toBe("claude-desktop");
    expect(boardAgentPreset({ ...card, source: "directory" })).toEqual({ machineId: "pc", cwd: card.cwd });
    expect(boardAgentPreset({ ...card, source_id: "codex-project:home:second" }).desktopProjectId).toBe("codex-project:home:second");
    expect(boardAgentPreset({ ...card, source: "claude_desktop", source_id: "claude-environment:profile-account:second" })).toEqual({ machineId: "pc", cwd: card.cwd, launcher: "claude-desktop", desktopProjectId: "claude-environment:profile-account:second" });
  });
});
