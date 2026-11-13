// @vitest-environment jsdom
// The sidebar's Recent count (#5146) and its new default of 5 (#6228): an untouched browser gets 5, a value chosen in
// Settings still wins, and Show more's count is kept until Show less.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { RECENT_COUNTS, RECENT_DEFAULT, recentCount, recentMore, setRecentCount, setRecentMore } from "./sidebar-recent";

beforeEach(() => localStorage.clear());
afterEach(() => localStorage.clear());

describe("the Recent count", () => {
  it("defaults to 5, the same number iOS and Android list, with nothing stored", () => {
    expect(RECENT_DEFAULT).toBe(5);
    expect(RECENT_COUNTS).toContain(5);
    expect(recentCount()).toBe(5);
  });

  it("keeps a count chosen in Settings and treats a broken stored value as the default", () => {
    setRecentCount(10);
    expect(recentCount()).toBe(10);
    setRecentCount(RECENT_DEFAULT); // the default stores nothing
    expect(localStorage.getItem("persona.sidebar-recent")).toBeNull();
    expect(recentCount()).toBe(5);
    localStorage.setItem("persona.sidebar-recent", "99");
    expect(recentCount()).toBe(5);
  });

  it("remembers how often Show more was pressed, only while it is a positive integer", () => {
    expect(recentMore()).toBe(0);
    setRecentMore(2);
    expect(recentMore()).toBe(2);
    setRecentMore(0);
    expect(localStorage.getItem("persona.sidebar-recent.more")).toBeNull();
    expect(recentMore()).toBe(0);
  });
});
