// Optimized Web with synthetic data only; no real hub, account, topic or external request.
// node web/e2e/parked-wait-sources.mjs <loopback-web-url> --shots <directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, ""), shots = flag("--shots");
assert(base && shots, "provide a loopback Web URL and --shots directory");
const origin = new URL(base);
assert(origin.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname));
assert(!origin.username && !origin.password && !origin.search && !origin.hash);
mkdirSync(shots, { recursive: true });
const timeout = setTimeout(() => { console.error("TIMEOUT parked source browser checks (180s)"); process.exit(124); }, 180_000);
const now = Date.now(), iso = (hours) => new Date(now - hours * 3600_000).toISOString();
const prURL = "https://github.com/example/demo/pull/73";
const issueURL = "https://github.com/example/demo/issues/912";
const commentURL = `${issueURL}#issuecomment-42`;
const topicURL = "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_example_group&open_thread_id=omt_example_topic";
const pr = (number) => ({ url: `https://github.com/example/demo/pull/${number}`, state: "open", draft: false,
  review: "required", checks: "success", conflicts: false, checked_at: iso(0) });
const tasks = [
  ["bound", "Review the new change", prURL, "Review team", 5],
  ["issue", "Wait for the issue response", issueURL, "Support team", 4],
  ["comment", "Follow the review comment", commentURL, "Review team", 3],
  ["topic", "Confirm the release date", topicURL, "Schedule owner", 2],
  ["missing", "Wait for the opening date", undefined, "Opening date", 1],
].map(([id, title, url, target, hours]) => {
  const evidence = { kind: "external_event", target, wake: "A response arrives", ...(url ? { url } : {}) };
  return { id: `source-${id}`, title, origin: "demo-main", created_at: iso(48), updated_at: iso(0), agents: [],
    ...(id === "bound" ? { pr_url: pr(7).url, pr_status: pr(7) } : {}),
    ...(id === "missing" ? { pr_url: pr(8).url, pr_status: pr(8) } : {}),
    ...(id === "topic" ? { thread: { channel: "feishu", chat_id: "oc_example_group", thread_id: "omt_example_topic", title: "Release questions" } } : {}),
    supervision: { supervisor: "demo-main", state: "parked",
      parked: { reason: "Waiting on an external change", until: iso(-24), waiting_on: "external_event", count: 1, evidence },
      reviews: [{ at: iso(hours), verdict: "parked", parks: 1, evidence }] } };
});
tasks.push({ id: "source-active", title: "Draft rollout notes", origin: "demo-main", created_at: iso(48),
  updated_at: iso(0), agents: [], supervision: { supervisor: "demo-main", state: "pending_review" } });
const session = { machine_id: "demo", session_id: "demo-main", title: "Example conversation",
  assistant: true, state: "idle", status: "idle", updated_at: iso(0) };
const reads = [], writes = [], cases = [];
const api = async (route) => {
  const req = route.request(), path = new URL(req.url()).pathname.replace(/^\/api\/v1/, "");
  let body = {};
  if (req.method() !== "GET") {
    writes.push({ method: req.method(), path });
    assert(/\/(?:read|seen)$/.test(path) || path === "/assistant/conversations/demo-main/today", `unexpected write: ${req.method()} ${path}`);
    body = { ok: true };
  } else {
    reads.push(path);
    if (path === "/machines") body = { machines: [{ id: "demo", name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") body = { sessions: [session] };
    else if (path === "/interactions") body = { interactions: [] };
    else if (path === "/assistant") body = { machine_id: "demo", main: "demo-main", conversations: [session], tasks: [], memory: [] };
    else if (path === "/todos") body = { todos: tasks };
    else if (path === "/assistant/repositories") body = { catalog_revision: 0, repositories: [] };
    else if (/^\/todos\/[^/]+\/repositories$/.test(path)) body = { schema_version: 1, revision: 0, repositories: [] };
    else if (path === "/sessions/demo/demo-main") body = { session };
    else if (path.endsWith("/feed")) body = { items: [] };
    else if (path.endsWith("/items")) body = { items: [], has_more: false };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
    else if (path === "/managers") body = { managers: [] };
  }
  await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
};
let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") || "/usr/bin/chromium", headless: true });
  const modes = ["light", "dark"].flatMap(theme => ["standard", "large"].flatMap(size => ["desktop", "phone"].map(view => ({ theme, size, view, lang: "zh-Hans" }))));
  modes.push({ theme: "light", size: "standard", view: "phone", lang: "en" }, { theme: "dark", size: "large", view: "phone", lang: "ja" });
  for (const mode of modes) {
    const context = await browser.newContext({ viewport: mode.view === "desktop" ? { width: 1280, height: 1000 } : { width: 390, height: 844 },
      isMobile: mode.view === "phone", hasTouch: mode.view === "phone", reducedMotion: "reduce", serviceWorkers: "block" });
    const errors = [];
    try {
      await context.addInitScript(({ theme, lang, size }) => {
        localStorage.setItem("persona.token", "synthetic-demo-token");
        localStorage.setItem("persona.language", lang);
        localStorage.setItem("persona.theme", theme);
        localStorage.setItem("persona.text-size", size);
      }, mode);
      await context.route("**/*", async route => {
        const url = new URL(route.request().url());
        if (url.origin !== origin.origin) { await route.abort(); return; }
        if (url.pathname.startsWith("/api/v1/")) { await api(route); return; }
        await route.continue();
      });
      await context.routeWebSocket("**/api/v1/ws**", ws => {
        ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
        ws.onMessage(raw => { if (JSON.parse(String(raw)).type === "app_ping") ws.send(JSON.stringify({ type: "pong" })); });
      });
      const page = await context.newPage();
      page.on("pageerror", e => errors.push(String(e)));
      page.setDefaultTimeout(10_000);
      const transcript = page.waitForResponse(response => new URL(response.url()).pathname === "/api/v1/sessions/demo/demo-main/items");
      await page.goto(`${base}/`);
      // Finish the independent startup load before measuring this local-only interaction.
      await transcript;
      await page.evaluate(() => document.fonts.ready);
      const tray = page.locator('[data-tray-entry="parked"]');
      await tray.waitFor();
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      const before = reads.length;
      const openedAt = performance.now();
      await tray.click();
      const rows = page.locator("[data-parked-task-row]");
      await rows.first().waitFor();
      const feedbackMs = performance.now() - openedAt;
      assert.equal(await rows.count(), 5);
      const source = (id) => page.locator(`[data-task-wait-info="source-${id}"] [data-wait-source]`);
      assert.equal(await source("bound").innerText(), "PR #73");
      assert.equal(await source("bound").getAttribute("href"), prURL);
      assert.equal(await source("issue").innerText(), "issue #912");
      const comment = { en: "Comment on #912", "zh-Hans": "#912 \u4e0b\u7684\u7559\u8a00", ja: "#912 \u306e\u30b3\u30e1\u30f3\u30c8" }[mode.lang];
      assert.equal(await source("comment").innerText(), comment);
      assert.equal(await source("comment").getAttribute("href"), commentURL);
      assert.equal(await source("topic").innerText(), "Release questions");
      assert.equal(await source("topic").getAttribute("href"), topicURL);
      assert.equal(await source("missing").count(), 0);
      assert.equal(await rows.locator('a[href="https://github.com/example/demo/pull/7"], a[href="https://github.com/example/demo/pull/8"]').count(), 0);
      assert.equal(await source("bound").getAttribute("title"), prURL);
      // The chat's independent transcript load can overlap opening the local tray.
      const backgroundReads = reads.slice(before).filter(path => path === "/sessions/demo/demo-main/items");
      const metadataReads = reads.slice(before).filter(path => path !== "/sessions/demo/demo-main/items");
      assert.deepEqual(metadataReads, [], `opening source names adds no HTTP metadata lookups: ${JSON.stringify(metadataReads)}`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false);
      const stem = `wait-sources-${mode.view}-${mode.theme}-${mode.size}-${mode.lang}`;
      await rows.first().scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(shots, `${stem}-top.png`) });
      await rows.last().scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(shots, `${stem}-bottom.png`) });
      assert.deepEqual(errors, []);
      cases.push({ ...mode, rows: 5, namedSources: 4, missingSources: 0, extraMetadataReads: metadataReads.length, backgroundReads, feedbackMs, screenshots: 2 });
    } finally { await context.close(); }
  }
  writeFileSync(join(shots, "readings.json"), JSON.stringify({ cases, writes, taskWrites: 0, realHubRequests: 0 }, null, 2) + "\n");
  console.log(`PASS parked source browser: ${cases.length} cases, ${cases.length * 2} screenshots; extra source reads=0; task writes=0`);
} catch (error) {
  console.error("FAIL parked source browser", error); process.exitCode = 1;
} finally { clearTimeout(timeout); await browser?.close(); }
