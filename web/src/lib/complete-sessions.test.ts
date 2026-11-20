import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "./types";

const api = vi.hoisted(() => vi.fn());
vi.mock("./api", () => {
  class ApiError extends Error {
    constructor(readonly status: number, message: string, readonly body: Record<string, unknown> = {}) { super(message); }
    get code() { return typeof this.body.code === "string" ? this.body.code : undefined; }
  }
  return { api, onCredentialChange: () => () => {}, credentialOwner: () => "test-owner", ApiError, errorText: (e: unknown) => String(e) };
});
import { ApiError } from "./api";
import { CompleteSessions } from "./complete-sessions";

const row = (machine_id: string, session_id: string, title = session_id): Session => ({ machine_id, session_id, title, cwd: "/demo", status: "idle" });
const page = (sessions: Session[], revision: string, extra: Record<string, unknown> = {}) =>
  ({ sessions, remove: [], has_more: false, revision, total: sessions.length, ...extra });
const query = (call: unknown[]) => Object.fromEntries(new URLSearchParams(String(call[0]).split("?")[1]));

beforeEach(() => api.mockReset());

describe("CompleteSessions", () => {
  it("follows a delta across its pages and advances only after the last one", async () => {
    const list = new CompleteSessions();
    api.mockResolvedValueOnce(page([row("m/1", "a/b"), row("m/1", "c")], "r1"));
    expect((await list.read()).sessions.map((s) => s.session_id)).toEqual(["a/b", "c"]);
    expect(query(api.mock.calls[0])).toEqual({ summary: "0", limit: "200" });

    api.mockResolvedValueOnce(page([row("m/1", "c", "changed")], "r2", { remove: [JSON.stringify(["m/1", "a/b"])], has_more: true, next_cursor: "k" }))
      .mockRejectedValueOnce(new Error("network"));
    await expect(list.read()).rejects.toThrow("network");
    expect(list.holds("r1")).toBe(true);
    expect(query(api.mock.calls[2])).toEqual({ summary: "0", limit: "200", since: "r1", cursor: "k" });

    api.mockResolvedValueOnce(page([row("m/1", "c", "changed")], "r2", { remove: [JSON.stringify(["m/1", "a/b"])], has_more: true, next_cursor: "k" }))
      .mockResolvedValueOnce(page([row("m/2", "new")], "r2"));
    const read = await list.read();
    expect(query(api.mock.calls[3])).toEqual({ summary: "0", limit: "200", since: "r1" });
    expect(read.changed).toBe(true);
    expect(read.sessions.map((s) => [s.session_id, s.title])).toEqual([["c", "changed"], ["new", "new"]]);
    expect(list.holds("r2")).toBe(true);
  });

  it("keeps the same list for an empty delta and rebuilds after an expired revision", async () => {
    const list = new CompleteSessions();
    api.mockResolvedValueOnce(page([row("m", "a")], "r1"));
    const first = await list.read();
    api.mockResolvedValueOnce(page([], "r1"));
    const same = await list.read();
    expect(same.changed).toBe(false);
    expect(same.sessions).toBe(first.sessions);
    api.mockRejectedValueOnce(new ApiError(409, "expired", { code: "revision_expired" }))
      .mockResolvedValueOnce(page([row("m", "b")], "s1"));
    expect((await list.read()).sessions.map((s) => s.session_id)).toEqual(["b"]);
    expect(query(api.mock.calls[3])).toEqual({ summary: "0", limit: "200" });
    expect(list.holds("s1")).toBe(true);
  });

  it("refuses a baseline whose pages disagree, and uses a hub's legacy answer without a checkpoint", async () => {
    const list = new CompleteSessions();
    api.mockResolvedValueOnce(page([row("m", "a")], "r1", { has_more: true, next_cursor: "k" }))
      .mockResolvedValueOnce(page([row("m", "b")], "r2"));
    await expect(list.read()).rejects.toThrow();
    api.mockResolvedValue(page([row("m", "a")], "r1", { has_more: true, next_cursor: "same" }));
    await expect(list.read()).rejects.toThrow(); // a cursor that repeats would page forever
    expect(api.mock.calls.length).toBe(4);
    api.mockReset();
    api.mockResolvedValueOnce({ sessions: [row("m", "old")] });
    expect((await list.read()).sessions.map((s) => s.session_id)).toEqual(["old"]);
    expect(list.holds("r1")).toBe(false);
    api.mockResolvedValueOnce({ sessions: [row("m", "again")] });
    await list.read();
    expect(query(api.mock.calls[1])).toEqual({ summary: "0", limit: "200" });
  });
});
