/// <reference types="node" />
// Settings draws the provider list, never the wizard or the import until a click opens one: their code (and
// ProviderPaste / ProviderScope, which only the wizard uses) loads on that first open, not with every Settings page
// (Settings.tsx, lazyOverlay / lazyDialog).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { describe, expect, it } from "vitest";

const root = new URL("../../", import.meta.url);
const dialogs = ["ProviderWizard", "ProviderImport", "ProviderPaste", "ProviderScope"];

// The assertions below resolve the real lazy chunks; collection pays their cold graph evaluation once, outside each
// test's unchanged deadline, instead of timing two sequential imports under the complete parallel suite.
await Promise.all([import("./ProviderWizard"), import("./ProviderImport")]);
const staticImport = new RegExp(`^import\\s+(?!type\\b)[^;]*from\\s+["'][./]*(?:views/)?(?:settings/)?(?:${dialogs.join("|")})["']`, "m");

function sources(dir: URL): string[] {
  return readdirSync(dir).flatMap((name) => {
    const u = new URL(name, dir);
    if (statSync(u).isDirectory()) return sources(new URL(name + "/", dir));
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [u.pathname] : [];
  });
}

describe("the provider dialogs boundary", () => {
  it("leaves the dialogs out of every static import but the wizard's own", () => {
    const allowed = new Set(["ProviderWizard.tsx"]); // it imports ProviderPaste and ProviderScope, in its own chunk
    for (const file of sources(root)) {
      const name = file.split("/").pop()!;
      if (allowed.has(name)) continue;
      expect(readFileSync(file, "utf8"), file).not.toMatch(staticImport);
    }
  });

  it("loads them in Settings on the first open", () => {
    const settings = readFileSync(new URL("../Settings.tsx", import.meta.url), "utf8");
    expect(settings).toMatch(/lazyOverlay\(\(\) => import\("\.\/settings\/ProviderWizard"\)\.then\(\(m\) => m\.ProviderWizard\)\)/);
    expect(settings).toMatch(/lazyDialog\(\(\) => import\("\.\/settings\/ProviderImport"\)\.then\(\(m\) => m\.ProviderImport\)\)/);
    expect(settings).toMatch(/useOpened\(editing !== null\)/);
  });

  it("resolves the dynamic imports to the existing components", async () => {
    expect(typeof (await import("./ProviderWizard")).ProviderWizard).toBe("function");
    expect(typeof (await import("./ProviderImport")).ProviderImport).toBe("function");
  });
});
