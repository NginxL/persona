// @vitest-environment jsdom
// The Toolchain view follows Machines view persistence: localStorage restores it and selecting it stores the choice.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import type { Machine } from "../../lib/types";

const storage = vi.hoisted(() => new Map<string, string>());
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.localStorage = {
    getItem: (k: string) => storage.get(k) ?? null,
    setItem: (k: string, v: string) => void storage.set(k, v),
    removeItem: (k: string) => void storage.delete(k),
  };
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[], features: [] as string[] }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) =>
      select({ ...real.getState(), loaded: true, machines: fake.machines, features: fake.features }),
  };
});

const { Machines } = await import("../Machines");
// Load the lazy Toolchain chunk up front (#12915). A fixed number of flushes does not cover a cold dynamic import: the
// click below fetched nothing, and the "not restored" checks passed even when Machines restored the view.
await import("./ToolchainPanel");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) =>
  new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

const waitUntil = async (predicate: () => boolean) => {
  await vi.waitFor(
    async () => {
      await act(async () => {
        await Promise.resolve();
      });
      expect(predicate()).toBe(true);
    },
    { timeout: 4000 },
  );
};

beforeEach(async () => {
  await initI18n("en");
  storage.clear();
  fake.machines = [{ id: "dev-box", name: "Dev Box", online: true }];
  fake.features = [];
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("Machines toolchain view persistence", () => {
  it("restores toolchain from storage and persists it when selected", async () => {
    const requested: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        requested.push(url);
        if (url.includes("/toolchain")) {
          return Promise.resolve(
            json({
              collected_at: "2026-11-21T17:00:00Z",
              cached: false,
              cache_ttl_sec: 60,
              duration_ms: 12,
              os: "linux",
              arch: "amd64",
              tools: [],
              agent_clis: [],
              environment: {},
            }),
          );
        }
        if (url.includes("/metrics")) return Promise.resolve(json({ machines: [] }));
        if (url.includes("/launchers/health")) return Promise.resolve(json({ records: [] }));
        return Promise.resolve(json({ ok: true }));
      }),
    );

    // Pre-seed storage with "toolchain": Machines restores it and probes the toolchain.
    storage.set("persona.machines.view", "toolchain");

    act(() => root.render(<Machines onToggleSidebar={() => {}} />));
    await act(async () => { await vi.dynamicImportSettled(); });
    await waitUntil(() => requested.some((u) => u.includes("/api/v1/machines/dev-box/toolchain")));

    expect(requested.some((u) => u.includes("/toolchain"))).toBe(true);
    expect(host.querySelector('[data-testid="toolchain-panel"]')).not.toBeNull();

    // Selecting Capabilities persists "capabilities".
    const capBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Capabilities");
    expect(capBtn).toBeTruthy();
    act(() => capBtn!.click());
    await waitUntil(() => storage.get("persona.machines.view") === "capabilities");
    expect(storage.get("persona.machines.view")).toBe("capabilities");

    // Clicking Toolchain triggers a fresh probe and persists the selection.
    requested.length = 0;
    const tcBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Toolchain");
    expect(tcBtn).toBeTruthy();
    act(() => tcBtn!.click());
    await act(async () => { await vi.dynamicImportSettled(); });
    await waitUntil(() => requested.some((u) => u.includes("/api/v1/machines/dev-box/toolchain")));

    expect(requested.some((u) => u.includes("/api/v1/machines/dev-box/toolchain"))).toBe(true);
    expect(storage.get("persona.machines.view")).toBe("toolchain");
  });
});
