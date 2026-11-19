import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { SETUP_SCHEME, SETUP_URL, setupAsset, setupFile } from "../src/setup-page.js";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");

test("the setup scheme serves its page's three files with fixed content types and nothing else", () => {
  assert.equal(SETUP_SCHEME, "persona-setup");
  assert.equal(SETUP_URL, "persona-setup://app/setup.html");
  const expected: ReadonlyArray<readonly [string, string]> = [
    ["setup.html", "text/html; charset=utf-8"],
    ["setup.css", "text/css; charset=utf-8"],
    ["setup.cjs", "text/javascript; charset=utf-8"],
  ];
  for (const [name, contentType] of expected) {
    const url = `persona-setup://app/${name}`;
    const asset = setupAsset(url);
    assert.ok(asset?.file.startsWith("file://") && asset.file.endsWith(`/setup/${name}`), `${name}: ${asset?.file}`);
    assert.equal(asset?.contentType, contentType);
    assert.equal(setupFile(url), asset?.file);
    assert.deepEqual(setupAsset(url, "HEAD"), asset);
  }
  assert.equal(setupAsset(SETUP_URL, "POST"), undefined);
  assert.equal(setupFile(SETUP_URL, "PUT"), undefined);
  for (const raw of [
    "persona-setup://app/setup-preload.cjs", "persona-setup://app/main.js", "persona-setup://app/locales/en.json",
    "persona-setup://app/", "persona-setup://app/setup/setup.html", "persona-setup://app/../main.js",
    "persona-setup://app/%2e%2e/main.js", "persona-setup://app/setup%2Ehtml", "persona-setup://app/..\\main.js",
    "persona-setup://other/setup.html", "persona-setup://app:1/setup.html", "persona-setup://someone@app/setup.html",
    "persona-setup://app/setup.html?x=1", "persona-setup://app/setup.html#top", "persona-island://app/setup.html",
    "file:///setup.html", "https://app/setup.html", "not a url",
  ]) assert.equal(setupAsset(raw), undefined, raw);
});

test("packaged setup loads through the registered persona-setup scheme without relaxing fuses", async () => {
  const main = await source("main.ts");
  const registerMatches = main.match(/protocol\.registerSchemesAsPrivileged\(/g) ?? [];
  assert.equal(registerMatches.length, 1, "Electron accepts only one registerSchemesAsPrivileged call");
  assert.match(main, /scheme:\s*ISLAND_SCHEME[\s\S]*scheme:\s*SETUP_SCHEME/);
  assert.match(main, /installSetupProtocol\(\);/);
  assert.match(main, /await setup\.loadURL\(SETUP_URL\);/);
  assert.ok(!main.includes("setup.loadFile("), "setup must not load from file:// inside app.asar");

  const setupIPC = await source("setup-ipc.ts");
  assert.match(setupIPC, /sender\.getURL\(\)\s*===\s*SETUP_URL/);
  assert.ok(!setupIPC.includes("pathToFileURL"), "setup IPC must authorize only the persona-setup URL");

  const builder = await readFile(join(import.meta.dirname, "..", "..", "electron-builder.yml"), "utf8");
  assert.match(builder, /grantFileProtocolExtraPrivileges:\s*false/);
  assert.match(builder, /onlyLoadAppFromAsar:\s*true/);
  assert.match(builder, /enableEmbeddedAsarIntegrityValidation:\s*true/);
});
