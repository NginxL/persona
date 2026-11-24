// @vitest-environment jsdom
// Model settings lists the background supervisor's purpose (#5082) right after Main chat, in Main chat's group, with what
// it is for. A hub since #5806 lists it in the catalog (last among its own purposes) and is asked nothing more; one before
// that answers it by its own id and leaves it out of the catalog; a hub without it lists nothing new. Each purpose
// shows the hub's description under its name (#5806).
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("./ConfigurationManager", () => ({ ConfigurationManager: ({ children }: { children: (id: string) => unknown }) => children("assistant") }));
vi.mock("./ModelProfiles", () => ({ ModelProfiles: () => null }));
vi.mock("../../lib/store", () => ({ useStore: (select: (s: { machines: []; features: [] }) => unknown) => select({ machines: [], features: [] }) }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { initI18n } from "../../i18n";
import type { ModelProvider, ModelPurpose } from "../../lib/model-config";
import { ModelConfiguration } from "./ModelConfiguration";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const providers: ModelProvider[] = [{ id: "launcher.default", name: "Default launcher", kind: "launcher" }];
const base = { group: "Default models", kind: "launcher" as const, revision: 1, override: {}, effective: { provider: "launcher.default", model: "default-model" }, fields: ["provider", "model", "effort"] };
const main: ModelPurpose = { ...base, id: "assistant", category_id: "assistant", name: "Main chat", launch: "headless" };
const agents: ModelPurpose = { ...base, id: "agent.new", category_id: "agent.new", name: "Agents", launch: "engine" };
const supervise: ModelPurpose = { ...base, id: "assistant.supervise", category_id: "assistant", parent: "assistant", name: "Background supervisor", group: "Assistant", launch: "headless", inherited_from: "Main chat" };

/** What the hub does with the purpose: answers it by id (as built, #4927), also lists it, or has none (an older hub). */
let mode: "detail" | "listed" | "described" | "used" | "none" | "failing";
let patches: Record<string, unknown>[];
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const DESCRIPTIONS: Record<string, string> = {
  assistant: "Talks with you, splits work into tasks and hands them out.",
  "agent.new": "The agents that do the tasks on your computers.",
  "assistant.supervise": "Follows up and reviews the tasks Persona hands out. Nobody waits on it.",
};
// the last 7 days' calls (#10498): 1,234 calls, 61.7K + 148K + 5.43M = 5.64M tokens without the cache reads
const usage = { calls: 1234, failures: 12, input_tokens: 61_700, output_tokens: 148_080, cache_read_input_tokens: 24_680_000, cache_creation_input_tokens: 5_430_000 };
// a purpose whose calls all went through: no failure count on its line
const cleanUsage = { ...usage, calls: 3, failures: 0 };
const described = (p: ModelPurpose): ModelPurpose => ({ ...p, description: DESCRIPTIONS[p.id] });
/** The paths the page asked the hub for, in order. */
const requested = () => vi.mocked(fetch).mock.calls.map(([input]) => String(input).split("?")[0]);
const hub = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const path = String(input).split("?")[0];
  if (init?.method === "PATCH") {
    patches.push(JSON.parse(String(init.body)));
    return json({ purpose: { ...supervise, override: { model: "small-model" }, effective: { ...supervise.effective, model: "small-model" }, revision: 2 }, providers });
  }
  // a hub since #5806 lists the row last among its own purposes, each with its description since #5086
  if (path.endsWith("/model-config")) return json({ purposes: mode === "listed" ? [main, agents, supervise] : mode === "used" ? [{ ...main, usage }, { ...agents, usage: cleanUsage }, supervise] : mode === "described" ? [described(main), described(agents), described(supervise)] : [main, agents], providers });
  if (path.endsWith("/purposes/assistant.supervise")) return mode === "none" ? json({ error: "not found" }, 404) : mode === "failing" ? json({ error: "hub busy" }, 503) : json({ purpose: supervise, providers });
  if (path.endsWith("/models")) return json({ error: "unavailable" }, 404);
  throw new Error(`Unexpected request: ${path}`);
};
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const rows = () => [...host.querySelectorAll<HTMLElement>("[data-model-purpose]")];
async function render() {
  function Harness() {
    const [open, onOpen] = useState<string | null>(null);
    return <ModelConfiguration open={open} onOpen={onOpen} onProviders={() => {}} />;
  }
  await act(async () => root.render(<Harness />));
  await flush();
}
beforeEach(async () => {
  await initI18n("en");
  mode = "detail";
  patches = [];
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  await initI18n("zh-Hans");
});

describe("the background supervisor's model purpose (#5082)", () => {
  it("is the row after Main chat, in its group, following it, with what it is for", async () => {
    await render();
    expect(rows().map((r) => r.dataset.modelPurpose)).toEqual(["assistant", "assistant.supervise", "agent.new"]);
    const [mainRow, row] = rows();
    expect(row.parentElement).toBe(mainRow.parentElement); // one group
    expect(row.textContent).toContain("Background supervisor");
    expect(row.textContent).toContain("Follows Main chat");
    expect(row.querySelector('[data-testid="purpose-description"]')?.textContent).toContain("Follows up and reviews the tasks Persona hands out");
    expect(mainRow.querySelector('[data-testid="purpose-description"]')).toBeNull();
  });

  it("opens its own editor and saves its own record", async () => {
    await render();
    await act(async () => [...rows()[1].querySelectorAll("button")].find((b) => b.textContent?.includes("Background supervisor"))!.click());
    await flush();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog.textContent).toContain("Background supervisor");
    await act(async () => [...dialog.querySelectorAll("button")].find((b) => b.textContent === "Save")!.click());
    await flush();
    expect(patches).toEqual([{ revision: 1, provider: null, model: null, effort: null }]);
    expect(vi.mocked(fetch).mock.calls.some(([input, init]) => (init as RequestInit | undefined)?.method === "PATCH" && String(input).endsWith("/model-config/purposes/assistant.supervise"))).toBe(true);
  });

  it("a hub that lists it in the catalog (#5806): one row, moved up to right after Main chat, and no request by its id", async () => {
    mode = "listed";
    await render();
    // the hub lists it last among its own purposes
    expect(rows().map((r) => r.dataset.modelPurpose)).toEqual(["assistant", "assistant.supervise", "agent.new"]);
    expect(rows()[1].parentElement).toBe(rows()[0].parentElement);
    expect(requested().filter((p) => p.endsWith("/purposes/assistant.supervise"))).toEqual([]);
    expect(requested().filter((p) => p.endsWith("/model-config"))).toHaveLength(1);
  });

  it("a hub that leaves it out of the catalog is still asked by its id, once", async () => {
    await render();
    expect(requested().filter((p) => p.endsWith("/purposes/assistant.supervise"))).toHaveLength(1);
  });

  it("shows each purpose's description under its name, the hub's own (#5806)", async () => {
    mode = "described";
    await render();
    const [mainRow, row, agentsRow] = rows();
    const description = (r: HTMLElement) => r.querySelector<HTMLElement>('[data-testid="purpose-description"]');
    expect(description(mainRow)!.textContent).toBe("Talks with you, splits work into tasks and hands them out.");
    // the hub's sentence, not this page's own for the background supervisor
    expect(description(row)!.textContent).toBe("Follows up and reviews the tasks Persona hands out. Nobody waits on it.");
    expect(description(agentsRow)!.textContent).toBe("The agents that do the tasks on your computers.");
    // right under the name: before the model and the pill that says how it runs
    const hint = description(mainRow)!.parentElement!;
    expect(hint.firstElementChild).toBe(description(mainRow));
    expect(hint.previousElementSibling!.textContent).toBe("Main chat");
  });

  it("has no row on a hub before it, and the rest loads as before", async () => {
    mode = "none";
    await render();
    expect(rows().map((r) => r.dataset.modelPurpose)).toEqual(["assistant", "agent.new"]);
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("says the page failed when the purpose could not be read, not that the hub has none", async () => {
    mode = "failing";
    await render();
    expect(rows()).toEqual([]);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("hub busy");
  });
});

describe("a purpose's model usage (#10498)", () => {
  it("shows the last 7 days' calls, tokens and failed calls, says what it counts, and shows nothing without calls", async () => {
    mode = "used";
    await render();
    const [mainRow, superviseRow, agentsRow] = rows();
    const line = mainRow.querySelector<HTMLElement>('[data-testid="purpose-usage"]')!;
    expect(line.textContent).toBe("Last 7 days: 1,234 calls · 5.64M tokens · 12 failed");
    expect(line.querySelector('[data-testid="purpose-usage-failures"]')!.className).toContain("text-red");
    expect(line.title).toContain("24.7M cache read");
    expect(line.title).toContain("cache reads are left out");
    expect(line.title).toContain("failed calls: 12");
    const clean = agentsRow.querySelector<HTMLElement>('[data-testid="purpose-usage"]')!;
    expect(clean.textContent).toBe("Last 7 days: 3 calls · 5.64M tokens");
    expect(clean.querySelector('[data-testid="purpose-usage-failures"]')).toBeNull();
    expect(superviseRow.querySelector('[data-testid="purpose-usage"]')).toBeNull();
  });
});
