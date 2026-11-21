// A project's lessons from finished tasks (#12295) against the mock hub's synthetic lessons (never a live hub): the
// section under the project's timeline with its kinds and counts, an edit that the hub keeps, a delete that asks first,
// and the switch in Settings › Persona, which the section's footer follows; screenshots at desktop and phone width,
// light and dark.
//   PORT=<mock port> python3 scripts/mock/mock_hub.py; PERSONA_HUB_URL=http://127.0.0.1:<mock port> npx vite
//   BASE_URL=http://127.0.0.1:<vite port> MOCK_URL=http://127.0.0.1:<mock port> SHOTS_DIR=<dir> node web/e2e/project-lessons.mjs
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5173");
const mock = new URL(process.env.MOCK_URL || base);
assert.equal(base.hostname, "127.0.0.1", "Use an isolated loopback preview");
assert.equal(mock.hostname, "127.0.0.1", "Use an isolated loopback mock hub");
const output = resolve(process.env.SHOTS_DIR || "project-lessons-shots");
await mkdir(output, { recursive: true });
const executablePath = process.env.PERSONA_E2E_CHROMIUM ||
  (process.platform === "darwin" ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" : "/usr/bin/google-chrome");
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("TIMEOUT project lessons"); process.exit(1); }, 240_000);
const l = JSON.parse(await readFile(new URL("../src/i18n/locales/en/knowledge-projectlessons.json", import.meta.url), "utf8"));
const settings = JSON.parse(await readFile(new URL("../src/i18n/locales/en/settings-assistantsettings.json", import.meta.url), "utf8"));
const common = JSON.parse(await readFile(new URL("../src/i18n/locales/en/common.json", import.meta.url), "utf8"));

// The mock's lessons as they start, and the switch as asked (the mock's own reset leaves the settings alone).
const reset = async (on = false) => {
  await fetch(new URL("/mock/reset", mock));
  await fetch(new URL("/api/v1/assistant", mock), { method: "PUT", headers: { Authorization: "Bearer fixture-token", "Content-Type": "application/json" },
    body: JSON.stringify({ task_lessons: on }) });
};
let shots = 0;

async function open(theme, narrow, large = false) {
  const context = await browser.newContext({ viewport: { width: narrow ? 390 : 1280, height: narrow ? 844 : 1000 }, colorScheme: theme,
    reducedMotion: "reduce", deviceScaleFactor: 2 });
  await context.addInitScript(({ theme, large }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
    if (large) localStorage.setItem("persona.text-size", "large");
  }, { theme, large });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  return { context, page, errors };
}

// The section from its title down, in the page as it is around it.
async function shoot(page, target, name) {
  await target.evaluate((el) => {
    el.scrollIntoView({ block: "start" });
    let box = el.parentElement;
    while (box && box.scrollHeight <= box.clientHeight) box = box.parentElement;
    box?.scrollBy(0, -72);
  });
  await page.mouse.move(0, 0);
  await page.waitForTimeout(400);
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(output, name) });
  shots++;
}

async function lessonsPage(theme, narrow, act, on = false, large = false) {
  await reset(on);
  const { context, page, errors } = await open(theme, narrow, large);
  await page.goto(new URL("/kb/projects/weather-app", base).href);
  const section = page.getByRole("region", { name: l.title });
  await section.waitFor();
  const rows = section.locator("li");
  assert.equal(await rows.count(), 4, "every lesson is listed");
  assert.match(await section.innerText(), new RegExp(l.kind_missing_context));
  assert.match(await section.innerText(), new RegExp(l.stale));
  assert.ok((await section.innerText()).includes(on ? l.footer_on : l.footer_off), "the footer says whether learning is on");
  assert.ok(await section.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "section overflows");
  for (const button of await section.getByRole("button").all()) {
    assert.ok(await button.evaluate((el) => el.scrollHeight <= el.clientHeight + 2), "a button's text wraps");
  }
  await shoot(page, section, `lessons-${narrow ? "phone" : "desktop"}-${theme}${on ? "-on" : ""}${large ? "-large-text" : ""}.png`);
  if (act === "edit") {
    await rows.nth(0).getByRole("button", { name: l.edit, exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.locator("textarea").fill("Reproduce widget layout bugs on the stock simulator before changing them.");
    await page.screenshot({ path: join(output, `lessons-edit-${narrow ? "phone" : "desktop"}-${theme}.png`) });
    shots++;
    await dialog.getByRole("button", { name: common.save, exact: true }).click();
    await dialog.waitFor({ state: "detached" });
    await section.getByText("Reproduce widget layout bugs on the stock simulator before changing them.").waitFor();
    assert.match(await rows.nth(0).innerText(), new RegExp(l.edited));
    const kept = await (await fetch(new URL("/api/v1/kb/projects/weather-app/lessons", mock), { headers: { Authorization: "Bearer fixture-token" } })).json();
    assert.equal(kept.lessons[0].text, "Reproduce widget layout bugs on the stock simulator before changing them.", "the hub kept the edit");
  }
  if (act === "delete") {
    await rows.nth(1).getByRole("button", { name: l.delete, exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByText(l.delete_body).waitFor();
    await page.waitForTimeout(400);
    await page.screenshot({ path: join(output, `lessons-delete-${narrow ? "phone" : "desktop"}-${theme}.png`) });
    shots++;
    await dialog.getByRole("button", { name: l.delete, exact: true }).click();
    await dialog.waitFor({ state: "detached" });
    await page.waitForFunction(() => document.querySelectorAll("section li").length > 0);
    assert.equal(await rows.count(), 3, "the deleted lesson is gone");
  }
  assert.deepEqual(errors, []);
  await context.close();
}

async function settingsPage(theme) {
  await reset();
  const { context, page, errors } = await open(theme, false);
  await page.goto(new URL("/settings?tab=persona", base).href);
  const group = page.locator('[data-settings-group="task-lessons"]');
  await group.waitFor();
  const toggle = group.getByRole("switch", { name: settings.task_lessons });
  assert.equal(await toggle.getAttribute("aria-checked"), "false", "off by default");
  if (theme === "light") {
    await toggle.click();
    await page.waitForFunction(() => document.querySelector('[data-settings-group="task-lessons"] [role="switch"]')?.getAttribute("aria-checked") === "true");
    const kept = await (await fetch(new URL("/api/v1/assistant", mock), { headers: { Authorization: "Bearer fixture-token" } })).json();
    assert.equal(kept.task_lessons, true, "the hub kept the switch");
  }
  await shoot(page, group, `settings-desktop-${theme}.png`);
  assert.deepEqual(errors, []);
  await context.close();
}

try {
  await lessonsPage("light", false, "edit");
  await lessonsPage("dark", false, "");
  await lessonsPage("light", true, "");
  await lessonsPage("dark", true, "delete");
  await lessonsPage("light", false, "", true);
  await lessonsPage("light", true, "", false, true); // the largest text size
  await settingsPage("light");
  await settingsPage("dark");
  await reset();
  console.log(`PASS project lessons: ${shots} screenshots in ${output}`);
} catch (e) {
  console.error("FAIL project lessons:", e);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
