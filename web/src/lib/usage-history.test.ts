import { describe, expect, it } from "vitest";
import { calendarLayout, dayTotal, heatLevels, hourLayout, lastDays, weeksThatFit, type UsageDay } from "./usage-history";

const days = (list: [string, number][]): UsageDay[] => list.map(([date, n]) => ({ date, cache_read: n }));

describe("heatLevels", () => {
  it("steps go by rank among the values with usage, the largest last, equal ones together", () => {
    expect(heatLevels([0, 10, 20, 30, 40])).toEqual([0, 1, 2, 3, 4]);
    expect(heatLevels([10, 10, 10, 1000])).toEqual([3, 3, 3, 4]);
    expect(heatLevels([5])).toEqual([4]);
    expect(heatLevels([0, 0])).toEqual([0, 0]);
    expect(heatLevels([])).toEqual([]);
  });

  it("one very large day does not push the others to the faintest (the iPhone's rule too)", () => {
    const list = [...Array.from({ length: 11 }, (_, i) => i + 1), 1000];
    expect(new Set(heatLevels(list))).toEqual(new Set([1, 2, 3, 4]));
  });
});

describe("calendarLayout", () => {
  it("puts a week in a column, Monday on top, this week last without the days to come", () => {
    // Tuesday 17 November 2026 is today.
    const l = calendarLayout(days([["2026-11-15", 5], ["2026-11-16", 10], ["2026-11-17", 20]]), 3);
    expect(l.columns).toHaveLength(3);
    expect(l.columns[0][0].date).toBe("2026-11-02");
    expect(l.columns[2][0].date).toBe("2026-11-16");
    expect(l.columns[2][2]).toEqual({ date: null, level: -1 });
    expect(l.today).toEqual([2, 1]);
    expect([l.columns[1][6].level, l.columns[2][0].level, l.columns[2][1].level, l.columns[0][0].level]).toEqual([2, 3, 4, 0]);
    expect(l.months).toEqual([{ column: 0, first: "2026-11-01" }]);
  });

  it("names a month over the first week that starts in it, not over a first column it began before", () => {
    const l = calendarLayout(days([["2026-11-17", 1]]), 8);
    expect(l.columns[0][0].date).toBe("2026-09-28");
    expect(l.months).toEqual([{ column: 1, first: "2026-10-01" }, { column: 5, first: "2026-11-01" }]);
  });

  it("has nothing for no days", () => {
    expect(calendarLayout([], 10)).toEqual({ columns: [], today: null, months: [] });
  });
});

describe("sums and widths", () => {
  it("a day is every kind of token; today and the last 30 days add up", () => {
    const list: UsageDay[] = [{ date: "2026-11-16", in: 1, cache_read: 99 }, { date: "2026-11-17", out: 5, cache_write: 5, req: 2 }];
    expect(dayTotal(list[1])).toBe(10);
    expect(lastDays(list, 1)).toBe(10);
    expect(lastDays(list, 30)).toBe(110);
  });

  it("fits as many weeks as the width holds, within 8 and 53", () => {
    expect(weeksThatFit(301, 13, 3)).toBe(19);
    expect(weeksThatFit(40, 13, 3)).toBe(8);
    expect(weeksThatFit(5000, 13, 3)).toBe(53);
  });
});

describe("hourLayout", () => {
  const h = (date: string, hour: number, out: number) => ({ t: 0, date, hour, out });
  it("a row a day, a column an hour; today's hours to come are -1; the hour that comes twice adds up", () => {
    const hours = [
      ...Array.from({ length: 24 }, (_, i) => h("2026-11-16", i, i === 9 ? 100 : 0)),
      h("2026-11-16", 1, 5), // the clocks went back: 1 a.m. twice
      h("2026-11-17", 0, 1),
      h("2026-11-17", 1, 0),
      h("2026-11-17", 2, 50), // the hour under way
    ];
    const l = hourLayout(hours);
    expect(l.dates).toEqual(["2026-11-16", "2026-11-17"]);
    expect(l.tokens[0][1]).toBe(5);
    expect(l.levels[0][9]).toBe(4); // the largest is the last step
    expect(l.levels[0][0]).toBe(0);
    expect(l.levels[1].slice(0, 3).every((v) => v >= 0)).toBe(true);
    expect(l.levels[1].slice(3)).toEqual(Array(21).fill(-1));
  });

  it("has nothing for no hours", () => {
    expect(hourLayout([])).toEqual({ dates: [], levels: [], tokens: [] });
  });
});
