// @vitest-environment jsdom
// Settings › Labs' terminal renderer as this browser keeps it (lib/labs.ts): the Terminal settings WebGL switch from
// before Labs still reads, a value from another version is kept, a write the browser doesn't keep is reported, and
// another tab's change is heard.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readTerminalRenderer, subscribeTerminalRenderer, TERMINAL_RENDERER_KEY, writeTerminalRenderer } from "./labs";

const KEY = "persona.terminal.webgl";

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("reading", () => {
  it("keeps the key from before Labs", () => {
    expect(TERMINAL_RENDERER_KEY).toBe(KEY);
  });

  it("is Standard with nothing saved", () => {
    expect(readTerminalRenderer()).toEqual({ value: "standard", source: "default", raw: null });
  });

  it("reads the Terminal settings WebGL switch saved before Labs", () => {
    localStorage.setItem(KEY, "1");
    expect(readTerminalRenderer()).toEqual({ value: "webgl", source: "saved", raw: "1" });
  });

  it.each(["0", "true", "webgl", ""])("takes %j (another version's, or hand-written) as Standard and leaves it in place", (raw) => {
    localStorage.setItem(KEY, raw);
    expect(readTerminalRenderer()).toEqual({ value: "standard", source: "invalid", raw });
    expect(localStorage.getItem(KEY)).toBe(raw);
  });

  it("is Standard, said so, when the browser won't let the page read its storage", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    expect(readTerminalRenderer()).toEqual({ value: "standard", source: "unreadable", raw: null });
  });
});

describe("writing", () => {
  it("keeps WebGL as the old 1, and Standard as no key", () => {
    expect(writeTerminalRenderer("webgl")).toEqual({ ok: true });
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(writeTerminalRenderer("standard")).toEqual({ ok: true });
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it("refuses a renderer it doesn't have without touching storage", () => {
    localStorage.setItem(KEY, "1");
    expect(writeTerminalRenderer("canvas" as never)).toEqual({ ok: false, reason: "invalid" });
    expect(localStorage.getItem(KEY)).toBe("1");
  });

  it("reports a browser that throws on the write, and keeps the old value", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    expect(writeTerminalRenderer("webgl")).toEqual({ ok: false, reason: "storage" });
    expect(readTerminalRenderer().value).toBe("standard");
  });

  it("reports a write that doesn't read back", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {});
    expect(writeTerminalRenderer("webgl")).toEqual({ ok: false, reason: "storage" });
  });
});

describe("following changes", () => {
  it("hears writes here and other tabs' changes to this key only", () => {
    const heard = vi.fn();
    const off = subscribeTerminalRenderer(heard);
    writeTerminalRenderer("webgl");
    expect(heard).toHaveBeenCalledTimes(1);
    window.dispatchEvent(new StorageEvent("storage", { key: KEY }));
    expect(heard).toHaveBeenCalledTimes(2);
    window.dispatchEvent(new StorageEvent("storage", { key: "persona.unrelated" }));
    expect(heard).toHaveBeenCalledTimes(2);
    off();
    writeTerminalRenderer("standard");
    expect(heard).toHaveBeenCalledTimes(2);
  });
});
