// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { assistantSwitchNeeded, prepareConversationSwitch, submitConversationSwitch } from "./conversation-switch";
import type { Session } from "./types";

const session = { machine_id: "computer", session_id: "chat", launcher: "claude", agent: "claude", permission_mode: "acceptEdits" } as Session;
afterEach(() => vi.unstubAllGlobals());
it("requires staging for another adapter even when its engine kind matches", () => {
  expect(assistantSwitchNeeded(session, { name: "other-claude", source: "zshrc" }, "claude")).toBe(false);
  expect(assistantSwitchNeeded(session, { name: "codex", source: "path" }, "codex")).toBe(true);
  expect(assistantSwitchNeeded(session, { name: "hosted-claude", source: "runtime-one" }, "claude")).toBe(true);
  const runtime = { ...session, source_kind: "runtime", source_instance_id: "runtime-one" };
  expect(assistantSwitchNeeded(runtime, { name: "other", source: "runtime-two" }, "claude")).toBe(true);
  expect(assistantSwitchNeeded(runtime, { name: "other", source: "runtime-one" }, "claude")).toBe(false);
});
it("reads the epoch without writing, then retries the exact explicit operation", async () => {
  const calls: { url: string; method?: string; body?: unknown }[] = [];
  let fail = true;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), method: init?.method, body: init?.body && JSON.parse(String(init.body)) });
    if (String(input).includes("/segments?")) return new Response(JSON.stringify({ enabled: true, epoch: 8 }), { headers: { "Content-Type": "application/json" } });
    if (fail) { fail = false; throw new TypeError("lost response"); }
    return new Response(JSON.stringify({ phase: "preparing" }), { headers: { "Content-Type": "application/json" } });
  }));
  const request = await prepareConversationSwitch(session, { launcher: "codex", model: "example-model", effort: "high" });
  expect(calls).toHaveLength(1);
  expect(request.expected_epoch).toBe(8);
  expect(request.permission_mode).toBe("acceptEdits");
  await expect(submitConversationSwitch(session, request)).rejects.toThrow();
  await submitConversationSwitch(session, request);
  expect(calls[1].body).toEqual(calls[2].body);
  expect(calls[1].body).toMatchObject({ source_machine_id: "computer", machine_id: "computer", launcher: "codex", expected_epoch: 8 });
  expect(calls.some((c) => c.url.endsWith("/model"))).toBe(false);
});
it("prepares a v4 UUID operation_id when crypto.randomUUID is unavailable (plain HTTP)", async () => {
  const real = globalThis.crypto;
  vi.stubGlobal("crypto", { getRandomValues: (a: Uint8Array) => real.getRandomValues(a) });
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ enabled: true, epoch: 3 }), { headers: { "Content-Type": "application/json" } })));
  const request = await prepareConversationSwitch(session, { launcher: "agy" });
  expect(request.operation_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  expect(request.expected_epoch).toBe(3);
  expect(request.launcher).toBe("agy");
});
