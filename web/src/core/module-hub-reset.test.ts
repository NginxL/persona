// A hub's module flags never outlive its hello (#13846, the hub flag rule in core/module-host.ts): each hello replaces
// the features whole, so a reconnect to a hub without the `modules` marker brings back what a publishing hub hid, and
// another login (reset(), signing out) clears them. Another hub is another origin (lib/hubs.ts switchURL): its page
// starts with no features at all.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connect, getState, reset, subscribe } from "../lib/store";
import { connectModuleHub, MODULES_FEATURE, moduleState, registerModule, resetModulesForTest, type ModuleDescriptor } from "./module-host";

class Socket {
  static last: Socket;
  readyState = 1;
  onopen?: () => void;
  onclose?: (event: { code: number }) => void;
  onmessage?: (event: { data: string }) => void;
  constructor(public url: string) { Socket.last = this; }
  send() {}
  close() { this.readyState = 3; }
  push(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }); }
}

const garden: ModuleDescriptor = { id: "garden", feature: "module.garden", title: () => "Garden", description: () => "", load: async () => ({}) };
const on = () => moduleState(garden, getState());

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("location", { protocol: "http:", host: "demo" });
  vi.stubGlobal("localStorage", { getItem: () => "demo-token", setItem: () => {}, removeItem: () => {} });
  vi.stubGlobal("window", { setTimeout, clearTimeout });
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({})));
  resetModulesForTest();
  connectModuleHub(getState, subscribe);
  registerModule(garden);
});

afterEach(() => {
  reset();
  resetModulesForTest();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the hub's module flags across connections", () => {
  it("are replaced by each hello, and cleared by another login", async () => {
    connect();
    expect(on()).toBe("on"); // no hello yet
    Socket.last.push({ type: "hello", features: [MODULES_FEATURE] });
    await vi.advanceTimersByTimeAsync(200);
    expect(on()).toBe("off");
    // the connection drops and the next hello comes from a hub that publishes no module flags
    Socket.last.onclose?.({ code: 1006 });
    await vi.advanceTimersByTimeAsync(60_000);
    Socket.last.push({ type: "hello", features: ["terminal_stream"] });
    await vi.advanceTimersByTimeAsync(200);
    expect(on()).toBe("on");
    // a publishing hub again, then another login: nothing of the old hello is left
    Socket.last.push({ type: "hello", features: [MODULES_FEATURE] });
    await vi.advanceTimersByTimeAsync(200);
    expect(on()).toBe("off");
    reset();
    expect(getState().features).toEqual([]);
    expect(on()).toBe("on");
  });
});
