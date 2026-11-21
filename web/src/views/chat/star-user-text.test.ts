import { beforeEach, expect, it, vi } from "vitest";
import type { Item } from "../../lib/types";
import { starUserText } from "./star-user-text";

const fullItem = vi.hoisted(() => vi.fn());
vi.mock("../../lib/api", () => ({ fullItem }));

const input = (id: string, text: string, extra?: Partial<Item>): Item => ({ id, seq: 1, role: "user", kind: "text", text, ...extra });
const origin = { mid: "fixture-machine", sid: "fixture-chat", item: "first#0" };

beforeEach(() => fullItem.mockReset());

it("keeps the parsed visible text for inputs already present whole", () => {
  expect(starUserText({ ...origin, userInputs: [input("first#0", "Whole input")] })).toBeUndefined();
  expect(fullItem).not.toHaveBeenCalled();
});

it("loads all clipped text blocks on intent and preserves the bubble's original grouping", async () => {
  const proof = "a".repeat(32);
  const pictured = input("first#1", "", { kind: "image" });
  const clipped = input("first#2", '<persona-context kind="skill-instructions">part of a hidden block', { full_size: 27_000, source_fingerprint: proof });
  const later = input("second#0", "Partial words", { full_size: 30_000 });
  const userInputs = [input("first#0", "First block"), pictured, clipped, later, input("second#1", "Last block")];
  const load = starUserText({ ...origin, userInputs, merged: true });
  expect(fullItem).not.toHaveBeenCalled();
  fullItem.mockResolvedValueOnce({ ...clipped, full_size: undefined,
    text: '<persona-context kind="skill-instructions">hidden instructions</persona-context>\nSecond block' })
    .mockResolvedValueOnce({ ...later, full_size: 0, text: "Whole later input" });
  await expect(load?.()).resolves.toBe("First block\nSecond block\n\nWhole later input\nLast block");
  expect(fullItem.mock.calls).toEqual([
    [origin.mid, origin.sid, clipped.id],
    [origin.mid, origin.sid, later.id],
  ]);
});

it("rejects a changed or still clipped full item instead of saving its preview", async () => {
  const block = input("line1#0", "Short preview", { full_size: 30_000, source_fingerprint: "a".repeat(32) });
  const load = starUserText({ ...origin, item: block.id, userInputs: [block] });
  fullItem.mockResolvedValueOnce({ ...block, full_size: undefined, text: "Replacement", source_fingerprint: "b".repeat(32) });
  await expect(load?.()).rejects.toThrow();
  fullItem.mockResolvedValueOnce(block);
  await expect(load?.()).rejects.toThrow();
});
