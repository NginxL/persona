import { afterEach, describe, expect, it } from "vitest";
import { endPull, PULL_BUSY_MS, PULL_RETRY_MAX_MS, PULL_RETRY_MS, pullKey, pullWait, resetPullGatesForTests, retryDelay, startPull } from "./pull-gate";

afterEach(resetPullGatesForTests);

describe("the pull gate (#13677)", () => {
  const key = pullKey("after", "m", "s");

  it("lets one ask out at a time per key", () => {
    expect(startPull(key, 0)).toBe(0);
    expect(startPull(key, 0)).toBe(PULL_BUSY_MS);
    expect(startPull(pullKey("inputs", "m", "s"), 0)).toBe(0); // another read of the session
    expect(startPull(pullKey("after", "m", "other"), 0)).toBe(0); // another session
    endPull(key, "ok", 0);
    expect(startPull(key, 0)).toBe(0);
  });

  it("waits longer after each failure, up to the cap, and starts over after a success", () => {
    let now = 0;
    const waits: number[] = [];
    for (let i = 0; i < 12; i++) {
      expect(startPull(key, now)).toBe(0);
      endPull(key, "failed", now);
      const w = pullWait(key, now);
      waits.push(w);
      expect(startPull(key, now + w - 1)).toBe(1); // not before the wait is over
      now += w;
    }
    expect(waits.slice(0, 4)).toEqual([PULL_RETRY_MS, 2 * PULL_RETRY_MS, 4 * PULL_RETRY_MS, 8 * PULL_RETRY_MS]);
    expect(Math.max(...waits)).toBe(PULL_RETRY_MAX_MS);
    expect(startPull(key, now)).toBe(0);
    endPull(key, "ok", now);
    expect(pullWait(key, now)).toBe(0);
  });

  it("does not count an aborted ask", () => {
    expect(startPull(key, 0)).toBe(0);
    endPull(key, "aborted", 0);
    expect(pullWait(key, 0)).toBe(0);
    expect(retryDelay(0)).toBe(0);
  });
});
