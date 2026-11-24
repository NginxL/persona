// Persona attending a recorded meeting (#2981), with synthetic fixtures and no live hub: the Knowledge recording's role and
// attending session, "Questions in the meeting" from GET /recordings/{id}/live (polled while recording), and Settings ›
// Persona › Meetings saving the default role. Run against an already running Vite server.
// BASE_URL=http://127.0.0.1:5791 PERSONA_E2E_CHROMIUM=<chrome> SHOTS_DIR=/tmp/mm/shots node web/e2e/recording-attendance.mjs
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5791");
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback Vite server");
assert.ok(["http:", "https:"].includes(base.protocol) && !base.username && !base.password && base.pathname === "/" && !base.search && !base.hash, "Use an origin without credentials or a route");
const executablePath = process.env.PERSONA_E2E_CHROMIUM || process.env.CHROME_BIN;
assert.ok(executablePath, "Set PERSONA_E2E_CHROMIUM or CHROME_BIN");
const output = resolve(process.env.SHOTS_DIR || "/tmp/mm/shots");
await mkdir(output, { recursive: true });
const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const earlier = "cccccccc-cccc-cccc-cccc-cccccccccccc";
const agent = "bbbbbbbb-0000-4000-8000-000000000001";
const detailPath = `/kb/recordings/${id}`;
const livePath = `/recordings/${id}/live`;
const MACHINE = { id: "macbook", name: "MacBook", online: true, os: "darwin" };
const SESSION = { machine_id: "macbook", session_id: agent, title: "Export cap review", cwd: "/home/example/app", status: "idle", state: "idle" };
const detail = () => ({
  kind: "recordings", id, path: `recordings/${id}`,
  meta: {
    title: "Design review", kind: "meeting", complete: false, started_at: "2026-11-10T14:00:00+08:00", duration: 1860, device: "phone", language: "en",
    revision: 5, minutes_state: "pending", attendance: "participant", attending_session: { machine_id: "macbook", session_id: agent },
  },
  summary: "",
  segments: [
    { start: 0, end: 9, text: "Ada: Today we look at the export experience of the example project." },
    { start: 1790, end: 1806, text: "Bo: Did we decide on the cap last time, or is it still open?" },
  ],
});
const live = () => ({
  next: 57, fed: 57, attendance: "participant", attending_session: { machine_id: "macbook", session_id: agent },
  asks: [
    {
      id: "11111111-1111-4111-8111-111111111111", question: "What did we decide about the export cap last time?", question_source: "typed", at: 1834.2,
      state: "answered", answer: "At the design review on November 3 you kept the 4 MB cap for this release. Bo owns the regression test; it is still open.",
      lang: "en", speak: true, sources: [{ kind: "recordings", id: earlier, name: "Design review", when: "2026-11-03 14:00–15:10" }, { kind: "projects", id: "example-app", name: "Example app" }],
      asked_at: "2026-11-10T06:30:34Z", answered_at: "2026-11-10T06:30:40Z",
    },
    {
      id: "22222222-2222-4222-8222-222222222222", question: "Did we decide on the cap last time, or is it still open?", question_source: "heard", at: 1810,
      state: "pending", answer: "", lang: "en", speak: true, sources: [], asked_at: "2026-11-10T06:30:10Z",
    },
  ],
});
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("FAIL recording attendance timed out"); process.exit(1); }, 180_000);
let screenshots = 0, runs = 0;
const fill = (text, values) => text.replace(/\{(\w+)\}/g, (_, k) => String(values[k]));

async function context(theme, lang, width) {
  const ctx = await browser.newContext({ viewport: { width, height: 1400 }, colorScheme: theme, timezoneId: "Asia/Shanghai", deviceScaleFactor: 1, reducedMotion: "reduce" });
  await ctx.addInitScript(({ theme, lang }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", lang);
    localStorage.setItem("persona.theme", theme);
  }, { theme, lang });
  const state = { reads: 0, writes: [], role: "listener" };
  await ctx.route("**/*", (route) => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
  await ctx.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, "");
    if (path === detailPath) return route.fulfill({ json: { detail: detail() } });
    if (path === livePath) {
      assert.equal(request.method(), "GET");
      assert.equal(request.headers()["x-persona-manager"], "assistant");
      state.reads++;
      return route.fulfill({ json: live() });
    }
    const assistant = () => ({ manager: "assistant", machine_id: "server", main: "main-1", conversations: [], meeting_attendance_default: state.role, notes_effective: "", main_tasks_quiet: true });
    if (path === "/assistant" && request.method() === "PUT") {
      const body = request.postDataJSON(); state.writes.push(body);
      assert.deepEqual(body, { meeting_attendance_default: "participant" });
      state.role = body.meeting_attendance_default;
      return route.fulfill({ json: assistant() });
    }
    assert.ok(["GET", "HEAD"].includes(request.method()), `Unexpected write ${request.method()} ${path}`);
    const data = {
      "/kb/recordings": { items: [{ kind: "recordings", id, name: "Design review", relevance: 0 }], tags: [] },
      "/sessions": { sessions: [SESSION], total: 1, has_more: false, revision: "fixture" },
      "/machines": { machines: [MACHINE] }, "/interactions": { interactions: [] },
      "/assistant": assistant(), "/assistant/tasks": { tasks: [] }, "/assistant/memory": { files: [] }, "/todos": { todos: [] },
      "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
      "/agents/sessions": { sessions: [] }, "/agents/prefs": {}, "/skills": { skills: [] },
      "/managers": { managers: [{ id: "assistant", name: "Persona", builtin: true, online: true }], default: "assistant" },
      "/personas": { personas: [] }, "/assistant/personas": { personas: [] }, "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
    };
    return route.fulfill({ json: data[path] ?? {} });
  });
  await ctx.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: ["managers", "meeting_attendance"], hub_name: "Example hub" }));
    ws.send(JSON.stringify({ type: "machines", machines: [MACHINE] }));
    ws.onMessage((message) => { const event = JSON.parse(String(message)); if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id })); });
  });
  return { ctx, state };
}

async function settle(page) {
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(350);
  assert.equal(await page.locator("vite-error-overlay").count(), 0);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), "No page overflow");
}

async function recording(theme, lang = "en", width = 1440) {
  // the knowledge page's strings and the recording module's (src/modules/recording, #13407)
  const s = Object.assign(...await Promise.all(["knowledge-kbdetail", "recording"].map(async (ns) => JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/${ns}.json`, import.meta.url), "utf8")))));
  const { ctx, state } = await context(theme, lang, width);
  const errors = [];
  const page = await ctx.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(new URL(detailPath, base).href);
  const asks = page.getByRole("region", { name: s.asks_title, exact: true });
  await asks.waitFor();
  await asks.getByText(live().asks[0].answer, { exact: true }).waitFor();
  await settle(page);
  assert.equal(await asks.locator("li[data-ask]").count(), 2);
  assert.equal(await asks.getByRole("status").innerText(), fill(s.live_following_other, { count: 57 }));
  assert.ok(await asks.getByText(s.ask_pending, { exact: true }).isVisible());
  assert.equal(await asks.getByRole("link", { name: "Design review" }).getAttribute("href"), `/kb/recordings/${earlier}`);
  assert.ok(await asks.evaluate((section) => section.scrollWidth <= section.clientWidth + 1), "No asks overflow");
  const attending = page.getByRole("link", { name: SESSION.title, exact: true });
  assert.equal(await attending.getAttribute("href"), `/s/macbook/${agent}`);
  assert.ok(await page.getByText(s.recording_participant, { exact: true }).isVisible());
  const name = width === 1440 ? `attendance-${theme}.png` : `attendance-${lang}-${width}.png`;
  await page.screenshot({ path: join(output, name), fullPage: true });
  screenshots++;
  if (runs === 0) {
    // Still recording: it reads the live state again (every 5 s while the page is shown).
    const started = Date.now();
    while (state.reads < 2 && Date.now() - started < 9000) await page.waitForTimeout(250);
    assert.ok(state.reads >= 2, "The live state is read again while recording");
  }
  assert.deepEqual(errors, []);
  await ctx.close();
  runs++;
}

async function settings(theme, lang = "en", width = 1440) {
  const s = JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/settings-assistantsettings.json`, import.meta.url), "utf8"));
  const { ctx, state } = await context(theme, lang, width);
  const errors = [];
  const page = await ctx.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(new URL("/settings?tab=persona", base).href);
  const group = page.locator('section[data-settings-group="meetings"]');
  const select = group.getByRole("combobox", { name: s.meeting_attendance, exact: true });
  await select.waitFor();
  await group.evaluate((el) => el.scrollIntoView({ block: "center" }));
  await settle(page);
  assert.equal(await select.inputValue(), "listener");
  assert.ok(await group.getByText(s.meeting_attendance_footer, { exact: true }).isVisible());
  assert.ok(await group.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "No group overflow");
  // The group from its title to its footer, with the group above it for context.
  const box = await group.boundingBox();
  assert.ok(box, "The Meetings group is laid out");
  const top = Math.max(0, box.y - 160);
  const clip = { x: 0, y: top, width, height: Math.min(box.y + box.height + 40, 1400) - top };
  await page.screenshot({ path: join(output, width === 1440 ? `settings-meetings-${theme}.png` : `settings-meetings-${lang}-${width}.png`), clip });
  screenshots++;
  await select.selectOption("participant");
  const started = Date.now();
  while (!state.writes.length && Date.now() - started < 5000) await page.waitForTimeout(100);
  assert.deepEqual(state.writes, [{ meeting_attendance_default: "participant" }]);
  await page.waitForTimeout(200);
  assert.equal(await select.inputValue(), "participant");
  assert.deepEqual(errors, []);
  await ctx.close();
  runs++;
}

try {
  for (const theme of ["light", "dark"]) await recording(theme);
  for (const lang of ["en", "zh-Hans", "ja"]) await recording("dark", lang, 390);
  for (const theme of ["light", "dark"]) await settings(theme);
  for (const lang of ["en", "zh-Hans", "ja"]) await settings("light", lang, 390);
  console.log(`PASS recording attendance: ${runs} browser runs, ${screenshots} screenshots; role, attending link, asks, live polling, overflow and the default role's save checked`);
} catch (err) {
  console.error(`FAIL recording attendance: ${err?.stack || err}`);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
