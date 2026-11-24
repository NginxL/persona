// The selector behind Overview's actionable Needs you rows and their count (#12561).
import { describe, expect, it } from "vitest";
import type { ManagerList } from "./managers";
import type { OutboxEntry } from "./outbox";
import { computerNeedsAction, overviewActions } from "./overview-actions";
import type { Machine, Session } from "./types";

const machine = (o: Partial<Machine> = {}): Machine => ({ id: "box", name: "Box", online: false, last_seen: "2026-01-01T00:00:00Z", ...o }) as Machine;
const roster = (online: boolean): ManagerList => ({
  default: "home",
  managers: [
    { id: "home", name: "Home", online: true, default: true },
    { id: "lab", name: "Lab", online },
  ],
});

describe("computerNeedsAction", () => {
  it("asks the owner about an unexpectedly offline computer", () => {
    expect(computerNeedsAction(machine({ manager: "lab" }), roster(true))).toBe(true);
  });

  it("leaves out a computer that is offline only because its manager is (#12561)", () => {
    expect(computerNeedsAction(machine({ manager: "lab" }), roster(false))).toBe(false);
  });

  it("still asks about a computer whose setup is unfinished, whatever its manager", () => {
    expect(computerNeedsAction(machine({ manager: "lab", trust: "pending", last_seen: undefined }), roster(false))).toBe(true);
  });
});

describe("overviewActions", () => {
  it("counts each failed send of a visible Agent and each actionable computer once", () => {
    const session = { machine_id: "box", session_id: "s1", title: "t", cwd: "/", status: "idle" } as Session;
    const failed = (id: string, at: number): OutboxEntry => ({ id, text: "x", images: [], at, state: "failed" }) as OutboxEntry;
    const out = overviewActions({
      outbox: { "box/s1": [failed("b", 2), failed("a", 1), { ...failed("c", 3), state: "sending" }], "box/gone": [failed("d", 4)] },
      sessions: [session],
      machines: [machine({ id: "box" }), machine({ id: "lab-box", manager: "lab" })],
      roster: roster(false),
    });
    expect(out.outbox.map((x) => x.entry.id)).toEqual(["a", "b"]);
    expect(out.computers.map((x) => x.machine.id)).toEqual(["box"]);
    expect(out.count).toBe(3);
  });
});
