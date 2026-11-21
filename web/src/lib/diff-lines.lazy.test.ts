/// <reference types="node" />
// A work group's edit rows (views/chat/ActionRow.tsx, a first-screen file) need only the line diff for their +N -N.
// Importing it from lib/compare.ts put the Git panel's document readers (Word, PowerPoint, Excel, PDF) and their
// lib-compare strings on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import { diffLines as fromCompare } from "./compare";
import { diffLines } from "./diff-lines";

const read = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");

describe("the line diff's loading boundary", () => {
  it("keeps lib/compare.ts and its strings off the first screen", () => {
    expect(read("../views/chat/ActionRow.tsx")).not.toMatch(/from\s+["'][./]*lib\/compare["']/);
    expect(read("./tool-detail.ts")).not.toMatch(/from\s+["']\.\/compare["']/);
    expect(read("./diff-lines.ts")).not.toMatch(/^import\s/m);
    expect(startup).not.toContain("lib-compare");
  });

  it("is the same diff the Compare view uses", () => {
    expect(fromCompare).toBe(diffLines);
    expect(diffLines(["a", "b"], ["a", "c"])).toEqual([
      { kind: "same", text: "a" },
      { kind: "del", text: "b" },
      { kind: "add", text: "c" },
    ]);
  });
});
