import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");

test("an explicit hub change closes the old window before making its own session and IPC", async () => {
  const main = await source("main.ts");
  const change = main.slice(main.indexOf("async function replaceMainWindow"), main.indexOf("async function showMain"));
  assert.match(change, /parseHubAddress\(previous\.webContents\.getURL\(\)\)\.url\.href === next\.url\.href/);
  assert.match(change, /const closed = new Promise<void>\(done => previous\.once\("closed", done\)\);/);
  assert.ok(change.indexOf("await closed;") < change.indexOf("mainWindow = undefined;"));
  const connect = main.slice(main.indexOf("connect: async"), main.indexOf("await setup.loadURL(SETUP_URL)"));
  assert.ok(connect.indexOf("const generation = hubWindow.generation") < connect.indexOf("await showMain()"));
  assert.ok(connect.indexOf("await showMain()") < connect.indexOf("await store().update({ hub: next.url.href })"));
  assert.ok(connect.indexOf("await store().update({ hub: next.url.href })") < connect.indexOf("setup.close()"));
  assert.match(connect, /generation !== hubWindow\.generation \|\| hub\?\.url\.href !== next\.url\.href/);
  assert.match(connect, /Could not save the hub connection/);
  assert.match(connect, /failedWindow\.destroy\(\)/);
  assert.match(main, /hubWindow\.replace\(\);/);
  assert.match(main, /hub: \(\) => \{\s+const url = current\.webContents\.getURL\(\);/);
  const ipc = await source("ipc.ts");
  assert.match(ipc, /inHub\(hub, event\.senderFrame\.url\)/);
});

test("local login-item IPC is separate from the surviving hub window's public bridge", async () => {
  const setup = await source("setup-ipc.ts");
  const preload = await source("setup-preload.cts");
  for (const suffix of ["get", "set"]) {
    assert.match(setup, new RegExp(`"desktop:setup-login-${suffix}"`));
    assert.match(preload, new RegExp(`"desktop:setup-login-${suffix}"`));
    assert.ok(!setup.includes(`"desktop:login-${suffix}"`), `setup must not own desktop:login-${suffix}`);
  }
  const hub = await source("ipc.ts");
  for (const name of ["desktop:login-get", "desktop:login-set"]) assert.match(hub, new RegExp(`"${name}"`));
});

test("quitting while setup is the only window does not open setup again (#9458)", async () => {
  const main = await source("main.ts");
  const closed = main.slice(main.indexOf('setup.once("closed"'), main.indexOf("const removeIPC = installSetupIPC"));
  assert.ok(closed.includes("if (quitting) return;"), "setup's closed handler must stop once the app is quitting");
  assert.ok(closed.indexOf("if (quitting) return;") < closed.indexOf("showMain()"), "the quitting check must come before reopening");
  // Every quit (menu, Dock, signal, tray) passes before-quit, which runs before windows close.
  assert.match(main, /app\.on\("before-quit", \(\) => \{ quitting = true;/);
});
