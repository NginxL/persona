// Synthetic retention settings and trimmed minutes, never a live hub.
// BASE_URL=http://127.0.0.1:18780 PERSONA_E2E_CHROMIUM=<chrome> SHOTS_DIR=/tmp/retention-shots node web/e2e/recording-retention.mjs
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:18780");
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback server");
assert.ok(["http:", "https:"].includes(base.protocol) && !base.username && !base.password && base.pathname === "/" && !base.search && !base.hash);
const executablePath = process.env.PERSONA_E2E_CHROMIUM || process.env.CHROME_BIN;
assert.ok(executablePath, "Set PERSONA_E2E_CHROMIUM or CHROME_BIN");
const output = resolve(process.env.SHOTS_DIR || "/tmp/retention-shots");
await mkdir(output, { recursive: true });
const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const detailPath = `/kb/recordings/${id}`;
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("FAIL recording retention timed out"); process.exit(1); }, 120_000);
let screenshots = 0;

async function run(theme, lang, large = false) {
  const strings = async (name) => JSON.parse(await readFile(new URL(`../src/i18n/locales/${lang}/${name}.json`, import.meta.url), "utf8"));
  const s = await strings("settings-assistantsettings"), k = await strings("recording");
  const context = await browser.newContext({ viewport: { width: 1000, height: 1500 }, colorScheme: theme, reducedMotion: "reduce" });
  await context.addInitScript(({ theme, lang, large }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", lang);
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.text-size", large ? "large" : "standard");
  }, { theme, lang, large });
  let saved = { recording_days: 14, minutes_days: 0 };
  const writes = [], errors = [];
  await context.route("**/*", (route) => new URL(route.request().url()).origin === base.origin ? route.continue() : route.abort());
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace(/^\/api\/v1/, "");
    if (path === "/assistant") {
      if (request.method() === "PUT") {
        assert.equal(request.headers()["x-persona-manager"], "assistant");
        const body = request.postDataJSON(); writes.push(body); saved = { ...saved, ...body };
      }
      return route.fulfill({ json: { manager: "assistant", notes: "", conversations: [], ...saved,
        minutes_days: saved.minutes_days === 0 ? 0 : Math.max(saved.minutes_days, saved.recording_days) } });
    }
    assert.ok(["GET", "HEAD"].includes(request.method()), `Unexpected write ${path}`);
    const data = {
      [detailPath]: { detail: { kind: "recordings", id, path: `recordings/${id}`,
        meta: { title: "Design review", kind: "meeting", complete: true, started_at: "2026-11-10T14:00:00Z", duration: 1800, revision: 1, minutes_state: "ready", trimmed_at: "2026-11-25T14:00:00Z" },
        minutes: { version: 1, revision: 1, title: "Design review", when: "2026-11-10 14:00–14:30", decisions: [{ text: "Keep the export cap.", at: 12 }], markdown: "# Design review\n\nKeep the export cap." } } },
      "/kb/recordings": { items: [{ kind: "recordings", id, name: "Design review", relevance: 0 }], tags: [] },
      "/sessions": { sessions: [], total: 0, has_more: false, revision: "fixture" },
      "/machines": { machines: [] }, "/interactions": { interactions: [] }, "/todos": { todos: [] },
      "/tasks": { tasks: [] }, "/memory": { files: [] },
      "/mutes": { spaces: [], sessions: [], allowed: [] }, "/pins": { spaces: [], sessions: [], sections: [] },
      "/agents/sessions": { sessions: [] }, "/agents/prefs": {},
      "/managers": { managers: [{ id: "assistant", name: "Persona", builtin: true, online: true }], default: "assistant" },
      "/personas": { personas: [] }, "/assistant/personas": { personas: [] }, "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
    };
    return route.fulfill({ json: data[path] ?? {} });
  });
  await context.routeWebSocket(/\/api\/v1\/ws(?:\?|$)/, (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: ["managers"], hub_name: "Example hub" }));
    ws.send(JSON.stringify({ type: "machines", machines: [] }));
    ws.onMessage((message) => { const event = JSON.parse(String(message)); if (event.type === "app_ping") ws.send(JSON.stringify({ type: "pong", id: event.id })); });
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(new URL("/settings?tab=persona", base).href);
  const mode = page.getByRole("combobox", { name: s.minutes_days, exact: true });
  await mode.waitFor();
  assert.equal(await mode.inputValue(), "forever");
  const shot = async (name, element) => {
    await element.scrollIntoViewIfNeeded();
    await page.evaluate(() => document.fonts.ready);
    assert.ok(await element.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), `${name} overflow`);
    await element.screenshot({ path: join(output, `${name}-${lang}-${theme}${large ? "-large" : ""}.png`) });
    screenshots++;
  };
  const group = page.locator('[data-settings-group="recordings"]');
  await shot("retention-forever", group);
  await mode.selectOption("days");
  const days = page.getByRole("spinbutton", { name: s.minutes_days, exact: true });
  await days.waitFor();
  assert.equal(await days.inputValue(), "365");
  assert.deepEqual(writes, []);
  assert.equal(saved.minutes_days, 0);
  await shot("retention-draft", group);
  await days.fill("1000"); await days.press("Enter");
  await page.waitForFunction(() => document.querySelector('input[type="number"][max="3650"]')?.value === "1000" && !document.querySelector('input[type="number"][max="3650"]')?.disabled);
  assert.equal(saved.minutes_days, 1000);
  await shot("retention-finite", group);
  await mode.selectOption("forever");
  await days.waitFor({ state: "detached" });
  assert.equal(saved.minutes_days, 0);
  assert.deepEqual(writes, [{ minutes_days: 1000 }, { minutes_days: 0 }]);

  await page.goto(new URL(detailPath, base).href);
  const minutes = page.getByRole("region", { name: k.minutes, exact: true });
  await minutes.getByText(k.minutes_kept_forever, { exact: true }).waitFor();
  assert.equal(await minutes.getByRole("button", { name: k.minutes_regenerate, exact: true }).count(), 0);
  await shot("trimmed-forever", minutes);
  saved.minutes_days = 365;
  await page.reload();
  await page.waitForFunction((forever) => {
    const note = document.querySelector("[data-minutes-kept]");
    return note && !note.textContent.includes(forever) && note.textContent.includes("2027");
  }, k.minutes_kept_forever);
  await shot("trimmed-finite", minutes);
  assert.deepEqual(errors, []);
  await context.close();
}
try {
  for (const theme of ["light", "dark"]) {
    await run(theme, "en");
    await run(theme, "en", true);
  }
  for (const lang of ["zh-Hans", "ja"]) await run("light", lang);
  console.log(`PASS recording retention: ${screenshots} screenshots; forever/finite save and trimmed notes in all three languages`);
} catch (error) {
  console.error(`FAIL recording retention: ${error.message.split("\n")[0]}`);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
