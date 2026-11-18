import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { assertDelivered, isKept, shownUndelivered } from "./undelivered";

describe("undelivered messages (#10194)", () => {
  it("reads 202 delivered later as kept, never as delivered", () => {
    expect(isKept({ delivered: "later" })).toBe(true);
    expect(isKept({})).toBe(false);
    expect(isKept(undefined)).toBe(false);
  });
  it("turns a bare operation receipt into an in-doubt failure", () => {
    for (const state of ["unknown", "pending"]) {
      let err: unknown;
      try { assertDelivered({ state }); } catch (e) { err = e; }
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).body.delivered).toBe("unknown");
    }
    expect(() => assertDelivered({ delivered: "later" })).not.toThrow();
    expect(() => assertDelivered(undefined)).not.toThrow();
  });
  it("hides a kept message the transcript already has", () => {
    const row = { undelivered: [
      { id: "a", text: "one", at: "2026-11-17T10:00:00Z", state: "waiting" as const },
      { id: "b", text: "two", at: "2026-11-17T10:01:00Z", state: "failed" as const },
    ] };
    expect(shownUndelivered(row, [{ client_msg_id: "a" }, {}]).map((m) => m.id)).toEqual(["b"]);
    expect(shownUndelivered(undefined, [])).toEqual([]);
    expect(shownUndelivered(row, [], ["b"]).map((m) => m.id)).toEqual(["a"]); // still drawn as sending here
  });
});
