import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import test from "node:test";

interface Protocol {
  readonly schemes?: string[];
}

interface BuilderConfiguration {
  readonly extends?: string;
  readonly protocols?: Protocol[] | null;
}

const { load } = createRequire(import.meta.url)("js-yaml") as { load(source: string): BuilderConfiguration };

// dist-test/test → the package root
const root = join(import.meta.dirname, "..", "..");

/** Read the effective protocol list through electron-builder's extends chain. */
function protocols(file: string): Protocol[] {
  const config = load(readFileSync(file, "utf8"));
  if (Object.hasOwn(config, "protocols")) return config.protocols ?? [];
  return config.extends ? protocols(resolve(dirname(file), config.extends)) : [];
}

test("release, unsigned and test packages register the persona URL scheme", async t => {
  for (const name of ["electron-builder.yml", "electron-builder.unsigned.yml", "electron-builder.update-test.yml"]) {
    await t.test(name, () => {
      assert.ok(protocols(join(root, name)).some(protocol => protocol.schemes?.includes("persona")), `${name} has or inherits a protocols entry with the persona scheme`);
    });
  }
});
