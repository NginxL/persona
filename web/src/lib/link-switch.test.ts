// A switch to Persona Link (#10662): which phases count as running, the 202's state against the list's, and that a
// running switch makes the computer's disconnect expected on every page that reads it (machineDeployment, machineAlerts).
import { describe, expect, it } from "vitest";
import { linkSwitchKey, linkSwitchNotice, linkSwitchRunning, shownLinkSwitch, type LinkSwitchAttempt } from "./link-switch";
import { machineAlerts } from "./machine-alerts";
import { machineDeployment } from "./machine-deploy";
import type { Machine, MachineLinkSwitch } from "./types";

describe("link switch state", () => {
  it("counts the three steps and the way back as running, nothing else", () => {
    expect(["checking", "installing", "connecting", "rolling_back"].map((phase) => linkSwitchRunning({ available: false, phase } as MachineLinkSwitch))).toEqual([true, true, true, true]);
    expect(["", "succeeded", "failed", undefined].map((phase) => linkSwitchRunning({ available: true, phase } as MachineLinkSwitch))).toEqual([false, false, false, false]);
    expect(linkSwitchRunning(undefined)).toBe(false);
  });

  it("shows the 202's state until the list is as new by the hub's clock", () => {
    const accepted: MachineLinkSwitch = { available: false, reason: "in_progress", phase: "checking", at: "2026-11-19T08:00:05Z" };
    const before: MachineLinkSwitch = { available: true, phase: "failed", error: "link_timeout", at: "2026-11-19T07:30:00Z" };
    const attempt: LinkSwitchAttempt = { starting: false, accepted, seen: linkSwitchKey(before) };
    expect(shownLinkSwitch(before, attempt)).toBe(accepted);
    const later: MachineLinkSwitch = { available: false, reason: "in_progress", phase: "installing", at: "2026-11-19T08:00:09Z" };
    expect(shownLinkSwitch(later, attempt)).toBe(later);
    // no times (an odd hub): until the list moves at all
    const untimed: LinkSwitchAttempt = { starting: false, accepted: { available: false, phase: "checking" }, seen: linkSwitchKey({ available: true }) };
    expect(shownLinkSwitch({ available: true }, untimed)?.phase).toBe("checking");
    expect(shownLinkSwitch({ available: false, phase: "connecting" }, untimed)?.phase).toBe("connecting");
    // the list says nothing about a switch yet, so it has no `at` (omitted when zero): the 202 holds until it moves
    const fresh: LinkSwitchAttempt = { starting: false, accepted, seen: linkSwitchKey({ available: true }) };
    expect(shownLinkSwitch({ available: true }, fresh)).toBe(accepted);
    expect(shownLinkSwitch(later, fresh)).toBe(later);
    expect(shownLinkSwitch(undefined, attempt)).toBeUndefined();
  });

  it("keeps a notice only while the list has not moved since the start", () => {
    const listed: MachineLinkSwitch = { available: true };
    const attempt: LinkSwitchAttempt = { starting: false, notice: { kind: "uncertain" }, seen: linkSwitchKey(listed) };
    expect(linkSwitchNotice({ ...listed }, attempt)).toEqual({ kind: "uncertain" });
    expect(linkSwitchNotice({ available: false, reason: "in_progress", phase: "checking" }, attempt)).toBeUndefined();
    expect(linkSwitchNotice(listed, { ...attempt, starting: true })).toBeUndefined();
  });

  it("makes the disconnect of a computer being switched expected, and only then", () => {
    const switching = { id: "box", name: "Box", kind: "link", online: false, error: "Persona Link is not connected", link_switch: { available: false, phase: "connecting" } } as Machine;
    expect(machineDeployment(switching).expectedDisconnect).toBe(true);
    expect(machineAlerts({ machine: switching }).map((a) => a.kind)).not.toContain("offline");
    const ended = { ...switching, link_switch: { available: false, phase: "failed", error: "link_timeout" } } as Machine;
    expect(machineDeployment(ended).expectedDisconnect).toBe(false);
    expect(machineAlerts({ machine: ended }).map((a) => a.kind)).toContain("offline");
    const online = { ...switching, online: true } as Machine;
    expect(machineDeployment(online).expectedDisconnect).toBe(false);
  });
});
