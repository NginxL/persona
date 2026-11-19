import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { HubWindowLifecycle } from "../src/hub-window-lifecycle.js";
import { DesktopIPCOwner } from "../src/ipc-owner.js";
import { loadStrings } from "../src/strings.js";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");
const locales = join(import.meta.dirname, "..", "..", "src", "locales");

function section(text: string, start: string, end?: string): string {
  const from = text.indexOf(start);
  assert.notEqual(from, -1, `missing source section: ${start}`);
  const to = end ? text.indexOf(end, from + start.length) : -1;
  if (end) assert.notEqual(to, -1, `missing source section: ${end}`);
  return text.slice(from, to < 0 ? undefined : to);
}

test("AC1: an offline saved-hub launch keeps its owner, partition and saved address while opening local setup", async () => {
  const main = await source("main.ts");
  const startup = section(main, "const saved = await store().read", "tray = installTray");
  const recovery = startup.slice(startup.indexOf("if (hub) await showMain()"));
  assert.notEqual(recovery.indexOf("if (hub) await showMain()"), -1, "the saved hub must still be loaded at startup");
  assert.match(recovery, /showSetup\(\)/, "connection recovery must reuse the existing local setup window");
  assert.doesNotMatch(recovery, /store\(\)\.update/, "startup recovery must not persist over the saved hub");
  assert.doesNotMatch(recovery, /hub\s*=\s*undefined/, "a connection failure is not a first-run, unconfigured profile");
  assert.doesNotMatch(recovery, /hubWindow\.replace\(\)/, "startup failure must not retire the saved hub's generation");

  const showMain = section(main, "async function showMain", "async function showSetup");
  assert.ok(showMain.indexOf("const owner = hub;") < showMain.indexOf("new BrowserWindow"));
  assert.match(showMain, /partition: hubPartition\(owner\)/, "the retry owner must retain its authenticated session partition");
});

test("AC2: a failed connection remains retryable without clearing or overwriting its saved hub", async () => {
  const main = await source("main.ts");
  const connect = section(main, "connect: async", "await setup.loadFile");
  const failure = connect.slice(connect.lastIndexOf("} catch (error)"));
  assert.match(failure, /generation !== hubWindow\.generation/, "obsolete failures must remain generation-guarded");
  assert.doesNotMatch(failure, /store\(\)\.update/, "a failed load must not persist a replacement or empty address");
  assert.doesNotMatch(failure, /hub\s*=\s*undefined/, "the failed address must remain available to Retry and to setup's hub getter");

  const lifecycle = new HubWindowLifecycle<string>();
  let attempts = 0;
  const saved = "https://hub.example.test/persona/";
  await assert.rejects(lifecycle.show(async () => { attempts++; throw new Error("offline"); }));
  const loaded = await lifecycle.show(async () => { attempts++; return saved; });
  assert.equal(loaded, saved, "a retry loads the same saved address");
  assert.equal(attempts, 2, "a settled failure must not leave the lifecycle stuck in its pending attempt");
});

test("AC1/AC2: local setup presents a connection explanation, Retry and an explicit Local settings choice", async () => {
  const html = await source("setup.html");
  const script = await source("setup.js");
  assert.match(html, /data-i18n="hub_connection_failed"/, "setup must explain the connection failure rather than looking like first run");
  for (const [id, key] of [["retry", "retry"], ["local-settings", "local_settings"]] as const) {
    assert.match(html, new RegExp(`id="${id}"`), `setup needs a #${id} action`);
    assert.match(html, new RegExp(`data-i18n="${key}"`), `#${id} needs localized text`);
    assert.match(script, new RegExp(`querySelector\\("#${id}"\\)`), `#${id} must have a renderer action`);
  }
  const retry = script.indexOf('querySelector("#retry")');
  assert.match(script.slice(retry < 0 ? 0 : retry), /saveHub\(field\.value\)/, "Retry must resubmit the unchanged address through the existing validated setup bridge");
});

test("AC5: connection recovery copy is complete and plain-language in every shipped locale", async () => {
  for (const locale of ["en", "zh-Hans", "ja"] as const) {
    const strings = await loadStrings(locales, locale);
    for (const key of ["hub_connection_failed", "retry", "local_settings"]) {
      assert.ok(strings[key]?.trim(), `${locale} is missing ${key}`);
      assert.notEqual(strings[key], key, `${locale} exposes the raw ${key} key`);
    }
  }
  const english = await loadStrings(locales, "en");
  assert.match(english.hub_connection_failed ?? "", /hub/i);
  assert.match(english.hub_connection_failed ?? "", /connect/i);
  assert.equal(english.retry, "Retry");
  assert.equal(english.local_settings, "Local settings");
});

test("AC3: an old-generation failure cannot destroy the replacement's lifecycle or IPC ownership", async () => {
  const lifecycle = new HubWindowLifecycle<string>();
  const ipc = new DesktopIPCOwner();
  let rejectOld: ((error: Error) => void) | undefined;
  const old = lifecycle.show(() => new Promise<string>((_, reject) => { rejectOld = reject; }));
  const oldIPC = ipc.install();
  lifecycle.replace();
  const replacement = lifecycle.show(async () => "https://new.example.test/");
  const newIPC = ipc.install();
  rejectOld!(new Error("old hub is offline"));
  await assert.rejects(old, /offline/);
  oldIPC();
  assert.equal(await replacement, "https://new.example.test/");
  assert.equal(ipc.active, true, "old cleanup must not remove the replacement's IPC handlers");
  newIPC();
  assert.equal(ipc.active, false);

  const main = await source("main.ts");
  const showMain = section(main, "async function showMain", "async function showSetup");
  const loadFailure = showMain.slice(showMain.indexOf("} catch (error) {", showMain.indexOf("await current.loadURL")));
  assert.match(loadFailure, /if \(owner === hub && generation === hubWindow\.generation\) throw error;/, "only the current owner may escalate its load failure");
  const connect = section(main, "connect: async", "await setup.loadFile");
  assert.ok(connect.indexOf("generation !== hubWindow.generation") < connect.indexOf("await store().update({ hub: next.url.href })"), "stale loads must be rejected before persistence");
});

test("AC4: quitting during pending startup cannot reopen recovery", async () => {
  const main = await source("main.ts");
  const startup = section(main, "if (hub) await showMain()", "tray = installTray");
  const startupSetup = startup.indexOf("await showSetup()");
  assert.notEqual(startupSetup, -1);
  assert.ok(startup.slice(0, startupSetup).includes("quitting"), "startup recovery must check quitting before opening another window");
  assert.match(main, /setup\.once\("closed"[\s\S]*?if \(quitting\) return;/, "the existing setup close path must keep its quit guard");
});

test("AC4: closing setup during a pending retry cannot reopen it when that attempt fails", async () => {
  const main = await source("main.ts");
  const connect = section(main, "connect: async", "await setup.loadFile");
  const failure = connect.slice(connect.lastIndexOf("} catch (error)"));
  assert.doesNotMatch(failure, /showSetup\(\)/, "a failed pending retry must not recreate setup after the person closed its last window");
});

test("AC4: a queued connection cannot start after its setup window has closed", async () => {
  const main = await source("main.ts");
  const connect = section(main, "connect: async", "await setup.loadFile");
  const run = section(connect, "const run = async", "const result = previous");
  const guard = run.indexOf("if (quitting || setup.isDestroyed())");
  assert.notEqual(guard, -1, "a queued submission must check that its setup still exists before starting another generation");
  assert.ok(guard < run.indexOf("await replaceMainWindow"), "the closed-setup guard must run before any new hub load or persistence");
});

test("AC4: cancelling a hub replacement restores the prior owner without touching a newer generation", async () => {
  const main = await source("main.ts");
  const closed = section(main, 'setup.once("closed"', "const removeIPC = installSetupIPC");
  assert.match(closed, /previousHub/, "closing an active replacement must restore the hub that was already saved");
  assert.match(closed, /previousConnectionFailed/, "the prior connection state must return with its owner");
  assert.match(closed, /generation === hubWindow\.generation|generation === currentGeneration/, "rollback must be fenced to the cancelled attempt's generation");
});

test("offline recovery keeps a second-instance deep link for the successful retry", async () => {
  const main = await source("main.ts");
  const navigate = section(main, "function navigateFromDeepLink", "function secureHubWindow");
  assert.match(navigate, /if \(!hub \|\| !mainWindow \|\| mainWindow\.isDestroyed\(\)\) \{ pendingDeepLink = raw; void showSetup\(\); return; \}/);
  const ready = section(main, 'current.once("ready-to-show"', 'current.webContents.on("render-process-gone"');
  assert.match(ready, /navigateFromDeepLink\(pendingDeepLink\)/, "the existing ready-to-show path must deliver the queued link after Retry");
});
