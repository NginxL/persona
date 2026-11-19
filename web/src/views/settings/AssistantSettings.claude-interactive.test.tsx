// @vitest-environment jsdom
// "Interactive Claude Code" (#10058, docs/assistant/assistant.md "Interactive Claude Code (#10058)"): an advanced switch,
// off by default, shown only by a hub that has it; its footer says what it is for and its risk, and Runs on says how the
// main chat runs while it is on, a fallback to print mode with its reason.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
import { initI18n } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { AssistantSettings } from "./AssistantSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
/** What `GET /assistant?view=main-runtime` answers: a report, or a failure (an older hub without the view). */
let execution: (() => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const tool = () => host.querySelector("[data-main-runtime-tool]");

let assistant: Record<string, unknown>;
let puts: unknown[];

beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  execution = undefined;
  puts = [];
  assistant = { manager: "assistant", notes: "", machine_id: "server", main: "main-1", main_machine_id: "server", claude_interactive: false };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input).split("/api/v1")[1];
    if (url === "/assistant?view=main-runtime") return execution ? execution() : json({ error: "not found" }, 404);
    if (url === "/assistant" && init?.method === "PUT") {
      const body = JSON.parse(String(init.body));
      puts.push(body);
      assistant = { ...assistant, ...body };
      return json(assistant);
    }
    if (url === "/assistant") return json(assistant);
    if (url === "/assistant/tasks") return json({ tasks: [] });
    if (url === "/assistant/memory") return json({ files: [] });
    throw new Error(`Unexpected path: ${url}`);
  }));
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }
const group = () => host.querySelector("#claude-interactive") ?? host.querySelector("[data-anchor='claude-interactive']");
const toggle = () => [...host.querySelectorAll<HTMLElement>("[role='switch']")].find((s) => s.getAttribute("aria-label") === "Interactive Claude Code");

describe("Settings › Assistant › Interactive Claude Code (#10058)", () => {
  it("is off by default, says what it is for and its risk, and turns on", async () => {
    await render();
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
    expect(host.textContent).toContain("5-hour limit");
    expect(host.textContent).toContain("Risk:");
    await act(async () => { toggle()!.click(); });
    await flush();
    expect(puts).toEqual([{ claude_interactive: true }]);
    expect(toggle()?.getAttribute("aria-checked")).toBe("true");
  });

  it("is not shown by a hub without the setting", async () => {
    delete assistant.claude_interactive;
    await render();
    expect(toggle()).toBeUndefined();
    expect(group()).toBeNull();
  });

  it("shows in Runs on how the main chat runs, and why it fell back to print mode", async () => {
    execution = () => json({ session_id: "main-1", machine_id: "server", state: "running", source: "native", harness: "claude", mode: "print", mode_note: "tmux is not installed on this computer" });
    await render();
    expect(tool()?.textContent).toBe("Claude Code (print mode)");
    expect(host.textContent).toContain("Interactive Claude Code did not start: tmux is not installed on this computer");
    act(() => root.unmount());
    root = createRoot(host);
    execution = () => json({ session_id: "main-1", machine_id: "server", state: "running", source: "native", harness: "claude", mode: "interactive" });
    await render();
    expect(tool()?.textContent).toBe("Claude Code (interactive)");
  });
});
