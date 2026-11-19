// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { refreshAgentsDisplay, resetAgentsDisplay, saveAgentsDisplay } from "../../lib/agents-display";
import type { Session } from "../../lib/types";
import { HistoryBrowser } from "./HistoryBrowser";
import { resumeInTerminal } from "../../lib/agent-resume";
import { navigate } from "../../lib/router";
const env = vi.hoisted(() => ({ rows: [] as Session[], features: [] as string[], search: vi.fn(), more: vi.fn(), state: { query: "", hits: [] as { session: Session }[], loaded: true, loading: false, loadingMore: false, error: "", moreError: "", hasMore: false } }));
vi.mock("../../lib/sessions", () => ({ useFullAgentSessions: () => ({ sessions: env.rows, loaded: true, error: "" }), retryFullAgentSessions: vi.fn() }));
vi.mock("../../lib/current-assistant", () => ({ useScoped: (rows: Session[]) => rows, useScope: () => ({ several: false, id: "main", has: () => true }), useScopedMachines: () => [{ id: "one", name: "Computer one", online: true }, { id: "two", name: "Computer two", online: true }] }));
vi.mock("../../lib/store", async original => ({ ...await original<typeof import("../../lib/store")>(), useStore: (read: (s: { features: string[] }) => unknown) => read(env), rememberSessionRow: vi.fn(), onHubEvent: () => () => {} }));
vi.mock("../../lib/runtime-search", () => ({ RUNTIME_SEARCH_FEATURE: "sessions_search", RuntimeSearchModel: class { subscribe = () => () => {}; snapshot = () => env.state; search = env.search; retry = vi.fn(); more = env.more; dispose = vi.fn(); } }));
vi.mock("../../lib/agent-resume", () => ({ terminalResume: (s: Session) => s.session_id === "resume" ? "available" : "hidden", useResume: () => ({ phase: "idle" }), resumeInTerminal: vi.fn((_mid, _sid, open) => { open(); return Promise.resolve(true); }) }));
vi.mock("../../lib/router", async original => ({ ...await original<typeof import("../../lib/router")>(), navigate: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
const row = (session_id: string, extra: Partial<Session> = {}) => ({ machine_id: "one", session_id, title: session_id, status: "offline", state: "offline", updated_at: "2026-11-16T12:00:00Z", ...extra } as Session);
async function render() { await act(async () => root.render(<HistoryBrowser />)); }
beforeEach(() => {
  env.rows = [row("resume", { agent: "codex" }), row("readonly", { agent: "claude", machine_id: "two" }), row("live", { state: "working", status: "working" }), row("waiting", { state: "needs_you" })];
  localStorage.clear(); env.features = []; env.state = { ...env.state, query: "", hits: [], hasMore: false }; vi.clearAllMocks();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); resetAgentsDisplay(); vi.unstubAllGlobals(); });
const ids = () => [...host.querySelectorAll("[data-history-row]")].map(el => el.getAttribute("data-history-row"));
describe("HistoryBrowser", () => {
  it("opens on stopped rows, counts only filtered rows, and preserves read-only navigation", async () => {
    await render(); expect(ids()).toEqual(["resume", "readonly"]); expect(host.querySelector("[data-history-count]")?.textContent).toBe("2");
    const agent = host.querySelector<HTMLSelectElement>(`select[aria-label="${t("session-history.agent")}"]`)!;
    await act(async () => { agent.value = "codex"; agent.dispatchEvent(new Event("change", { bubbles: true })); });
    expect(ids()).toEqual(["resume"]); expect(host.querySelector("[data-history-count]")?.textContent).toBe("1");
  });
  it("uses the existing Resume and opens the regular work detail; a resumed row leaves on the next snapshot", async () => {
    await render(); const button = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("session-history.resume_named", { title: "resume" })}"]`)!;
    await act(async () => button.click()); expect(resumeInTerminal).toHaveBeenCalledWith("one", "resume", expect.any(Function));
    expect(navigate).toHaveBeenCalledWith({ name: "session", mid: "one", sid: "resume", work: true, view: "term" });
    env.rows = env.rows.map(s => s.session_id === "resume" ? { ...s, state: "idle", status: "idle" } : s); await render(); expect(ids()).toEqual(["readonly"]);
  });
  it("routes conversation search through the existing model, filters live hits and reconciles resumed results", async () => {
    env.features = ["sessions_search"]; env.state = { ...env.state, query: "needle", hits: [{ session: row("resume") }, { session: row("live", { state: "working" }) }], hasMore: true };
    await render(); const input = host.querySelector("input")!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "needle"); input.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(env.search).toHaveBeenLastCalledWith("needle", { manager: undefined }); expect(ids()).toEqual(["resume"]);
    expect(host.textContent).toContain(t("paged-agents.more"));
    env.rows = env.rows.map(s => s.session_id === "resume" ? { ...s, state: "idle", status: "idle" } : s); await render(); expect(ids()).toEqual([]);
  });
  it("follows the acknowledged hub preference while History stays open", async () => {
    env.rows = [row("bot", { pane_id: "botmux:example/topic" }), row("ordinary")];
    vi.stubGlobal("fetch", vi.fn(async (_url, init) => {
      const showBotmux = init?.method === "PUT" ? JSON.parse(String(init.body)).show_botmux : true;
      return Response.json({ show_botmux: showBotmux, configured: true });
    }));
    await render();
    expect(ids()).toEqual(["ordinary"]);
    await act(async () => { await refreshAgentsDisplay(); });
    expect(ids()).toHaveLength(2);
    await act(async () => { await saveAgentsDisplay(false); });
    expect(ids()).toEqual(["ordinary"]);
  });
  it("drops removed identities from an older search window", async () => {
    env.features = ["sessions_search"]; env.state = { ...env.state, query: "needle", hits: [{ session: row("removed") }] };
    await render(); const input = host.querySelector("input")!;
    await act(async () => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "needle"); input.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(ids()).toEqual([]);
  });

});
