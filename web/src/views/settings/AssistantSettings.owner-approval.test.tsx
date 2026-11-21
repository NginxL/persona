// @vitest-environment jsdom
// Exact owner replies: advanced, reversible settings with the hub's confirmed values, never a chat-only opt-in.
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
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
let assistant: Record<string, unknown>;
let puts: Record<string, unknown>[];

beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  puts = [];
  assistant = { manager: "assistant", notes: "", machine_id: "server", main: "main-1", main_machine_id: "server",
    owner_reply_approval: false, persistent_owner_action_cards: false, owner_reply_approval_phrases: ["发", "同意"] };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input).split("/api/v1")[1];
    if (url === "/assistant?view=main-runtime") return json({ error: "not found" }, 404);
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
const toggle = (label: string) => [...host.querySelectorAll<HTMLElement>("[role='switch']")].find((s) => s.getAttribute("aria-label") === label);
const phrases = () => host.querySelector<HTMLTextAreaElement>("#owner-approval-phrases")!;
const savePhrases = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Save phrases")!;
async function typePhrases(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(phrases(), value);
    phrases().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("Settings › Assistant › Exact approval replies", () => {
  it("shows both switches off, saves their confirmed values, and reverses them", async () => {
    await render();
    const approval = toggle("Approve a batch with an exact reply")!;
    const persistent = toggle("Keep unanswered proposals until answered")!;
    expect(approval.getAttribute("aria-checked")).toBe("false");
    expect(persistent.getAttribute("aria-checked")).toBe("false");
    expect(phrases().value).toBe("发\n同意");
    expect(host.textContent).toContain("never creates an Always rule");

    await act(async () => { approval.click(); }); await flush();
    await act(async () => { persistent.click(); }); await flush();
    await act(async () => { approval.click(); }); await flush();
    expect(puts).toEqual([
      { owner_reply_approval: true },
      { persistent_owner_action_cards: true },
      { owner_reply_approval: false },
    ]);
    expect(approval.getAttribute("aria-checked")).toBe("false");
    expect(persistent.getAttribute("aria-checked")).toBe("true");
  });

  it("saves exact newline-delimited phrases and echoes the saved list", async () => {
    await render();
    await typePhrases("  发  \nAgree\n同意");
    await act(async () => { savePhrases().click(); }); await flush();
    expect(puts).toEqual([{ owner_reply_approval_phrases: ["发", "Agree", "同意"] }]);
    expect(phrases().value).toBe("发\nAgree\n同意");
    expect(savePhrases().disabled).toBe(true);
  });

  it("hides the advanced group on an older hub", async () => {
    delete assistant.owner_reply_approval;
    delete assistant.persistent_owner_action_cards;
    delete assistant.owner_reply_approval_phrases;
    await render();
    expect(toggle("Approve a batch with an exact reply")).toBeUndefined();
    expect(host.querySelector("#owner-approval-phrases")).toBeNull();
  });
});
