import { expect, it } from "vitest";
import { testWorkerLimit, testWorkers } from "./test-scheduling";

it("bounds file workers without leaving a small machine serial", () => {
  expect(testWorkers(1)).toBe(1);
  expect(testWorkers(2)).toBe(1);
  expect(testWorkers(4)).toBe(3);
  expect(testWorkers(9)).toBe(8);
  expect(testWorkers(64)).toBe(8);
  expect(testWorkers(128)).toBe(8);
});

it("keeps an explicit caller or project worker limit", () => {
  expect(testWorkers(64, 2)).toBe(2);
  expect(testWorkers(64, "50%")).toBe("50%");
  expect(testWorkerLimit().config?.call({}, { test: { maxWorkers: 2 } }, {} as never)).toBeUndefined();
  expect(testWorkerLimit().config?.call({}, { test: { maxWorkers: "50%" } }, {} as never)).toBeUndefined();
});

it("sets a default through Vite's config hook without replacing other test options", () => {
  expect(testWorkerLimit(64).config?.call({}, { test: { isolate: true } }, {} as never)).toEqual({ test: { maxWorkers: 8 } });
});
