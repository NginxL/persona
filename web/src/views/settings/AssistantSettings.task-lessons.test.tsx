// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

// Settings › Persona › "Learn from finished tasks" (#12295): off by default, saved alone, taken back when refused, and
// absent on a hub before it.

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
/** The hub's assistant settings; a field left out is one an older hub does not have. */
let stored: Record<string, unknown>;
let writes: Record<string, unknown>[];
/** What a PUT answers instead of saving, when set. */
let answer: (() => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const learning = () => host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-assistantsettings.task_lessons")}"]`);
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  vi.mocked(toast).mockClear();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  stored = { manager: "assistant", notes: "", agent_tasks: false, task_lessons: false, archive_closed_days: 3 }; writes = []; answer = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)); writes.push(body);
        if (answer) return answer();
        stored = { ...stored, ...body };
      }
      return json(stored);
    }
    if (path.endsWith("/tasks")) return json({ tasks: [] });
    if (path.endsWith("/memory")) return json({ files: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }
async function click(node: HTMLElement) { await act(async () => { node.click(); }); await flush(); }

it("shows the hub's value, off by default, with what it does and where the lessons are", async () => {
  await render();
  expect(learning()!.getAttribute("aria-checked")).toBe("false");
  expect(host.textContent).toContain(t("settings-assistantsettings.task_lessons_hint"));
  expect(host.textContent).toContain(t("settings-assistantsettings.task_lessons_footer"));
});

it("saves only its own field and shows what the hub saved", async () => {
  await render();
  await click(learning()!);
  expect(writes).toEqual([{ task_lessons: true }]);
  expect(learning()!.getAttribute("aria-checked")).toBe("true");
  await click(learning()!);
  expect(writes).toEqual([{ task_lessons: true }, { task_lessons: false }]);
  expect(learning()!.getAttribute("aria-checked")).toBe("false");
  expect(toast).not.toHaveBeenCalled();
});

it("goes back with the hub's reason when the change is refused", async () => {
  answer = () => json({ error: "Synthetic refusal" }, 403);
  await render();
  await click(learning()!);
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic refusal"), "error");
  expect(learning()!.getAttribute("aria-checked")).toBe("false");
});

it("has no row on a hub before it", async () => {
  delete stored.task_lessons;
  await render();
  expect(learning()).toBeNull();
  expect(host.textContent).not.toContain(t("settings-assistantsettings.task_lessons_footer"));
});

it("is found by the settings search", async () => {
  const { searchSettings } = await import("./search");
  const { groups } = searchSettings("learn from finished");
  expect(groups.some((g) => g.anchor === "task-lessons")).toBe(true);
});
