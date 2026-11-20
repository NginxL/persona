// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TaskExecutionFields } from "./TaskExecutionFields";
import { blankTask } from "../../lib/routines";
import { getState } from "../../lib/store";
import { chooseManager, MANAGER_HEADER, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Read = { path: string; manager: string | null; resolve: (response: Response) => void };
let host: HTMLDivElement, root: Root;
const reads: Read[] = [];
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const purpose = (model: string) => ({ purpose: { machine_id: "box", effective: { provider: "launcher.shared", model } } });
const launchers = { launchers: [{ name: "shared", agent: "codex" }], default: null };
const models = (model: string) => ({ agent: "codex", models: [{ id: model, name: model }] });
const reply = async (read: Read, body: unknown, status = 200) => { await act(async () => read.resolve(json(body, status))); await flush(); };
const read = (manager: string, kind: string) => {
  const found = reads.find((r) => r.manager === manager && r.path.includes(kind));
  expect(found, `${manager} ${kind}`).toBeTruthy();
  return found!;
};
const render = async () => {
  await act(async () => root.render(<TaskExecutionFields draft={blankTask()} disabled={false} onChange={() => {}} />));
  await act(async () => { const el = host.querySelector("details")!; el.open = true; el.dispatchEvent(new Event("toggle")); });
  await flush();
};
beforeEach(() => {
  reads.length = 0;
  resetManagerChoice();
  Object.assign(getState(), { machines: [
    { id: "box", name: "First computer", manager: "first", online: true },
    { id: "box", name: "Second computer", manager: "second", online: true },
  ] });
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/assistant") && !String(input).includes("/purposes/")) return Promise.resolve(json({}));
    return new Promise<Response>((resolve) => {
      reads.push({ path: String(input), manager: new Headers(init?.headers).get(MANAGER_HEADER), resolve });
    });
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount()); host.remove(); resetManagerChoice(); vi.unstubAllGlobals();
});

it("waits for a known default and refreshes its inherited computer when that default changes", async () => {
  await render();
  expect(reads).toEqual([]);
  await act(async () => setRosterDefault("first")); await flush();
  await reply(read("first", "/purposes/assistant"), purpose("first-model"));
  await reply(read("first", "/launchers?"), launchers);
  await reply(read("first", "/models?"), models("first-model"));
  expect(host.querySelector("summary")!.textContent).toContain("first-model");
  await act(async () => setRosterDefault("second")); await flush();
  expect(host.textContent).not.toContain("first-model");
  expect(host.textContent).not.toContain("First computer");
  await reply(read("second", "/purposes/assistant"), { purpose: null });
  expect(reads.filter((r) => r.manager === "second").map((r) => r.path)).toEqual([read("second", "/purposes/assistant").path]);
  expect(host.textContent).not.toContain("first-model");
});

it.each(["/purposes/assistant", "/launchers?", "/models?"])("retires a late %s response when another assistant is selected", async (held) => {
  chooseManager("first");
  await render();
  const firstPurpose = read("first", "/purposes/assistant");
  if (held !== "/purposes/assistant") await reply(firstPurpose, purpose("first-model"));
  if (held === "/models?") await reply(read("first", "/launchers?"), launchers);
  const stale = read("first", held);
  await act(async () => chooseManager("second")); await flush();
  expect(host.textContent).not.toContain("first-model");
  expect(host.textContent).not.toContain("First computer");
  await reply(read("second", "/purposes/assistant"), purpose("second-model"));
  await reply(read("second", "/launchers?"), launchers);
  const picker = () => host.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
  expect(picker().closest("fieldset")?.disabled).toBe(true);
  // The old transport deliberately ignores AbortSignal, including after the current read fails.
  await reply(read("second", "/models?"), { error: "Second catalog unavailable" }, 503);
  await reply(stale, held === "/purposes/assistant" ? purpose("stale-model") : held === "/launchers?" ? {
    launchers: [{ name: "stale-launcher" }], default: { provider: "stale-launcher", model: "stale-model" },
  } : models("stale-model"));
  expect(host.textContent).toContain("Second catalog unavailable");
  expect(host.textContent).toContain("second-model");
  expect(host.textContent).not.toContain("stale-model");
  expect(host.textContent).not.toContain("stale-launcher");
  expect(reads.every((r) => r.manager === "first" || r.manager === "second")).toBe(true);
  for (const r of reads.filter((r) => r.path.includes("/launchers?"))) expect(r.path).toContain("purpose=task");
});

it("does not reuse a former assistant's defaults after the current purpose read fails", async () => {
  chooseManager("first"); await render();
  await reply(read("first", "/purposes/assistant"), purpose("first-model"));
  await reply(read("first", "/launchers?"), launchers);
  await reply(read("first", "/models?"), models("first-model"));
  await act(async () => chooseManager("second")); await flush();
  await reply(read("second", "/purposes/assistant"), { error: "Second defaults unavailable" }, 503);
  expect(host.textContent).toContain("Second defaults unavailable");
  expect(host.textContent).not.toContain("first-model");
  expect(reads.filter((r) => r.manager === "second").map((r) => r.path)).toEqual([read("second", "/purposes/assistant").path]);
});
