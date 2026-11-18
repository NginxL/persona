import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// The usage popover (UsagePopover.tsx) opens upward from the sidebar's bottom over the rows' column. The column's own layers
// (the sticky Persona row and group headers at z-30, segments, chips) must stay inside it, or they draw over the popover.
const source = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");

describe("sidebar layers", () => {
  it("the rows' column is its own stacking context, under the usage popover", () => {
    const rows = source("./Sidebar.tsx").match(/<div ref=\{nav\} data-sidebar-rows[^\n]*?className="([^"]*)"/);
    expect(rows?.[1].split(/\s+/)).toContain("isolate");
    expect(source("./UsagePopover.tsx")).toMatch(/role="dialog"[^\n]*?className="[^"]*\babsolute bottom-full\b[^"]*\bz-\d+/);
  });
});
