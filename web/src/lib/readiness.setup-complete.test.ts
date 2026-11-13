// @vitest-environment jsdom
// Setup completion updates readiness without promotion or browser-local launch tracking.
import { afterEach, describe, expect, it, vi } from "vitest";
import { getState } from "./store";
import { refreshReadiness, resetReadiness, type Readiness } from "./readiness";

const toast = vi.hoisted(() => vi.fn());
vi.mock("../ui/atoms", async (original) => ({ ...(await original<typeof import("../ui/atoms")>()), toast }));

const readiness = (ready: boolean, firstAnswered: boolean): Readiness => ({
  version: 1, mode: ready ? "full" : "setup", ready, next: ready && firstAnswered ? null : ready ? "first_message" : "model",
  machine: null, launcher: "claude", dismissed: false, checked_at: "2026-11-13T00:00:00Z",
  steps: [{ id: "model", state: ready ? "ok" : "todo" }, { id: "first_message", state: firstAnswered ? "ok" : "todo" }],
});

afterEach(() => {
  resetReadiness();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  toast.mockClear();
});

describe("setup completes without promotion", () => {
  for (const legacy of [null, JSON.stringify({ launches: 20, threshold: 20, done: false, welcomed: false }), "{damaged"]) {
    it(`does not show a toast or track setup with ${legacy === null ? "fresh" : legacy.startsWith("{damaged") ? "damaged" : "old"} prompt storage`, async () => {
      Object.assign(getState(), { features: ["assistant_readiness"] });
      localStorage.clear();
      if (legacy !== null) localStorage.setItem("persona.starPrompt", legacy);
      const storage = vi.spyOn(Storage.prototype, "setItem");
      const open = vi.spyOn(window, "open").mockReturnValue(null);
      const changed = vi.fn();
      const next = [readiness(true, false), readiness(true, true)];
      vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(next.shift()), { status: 200, headers: { "Content-Type": "application/json" } })));
      resetReadiness(readiness(false, false));
      window.addEventListener("storage", changed);
      try {
        await refreshReadiness({ changed: true });
        await refreshReadiness({ changed: true });
        // A removed lazy prompt must not appear on a later task, either.
        await vi.dynamicImportSettled();
        expect(toast).not.toHaveBeenCalled();
        expect(open).not.toHaveBeenCalled();
        expect(storage).not.toHaveBeenCalled();
        expect(changed).not.toHaveBeenCalled();
        expect(localStorage.getItem("persona.starPrompt")).toBe(legacy);
        expect(fetch).toHaveBeenCalledTimes(2);
      } finally {
        window.removeEventListener("storage", changed);
      }
    });
  }
});
