// @vitest-environment jsdom
// The water and protein pages (#13176) are the default manager's declarative pages under the reserved id `-health-intake`:
// the renderer's requests for it go to the hub's intake page route with `space=health` and no manager, whichever
// assistant the Persona page shows; every other id keeps its own path. The real client and manager choice, fetch stubbed.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { actUI, isReservedUI, renderUI, uiPath } from "./extension-ui";
import { HEALTH_INTAKE_PAGE, HEALTH_INTAKE_UI, healthIntakePath, healthIntakeState } from "./health-intake-ui";
import { chooseManager, resetManagerChoice } from "./manager-choice";
import { parse } from "./router";
import { SUBSCRIPTIONS_UI } from "./subscriptions-ui";

let seen: { method: string; url: string; manager: string | null; body: Record<string, unknown> }[];
const real = globalThis.fetch;

beforeEach(() => {
  seen = [];
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    seen.push({ method: init.method ?? "GET", url: url.replace(/^.*\/api\/v1/, ""), manager: new Headers(init.headers).get("X-Persona-Manager"), body: init.body ? JSON.parse(String(init.body)) : {} });
    return new Response(JSON.stringify({ view: { title: "Water", sections: [] } }), { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = real;
  resetManagerChoice();
});

describe("the reserved id and its path", () => {
  it("maps `-health-intake` to the hub's intake page route, in the health space", () => {
    expect(HEALTH_INTAKE_UI).toBe("-health-intake");
    expect(HEALTH_INTAKE_PAGE).toBe("pages");
    expect(uiPath(HEALTH_INTAKE_UI, HEALTH_INTAKE_PAGE)).toBe("/assistant/health/intake/ui?space=health");
    expect(isReservedUI(HEALTH_INTAKE_UI)).toBe(true);
  });

  it("leaves the subscription pages and every extension's own path as they were", () => {
    expect(uiPath(SUBSCRIPTIONS_UI, "pages")).toBe("/subscriptions/-/ui");
    expect(uiPath("example", "home")).toMatch(/^\/(extensions|plugins)\/example\/-\/ui\/home$/);
    expect(isReservedUI(SUBSCRIPTIONS_UI)).toBe(true);
    expect(isReservedUI("example")).toBe(false);
    expect(isReservedUI("toString")).toBe(false); // not a key of the table's prototype
  });

  it("the water and protein first states are the ones the hub names", () => {
    expect(healthIntakeState("water")).toBe('{"kind":"water"}');
    expect(healthIntakeState("protein")).toBe('{"kind":"protein"}');
  });
});

describe("the page's requests", () => {
  it("draw and press over `…/health/intake/ui?space=health`, with the state, and no manager with another one shown", async () => {
    chooseManager("m-1");
    seen.length = 0; // choosing one reads its page again
    await renderUI(HEALTH_INTAKE_UI, HEALTH_INTAKE_PAGE, undefined, healthIntakeState("protein"));
    await actUI(HEALTH_INTAKE_UI, HEALTH_INTAKE_PAGE, "log", { state: '{"kind":"protein","days":7}', values: { amount: "30" } });
    expect(seen.map((s) => `${s.method} ${s.url}`)).toEqual(["POST /assistant/health/intake/ui?space=health", "POST /assistant/health/intake/ui?space=health"]);
    expect(seen.map((s) => s.manager)).toEqual([null, null]);
    expect(seen[0].body).toMatchObject({ state: '{"kind":"protein"}', context: { platform: "web" } });
    expect(seen[0].body).not.toHaveProperty("action");
    // the state the hub gave goes back as it is, with the action and the values
    expect(seen[1].body).toMatchObject({ state: '{"kind":"protein","days":7}', action: "log", values: { amount: "30" } });
  });
});

describe("opening it from a ring", () => {
  it("is the extension page route the renderer already opens pages on, with the state in ?state=", () => {
    const water = healthIntakePath("water");
    expect(water).toBe("/extension/-health-intake/pages?state=%7B%22kind%22%3A%22water%22%7D");
    const [path, query] = water.split("?");
    expect(parse(path, query)).toEqual({ name: "extension", extension: "-health-intake", ui: "pages" });
    expect(new URLSearchParams(query).get("state")).toBe('{"kind":"water"}');
    expect(new URLSearchParams(healthIntakePath("protein").split("?")[1]).get("state")).toBe('{"kind":"protein"}');
  });
});
