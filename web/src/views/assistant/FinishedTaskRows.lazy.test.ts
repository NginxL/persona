/// <reference types="node" />
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../../i18n/startup-namespaces.json";
import { startupMatcher } from "../../i18n/startup-keys";

// #10922: the Finished tray's rows come with its first opening, in the Parked rows' chunk, not with the first screen.
describe("the Finished tray's rows stay off the first screen", () => {
  it("loads its rows, seeing a task and its empty line only when the tray opens, with no lazy entry of its own", () => {
    const source = readFileSync(new URL("./Tasks.tsx", import.meta.url), "utf8");
    expect(source).not.toMatch(/^import[^;]*from\s+["']\.\/(FinishedTaskRows|ParkedTaskRow)["']/m);
    expect(source).not.toMatch(/^import[^;]*from\s+["']\.\.\/\.\.\/lib\/todo-seen["']/m);
    expect(source).toContain('lazyPage(() => import("./ParkedTaskRow").then((m) => m.FinishedTaskRows))');
    expect(source).not.toContain('import("./FinishedTaskRows")');
    const inStartup = startupMatcher(startup);
    expect(inStartup("assistant-finishedtaskrows.empty")).toBe(false);
    expect(inStartup("assistant-tasks.tray_finished")).toBe(true);
  });
});
