// The feature-module boundary (web/eslint.modules.config.mjs, #13434, docs/web/web.md "Modules") over the cases a review
// found: a module importing the list by any spelling ("../../modules", "../../modules/", "../", "../index"), and core
// reaching a module through a template literal. Each case is linted as if it were the file named.
import { fileURLToPath } from "node:url";
import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

// web/, where the config lives (kept here, under src, so every path this file names stays inside web/:
// scripts/ci/paths.py reads quoted relative paths in a test as what it reads)
const cwd = fileURLToPath(new URL("../../", import.meta.url));
const eslint = new ESLint({ cwd, overrideConfigFile: "eslint.modules.config.mjs" });

async function problems(file: string, code: string): Promise<number> {
  const [result] = await eslint.lintText(code, { filePath: `${cwd}${file}` });
  return result!.messages.filter((m) => m.severity === 2).length;
}

describe("the module boundary", () => {
  it("keeps a module from importing the module list, however it is spelled", async () => {
    for (const spec of ["../../modules", "../../modules/", "../../modules/index", "../", "..", "../index", "../index.ts"]) {
      expect(await problems("src/modules/health/Example.ts", `import x from "${spec}";\nexport { x };`), spec).toBeGreaterThan(0);
      expect(await problems("src/modules/health/Example.ts", `export const f = () => import("${spec}");`), `import(${spec})`).toBeGreaterThan(0);
    }
  });

  it("keeps a module from another module it does not need, and lets it import its own files and core", async () => {
    expect(await problems("src/modules/health/Example.ts", 'import "../garden/module";')).toBeGreaterThan(0);
    expect(await problems("src/modules/health/Example.ts", 'import "../../modules/garden/x";')).toBeGreaterThan(0);
    expect(await problems("src/modules/health/Example.ts", "export const f = (x: string) => import(`../${x}`);")).toBeGreaterThan(0);
    expect(await problems("src/modules/health/Example.ts", 'import "./reports";\nimport "../../lib/router";\nimport "../../core/module-host";')).toBe(0);
  });

  it("keeps core out of every module and the list, template literals included", async () => {
    for (const code of [
      "export const f = (x: string) => import(`../modules/health/${x}`);",
      "export const f = (x: string) => import(`../modules/${x}`);",
      "export const f = (x: string) => import(`../mod${x}`);",
      "export const f = (x: string) => import(`../${x}`);",
      'import "../modules";',
      'import "../modules/health/module";',
      'export { health } from "../modules/health/module";',
    ]) expect(await problems("src/core/Example.ts", code), code).toBeGreaterThan(0);
    expect(await problems("src/core/Example.ts", "export const f = (x: string) => import(`../lib/${x}`);")).toBe(0);
  });

  it("lets the rest of the app import the list, never a module's files", async () => {
    expect(await problems("src/App.tsx", 'import "./modules";')).toBe(0);
    expect(await problems("src/views/Example.ts", 'import "../modules/index";')).toBe(0);
    expect(await problems("src/views/Example.ts", 'import "../modules/health/Health";')).toBeGreaterThan(0);
    expect(await problems("src/views/Example.ts", "export const f = (x: string) => import(`../modules/health/${x}`);")).toBeGreaterThan(0);
    // tests may reach a module's files to test them
    expect(await problems("src/views/Example.test.ts", 'import "../modules/health/reports";')).toBe(0);
  });
});
