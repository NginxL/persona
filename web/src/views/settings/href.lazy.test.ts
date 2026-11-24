/// <reference types="node" />
// The Persona page, a first-screen file, links to Settings › Extensions. It needs only the URL: importing settingsHref
// from nav.ts put nav.ts and the words of every category (its TAB_INFO list, built at load) on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../../i18n/startup-namespaces.json";
import { settingsHref } from "./href";
import * as nav from "./nav";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("settingsHref", () => {
  it("keeps nav.ts and its category words off the first screen", () => {
    expect(read("../assistant/Persona.tsx")).not.toMatch(/from\s+["']\.\.\/settings\/nav["']/);
    // href.ts may name nav.ts's types, which leave no import behind
    expect(read("./href.ts")).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/nav["']/m);
    expect(startup.filter((key) => key === "settings-nav.hint_" || key.startsWith("settings-nav.tab_"))).toEqual([]);
  });

  it("is the same function nav.ts offers Settings", () => {
    expect(nav.settingsHref).toBe(settingsHref);
    expect(settingsHref("general", null, false)).toBe("/settings");
    expect(settingsHref("general", null, true)).toBe("/settings?tab=general");
    expect(settingsHref("extensions", "a b", false, "m1")).toBe("/settings?tab=extensions&extension=a%20b&manager=m1");
    expect(settingsHref("agents", "ignored", false, null, "codex")).toBe("/settings?tab=agents#settings-codex");
    expect(nav.settingsAt("?tab=extensions&extension=x")).toEqual({ tab: "extensions", sub: "x", section: null });
  });
});
