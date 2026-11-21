import { describe, expect, it } from "vitest";
import { heldLaunchRows, heldLaunchWorking, isInFlightLaunch, LAUNCH_HANDOFF_MS, launchHasFirstTurn, launchHandoffSettled, nextLaunchExpiry, settledLaunches } from "./launch-handoff";
import type { AgentLaunch } from "./new-agent";
import type { Session } from "./types";

const T0 = Date.parse("2026-11-21T08:00:00Z");

const sess = (over: Partial<Session> = {}): Session => ({
  machine_id: "m1",
  session_id: "s-new",
  title: "New Agent",
  cwd: "/code/app",
  status: "idle",
  state: "idle",
  ...over,
});

const launch = (over: Partial<AgentLaunch> = {}): AgentLaunch => ({
  id: "local-1",
  machineId: "m1",
  cwd: "/code/app",
  launcher: "claude",
  text: "Fix issue",
  attachments: [],
  phase: "ready",
  session: sess(),
  uploaded: {},
  startOpId: "op-1",
  startPromptOpId: "op-2",
  messageId: "msg-1",
  handoffAt: T0,
  ...over,
});

describe("launch-handoff (#12380)", () => {
  it("identifies launches with a first turn vs empty starts", () => {
    expect(launchHasFirstTurn({ text: "hello", attachments: [] })).toBe(true);
    expect(launchHasFirstTurn({ text: "  \n ", attachments: [] })).toBe(false);
    expect(launchHasFirstTurn({ text: "", attachments: [], outgoing: { text: "hi", images: [] } })).toBe(true);
    expect(launchHasFirstTurn({ text: "", attachments: [], outgoing: { text: "", images: [{ mime: "image/png", data: "AQ==" }] } })).toBe(true);
  });

  it("identifies actively starting/sending launches", () => {
    expect(isInFlightLaunch(launch({ phase: "creating" }))).toBe(true);
    expect(isInFlightLaunch(launch({ phase: "uploading" }))).toBe(true);
    expect(isInFlightLaunch(launch({ phase: "sending" }))).toBe(true);
    expect(isInFlightLaunch(launch({ phase: "ready" }))).toBe(false);
    expect(isInFlightLaunch(launch({ phase: "failed" }))).toBe(false);
  });

  it("holds a newly launched idle or transient unread session in working state until the hub confirms working", () => {
    const l = launch();
    const idleHeld = heldLaunchWorking(sess({ state: "idle", status: "idle" }), l, T0 + 1_000);
    expect(idleHeld.state).toBe("working");
    expect(idleHeld.status).toBe("working");

    const unreadHeld = heldLaunchWorking(sess({ state: "unread", status: "idle" }), l, T0 + 1_000);
    expect(unreadHeld.state).toBe("working");
    expect(unreadHeld.status).toBe("working");

    // No handoffAt (empty start) or failed launch is never forced into working.
    const emptyStart = sess({ state: "idle", status: "idle" });
    expect(heldLaunchWorking(emptyStart, launch({ handoffAt: undefined }), T0 + 1_000)).toBe(emptyStart);
    expect(heldLaunchWorking(emptyStart, launch({ phase: "failed" }), T0 + 1_000)).toBe(emptyStart);
  });

  it("settles as soon as the hub reports working, needs_you, offline, errors, a reply, or after the handoff timeout", () => {
    const l = launch();
    expect(launchHandoffSettled(sess({ state: "idle" }), l, T0 + 5_000)).toBe(false);
    expect(launchHandoffSettled(sess({ state: "working", status: "working" }), l, T0 + 5_000)).toBe(true);
    expect(launchHandoffSettled(sess({ state: "needs_you", status: "blocked" }), l, T0 + 5_000)).toBe(true);
    expect(launchHandoffSettled(sess({ state: "offline", status: "offline" }), l, T0 + 5_000)).toBe(true);
    expect(launchHandoffSettled(sess({ state: "idle", start_error: "boom" }), l, T0 + 5_000)).toBe(true);
    expect(launchHandoffSettled(sess({ state: "idle", failure_reason: "boom" }), l, T0 + 5_000)).toBe(true);
    expect(launchHandoffSettled(sess({ state: "unread", last_text: "All done." }), l, T0 + 5_000)).toBe(true);
    expect(launchHandoffSettled(sess({ state: "idle" }), l, T0 + LAUNCH_HANDOFF_MS)).toBe(true);
  });

  it("clears handoffAt once working is observed so a later working -> unread transition is never masked", () => {
    const initial = { "local-1": launch() };
    // Before hub reports working, handoffAt remains set and rows project as working.
    const stillPending = settledLaunches(initial, [sess({ state: "idle" })], T0 + 1_000);
    expect(stillPending).toBe(initial);
    expect(heldLaunchRows([sess({ state: "idle" })], stillPending, T0 + 1_000)[0].state).toBe("working");

    // Once hub reports working, handoffAt settles.
    const afterWorking = settledLaunches(stillPending, [sess({ state: "working", status: "working" })], T0 + 2_000);
    expect(afterWorking["local-1"].handoffAt).toBeUndefined();
    expect(nextLaunchExpiry(afterWorking, T0 + 2_000)).toBeUndefined();

    // When the turn later completes as unread, it stays unread.
    const finished = sess({ state: "unread", status: "idle" });
    expect(heldLaunchRows([finished], afterWorking, T0 + 3_000)[0].state).toBe("unread");
  });
});
