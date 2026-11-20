import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { join } from "node:path";
import test from "node:test";

interface BuildMetadata {
  minimums: string[];
  sdk: string;
  tool: string | null;
  toolVersion: string | null;
}

const root = join(import.meta.dirname, "..", "..");
const { parseBuildMetadata } = createRequire(import.meta.url)(join(root, "scripts", "macos-after-pack.cjs")) as {
  parseBuildMetadata(output: string): BuildMetadata;
};

const universal = `Persona (architecture x86_64):
Load command 11
      cmd LC_BUILD_VERSION
  cmdsize 32
 platform MACOS
    minos 13.0
      sdk 26.5
   ntools 1
     tool 4
  version 24.0
Persona (architecture arm64):
Load command 11
      cmd LC_BUILD_VERSION
  cmdsize 32
 platform MACOS
    minos 13.0
      sdk 26.5
   ntools 1
     tool 4
  version 24.0
`;

test("the afterPack parser preserves SDK and linker metadata from every slice", () => {
  assert.deepEqual(parseBuildMetadata(universal), {
    minimums: ["13.0", "13.0"], sdk: "26.5", tool: "4", toolVersion: "24.0",
  });
});

test("the afterPack parser rejects missing or ambiguous build metadata", () => {
  assert.throws(() => parseBuildMetadata("Persona:\n platform MACOS\n"), /no macOS minimum/);
  assert.throws(() => parseBuildMetadata(universal.replace("sdk 26.5", "sdk 26.4")), /one SDK version/);
  assert.throws(() => parseBuildMetadata(universal.replace("  version 24.0", "")), /incomplete linker metadata/);
});
