// @vitest-environment jsdom
// The water and protein page (#13176): the declarative page of the hub's intake route in a full page of its own, for the
// state in `?state=`, drawn again when the hub says a record was added, undone or a goal saved (`health_intake`), and a
// way back to the Health home. Today's rings open it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({
  calls: [] as { path: string; body?: Record<string, unknown> }[],
  listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(),
}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: async (path: string, init: { json?: Record<string, unknown> } = {}) => {
    hub.calls.push({ path, body: init.json });
    return { view: { title: "Water", sections: [{ items: [{ type: "text", text: `drawn ${hub.calls.length}` }] }] } };
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
      if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
      hub.listeners.get(type)!.add(l);
      return () => void hub.listeners.get(type)?.delete(l);
    },
  };
});

import { healthIntakePath, healthIntakeState } from "../../lib/health-intake-ui";
import { HealthIntakePage, kindOf } from "./HealthIntakePage";
import { t } from "../../i18n";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const open = async (path: string) => {
  history.replaceState(null, "", path);
  await act(async () => root.render(<HealthIntakePage onToggleSidebar={() => {}} />));
  // the page's renderer is a chunk of its own: wait for its first draw
  await vi.waitFor(() => expect(host.textContent).toContain("drawn"), { timeout: 20000 });
  await settle();
};
const drawn = () => hub.calls.filter((c) => c.path === "/assistant/health/intake/ui?space=health");

// the page's renderer is a big chunk: load it once, outside any test's time limit (a busy machine takes a while)
beforeAll(async () => {
  await import("../../views/settings/ExtensionView");
}, 120_000);

beforeEach(() => {
  hub.calls.length = 0;
  hub.listeners.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("the page", () => {
  it("opens the page for the state a ring's link carries, over the hub's intake route", async () => {
    await open(healthIntakePath("protein"));
    expect(drawn()[0].body).toMatchObject({ state: healthIntakeState("protein") });
    expect(host.querySelector("h1")?.textContent).toBe(t("health.today_protein"));
    expect(host.textContent).toContain("drawn 1");
  });

  it("and the water link's state opens the water page", async () => {
    await open(healthIntakePath("water"));
    expect(drawn()[0].body).toMatchObject({ state: healthIntakeState("water") });
    expect(host.querySelector("h1")?.textContent).toBe(t("health.today_water"));
  });

  it("opens water when there is no state, as the hub does, or one it can't read", async () => {
    await open("/extension/-health-intake/pages");
    expect(drawn()[0].body).toMatchObject({ state: healthIntakeState("water") });
    expect(kindOf(undefined)).toBe("water");
    expect(kindOf("not json")).toBe("water");
    expect(kindOf('{"kind":"other"}')).toBe("water");
    expect(kindOf('{"kind":"protein","days":30}')).toBe("protein");
  });

  it("is drawn again when the hub says a record changed, and only for that message", async () => {
    await open(healthIntakePath("water"));
    expect(drawn()).toHaveLength(1);
    vi.useFakeTimers();
    act(() => void hub.listeners.get("subscription")?.forEach((l) => l({ type: "subscription" })));
    act(() => void hub.listeners.get("health_intake")?.forEach((l) => l({ type: "health_intake" })));
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    vi.useRealTimers();
    await settle();
    expect(drawn()).toHaveLength(2);
    expect(host.textContent).toContain("drawn 2");
  });

  it("has a way back to the Health home, and another page of the app does not listen for health_intake", async () => {
    await open(healthIntakePath("water"));
    act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("health.title")}"]`)!.click());
    expect(location.pathname).toBe("/health/new");
    act(() => root.unmount());
    root = createRoot(host);
    hub.listeners.clear();
    const { LegacyExtensionUI } = await import("../../views/settings/ExtensionView");
    act(() => root.render(<LegacyExtensionUI extension="demo" ui="home" />));
    await settle();
    expect(hub.listeners.get("health_intake")?.size ?? 0).toBe(0);
  });
});
