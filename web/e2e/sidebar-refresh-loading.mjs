// Synthetic real-browser regression for #12231: a warm profile must paint kept sidebar rows during full reloads.
// node web/e2e/sidebar-refresh-loading.mjs <loopback URL> <artifact directory> [before] [large]
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, artifacts, mode = "default", textSize = "standard"] = process.argv.slice(2);
assert.match(base ?? "", /^http:\/\/127\.0\.0\.1:\d+$/);
assert(artifacts, "Provide an artifact directory");
assert(["default", "before"].includes(mode), "Mode must be default or before");
assert(["standard", "large"].includes(textSize), "Text size must be standard or large");
const before = mode === "before";
mkdirSync(artifacts, { recursive: true });

const watchdog = setTimeout(() => {
  console.error("RESULT: sidebar-refresh-loading timed out (180s)");
  process.exit(124);
}, 180_000);

const DELAYS = { websocketHello: 100, assistant: 1500, todos: 300, pins: 300, summary: 200, complete: 300, completeCursor: 1500 };
const AT = "2026-11-22T12:00:00.000Z";
const machine = { id: "demo", name: "Synthetic computer", online: true, os: "linux" };
const main = {
  machine_id: "demo", session_id: "demo-main", title: "Persona", cwd: "/tmp/synthetic", assistant: true,
  started_by_persona: true, state: "idle", status: "idle", updated_at: AT, capabilities: { send: true, read: true },
};
const weekend = { ...main, session_id: "demo-weekend", title: "Weekend plans", updated_at: "2026-11-22T11:55:00.000Z" };
const agentOne = {
  machine_id: "demo", session_id: "agent-one", title: "Agent one", cwd: "/tmp/synthetic/one", state: "working",
  status: "working", updated_at: "2026-11-22T11:50:00.000Z", capabilities: { send: true, read: true },
};
const agentTwo = { ...agentOne, session_id: "agent-two", title: "Agent two", cwd: "/tmp/synthetic/two", updated_at: "2026-11-22T11:45:00.000Z" };
const sessions = [main, weekend, agentOne, agentTwo];
const features = ["sessions_list", "sessions_lookup", "sessions_agents_only", "sessions_todo"];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const pageBody = (rows, more = false, cursor) => ({
  sessions: rows, remove: [], added: [], has_more: more, ...(more ? { next_cursor: cursor } : {}), revision: "fixture-r1", total: 2,
});
const counts = { total: 2, machine: { demo: 2 }, project: {}, group: {}, state: { working: 2 }, todo: { "": 2 }, revision: "fixture-r1" };

/** Page-clock first paint and response-end marks. A full reload gives this a fresh performance time origin. */
const WATCH = () => {
  const seen = {
    weekend: null, agentOne: null, agentTwo: null,
    assistantAnswer: null, agentFirstAnswer: null, agentCursorAnswer: null,
  };
  window.__sidebarSeen = seen;
  const rows = () => [...document.querySelectorAll('[data-sidebar-column] a[title]')];
  const visible = (title) => rows().some((row) => {
    if (row.title.split("\n")[0] !== title || row.closest("[inert]") || !row.getClientRects().length) return false;
    const rect = row.getBoundingClientRect();
    const style = getComputedStyle(row);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  });
  const responseEnd = (pathname, predicate = () => true) => {
    let end = null;
    for (const entry of performance.getEntriesByType("resource")) {
      const url = new URL(entry.name, location.href);
      if (url.pathname === pathname && predicate(url.searchParams) && entry.responseEnd > 0) {
        end = end === null ? entry.responseEnd : Math.min(end, entry.responseEnd);
      }
    }
    return end;
  };
  const tick = () => {
    if (seen.weekend === null && visible("Weekend plans")) seen.weekend = performance.now();
    if (seen.agentOne === null && visible("Agent one")) seen.agentOne = performance.now();
    if (seen.agentTwo === null && visible("Agent two")) seen.agentTwo = performance.now();
    if (seen.assistantAnswer === null) seen.assistantAnswer = responseEnd("/api/v1/assistant", (q) => !q.has("view"));
    if (seen.agentFirstAnswer === null) seen.agentFirstAnswer = responseEnd("/api/v1/sessions", (q) =>
      q.get("summary") === "0" && !q.has("cursor") && !q.has("since") && !q.has("counts") && !q.has("session_id"));
    if (seen.agentCursorAnswer === null) seen.agentCursorAnswer = responseEnd("/api/v1/sessions", (q) =>
      q.get("summary") === "0" && q.get("cursor") === "complete-next");
    requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
};

const timings = {};
const checks = [];
const pageErrors = [];
let activeLoad = "setup";
let thrown = null;
let browser;
const check = (name, ok, detail) => {
  checks.push({ name, ok: Boolean(ok), ...(detail === undefined ? {} : { detail }) });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};

try {
  browser = await chromium.launch({ channel: "chrome" });
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 960 }, colorScheme: theme, locale: "en-US", reducedMotion: "reduce", serviceWorkers: "block",
    });
    await context.addInitScript(({ scheme, size }) => {
      localStorage.setItem("persona.token", "synthetic-sidebar-refresh-token");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", scheme);
      localStorage.setItem("persona.text-size", size);
    }, { scheme: theme, size: textSize });
    await context.addInitScript(WATCH);

    const requests = [];
    let failComplete = false;
    await context.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== new URL(base).origin) return route.abort();
      if (!url.pathname.startsWith("/api/v1/")) return route.continue();
      const path = url.pathname.replace(/^\/api\/v1/, "");
      requests.push({ load: activeLoad, method: request.method(), path, query: url.search });
      let body = {};
      let delay = 0;
      let status = 200;
      if (path === "/assistant") {
        delay = DELAYS.assistant;
        body = { manager: "assistant", machine_id: "demo", main: main.session_id, conversations: [main, weekend], tasks: [], memory: [] };
      } else if (path === "/todos") {
        delay = DELAYS.todos;
        body = { todos: [] };
      } else if (path === "/pins") {
        delay = DELAYS.pins;
        body = { sessions: [], spaces: ["demo/Synthetic pinned space"] };
      } else if (path === "/sessions") {
        const q = url.searchParams;
        if (failComplete && q.get("summary") === "0" && !q.has("cursor")) {
          delay = DELAYS.complete;
          status = 503;
          body = { error: "Synthetic complete-list failure", code: "internal" };
        } else if (q.has("counts")) {
          delay = DELAYS.summary;
          body = counts;
        } else if (q.has("session_id")) {
          delay = DELAYS.summary;
          const row = sessions.find((s) => s.machine_id === q.get("machine_id") && s.session_id === q.get("session_id"));
          body = { sessions: row ? [row] : [] };
        } else if (q.has("since")) {
          delay = DELAYS.summary;
          body = pageBody([]);
        } else if (q.get("summary") === "0" && q.has("cursor")) {
          delay = DELAYS.completeCursor;
          body = pageBody([agentTwo]);
        } else if (q.get("summary") === "0") {
          delay = DELAYS.complete;
          body = pageBody([agentOne], true, "complete-next");
        } else if (q.get("summary") === "1" && q.has("cursor")) {
          delay = DELAYS.completeCursor;
          body = pageBody([agentTwo]);
        } else if (q.get("summary") === "1") {
          delay = DELAYS.summary;
          body = pageBody([agentOne], true, "summary-next");
        } else {
          delay = DELAYS.summary;
          body = { sessions: [agentOne, agentTwo] };
        }
      } else if (path === "/machines") body = { machines: [machine] };
      else if (path === "/managers") body = { default: "assistant", managers: [{ id: "assistant", name: "Synthetic assistant", online: true, default: true, builtin: true }] };
      else if (path === "/interactions") body = { interactions: [] };
      else if (path === "/mutes") body = { sessions: [], spaces: [], allowed: [] };
      else if (path.endsWith("/feed")) body = { items: [], has_more: false };
      else {
        const detail = /^\/sessions\/([^/]+)\/([^/]+)$/.exec(path);
        if (detail) {
          const row = sessions.find((s) => s.machine_id === decodeURIComponent(detail[1]) && s.session_id === decodeURIComponent(detail[2]));
          body = { session: row ?? main, items: [], has_more: false };
        } else if (path === "/extensions/ui") body = { slots: [] };
        else if (path === "/extensions") body = { extensions: [] };
      }
      if (delay) await sleep(delay);
      return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    });
    await context.routeWebSocket("**/api/v1/ws**", (ws) => {
      const hello = setTimeout(() => ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Synthetic hub", features })), DELAYS.websocketHello);
      ws.onMessage((raw) => {
        try {
          if (JSON.parse(String(raw)).type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
        } catch { /* A non-JSON synthetic client message needs no answer. */ }
      });
      ws.onClose(() => clearTimeout(hello));
    });

    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    page.on("pageerror", (error) => pageErrors.push({ theme, load: activeLoad, error: String(error) }));
    timings[theme] = {};

    const load = async (name, reload) => {
      activeLoad = `${theme}-${name}`;
      requests.length = 0;
      if (reload) await page.reload({ waitUntil: "commit" });
      else await page.goto(base, { waitUntil: "commit" });
      let loadingAt = null;
      if (reload) {
        if (before) await page.waitForFunction(() => performance.now() >= 600);
        else await page.waitForFunction(() => {
          const seen = window.__sidebarSeen;
          return seen && ((seen.weekend !== null && seen.agentOne !== null) || seen.agentTwo !== null);
        });
        loadingAt = await page.evaluate(() => performance.now());
        await page.screenshot({ path: join(artifacts, `${mode}-${theme}-${name}-loading.png`) });
      }
      await page.waitForFunction(() => {
        const seen = window.__sidebarSeen;
        return seen && ["weekend", "agentOne", "agentTwo", "assistantAnswer", "agentFirstAnswer", "agentCursorAnswer"].every((key) => seen[key] !== null);
      });
      await page.waitForTimeout(100);
      const seen = await page.evaluate(() => ({ ...window.__sidebarSeen }));
      const settledAt = await page.evaluate(() => performance.now());
      const rowCounts = await page.locator('[data-sidebar-column] a[title]').evaluateAll((anchors) => {
        const result = { "Weekend plans": 0, "Agent one": 0, "Agent two": 0 };
        for (const anchor of anchors) {
          const title = anchor.title.split("\n")[0];
          if (title in result && !anchor.closest("[inert]") && anchor.getClientRects().length) result[title]++;
        }
        return result;
      });
      const requested = (summary, cursor) => requests.some((r) => {
        if (r.path !== "/sessions") return false;
        const q = new URLSearchParams(r.query);
        return q.get("summary") === summary && (cursor ? q.get("cursor") === "complete-next" : !q.has("cursor") && !q.has("since") && !q.has("counts") && !q.has("session_id"));
      });
      const assistantResources = await page.evaluate(() => performance.getEntriesByType("resource").filter((entry) => new URL(entry.name).pathname === "/api/v1/assistant").map((entry) => ({ name: entry.name, startTime: entry.startTime, responseEnd: entry.responseEnd, duration: entry.duration })));
      const timing = { ...seen, assistantResources, loadingAt, settledAt, rowCounts, completeFirstRequested: requested("0", false), completeCursorRequested: requested("0", true) };
      timings[theme][name] = timing;
      console.log(`${theme} ${name} ${JSON.stringify(timing)}`);
      check(`${theme} ${name}: complete fallback read both pages`, timing.completeFirstRequested && timing.completeCursorRequested, timing);
      for (const [title, count] of Object.entries(rowCounts)) check(`${theme} ${name}: ${title} settled exactly once`, count === 1, rowCounts);
      if (!before && reload) {
        check(`${theme} ${name}: Weekend plans paints before the full assistant answer`, seen.weekend < seen.assistantAnswer, { weekend: seen.weekend, assistantAnswer: seen.assistantAnswer });
        check(`${theme} ${name}: Agent one paints before Agent two`, seen.agentOne < seen.agentTwo, { agentOne: seen.agentOne, agentTwo: seen.agentTwo });
      }
      if (reload) await page.screenshot({ path: join(artifacts, `${mode}-${theme}-${name}-settled.png`) });
    };

    await load("warm", false);
    await load("reload-1", true);
    await load("reload-2", true);

    // The complete list's existing failure and Retry path, photographed before the retry succeeds.
    activeLoad = `${theme}-error`;
    requests.length = 0;
    failComplete = true;
    await page.reload({ waitUntil: "commit" });
    const agents = page.locator("[data-sidebar-agents]");
    const alert = agents.getByRole("alert");
    await alert.waitFor();
    const retry = agents.getByRole("button", { name: "Retry", exact: true });
    await retry.waitFor();
    const failedFirstReads = requests.filter((r) => {
      if (r.path !== "/sessions") return false;
      const q = new URLSearchParams(r.query);
      return q.get("summary") === "0" && !q.has("cursor") && !q.has("since") && !q.has("counts") && !q.has("session_id");
    }).length;
    check(`${theme}: failed complete read shows its error and Retry`, failedFirstReads === 1 && await alert.isVisible() && await retry.isVisible(), { failedFirstReads, alert: await alert.textContent() });
    await page.screenshot({ path: join(artifacts, `${mode}-${theme}-error.png`) });

    failComplete = false;
    await retry.click();
    await page.waitForFunction(() => window.__sidebarSeen?.agentTwo !== null);
    const retriedFirstReads = requests.filter((r) => {
      if (r.path !== "/sessions") return false;
      const q = new URLSearchParams(r.query);
      return q.get("summary") === "0" && !q.has("cursor") && !q.has("since") && !q.has("counts") && !q.has("session_id");
    }).length;
    const errorRowCounts = await page.locator('[data-sidebar-column] a[title]').evaluateAll((anchors) => {
      const result = { "Agent one": 0, "Agent two": 0 };
      for (const anchor of anchors) {
        const title = anchor.title.split("\n")[0];
        if (title in result && !anchor.closest("[inert]") && anchor.getClientRects().length) result[title]++;
      }
      return result;
    });
    timings[theme].error = { failedFirstReads, retriedFirstReads, rowCounts: errorRowCounts };
    check(`${theme}: Retry rereads the complete list and settles every row once`, retriedFirstReads === 2 && errorRowCounts["Agent one"] === 1 && errorRowCounts["Agent two"] === 1, timings[theme].error);
    check(`${theme}: no page errors`, !pageErrors.some((entry) => entry.theme === theme), pageErrors.filter((entry) => entry.theme === theme));
    await context.close();
  }
} catch (error) {
  thrown = String(error?.stack ?? error);
  console.error(error);
} finally {
  await browser?.close();
  clearTimeout(watchdog);
}

writeFileSync(join(artifacts, "results.json"), JSON.stringify({ issue: 12231, mode, textSize, base, viewport: { width: 1440, height: 960 }, delays: DELAYS, timings, checks, pageErrors, error: thrown }, null, 2) + "\n");
const bad = thrown !== null || checks.some((entry) => !entry.ok) || pageErrors.length > 0;
console.log(`RESULT: sidebar-refresh-loading ${bad ? "failed" : "passed"} (${checks.filter((entry) => entry.ok).length}/${checks.length} checks)`);
process.exit(bad ? 1 : 0);
