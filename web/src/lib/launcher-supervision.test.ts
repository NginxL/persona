import { expect, it } from "vitest";
import { launcherSupervisionPolicy } from "./launcher-supervision";
it("does not inherit ordinary Assistant capability as a supervision channel", () => {
  expect(launcherSupervisionPolicy({ name: "opencode", source: "opencode", assistant: true })).toBe(false);
  expect(launcherSupervisionPolicy({ name: "opencode", source: "opencode", assistant: true, supervision_policy: true })).toBe(true);
  expect(launcherSupervisionPolicy({ name: "ordinary", source: "opencode", supervision_policy: true })).toBe(false);
  expect(launcherSupervisionPolicy({ name: "grok", source: "grok", assistant: true })).toBe(false);
  expect(launcherSupervisionPolicy({ name: "codex", source: "path", agent: "codex" })).toBe(true);
});
