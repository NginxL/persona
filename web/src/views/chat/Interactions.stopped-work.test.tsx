// @vitest-environment jsdom
// A manager work card outlives its stopped Agent. Only answers the manager settles locally remain enabled (#6600).
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
import { PendingCards } from "./Interactions";
import { getState, reset, upsertSession, useStore } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const question = "The model service failed. What next?";
const work = {
  id: "work_1", machine_id: "m", session_id: "s", kind: "question", tool_name: "persona_triage",
  questions: [{ question, options: [{ label: "Handled, continue" }, { label: "Leave it", action: "defer" }] }],
} as Interaction;
const permission = { id: "p_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "persona_triage", summary: "Run the tests" } as Interaction;

function Dock() {
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => Object.values(s.answered));
  return <PendingCards interactions={interactions} answered={answered} narrow={false} keyboard={false} onDismissKeyboard={() => {}} />;
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("[data-card] button")];
const button = (label: string) => buttons().find((b) => b.textContent === label || b.getAttribute("aria-label") === label)!;
const locked = (b: HTMLButtonElement) => b.disabled || !!b.closest("fieldset[disabled]");

beforeEach(() => {
  calls.length = 0;
  upsertSession({
    machine_id: "m", session_id: "s", title: "Stopped Agent", status: "offline", state: "needs_you",
    capabilities: { read: true, terminal: true, send: false, approve: false },
  } as Session);
  Object.assign(getState(), { interactions: [work], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

describe("a stopped Agent's manager work card", () => {
  it("allows defer and dismiss, but disables Continue and a custom answer", async () => {
    act(() => root.render(<Dock />));
    expect(locked(button("Handled, continue"))).toBe(true);
    expect(locked(button("Leave it"))).toBe(false);
    expect(locked(button(t("chat-interactions.hand_back")))).toBe(false);
    expect(host.querySelector<HTMLInputElement>(`input[aria-label="${t("chat-interactions.custom_answer")}"]`)?.disabled).toBe(true);
    expect(locked(button(t("chat-interactions.send_answer")))).toBe(true);

    act(() => button("Handled, continue").click());
    expect(calls).toEqual([]);
    act(() => button("Leave it").click());
    expect(locked(button(t("chat-interactions.send_answer")))).toBe(false);
    act(() => button(t("chat-interactions.send_answer")).click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/work_1/resolve", body: { answers: { [question]: "Leave it" } } }]);
    expect(host.querySelector("[data-card]")).toBeNull();
  });

  it("sends a bare dismiss without an Agent answer", async () => {
    act(() => root.render(<Dock />));
    act(() => button(t("chat-interactions.hand_back")).click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/work_1/resolve", body: { dismiss: true } }]);
  });

  it("does not enable a permission card on the same stopped session", () => {
    Object.assign(getState(), { interactions: [permission] });
    act(() => root.render(<Dock />));
    for (const label of [t("chat-interactions.allow"), t("chat-interactions.deny"), t("chat-interactions.hand_back")]) {
      expect(locked(button(label))).toBe(true);
    }
  });
});
