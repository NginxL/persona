import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import test from "node:test";
import { PageRoutes } from "../src/page-routes.js";

const source = (name: string) => readFile(join(import.meta.dirname, "..", "..", "src", name), "utf8");

function routes(): { routes: PageRoutes; sent: string[] } {
  const sent: string[] = [];
  return { routes: new PageRoutes(route => sent.push(route)), sent };
}

test("a route that comes before the page listens waits for it, and only the latest waits", () => {
  const { routes: r, sent } = routes();
  r.open("/persona/c-1"); // the persona:// link that started the app, at the window's first paint
  r.open("/s/mac-1/s-1");
  assert.deepEqual(sent, []);
  r.listen();
  assert.deepEqual(sent, ["/s/mac-1/s-1"]);
  r.listen(); // a second subscription gets nothing again
  assert.deepEqual(sent, ["/s/mac-1/s-1"]);
});

test("once the page listens a route goes at once", () => {
  const { routes: r, sent } = routes();
  r.listen();
  assert.deepEqual(sent, []);
  r.open("/inbox/i-1");
  r.open("/todos/t-1");
  assert.deepEqual(sent, ["/inbox/i-1", "/todos/t-1"]);
});

test("a new document waits again, and a route that waits stays for it", () => {
  const { routes: r, sent } = routes();
  r.listen();
  r.started(); // the page reloads: its app does not listen yet
  r.open("/machines/m-1");
  assert.deepEqual(sent, []);
  r.started(); // another document before it listened: the route still waits
  r.listen();
  assert.deepEqual(sent, ["/machines/m-1"]);
});

test("the preload says when a document starts and when its app listens, after its listener is in place", async () => {
  const preload = await source("preload.cts");
  const started = preload.indexOf('ipcRenderer.send("desktop:page-started");');
  assert.ok(started > 0 && started < preload.indexOf("const api = Object.freeze({"), "sent when the preload loads");
  const subscribe = preload.slice(preload.indexOf("onOpenNotification("), preload.indexOf("desktopPresence("));
  const on = subscribe.indexOf('ipcRenderer.on("desktop:notification-open", listener);');
  const listening = subscribe.indexOf('ipcRenderer.send("desktop:notification-listening");');
  assert.ok(on >= 0 && listening > on, "the listener is registered before the main process is told");
});

test("only the hub page's own signals move its routes, and main.ts sends no route around them", async () => {
  const ipc = await source("ipc.ts");
  assert.match(ipc, /listen\("desktop:page-started", \(event: Electron\.IpcMainEvent\) => \{\s+if \(authorized\(event\)\) deps\.routes\.started\(\);/);
  assert.match(ipc, /listen\("desktop:notification-listening", \(event: Electron\.IpcMainEvent\) => \{\s+if \(authorized\(event\)\) deps\.routes\.listen\(\);/);
  const main = await source("main.ts");
  const sends = main.match(/webContents\.send\("desktop:notification-open"/g) ?? [];
  assert.equal(sends.length, 1, "one send, the window's PageRoutes delivery");
  assert.match(main, /new PageRoutes\(route => \{\s+if \(!current\.isDestroyed\(\)\) current\.webContents\.send\("desktop:notification-open", route\);/);
});

test("a link the app was started with (Windows, Linux) waits like one that comes before the window", async () => {
  const main = await source("main.ts");
  const first = main.indexOf("pendingDeepLink = process.argv.find(value => /^(?:persona|jarvis):\\/\\//i.test(value));");
  assert.ok(first > main.indexOf("const locked = app.requestSingleInstanceLock();"), "read by the instance that keeps the lock");
  assert.ok(first < main.indexOf('app.on("second-instance"'));
});
