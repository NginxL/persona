// @vitest-environment jsdom
// The readiness help (ReadinessHelp.tsx) is a chunk of its own (#10538): a ready first screen does not fetch it, a step's
// card that can open it does, so the tap on the card's button draws the dialog in its own render.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { actionLabel, resetReadiness, type Readiness } from "../../lib/readiness";
import { getState, reset } from "../../lib/store";

const loaded = vi.hoisted(() => vi.fn());
vi.mock("./ReadinessHelp", async (original) => {
  loaded();
  return await original<typeof import("./ReadinessHelp")>();
});
const { HelpDialog, ReadinessCard, useReadinessGate } = await import("./Readiness");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ready: Readiness = {
  version: 1,
  mode: "full",
  ready: true,
  next: null,
  machine: { id: "m1", name: "Synthetic Mac" },
  launcher: "claude",
  steps: [
    { id: "computer", state: "ok", machine: "m1" },
    { id: "claude", state: "ok" },
    { id: "model", state: "ok" },
    { id: "first_message", state: "ok" },
  ],
  dismissed: false,
  checked_at: "2026-11-18T09:00:00Z",
};
const signedOut: Readiness = {
  ...ready,
  mode: "setup",
  ready: false,
  next: "model",
  steps: [
    { id: "computer", state: "ok", machine: "m1" },
    { id: "claude", state: "ok" },
    { id: "model", state: "problem", code: "not_logged_in", machine: "m1", launcher: "claude", title: "Claude Code is not signed in", action: { id: "connect_model" }, alt_actions: [{ id: "login_claude" }] },
    { id: "first_message", state: "problem", code: "not_logged_in" },
  ],
};

// stands in for ChatView and PersonaAssistant: the gate, its card and the dialogs it opens
function Holder() {
  const gate = useReadinessGate(true);
  return (
    <>
      <ReadinessCard gate={gate} />
      {gate.dialog}
    </>
  );
}

afterEach(() => {
  reset();
  resetReadiness();
  vi.unstubAllGlobals();
});

it("fetches the help's chunk once a card that opens it is on screen, so its tap draws the help at once", async () => {
  let current = ready;
  const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(json(current))));
  Object.assign(getState(), { features: ["assistant_readiness"] });
  resetReadiness(ready);
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => root.render(<Holder />));
  // ready: nothing on screen opens the help
  expect(host.querySelector('[data-testid="readiness-card"]')).toBeNull();
  expect(loaded).not.toHaveBeenCalled();

  // a missing step: its card is up, and the help's chunk is on its way before any tap
  current = signedOut;
  await act(async () => resetReadiness(signedOut));
  expect(host.querySelector('[data-testid="readiness-card"]')).not.toBeNull();
  await vi.waitFor(() => expect(loaded).toHaveBeenCalledTimes(1));
  await act(() => HelpDialog.preload()); // the load the card started (lazyPage loads once), now in

  const how = [...host.querySelectorAll("button")].find((b) => b.textContent === actionLabel("login_claude"));
  expect(how).toBeTruthy();
  act(() => how!.click());
  expect(document.body.querySelector('[role="dialog"]')?.textContent).toContain("claude auth login");
  await act(async () => root.unmount());
  host.remove();
});
