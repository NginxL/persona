import { describe, expect, it } from "vitest";
import fixture from "../../../../spec/fixtures/minutes-wait/cases.json";
import { minutesWait } from "./minutes-wait";

// The shared cases (spec/fixtures/minutes-wait, #13461), which the hub and the iPhone run too.
describe("minutesWait", () => {
  expect(fixture.version).toBe(1);
  expect(fixture.cases.length).toBeGreaterThanOrEqual(15);
  it.each(fixture.cases.map((c) => [c.name, c] as const))("%s", (_, c) => {
    const got = minutesWait(c.transcription, c.minutes_state, c.has_minutes);
    const want = c.expected as { line: string; done_minutes?: number; total_minutes?: number };
    expect(got === null ? "none" : got.line).toBe(want.line);
    expect(got && got.line === "checking" ? got.done : undefined).toBe(want.done_minutes);
    expect(got && got.line === "checking" ? got.total : undefined).toBe(want.total_minutes);
  });
});
