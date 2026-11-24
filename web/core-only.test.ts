// The core-only lane's plugin (web/core-only.ts, #13932): the module list resolves to an empty one, a core file reaching
// a module's file is refused, and a module's own imports and everything else resolve as before.
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { coreOnly, moduleFileKind } from "./core-only";

const src = fileURLToPath(new URL("./src/", import.meta.url));
const modules = src + "modules/";

describe("moduleFileKind", () => {
  it("tells the module list, a module's file and everything else apart", () => {
    expect(moduleFileKind(modules + "index.ts")).toBe("list");
    expect(moduleFileKind(modules + "health/module.ts")).toBe("module");
    expect(moduleFileKind(modules + "health/Health.tsx?import")).toBe("module");
    expect(moduleFileKind(src + "core/module-host.ts")).toBeNull();
    expect(moduleFileKind(src + "modules.ts")).toBeNull();
  });
});

type Resolve = (source: string, importer?: string) => Promise<unknown>;

/** The plugin's resolveId, with Vite's own resolution standing in as a plain path join. */
function resolver(): { resolve: Resolve; errors: string[] } {
  const plugin = coreOnly(true);
  const errors: string[] = [];
  const ctx = {
    resolve: async (source: string, importer: string) => ({ id: source.startsWith(".") ? fileURLToPath(new URL(source, "file://" + importer)) : source, external: false }),
    error: (message: string) => {
      errors.push(message);
      throw new Error(message);
    },
  };
  const hook = plugin.resolveId as unknown as (this: typeof ctx, source: string, importer: string | undefined, options: object) => Promise<unknown>;
  return { resolve: (source, importer) => hook.call(ctx, source, importer, {}), errors };
}

describe("coreOnly", () => {
  it("is inert unless the lane is on", () => {
    const off = coreOnly(false).apply as (config: object, env: object) => boolean;
    const on = coreOnly(true).apply as (config: object, env: object) => boolean;
    expect(off({}, {})).toBe(false);
    expect(on({}, {})).toBe(true);
  });

  it("gives the core an empty module list", async () => {
    const { resolve } = resolver();
    expect(await resolve("./modules/index.ts", src + "App.tsx")).toBe("\0persona-core-only:modules");
    const code = (coreOnly(true).load as unknown as (id: string) => string | null)("\0persona-core-only:modules");
    expect(code).toContain("export const MODULES = [];");
    expect(code).toContain("export function registerModules()");
  });

  it("refuses a core file that reaches a module's file, naming both", async () => {
    const { resolve, errors } = resolver();
    await expect(resolve("../modules/health/reports.ts", src + "lib/manager-choice.ts")).rejects.toThrow(/core-only build/);
    expect(errors[0]).toContain("src/lib/manager-choice.ts imports src/modules/health/reports.ts");
  });

  it("lets a module import its own files and the core, and leaves the rest alone", async () => {
    const { resolve } = resolver();
    expect(await resolve("./reports.ts", modules + "health/Health.tsx")).toEqual({ id: modules + "health/reports.ts", external: false });
    expect(await resolve("../../lib/api.ts", modules + "health/Health.tsx")).toEqual({ id: src + "lib/api.ts", external: false });
    expect(await resolve("./lib/api.ts", src + "App.tsx")).toEqual({ id: src + "lib/api.ts", external: false });
    expect(await resolve("react", src + "App.tsx")).toEqual({ id: "react", external: false });
    expect(await resolve("./x.ts")).toBeNull();
  });
});
