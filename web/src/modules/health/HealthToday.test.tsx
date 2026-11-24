// @vitest-environment jsdom
// "Today" on the Health home (#13176): the water and protein rings with their buttons. It shows only when the hub's hello
// has `health_intake`; a press moves the ring at once and the hub's total settles it; a logged record offers Undo (DELETE),
// a failed one is taken back out with the reason and a Retry that keeps the record number; the hub's `health_intake`
// message reads the day again. The hub is a stand-in that keeps records by id as the real one does.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface Rec { id: string; kind: "water" | "protein"; amount: number; deleted_at?: string }
const hub = vi.hoisted(() => ({
  features: ["health_intake"] as string[],
  listeners: new Map<string, Set<() => void>>(),
  calls: [] as { method: string; path: string; json?: Record<string, unknown> }[],
  records: new Map<string, { id: string; kind: "water" | "protein"; amount: number; deleted_at?: string }>(),
  cup: 250,
  goals: { water: 2000, protein: 60 },
  failWith: "" as string,
  failDelete: "" as string,
  loseResponse: false,
  gate: null as Promise<void> | null,
  readGate: null as Promise<void> | null,
}));
const total = (kind: "water" | "protein") => [...hub.records.values()].filter((r) => r.kind === kind && !r.deleted_at).reduce((n, r) => n + r.amount, 0);
const totals = () => ({ water: total("water"), protein: total("protein") });

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: async (path: string, init: { method?: string; json?: Record<string, unknown> } = {}) => {
    const method = init.method ?? "GET";
    hub.calls.push({ method, path, json: init.json });
    if (path.startsWith("/assistant?space=health")) return { machine_id: "mac", conversations: [] };
    if (path.startsWith("/assistant/memory")) return { files: [] };
    if (path.endsWith("/health/reports")) return { reports: [] };
    if (!path.startsWith("/assistant/health/intake")) return {};
    if (method === "GET") {
      const gate = hub.readGate;
      const snapshot = totals();
      const goals = { ...hub.goals };
      if (gate) await gate;
      return {
        water: { today: snapshot.water, goal: goals.water, entries: [] },
        protein: { today: snapshot.protein, goal: goals.protein, entries: [] },
        days: [],
        zone: "UTC",
      };
    }
    if (hub.gate) await hub.gate;
    if (method === "POST") {
      if (hub.failWith) throw new Error(hub.failWith);
      const body = init.json as { id: string; kind: "water" | "protein"; amount?: number };
      if (!hub.records.has(body.id)) hub.records.set(body.id, { id: body.id, kind: body.kind, amount: body.amount ?? hub.cup });
      const r = hub.records.get(body.id)!;
      if (hub.loseResponse) throw new Error("response lost");
      return { entry: { ...r, at: "2026-11-23T08:00:00Z", note: "", seq: hub.records.size, created_at: "2026-11-23T08:00:00Z" }, today: totals(), zone: "UTC" };
    }
    if (hub.failDelete) throw new Error(hub.failDelete);
    hub.records.delete(decodeURIComponent(path.split("?")[0].split("/").pop()!));
    return { today: totals(), zone: "UTC" };
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), features: hub.features } as ReturnType<typeof real.getState>),
    onHubEvent: (type: string, l: () => void) => {
      hub.listeners.set(type, (hub.listeners.get(type) ?? new Set()).add(l));
      return () => void hub.listeners.get(type)?.delete(l);
    },
  };
});
vi.mock("../../views/chat/ChatView", () => ({ ChatView: () => null }));

import { Toasts } from "../../ui/atoms";
import { Health } from "./Health";
import { HealthToday } from "./HealthToday";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (node: React.ReactNode = <HealthToday />) => {
  act(() => root.render(<>{node}<Toasts /></>));
  await flush();
};
const ring = (kind: "water" | "protein") => host.querySelector<HTMLElement>(`[data-testid="health-today-${kind}"]`)!;
const meter = (kind: "water" | "protein") => ring(kind).querySelector<HTMLElement>('[role="meter"]')!;
const button = (scope: ParentNode, label: string) => [...scope.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const press = async (b: HTMLButtonElement | undefined) => {
  expect(b, "button").toBeDefined();
  act(() => b!.click());
  await flush();
};
// toasts live in the module, past a test: the newest is the last
const toasts = () => [...document.body.querySelectorAll<HTMLElement>("[aria-live] > div")];
const newest = () => toasts().at(-1)!;
const posts = () => hub.calls.filter((c) => c.method === "POST");
const reads = () => hub.calls.filter((c) => c.method === "GET" && c.path.startsWith("/assistant/health/intake"));
const type = (input: HTMLInputElement, text: string) => {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    set.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const dialogInput = () => document.body.querySelector<HTMLInputElement>('[data-testid="health-today-amount"]');
const dialogLog = () => button(document.body, t("health.today_log"));
const seed = (...rs: Rec[]) => rs.forEach((r) => hub.records.set(r.id, r));
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

beforeEach(() => {
  hub.features = ["health_intake"];
  hub.listeners.clear();
  hub.calls.length = 0;
  hub.records.clear();
  hub.cup = 250;
  hub.goals = { water: 2000, protein: 60 };
  hub.failWith = "";
  hub.failDelete = "";
  hub.loseResponse = false;
  hub.gate = null;
  hub.readGate = null;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  vi.restoreAllMocks();
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("Today: what it shows", () => {
  it("draws the water and protein rings with today's amount and the goal under it", async () => {
    seed({ id: "a", kind: "water", amount: 750 }, { id: "b", kind: "protein", amount: 20 });
    await render();
    expect(meter("water").getAttribute("aria-valuenow")).toBe("38"); // 750 of 2,000
    expect(meter("protein").getAttribute("aria-valuenow")).toBe("33"); // 20 of 60
    expect(ring("water").textContent).toContain("750");
    expect(ring("water").textContent).toContain(t("health.today_goal", { goal: "2,000", unit: t("health.today_unit_water") }));
    expect(ring("protein").textContent).toContain(t("health.today_goal", { goal: "60", unit: t("health.today_unit_protein") }));
    expect(host.querySelector('[data-testid="health-today"]')?.textContent).toContain(t("health.today_title"));
    // the one day, and the health space: no manager is named (the next test), the space is
    expect(reads().map((c) => c.path)).toEqual(["/assistant/health/intake?space=health&days=1"]);
  });

  it("puts the buttons under their ring: a glass and another amount under water, grams under protein", async () => {
    await render();
    expect([...ring("water").querySelectorAll("button")].map((b) => b.textContent)).toEqual([t("health.today_drank"), t("health.today_other")]);
    expect([...ring("protein").querySelectorAll("button")].map((b) => b.textContent)).toEqual([t("health.today_grams")]);
  });

  it("draws each ring in its own color token, not a named hue the Activity rings could be taken for", async () => {
    await render();
    const arcs = (k: "water" | "protein") => ring(k).querySelector("[data-gauge-arc]");
    seed({ id: "a", kind: "water", amount: 500 }, { id: "b", kind: "protein", amount: 10 });
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    expect(arcs("water")?.getAttribute("stroke")).toBe("var(--health-water)");
    expect(arcs("protein")?.getAttribute("stroke")).toBe("var(--health-protein)");
  });

  it("shows nothing, and asks nothing, while the hub has no health_intake feature", async () => {
    hub.features = [];
    await render();
    expect(host.querySelector('[data-testid="health-today"]')).toBeNull();
    expect(hub.calls).toEqual([]);
    hub.features = ["terminal_stream", "managers"]; // a hub with other features, an older one
    await render(<HealthToday key="again" />);
    expect(host.querySelector('[data-testid="health-today"]')).toBeNull();
    expect(hub.calls).toEqual([]);
  });

  it("says why it can't read the day, with Retry, and draws it once a read works", async () => {
    hub.readGate = Promise.reject(new Error("hub away"));
    hub.readGate.catch(() => {});
    await render();
    expect(host.textContent).toContain(t("health.today_load_failed", { error: "hub away" }));
    hub.readGate = null;
    await press(button(host, t("health.retry")));
    expect(ring("water")).not.toBeNull();
  });

  it("is the first thing on the Health home, above the box and the checkup reports", async () => {
    await render(<Health fresh onToggleSidebar={() => {}} />);
    const today = host.querySelector('[data-testid="health-today"]')!;
    const hero = host.querySelector("h2")!;
    expect(today).not.toBeNull();
    expect(today.compareDocumentPosition(hero) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // without the feature the home is as it was
    hub.features = [];
    await render(<Health fresh key="plain" onToggleSidebar={() => {}} />);
    expect(host.querySelector('[data-testid="health-today"]')).toBeNull();
    expect(host.querySelector("h2")).not.toBeNull();
  });
});

describe("Today: logging", () => {
  it("a glass moves the ring before the hub answers, then takes the hub's total", async () => {
    hub.cup = 300; // the hub's cup is not the 250 the page assumes until it has seen one
    await render();
    let release!: () => void;
    hub.gate = new Promise<void>((r) => (release = r));
    await press(button(ring("water"), t("health.today_drank")));
    expect(ring("water").textContent).toContain("250"); // at once: the default cup
    expect(posts()).toHaveLength(1);
    const body = posts()[0];
    expect(body.path).toBe("/assistant/health/intake?space=health");
    expect(body.json).toMatchObject({ kind: "water" });
    expect(body.json?.id).toMatch(UUID);
    expect(body.json).not.toHaveProperty("amount"); // a glass is the hub's cup, not a number from here
    expect(typeof body.json?.at).toBe("string");
    release();
    await flush();
    expect(ring("water").textContent).toContain("300"); // aligned with the hub's own total
    expect(ring("water").textContent).not.toContain("550");
    // the toast says what was logged, and offers Undo
    const row = newest();
    expect(row.textContent).toContain(t("health.today_logged_water", { amount: "300" }));
    expect(button(row, t("health.today_undo"))).toBeDefined();
  });

  it("another amount is asked for in a box, and logged as that number of millilitres", async () => {
    await render();
    await press(button(ring("water"), t("health.today_other")));
    expect(dialogInput()).not.toBeNull();
    type(dialogInput()!, "330");
    await press(dialogLog());
    expect(posts()).toHaveLength(1);
    expect(posts()[0].json).toMatchObject({ kind: "water", amount: 330 });
    expect(ring("water").textContent).toContain("330");
    expect(dialogInput()).toBeNull(); // the box closes once it is logged
  });

  it("grams of protein are asked for the same way and move only the protein ring", async () => {
    await render();
    await press(button(ring("protein"), t("health.today_grams")));
    type(dialogInput()!, "32.5");
    await press(dialogLog());
    expect(posts()[0].json).toMatchObject({ kind: "protein", amount: 32.5 });
    expect(ring("protein").textContent).toContain("32.5");
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0");
    expect(newest().textContent).toContain(t("health.today_logged_protein", { amount: "32.5" }));
  });

  it("an amount that is not a number, is zero or is over the limit stays in the box and is not sent", async () => {
    await render();
    await press(button(ring("water"), t("health.today_other")));
    for (const bad of ["", "abc", "0", "-5", "3001", "12.34"]) {
      type(dialogInput()!, bad);
      await press(dialogLog());
      expect(dialogInput()?.getAttribute("aria-invalid"), `"${bad}"`).toBe("true");
    }
    expect(posts()).toEqual([]);
    type(dialogInput()!, "3000"); // the limit itself is fine, and typing clears the complaint
    expect(dialogInput()?.getAttribute("aria-invalid")).toBeNull();
    await press(dialogLog());
    expect(posts()).toHaveLength(1);
    // protein's limit is its own
    await press(button(ring("protein"), t("health.today_grams")));
    type(dialogInput()!, "201");
    await press(dialogLog());
    expect(dialogInput()?.getAttribute("aria-invalid")).toBe("true");
    expect(posts()).toHaveLength(1);
  });

  it("each press is its own record, with its own number", async () => {
    await render();
    await press(button(ring("water"), t("health.today_drank")));
    await press(button(ring("water"), t("health.today_drank")));
    const ids = posts().map((c) => c.json?.id);
    expect(new Set(ids).size).toBe(2);
    expect(ring("water").textContent).toContain("500");
  });
});

describe("Today: undo", () => {
  it("Undo in the toast deletes that record and takes it off the ring", async () => {
    await render();
    await press(button(ring("water"), t("health.today_drank")));
    const id = posts()[0].json?.id as string;
    await press(button(newest(), t("health.today_undo")));
    const del = hub.calls.filter((c) => c.method === "DELETE");
    expect(del.map((c) => c.path)).toEqual([`/assistant/health/intake/${id}?space=health`]);
    expect(ring("water").textContent).toContain("0");
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0");
    expect(hub.records.size).toBe(0);
    expect(toasts().map((r) => r.textContent).join()).toContain(t("health.today_undone"));
  });

  it("a failed undo leaves the record on the ring, says why, and Retry deletes it", async () => {
    await render();
    await press(button(ring("water"), t("health.today_drank")));
    hub.failDelete = "hub busy";
    await press(button(newest(), t("health.today_undo")));
    expect(meter("water").getAttribute("aria-valuenow")).toBe("13"); // 250 of 2,000 still counts
    const failed = toasts().find((r) => r.textContent?.includes(t("health.today_undo_failed", { error: "hub busy" })))!;
    expect(failed).toBeDefined();
    hub.failDelete = "";
    await press(button(failed, t("health.retry")));
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0");
  });
});

describe("Today: a log the hub refuses", () => {
  it("puts the amount back, says why, and Retry sends the same record number so it is logged once", async () => {
    await render();
    let release!: () => void;
    hub.gate = new Promise<void>((r) => (release = r));
    hub.failWith = "storage unavailable";
    await press(button(ring("water"), t("health.today_drank")));
    expect(ring("water").textContent).toContain("250"); // local first
    release();
    await flush();
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0"); // rolled back
    const failed = toasts().find((r) => r.textContent?.includes(t("health.today_log_failed", { error: "storage unavailable" })))!;
    expect(failed).toBeDefined();
    hub.failWith = "";
    hub.gate = null;
    await press(button(failed, t("health.retry")));
    const ids = posts().map((c) => c.json?.id);
    expect(ids).toHaveLength(2);
    expect(ids[1]).toBe(ids[0]);
    expect(posts()[1].json?.at).toBe(posts()[0].json?.at);
    expect(hub.records.size).toBe(1);
    expect(ring("water").textContent).toContain("250");
  });

  it("a failure of one press does not take back another that went through", async () => {
    await render();
    await press(button(ring("protein"), t("health.today_grams")));
    type(dialogInput()!, "30");
    await press(dialogLog());
    hub.failWith = "nope";
    await press(button(ring("water"), t("health.today_drank")));
    expect(ring("protein").textContent).toContain("30");
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0");
  });
});

describe("Today: the hub's message", () => {
  it("reads the day again when the hub says a record was added or undone, wherever that was", async () => {
    await render();
    expect(reads()).toHaveLength(1);
    seed({ id: "phone", kind: "water", amount: 500 });
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    expect(reads()).toHaveLength(2);
    expect(ring("water").textContent).toContain("500");
    hub.records.clear();
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0");
  });

  it("also reads again on a hello, as the page's other cards do", async () => {
    await render();
    seed({ id: "x", kind: "protein", amount: 12 });
    act(() => hub.listeners.get("hello")?.forEach((l) => l()));
    await flush();
    expect(ring("protein").textContent).toContain("12");
  });

  it("waits for a write in flight, then reads once: an older read cannot undo its answer", async () => {
    await render();
    let release!: () => void;
    hub.gate = new Promise<void>((r) => (release = r));
    await press(button(ring("water"), t("health.today_drank")));
    const before = reads().length;
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    expect(reads()).toHaveLength(before); // nothing asked while the write is out
    release();
    await flush();
    expect(reads()).toHaveLength(before + 1); // one read once it is done, not one per message
    expect(ring("water").textContent).toContain("250");
  });
});

describe("Today: opening a ring's page", () => {
  const open = (kind: "water" | "protein") => host.querySelector<HTMLAnchorElement>(`[data-testid="health-today-${kind}-open"]`)!;

  it("a ring is a link to its page: the extension pages' route, the reserved id, the state in ?state=", async () => {
    await render();
    expect(open("water").getAttribute("href")).toBe("/extension/-health-intake/pages?state=%7B%22kind%22%3A%22water%22%7D");
    expect(open("protein").getAttribute("href")).toBe("/extension/-health-intake/pages?state=%7B%22kind%22%3A%22protein%22%7D");
    expect(open("water").querySelector('[role="meter"]')).not.toBeNull();
    // the buttons under a ring are not inside its link
    expect(open("water").querySelector("button")).toBeNull();
  });

  it("a plain click on a ring opens that page in the app; a modified click is left to the browser", async () => {
    history.replaceState(null, "", "/health/new");
    await render();
    const before = location.pathname + location.search;
    const modified = new MouseEvent("click", { bubbles: true, cancelable: true, metaKey: true });
    act(() => void open("protein").dispatchEvent(modified));
    expect(modified.defaultPrevented).toBe(false);
    expect(location.pathname + location.search).toBe(before);
    act(() => open("protein").click());
    expect(location.pathname).toBe("/extension/-health-intake/pages");
    expect(new URLSearchParams(location.search).get("state")).toBe('{"kind":"protein"}');
    history.replaceState(null, "", "/health/new");
    act(() => open("water").click());
    expect(new URLSearchParams(location.search).get("state")).toBe('{"kind":"water"}');
  });
});

describe("Today: read and retry regressions", () => {
  it("repeats a read invalidated by a write that has already completed", async () => {
    await render();
    let releaseRead!: () => void;
    hub.readGate = new Promise<void>((r) => (releaseRead = r));
    hub.goals.water = 3000;
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    const count = reads().length;
    await press(button(ring("water"), t("health.today_drank")));
    releaseRead();
    await flush();
    expect(reads()).toHaveLength(count + 1);
    expect(ring("water").textContent).toContain(t("health.today_goal", { goal: "3,000", unit: t("health.today_unit_water") }));
  });

  it("reports a tombstone returned by Retry as removed rather than newly logged", async () => {
    await render();
    // Keep a prior test's identical notice outside the toast duplicate window.
    vi.spyOn(Date, "now").mockReturnValue(Date.now() + 10_000);
    hub.loseResponse = true;
    await press(button(ring("water"), t("health.today_drank")));
    const id = posts()[0].json?.id as string;
    expect(total("water")).toBe(250); // saved before the response was lost
    hub.records.get(id)!.deleted_at = "2026-11-23T08:01:00Z";
    hub.loseResponse = false;
    await press(button(newest(), t("health.retry")));
    expect(posts()[1].json).toEqual(posts()[0].json);
    expect(hub.records.size).toBe(1);
    expect(meter("water").getAttribute("aria-valuenow")).toBe("0");
    expect(newest().textContent).toBe(t("health.today_undone"));
    expect(button(newest(), t("health.today_undo"))).toBeUndefined();
  });

  it("keeps the newest broadcast result when two reads finish out of order", async () => {
    await render();
    let releaseOlder!: () => void;
    hub.readGate = new Promise<void>((r) => (releaseOlder = r));
    seed({ id: "first", kind: "water", amount: 250 });
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    let releaseNewer!: () => void;
    hub.readGate = new Promise<void>((r) => (releaseNewer = r));
    seed({ id: "second", kind: "water", amount: 250 });
    act(() => hub.listeners.get("health_intake")?.forEach((l) => l()));
    await flush();
    releaseNewer();
    await flush();
    expect(meter("water").getAttribute("aria-valuenow")).toBe("25");
    releaseOlder();
    await flush();
    expect(meter("water").getAttribute("aria-valuenow")).toBe("25");
  });

  it.each(["0.5", "0.9"])("rejects %s ml of water before making a request", async (amount) => {
    await render();
    await press(button(ring("water"), t("health.today_other")));
    type(dialogInput()!, amount);
    await press(dialogLog());
    expect(posts()).toHaveLength(0);
    expect(dialogInput()?.getAttribute("aria-invalid")).toBe("true");
  });
});

describe("Today: minimum amounts", () => {
  it.each([ ["water", "1"], ["protein", "0.1"] ] as const)("accepts %s at %s", async (kind, amount) => {
    await render();
    await press(button(ring(kind), t(kind === "water" ? "health.today_other" : "health.today_grams")));
    type(dialogInput()!, amount);
    await press(dialogLog());
    expect(posts()).toHaveLength(1);
    expect(posts()[0].json).toMatchObject({ kind, amount: Number(amount) });
    expect(dialogInput()).toBeNull();
    expect(ring(kind).textContent).toContain(amount);
  });
});
