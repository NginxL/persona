// The section bar's shared cases (spec/fixtures/inbox/bar*.json, docs/assistant/inbox.md "Section bar", #9053), replayed
// through what the list itself uses: the items filed by sections() (matches, statusOf), the Agents waiting picked by
// waitingAgents(), then the bar's own rules. So the counts checked here are the numbers the Inbox draws.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { AgentMutes } from "./agent-groups";
import { sections, waitingAgents, type InboxFilter, type InboxItem } from "./inbox";
import { barLanding, barRows, barStep, inboxBar, searching, type BarChoice, type BarSearch, type BarStep } from "./inbox-bar";
import type { Interaction, Session } from "./types";

const fixture = <T,>(name: string) => JSON.parse(readFileSync(join(__dirname, "../../../spec/fixtures/inbox", name), "utf8")) as { operation: string; cases: T[] };

type BarCase = {
  name: string;
  input: {
    items: InboxItem[];
    interactions: Interaction[];
    sessions: Session[];
    mutes: AgentMutes;
    settings: { source: string; kind: string; query: string; chosen: BarChoice };
    done_days_total?: number;
  };
  expected: { bar: { id: BarChoice; count: number | null; tone: string | null }[]; shown_sections: string[]; empty: string | null };
};
type LandingCase = { name: string; input: { item: InboxItem }; expected: { choice: BarChoice; open_collapsed: boolean } };
type SearchCase = {
  name: string;
  input: { start: { chosen: BarChoice; saved: BarChoice | null; query: string }; steps: BarStep[] };
  expected: { after: { chosen: BarChoice; saved: BarChoice | null }[] };
};

/** The fixture's input as the web list has it: sessions by computer, the filter the chips and the search make. */
function replay({ items, interactions, sessions, mutes, settings, done_days_total }: BarCase["input"]) {
  const byMachine: Record<string, Session[]> = {};
  for (const s of sessions) (byMachine[s.machine_id] ??= []).push(s);
  const filter: InboxFilter = { source: settings.source, kind: settings.kind, query: settings.query };
  const agents = waitingAgents(interactions, byMachine, mutes, filter);
  const rows = barRows(sections(items, filter), agents.length);
  return inboxBar({ rows, chosen: settings.chosen, chips: Boolean(settings.source || settings.kind), search: searching(settings.query), doneDays: done_days_total });
}

describe("the section bar (spec/fixtures/inbox/bar.json)", () => {
  const file = fixture<BarCase>("bar.json");
  it("is the inbox_bar operation", () => expect(file.operation).toBe("inbox_bar"));
  for (const c of file.cases) {
    it(c.name, () => {
      const got = replay(c.input);
      expect(got.bar).toEqual(c.expected.bar);
      expect(got.shown).toEqual(c.expected.shown_sections);
      expect(got.empty).toBe(c.expected.empty);
    });
  }
});

describe("where an item opened from outside the list lands (bar_landing.json)", () => {
  const file = fixture<LandingCase>("bar_landing.json");
  it("is the inbox_bar_landing operation", () => expect(file.operation).toBe("inbox_bar_landing"));
  for (const c of file.cases) {
    it(c.name, () => {
      expect(barLanding(c.input.item)).toEqual({ choice: c.expected.choice, openCollapsed: c.expected.open_collapsed });
    });
  }
});

describe("how a search moves between choices (bar_search.json)", () => {
  const file = fixture<SearchCase>("bar_search.json");
  it("is the inbox_bar_search operation", () => expect(file.operation).toBe("inbox_bar_search"));
  for (const c of file.cases) {
    it(c.name, () => {
      let s: BarSearch = { ...c.input.start };
      const after = c.input.steps.map((step) => {
        s = barStep(s, step);
        return { chosen: s.chosen, saved: s.saved };
      });
      expect(after).toEqual(c.expected.after);
    });
  }
});
