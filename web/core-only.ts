// The core-only lane (#13932, part of #13434 and #13430; docs/web/web.md "Modules"): Persona Core built and tested with
// zero feature modules, so the core alone is a tested product. `PERSONA_WEB_MODULES=none` (npm run build:core-only,
// npm run test:core-only, scripts/ci/web.sh) turns it on; without it this plugin does nothing.
//
// In that mode the module list (src/modules/index.ts) resolves to an empty one, so no module registers, and any import
// from a file outside src/modules that lands in a module's files fails the build or the test file: the ESLint boundary
// catches the import paths it can read, this catches whatever reaches a module at all once the paths are resolved.
// Module files themselves (their code and their tests) are never reached in this mode; vite.config.ts leaves their
// tests out. The build writes to node_modules/.cache/persona-core-only: outside the folders a dev server or a browser
// test's Vite watches, and never served.
import { fileURLToPath } from "node:url";
import { relative, sep } from "node:path";
import type { Plugin } from "vite";

const environment = (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env ?? {};

/** Whether this run is the core-only lane. */
export const CORE_ONLY = environment.PERSONA_WEB_MODULES === "none";

const MODULES_DIR = fileURLToPath(new URL("./src/modules/", import.meta.url));
const EMPTY_LIST = "\0persona-core-only:modules";

/** Where a resolved id lies relative to src/modules: "list" for the module list, "module" for a module's own file. */
export function moduleFileKind(id: string, modulesDir = MODULES_DIR): "list" | "module" | null {
  const file = id.split("?")[0]!;
  if (!file.startsWith(modulesDir)) return null;
  const rest = file.slice(modulesDir.length);
  if (rest === "index.ts" || rest === "index") return "list";
  return "module";
}

/** A readable path for an error, relative to web/. */
const shown = (id: string) => relative(fileURLToPath(new URL(".", import.meta.url)), id.split("?")[0]!).split(sep).join("/");

/** The core-only plugin: the empty module list and the refusal of core → module imports. Inert unless `enabled`. */
export function coreOnly(enabled = CORE_ONLY): Plugin {
  return {
    name: "persona-core-only",
    enforce: "pre",
    apply: () => enabled,
    async resolveId(source, importer, options) {
      if (source === EMPTY_LIST) return EMPTY_LIST;
      if (!importer || importer.startsWith("\0")) return null;
      const resolved = await this.resolve(source, importer, { ...options, skipSelf: true });
      if (!resolved || resolved.external) return resolved;
      const kind = moduleFileKind(resolved.id);
      if (kind === null) return resolved;
      // a module's file importing its own files (or core) never gets here: nothing outside src/modules reaches one
      if (moduleFileKind(importer) === "module") return resolved;
      if (kind === "list") return EMPTY_LIST;
      this.error(`core-only build (#13932): ${shown(importer)} imports ${shown(resolved.id)}, a feature module's file. The core must work with no modules: reach a module through the module host (docs/web/web.md "Modules").`);
    },
    load(id) {
      if (id !== EMPTY_LIST) return null;
      // the module list's exports (src/modules/index.ts), with no module in it
      return [
        `// The module list in the core-only lane (web/core-only.ts): empty.`,
        `export const MODULES = [];`,
        `export function registerModules() {}`,
      ].join("\n");
    },
  };
}
