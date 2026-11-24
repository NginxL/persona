// @vitest-environment jsdom
// Health's water and protein records belong to the health space, which stays the default manager's whichever assistant the
// Persona page shows (docs/assistant/managers.md "Clients", docs/assistant/health.md): every intake request names
// `space=health` and none carries the shown manager's header. The real client and the real manager choice, only fetch stubbed.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import { deleteEntry, newDraft, postEntry, readToday } from "./intake";

const MANAGER = "X-Persona-Manager";
let seen: { method: string; url: string; manager: string | null }[];
const real = globalThis.fetch;

beforeEach(() => {
  seen = [];
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    seen.push({ method: init.method ?? "GET", url: url.replace(/^.*\/api\/v1/, ""), manager: new Headers(init.headers).get(MANAGER) });
    return new Response(JSON.stringify({ water: { today: 0, goal: 2000 }, protein: { today: 0, goal: 60 }, entry: { amount: 250 }, today: { water: 0, protein: 0 } }), { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = real;
  resetManagerChoice();
});

describe("intake requests and the shown manager", () => {
  it("the control: an assistant request without `space=` does name the shown manager", async () => {
    const { api } = await import("../../lib/api");
    chooseManager("m-1");
    await api("/assistant/health/intake?days=1");
    expect(seen.at(-1)?.manager).toBe("m-1"); // so the next test proves something
  });

  it("read, log and undo all name the health space and no manager, with another manager shown", async () => {
    chooseManager("m-1");
    seen.length = 0; // choosing one reads its page again
    await readToday();
    const draft = newDraft("water");
    await postEntry(draft);
    await deleteEntry(draft.id);
    expect(seen.map((s) => `${s.method} ${s.url}`)).toEqual([
      "GET /assistant/health/intake?space=health&days=1",
      "POST /assistant/health/intake?space=health",
      `DELETE /assistant/health/intake/${draft.id}?space=health`,
    ]);
    expect(seen.map((s) => s.manager)).toEqual([null, null, null]);
  });

  it("is the same with the default shown", async () => {
    await readToday();
    expect(seen.map((s) => s.manager)).toEqual([null]);
  });
});
