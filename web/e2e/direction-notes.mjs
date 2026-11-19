// Settings › Persona › Direction notes (#10086) against the mock hub's directions (synthetic data only, never a live
// hub): each current direction lists its notes, an edit saves through PUT …/directions/{id}/notes and reads back from the
// hub; screenshots at desktop and phone width, light and dark.
//   PORT=<mock port> MOCK_LEADERS=1 python3 scripts/mock/mock_hub.py; PERSONA_HUB_URL=http://127.0.0.1:<mock port> npx vite
//   BASE_URL=http://127.0.0.1:<vite port> SHOTS_DIR=<dir> node web/e2e/direction-notes.mjs
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5173");
assert.equal(base.hostname, "127.0.0.1", "Use an isolated loopback preview");
const output = resolve(process.env.SHOTS_DIR || "direction-notes-shots");
await mkdir(output, { recursive: true });
const executablePath = process.env.PERSONA_E2E_CHROMIUM ||
  (process.platform === "darwin" ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" : "/usr/bin/google-chrome");
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("TIMEOUT direction notes"); process.exit(1); }, 180_000);
const s = JSON.parse(await readFile(new URL("../src/i18n/locales/en/settings-directionnotes.json", import.meta.url), "utf8"));
const common = JSON.parse(await readFile(new URL("../src/i18n/locales/en/common.json", import.meta.url), "utf8"));

let shots = 0;
async function run(theme, narrow, edit) {
  const context = await browser.newContext({ viewport: { width: narrow ? 390 : 1280, height: narrow ? 844 : 1000 }, colorScheme: theme,
    reducedMotion: "reduce", deviceScaleFactor: 2 });
  await context.addInitScript(({ theme }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
  }, { theme });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(new URL("/settings?tab=persona", base).href);
  const group = page.locator('[data-settings-group="direction-notes"]');
  await group.waitFor();
  const rows = group.locator("[data-direction]");
  await rows.first().waitFor();
  assert.equal(await rows.count(), 4, "every current direction is listed");
  const first = rows.nth(0).locator("textarea");
  assert.match(await first.inputValue(), /Readings that count/);
  if (edit) {
    const second = rows.nth(1);
    await second.locator("textarea").fill("Back up the database before any migration.\n- Readings: row counts before and after.");
    const save = second.getByRole("button", { name: common.save, exact: true });
    assert.equal(await save.isDisabled(), false);
    await save.click();
    await page.getByText(s.saved.replace("{name}", ""), { exact: false }).first().waitFor();
    const id = await second.getAttribute("data-direction");
    const listed = await (await fetch(new URL("/api/v1/assistant/leaders", base), { headers: { Authorization: "Bearer fixture-token" } })).json();
    assert.match(listed.leaders.find((l) => l.id === id).notes, /Back up the database/, "the hub kept the notes");
    assert.equal(await save.isDisabled(), true, "saved: nothing left to save");
  }
  // The group from its title down, in the page as it is (the top bar and the settings around it), not cut out.
  await group.evaluate((el) => {
    el.scrollIntoView({ block: "start" });
    let box = el.parentElement;
    while (box && box.scrollHeight <= box.clientHeight) box = box.parentElement;
    box?.scrollBy(0, -96);
  });
  await page.mouse.move(0, 0);
  await page.waitForTimeout(400); // the save toast fades
  await page.evaluate(() => document.fonts.ready);
  assert.ok(await group.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "group overflows");
  await page.screenshot({ path: join(output, `${narrow ? "phone" : "desktop"}-${theme}.png`) });
  shots++;
  assert.deepEqual(errors, []);
  await context.close();
}

try {
  await run("light", false, true);
  await run("dark", false, false);
  await run("light", true, false);
  await run("dark", true, false);
  console.log(`PASS direction notes: ${shots} screenshots in ${output}`);
} catch (e) {
  console.error("FAIL direction notes:", e);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
