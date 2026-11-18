// @vitest-environment jsdom
// A plan started with plan_then goes on in that recorded mode (#3784): the card names it, offers no competing approval
// modes, and both its Approve button and Enter send it. A plan without the field keeps the existing choices and default.
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
    return {};
  }),
}));
import { InteractionCard } from "./Interactions";
import { t } from "../../i18n";
import { modeTitle } from "../../lib/session-actions";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Parse the newer wire field instead of asserting it into Interaction: this must reach the old card at runtime.
const planWithThen = JSON.parse(
  '{"id":"pl_then","machine_id":"m","session_id":"s","kind":"plan","tool_name":"ExitPlanMode","plan":"1. Finish the work","plan_then":"bypassPermissions"}',
) as Interaction;
const planWithAutoThen = { ...planWithThen, id: "pl_auto", plan_then: "auto" };
const legacyPlan = JSON.parse(
  '{"id":"pl_legacy","machine_id":"m","session_id":"s","kind":"plan","tool_name":"ExitPlanMode","plan":"1. Finish the work"}',
) as Interaction;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const show = (ix: Interaction) => act(() => root.render(<InteractionCard ix={ix} />));
const approve = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("chat-interactions.approve"))!;
const modeButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-interactions.approve_mode")}"]`);
const menuRows = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] [role="menuitem"]')];

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("a plan card with a recorded following mode (#3784)", () => {
  it("shows plan_then by its mode name and offers no conflicting approval-mode menu", () => {
    show(planWithThen);
    expect(host.textContent).toContain(modeTitle("bypassPermissions"));
    expect(modeButton()).toBeNull();
  });

  it("treats auto as a fixed following mode", async () => {
    show(planWithAutoThen);
    expect(host.textContent).toContain(modeTitle("auto"));
    expect(modeButton()).toBeNull();
    act(() => approve().click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/pl_auto/resolve", body: { approve: true, mode: "auto" } }]);
  });

  it.each(["the Approve button", "Enter"] as const)("approves with plan_then from %s", async (action) => {
    show(planWithThen);
    if (action === "the Approve button") {
      act(() => approve().click());
    } else {
      const card = host.querySelector<HTMLElement>("[data-card-keys]")!;
      act(() => card.focus());
      act(() => card.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
    }
    await flush();
    expect(calls).toEqual([{ path: "/interactions/pl_then/resolve", body: { approve: true, mode: "bypassPermissions" } }]);
  });
});

describe("a plan card without a recorded following mode", () => {
  it("keeps Approve's existing acceptEdits default", async () => {
    show(legacyPlan);
    act(() => approve().click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/pl_legacy/resolve", body: { approve: true, mode: "acceptEdits" } }]);
  });

  it("keeps all three existing approval-mode choices", async () => {
    show(legacyPlan);
    act(() => modeButton()!.click());
    expect(menuRows().map((row) => row.textContent)).toEqual([
      t("chat-interactions.mode_accept_edits"),
      t("chat-interactions.mode_bypass"),
      t("chat-interactions.mode_default"),
    ]);
    act(() => menuRows().find((row) => row.textContent === t("chat-interactions.mode_default"))!.click());
    await flush();
    expect(calls).toEqual([{ path: "/interactions/pl_legacy/resolve", body: { approve: true, mode: "default" } }]);
  });
});
