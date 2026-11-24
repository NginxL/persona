import { describe, expect, it } from "vitest";
import triageFixture from "../../../spec/fixtures/triage/triage.json";
import groupsFixture from "../../../spec/fixtures/triage/groups.json";
import relatedFixture from "../../../spec/fixtures/triage/related.json";
import type { Session } from "./types";
import { makeSessionLookup, type Todo } from "./todos";
import { relatedKeys, TRIAGE_SECTIONS, triage, triageTodos } from "./todo-triage";

// The hub reads these same rules. The web presents the legacy `pr` section inside Parked; native clients still use `pr`.
describe("the To-dos SOP's shared cases (spec/fixtures/triage)", () => {
  for (const c of triageFixture.cases) {
    it(`triage: ${c.name}`, () => {
      const now = Date.parse(c.input.now);
      const lookup = makeSessionLookup(c.input.sessions as unknown as Session[]);
      const input = c.input as { restored_at?: string };
      const restored = input.restored_at ? Date.parse(input.restored_at) : 0;
      const got = triage(c.input.todo as unknown as Todo, lookup, now, restored);
      expect({ section: got.section, rule: got.rule }).toEqual({ section: c.expected.section === "pr" ? "parked" : c.expected.section, rule: c.expected.rule });
      const want = c.expected as { last?: string };
      if (want.last) expect(got.last).toBe(Date.parse(want.last));
    });
  }

  for (const c of groupsFixture.cases) {
    it(`groups: ${c.name}`, () => {
      const lookup = makeSessionLookup(c.input.sessions as unknown as Session[]);
      const r = triageTodos(c.input.todos as unknown as Todo[], lookup, Date.parse(c.input.now), new Map(), { quiet: c.input.include_quiet });
      const want = c.expected.sections as Record<string, string[][]>;
      for (const s of TRIAGE_SECTIONS) expect(r.sections[s].map((g) => g.members.map((m) => m.id))).toEqual(s === "parked" ? [...(want.pr ?? []), ...(want.parked ?? [])] : want[s] ?? []);
      expect([r.open, r.shelved, r.finished, r.cancelled]).toEqual([c.expected.open, c.expected.shelved, c.expected.finished, (c.expected as { cancelled?: number }).cancelled ?? 0]);
    });
  }

  for (const c of relatedFixture.cases) {
    it(`related keys: ${c.name}`, () => {
      expect(relatedKeys(c.input.todo as unknown as Todo)).toEqual(c.expected.keys);
    });
  }
});
