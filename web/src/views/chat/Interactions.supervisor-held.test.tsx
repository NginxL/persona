// @vitest-environment jsdom
// A card a task's supervisor holds (lib/supervisor-held.ts: `delegating` on a card that is not a question) is the
// supervisor's until it passes it up: the chat shows what it asks, read-only, with a line saying the supervisor decides
// it, and never its answers (Yes / No, Allow / Deny, hand back). The same card without the mark is the user's as before.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return { ok: true };
  }),
}));
import { InteractionCard } from "./Interactions";
import { answerCard } from "../../lib/card-answer";
import { getState, reset } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const screen: Interaction = {
  id: "screen_1",
  machine_id: "m",
  session_id: "s",
  kind: "screen",
  dialog: { question: "Do you want to proceed?", detail: "rmdir build", options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }] },
};
const permission: Interaction = { id: "perm_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: "rm -rf build" } };

let host: HTMLDivElement;
let root: Root;
const labels = () => [...host.querySelectorAll("button")].map((b) => b.textContent?.trim() ?? "");
const offers = (label: string) => labels().some((l) => l.includes(label));
const render = (ix: Interaction) => act(() => root.render(<InteractionCard ix={ix} />));

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s", agent: "claude", status: "blocked", state: "needs_you" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.restoreAllMocks();
});

describe("a card the task's supervisor holds", () => {
  it("shows a terminal dialog read-only, saying the supervisor handles it, with no Yes / No or hand back", () => {
    const held = { ...screen, delegating: true };
    getState().interactions = [held];
    render(held);
    const card = host.querySelector<HTMLElement>("[data-card]")!;
    expect(card.textContent).toContain("Do you want to proceed?");
    expect(card.textContent).toContain("rmdir build");
    expect(card.textContent).toContain(t("chat-delegate.supervisor_held"));
    expect(host.querySelector("[data-supervisor-held]")?.textContent).toContain(t("chat-delegate.supervisor_held_hint"));
    expect(offers("Yes")).toBe(false);
    expect(offers("No")).toBe(false);
    expect(offers(t("chat-interactions.hand_back"))).toBe(false);
    expect(host.querySelector("button[data-card-answer]")).toBeNull();
    expect(host.querySelector("input, textarea")).toBeNull();
  });

  it("shows a held permission without Allow / Deny", () => {
    const held = { ...permission, delegating: true };
    getState().interactions = [held];
    render(held);
    expect(host.querySelector("[data-supervisor-held]")).not.toBeNull();
    expect(offers(t("chat-interactions.allow"))).toBe(false);
    expect(offers(t("chat-interactions.deny"))).toBe(false);
  });

  it("is refused by answerCard without a request, whatever page asks", async () => {
    const r = await answerCard({ ...screen, delegating: true }, { option: "1" });
    expect(r).toEqual({ ok: false, error: t("chat-delegate.supervisor_held_refused") });
    expect(calls).toEqual([]);
  });

  it("keeps a terminal dialog without the mark the user's: its Yes / No and hand back answer it", () => {
    getState().interactions = [screen];
    render(screen);
    expect(host.querySelector("[data-supervisor-held]")).toBeNull();
    expect(offers("Yes")).toBe(true);
    expect(offers("No")).toBe(true);
    expect(host.querySelector("button[data-card-answer]")).not.toBeNull();
  });
});
