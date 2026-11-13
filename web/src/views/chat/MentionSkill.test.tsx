// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { getContexts, removeContext } from "../../lib/links";
import { sendDraft } from "../../lib/drafts";
import { parseQueued } from "../../lib/queue";
import { restoreQueued } from "./QueueStrip";
import { skillContext, skillDraft } from "../../lib/skill-mentions";
import { Composer, type Outgoing } from "./Composer";
import type { MentionSource } from "./ComposerPanel";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: query === "(any-pointer: fine)", media: query, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const skills = [
  { id: "a", name: "review", description: "Inspect security boundaries", source: "project · ." },
  { id: "b", name: "review", description: "Check wording", source: "user" },
];
const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({ catalog_version: 1, complete: true, skills }));
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: (path: string, init?: unknown) => api(path, init) }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const source: MentionSource = { files: () => [], skills: { manager: "helper-a", session: "chat", launcher: "codex", environment: "profile-a" } };
const onSend = vi.fn(async (_m: Outgoing) => true);
const render = (mention = source) => act(() => root.render(<Composer draftKey="skill-test" placeholder="" mention={mention} machineId="computer" onSend={onSend} />));
const box = () => host.querySelector("textarea")!;
const options = () => [...host.querySelectorAll<HTMLButtonElement>('[role="option"]')];
const waitForOptions = (count: number) => vi.waitFor(async () => {
  await act(async () => {});
  expect(options()).toHaveLength(count);
});
async function type(text: string) {
  await act(async () => {
    box().focus();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), text);
    box().setSelectionRange(text.length, text.length);
    box().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const key = (k: string) => act(() => { box().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); });
beforeAll(() => loadPages());
beforeEach(() => {
  Element.prototype.scrollIntoView ??= () => {};
  localStorage.clear();
  getContexts("skill-test").forEach((c) => removeContext("skill-test", c.id));
  api.mockClear();
  onSend.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

it("searches name/description locally, disambiguates equal names, selects with keys and retains explicit identity on send", async () => {
  render();
  await type("@");
  await waitForOptions(3); // two skills and the existing Add files action
  expect(options().map((o) => o.textContent)).toEqual(expect.arrayContaining([expect.stringContaining("project · ."), expect.stringContaining("user")]));
  await type("@security");
  expect(options()).toHaveLength(1);
  expect(api).toHaveBeenCalledTimes(1);
  expect(api.mock.calls[0][1]).toMatchObject({ headers: { "X-Persona-Manager": "helper-a" } });
  key("Enter");
  expect(box().value).toBe("");
  expect(host.textContent).toContain("review · project · .");
  await type("Check this");
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(onSend.mock.calls[0][0].text).toContain('kind="skill" title="review" url="" ref="a"');
  expect(localStorage.getItem("draft:skill-test") ?? "").toBe("");
  key("ArrowUp");
  expect(box().value).toBe("Check this");
  expect(getContexts("skill-test")[0].attrs?.ref).toBe("a");
  key("ArrowDown");
  expect(box().value).toBe("");
  expect(getContexts("skill-test")).toHaveLength(0);
});

it("supports touch/click removal, restores edit/queue references and persists the selected identity in the draft", async () => {
  render();
  await type("@wording");
  await waitForOptions(1);
  await act(async () => options()[0].click());
  await type("Use this");
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 280)); });
  const draft = localStorage.getItem("draft:skill-test")!;
  expect(draft).toContain('ref="b"');
  act(() => sendDraft("skill-test", { type: "replace", text: draft }));
  expect(box().value).toBe("Use this");
  expect(getContexts("skill-test").filter((c) => c.kind === "skill")).toHaveLength(1);
  act(() => host.querySelector<HTMLButtonElement>('button[aria-label="移除这个上下文"]')!.click());
  expect(getContexts("skill-test")).toHaveLength(0);
});

it.each(["manager", "directory"])("never offers stale rows across %s switches, ignores late responses, and allows ordinary chat on failure", async (change) => {
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
  render();
  await type("@review");
  await vi.waitFor(() => expect(resolve).toBeTypeOf("function"));
  render(change === "directory" ? { ...source, cwd: "/example/changed" } : { ...source, skills: { ...source.skills!, manager: "helper-b", environment: "profile-b" } });
  await waitForOptions(2);
  await act(async () => resolve({ catalog_version: 1, complete: true, skills: [{ ...skills[0], name: "stale" }] }));
  expect(host.textContent).not.toContain("stale");
  api.mockImplementationOnce(async () => { throw new Error("unavailable"); });
  key("Escape");
  await type("text");
  await type("@review");
  expect(options()).toHaveLength(0);
  expect(host.querySelector('[role="status"]')?.textContent).toBeTruthy();
  await type("Email me@example.com and literal @review");
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(onSend.mock.calls[0][0].text).toBe("Email me@example.com and literal @review");
});

it("retains the exact reference through queue pullback and refused sends", async () => {
  render();
  const raw = skillDraft([skillContext(skills[1])], "Queued words");
  await act(async () => restoreQueued("skill-test", { machine_id: "computer", session_id: "chat" } as import("../../lib/types").Session, parseQueued(raw)));
  expect(box().value).toBe("Queued words");
  expect(getContexts("skill-test")[0].attrs?.ref).toBe("b");
  onSend.mockImplementationOnce(async () => false);
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(box().value).toBe("Queued words");
  expect(getContexts("skill-test")[0].attrs?.ref).toBe("b");
});

it("uses the ordinary session catalog and retains its selection on a refused send", async () => {
  render({ ...source, skills: { ...source.skills!, agent: true } });
  await type("@security");
  await waitForOptions(1);
  expect(api.mock.calls[0][0]).toBe("/sessions/computer/chat/skills");
  key("Enter");
  await type("Review the change");
  onSend.mockImplementationOnce(async () => false);
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(onSend.mock.calls[0][0].text).toContain('ref="a"');
  expect(getContexts("skill-test")[0].attrs?.ref).toBe("a");
  expect(box().value).toBe("Review the change");
});

it("keeps existing file rows reachable beside a large skill catalog and searches beyond the empty preview", async () => {
  api.mockImplementationOnce(async () => ({ catalog_version: 1, complete: true,
    skills: Array.from({ length: 50 }, (_, i) => ({ ...skills[0], id: `skill-${i}`, name: `review-${i}` })),
  }));
  render({ ...source, files: () => ["/example/report.ts"] });
  await type("@");
  await waitForOptions(5); // three skill previews, the existing file, and Add files
  expect(options().some((o) => o.textContent?.includes("report.ts"))).toBe(true);
  expect(options().filter((o) => o.textContent?.includes("review-"))).toHaveLength(3);
  await type("@review-49");
  expect(options()).toHaveLength(1);
  expect(options()[0].textContent).toContain("review-49");
  expect(api).toHaveBeenCalledTimes(1);
});
