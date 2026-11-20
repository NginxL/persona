// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HiddenAgentDirs, hiddenPatternError, type HiddenAgentDirsState } from "./HiddenAgentDirs";
import { t } from "../../i18n";

const calls: { path: string; method: string; json?: HiddenAgentDirsState; resolve: (v: unknown) => void; reject: (e: Error) => void }[] = [];
vi.mock("../../lib/api", async (orig) => ({ ...await orig<typeof import("../../lib/api")>(), api: (path: string, options?: { method: string; json: HiddenAgentDirsState }) => new Promise((resolve, reject) => calls.push({ path, method: options?.method ?? "GET", json: options?.json, resolve, reject })) }));
const scope = vi.hoisted(() => ({ machines: [] as { id: string; name: string; online: boolean }[] }));
const bothMachines = [{ id: "computer", name: "Computer", online: false }, { id: "other", name: "Other", online: true }];
vi.mock("../../lib/current-assistant", () => ({ useScopedMachines: () => scope.machines }));
let host: HTMLDivElement, root: Root;
beforeEach(() => {
 (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
 calls.length = 0; scope.machines = bothMachines; host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });
const original: HiddenAgentDirsState = { rules: [{ pattern: "~/work/*", enabled: true }, { pattern: "*scratch*", enabled: false }], home: "/home/me" };
async function show() {
 await act(async () => root.render(<HiddenAgentDirs />));
 await act(async () => calls[0].resolve(original));
}
it("loads an offline computer's own list and toggles one rule by saving that computer's whole list", async () => {
 await show();
 expect(calls[0].path).toBe("/machines/computer/agent-dirs/hidden");
 const sw = host.querySelector<HTMLButtonElement>('[role="switch"]')!;
 expect(sw.getAttribute("aria-checked")).toBe("true");
 await act(async () => sw.click());
 expect(sw.getAttribute("aria-checked")).toBe("false");
 expect(sw.disabled).toBe(true);
 expect(calls[1]).toMatchObject({ path: "/machines/computer/agent-dirs/hidden", method: "PUT", json: { rules: [{ ...original.rules[0], enabled: false }, original.rules[1]] } });
 await act(async () => calls[1].resolve(calls[1].json));
 expect(sw.disabled).toBe(false);
});
it("switching computers loads the other computer's list", async () => {
 await show();
 const select = host.querySelector<HTMLSelectElement>("select")!;
 await act(async () => { select.value = "other"; select.dispatchEvent(new Event("change", { bubbles: true })); });
 expect(calls.at(-1)!.path).toBe("/machines/other/agent-dirs/hidden");
 await act(async () => calls.at(-1)!.resolve({ rules: [] }));
 expect(host.textContent).toContain(t("hidden-agent-dirs.empty"));
});
it("rolls back rejected changes and displays a retryable failure", async () => {
 await show();
 await act(async () => host.querySelector<HTMLButtonElement>('[role="switch"]')!.click());
 await act(async () => calls[1].reject(new Error("unavailable")));
 expect(host.querySelector('[role="switch"]')!.getAttribute("aria-checked")).toBe("true");
 expect(host.textContent).toContain("unavailable");
 expect(host.textContent).toContain(t("hidden-agent-dirs.retry"));
});
it("says why a typed folder cannot be added", () => {
 expect(hiddenPatternError("relative", [])).toBe(t("hidden-agent-dirs.pattern_prefix"));
 expect(hiddenPatternError("~/", [])).toBe(t("hidden-agent-dirs.pattern_root"));
 expect(hiddenPatternError("~/work/*/", original.rules)).toBe(t("hidden-agent-dirs.pattern_exists"));
 expect(hiddenPatternError("*botfarm*", original.rules)).toBe("");
});
it("adds a new rule as Block unless Hide is chosen, and switches an existing rule's action (#10053)", async () => {
 await show();
 const picker = (label: string) => host.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!;
 // A rule saved before blocking (no action) hides.
 expect(picker(`${t("hidden-agent-dirs.action")}: ~/work/*`).value).toBe("hide");
 expect(picker(t("hidden-agent-dirs.action")).value).toBe("block");
 const input = host.querySelector<HTMLInputElement>(`input[aria-label="${t("hidden-agent-dirs.pattern")}"]`)!;
 const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
 await act(async () => { setter.call(input, "~/.bots/runs"); input.dispatchEvent(new Event("input", { bubbles: true })); });
 const add = [...host.querySelectorAll("button")].find((b) => b.textContent === t("hidden-agent-dirs.add"))!;
 await act(async () => add.click());
 expect(calls[1]).toMatchObject({ method: "PUT", json: { rules: [...original.rules, { pattern: "~/.bots/runs", enabled: true, action: "block" }] } });
 await act(async () => calls[1].resolve(calls[1].json));
 expect(host.textContent).toContain(t("hidden-agent-dirs.enabled_block"));
 const first = picker(`${t("hidden-agent-dirs.action")}: ~/work/*`);
 await act(async () => { first.value = "block"; first.dispatchEvent(new Event("change", { bubbles: true })); });
 expect(calls[2]).toMatchObject({ method: "PUT", json: { rules: [{ ...original.rules[0], action: "block" }, original.rules[1], { pattern: "~/.bots/runs", enabled: true, action: "block" }] } });
});
// #8584: input that cleans to the root or to a pattern matching every folder is refused, not silently dropped;
// only blank input says nothing.
it("refuses the root and patterns that match every folder", () => {
 const root = t("hidden-agent-dirs.pattern_root");
 for (const p of ["/", "//", "*", "**", "/*", "/**", "//*/", "~/", "~//"]) expect(hiddenPatternError(p, []), p).toBe(root);
 for (const p of ["/opt/x/", "*botfarm*", "~/*"]) expect(hiddenPatternError(p, []), p).toBe("");
 expect(hiddenPatternError("", [])).toBe("");
 expect(hiddenPatternError("   ", [])).toBe("");
});
it("disables Add for input that cleans to the root, and saves a cleaned folder", async () => {
 await show();
 const input = host.querySelector<HTMLInputElement>('input[aria-label]')!;
 const type = (value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
 });
 const addButton = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("hidden-agent-dirs.add"))!;
 await type("//");
 expect(addButton().disabled).toBe(true);
 expect(host.textContent).toContain(t("hidden-agent-dirs.pattern_root"));
 await type("/opt/x//");
 expect(addButton().disabled).toBe(false);
 await act(async () => addButton().click());
 expect(calls.at(-1)).toMatchObject({ method: "PUT", json: { rules: [...original.rules, { pattern: "/opt/x", enabled: true }] } });
});

// #8584: a save is bound to the computer and load it started from. When the shown computer changes (the scope's
// machines change, or Retry reloads) while a PUT is out, its answer or rollback must not land in the new view, and the
// new view is not locked behind it.
describe("a save still out when the view changes", () => {
 const otherRules: HiddenAgentDirsState = { rules: [{ pattern: "/opt/other", enabled: true }] };
 const sw = () => host.querySelector<HTMLButtonElement>('[role="switch"]')!;
 async function pendingSaveThenOnlyOther() {
  await show();
  await act(async () => sw().click());
  expect(calls[1]).toMatchObject({ path: "/machines/computer/agent-dirs/hidden", method: "PUT" });
  scope.machines = [bothMachines[1]]; // the first computer leaves the scope: "other" is shown now
  await act(async () => root.render(<HiddenAgentDirs />));
  expect(calls.at(-1)).toMatchObject({ path: "/machines/other/agent-dirs/hidden", method: "GET" });
 }
 async function expectOtherView() {
  expect(host.textContent).toContain("/opt/other");
  expect(host.textContent).not.toContain("~/work/*");
  expect(sw().disabled).toBe(false);
  await act(async () => sw().click());
  expect(calls.at(-1)).toMatchObject({ path: "/machines/other/agent-dirs/hidden", method: "PUT", json: { rules: [{ pattern: "/opt/other", enabled: false }] } });
 }

 it.each(["resolves", "rejects"] as const)("ignores the first computer's PUT that %s after the view moved on", async (outcome) => {
  await pendingSaveThenOnlyOther();
  await act(async () => calls[2].resolve(otherRules));
  if (outcome === "resolves") await act(async () => calls[1].resolve(calls[1].json));
  else await act(async () => calls[1].reject(new Error("unavailable")));
  expect(host.textContent).not.toContain("unavailable");
  await expectOtherView();
 });

 it("ignores the old PUT after a Retry reloaded the view", async () => {
  await pendingSaveThenOnlyOther();
  await act(async () => calls[2].reject(new Error("offline")));
  const retry = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("hidden-agent-dirs.retry"))!;
  await act(async () => retry.click());
  expect(calls[3]).toMatchObject({ path: "/machines/other/agent-dirs/hidden", method: "GET" });
  await act(async () => calls[3].resolve(otherRules));
  await act(async () => calls[1].resolve(calls[1].json));
  await expectOtherView();
 });
});
