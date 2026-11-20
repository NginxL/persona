// Browser checks and screenshots for the configuration showcase (#9877 follow-up, docs/web/web.md "Configuration forms"):
// web/e2e/configuration-showcase.html, the real form over the showcase's mock modules, plus every scenario.
//
// Needs the Vite dev server (no hub is used: the page reads nothing from one) and Chrome or any Chromium through
// --executable:
//
//   (cd web && npx vite --port 5977 --strictPort &)
//   node web/e2e/configuration-showcase.mjs http://127.0.0.1:5977 --shots <dir> [--executable <chromium>]
//
// Shots go to <dir>, never into the repository. Prints one line per check and a verdict; exits 1 on a failure and after
// four minutes.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const executable = flag("--executable");
if (!base || !shots) {
  console.error("usage: node web/e2e/configuration-showcase.mjs <web URL> --shots <dir> [--executable <chromium>]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 4 minutes");
  process.exit(1);
}, 240_000).unref();

let failed = false;
const ok = (msg) => console.log(`ok  ${msg}`);
const fail = (msg) => {
  failed = true;
  console.log(`FAIL ${msg}`);
};

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
async function context(theme, width, size, language = "en") {
  const phone = width < 600;
  const ctx = await browser.newContext({ viewport: { width, height: phone ? 844 : 900 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: "reduce", hasTouch: phone, isMobile: phone });
  await ctx.addInitScript(([t, s, l]) => {
    localStorage.setItem("persona.language", l);
    localStorage.setItem("persona.theme", t);
    if (s) localStorage.setItem("persona.text-size", s);
  }, [theme, size, language]);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => fail(`page error: ${e.message}`));
  return { ctx, page };
}
const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
const form = (page, key) => page.locator(`[data-showcase-form="${key}"]`);
const field = (page, key, id) => form(page, key).locator(`[data-configuration-field="${id}"]`);
const open = async (page, scenario) => {
  await page.goto(`${base}/e2e/configuration-showcase.html${scenario ? `?scenario=${scenario}` : ""}`);
  await page.waitForSelector("[data-showcase-form]", { timeout: 20000 });
  await page.waitForTimeout(700);
  if (await page.locator("[data-showcase-invalid]").count()) fail(`${scenario ?? "default"}: the fixture self-check failed`);
};
const SAVE_TEXT = { en: { saved: "Saved", reset: "Reset" }, "zh-Hans": { saved: "已保存", reset: "重置" } };
/** Document coordinates keep a scroll from looking like a layout change. */
const geometryOf = (row) => row.evaluate((el) => {
  const rect = el.getBoundingClientRect();
  const next = el.nextElementSibling.getBoundingClientRect();
  return [rect.top + window.scrollY, rect.height, next.top + window.scrollY];
});
const stable = (a, b) => a.every((n, i) => Math.abs(n - b[i]) <= 0.5);
const showGeometry = ([top, height, next]) => `${top.toFixed(2)}/${height.toFixed(2)} → ${next.toFixed(2)}`;

try {
  // The full showcase at every size and theme, plus the Simplified Chinese view.
  for (const [theme, width, size, language = "en"] of [["light", 1280], ["dark", 1280], ["light", 390], ["dark", 390], ["light", 390, "large"], ["light", 1280, undefined, "zh-Hans"], ["light", 390, undefined, "zh-Hans"]]) {
    const tag = `showcase-${width}-${theme}${size ? `-${size}` : ""}${language === "zh-Hans" ? "-zh" : ""}`;
    const { ctx, page } = await context(theme, width, size, language);
    await open(page);
    if (await sideways(page)) fail(`${tag}: the showcase scrolls sideways`);
    const modules = await page.$$eval("[data-showcase-module]", (els) => els.map((e) => e.getAttribute("data-showcase-module")));
    if (modules.length !== 3) fail(`${tag}: modules ${modules}`);
    const fields = await page.locator("[data-showcase-form] [data-configuration-field]").count();
    if (fields !== 13) fail(`${tag}: only ${fields} fields drawn`);
    // The fixture's width is scoped in the page (e2e is out of Tailwind's scan), and the compact rows stay low: the
    // review's pre-refinement rows were 176/193 px.
    const layout = await page.evaluate(() => {
      const app = document.getElementById("app");
      const rect = app.getBoundingClientRect();
      const row = (form, id) => Math.round(document.querySelector(`[data-showcase-form="${form}"] [data-configuration-field="${id}"]`)?.getBoundingClientRect().height ?? 0);
      return { width: Math.round(rect.width), left: Math.round(rect.left), line: row("showcase-editor", "line_height"), topic: row("showcase-digest", "topic"), text: document.body.innerText };
    });
    const expected = width >= 600 ? 760 : width;
    if (Math.abs(layout.width - expected) > 2) fail(`${tag}: content width ${layout.width} (expected ${expected})`);
    if (width >= 600 && layout.left < 100) fail(`${tag}: content is not centered (left ${layout.left})`);
    if (layout.line > 160 || layout.topic > 160) fail(`${tag}: rows are ${layout.line}/${layout.topic} px`);
    for (const word of ["Mock", "mock", "模拟数据", "配置展示", "Scenario", "scenario", "Fixture", "fixture"]) {
      if (layout.text.includes(word)) fail(`${tag}: the page still shows "${word}"`);
    }
    await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
    ok(`${tag}: ${modules.length} modules, ${fields} fields, ${layout.width}px wide, rows ${layout.line}/${layout.topic}px`);

    // A short footer keeps its height as Reset appears or disappears.
    const words = SAVE_TEXT[language];
    const enabled = field(page, "showcase-digest", "enabled");
    const toggle = enabled.getByRole("switch");
    const resetButton = enabled.getByRole("button", { name: words.reset, exact: true });
    const saved = enabled.getByText(words.saved, { exact: true });
    await resetButton.waitFor({ timeout: 5000 });
    const start = await geometryOf(enabled);
    await toggle.click();
    await resetButton.waitFor({ state: "detached", timeout: 5000 });
    await saved.waitFor({ timeout: 5000 });
    const off = await geometryOf(enabled);
    if (width === 1280 && theme === "light" && language === "en") await enabled.screenshot({ path: join(shots, `${tag}-toggle-off.png`) });
    await toggle.click();
    await resetButton.waitFor({ timeout: 5000 });
    await saved.waitFor({ timeout: 5000 });
    const on = await geometryOf(enabled);
    if (width === 1280 && theme === "light" && language === "en") await enabled.screenshot({ path: join(shots, `${tag}-toggle-on.png`) });
    await resetButton.click();
    await resetButton.waitFor({ state: "detached", timeout: 5000 });
    await saved.waitFor({ timeout: 5000 });
    const reset = await geometryOf(enabled);
    for (const [name, state] of [["off", off], ["on", on], ["reset", reset]]) {
      if (!stable(start, state)) fail(`${tag}: toggle ${name} moves the row ${showGeometry(start)} vs ${showGeometry(state)}`);
    }
    ok(`${tag}: toggle row stable ${showGeometry(start)} across off/on/reset`);

    if (theme === "light" && !size && language === "en") {
      // A maximum of one is a single choice: choosing Chinese replaces Auto at once, with no second check.
      const languageField = field(page, "showcase-editor", "snippet_language");
      const radios = languageField.locator('[role="radio"]');
      await radios.nth(1).click();
      const picked = await radios.evaluateAll((els) => els.map((e) => e.getAttribute("aria-checked")));
      if (picked.join(",") !== "false,true,false") fail(`${tag}: the replacement checked ${picked}`);
      await languageField.locator("button", { hasText: "Save" }).click();
      await languageField.locator("text=Saved").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: the replacement was not saved`));
      ok(`${tag}: a single-choice multiselect replaces its choice at once and saves`);

      // An overridden choice goes back to its default in this page's memory.
      const renderer = field(page, "showcase-terminal", "renderer");
      await renderer.locator("button", { hasText: "Reset" }).click();
      await renderer.locator("text=Saved").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: Reset did not save`));
      if (await renderer.locator("button", { hasText: "Reset" }).count()) fail(`${tag}: Reset is still offered after resetting`);
      ok(`${tag}: Reset returns an overridden value to its default`);

      // Unicode text saves as typed.
      const note = field(page, "showcase-digest", "note").locator("textarea");
      await note.fill("第一行：你好，世界 👋\n第二行");
      await field(page, "showcase-digest", "note").locator("button", { hasText: "Save" }).click();
      await field(page, "showcase-digest", "note").locator("text=Saved").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: Unicode text did not save`));
      if ((await note.inputValue()) !== "第一行：你好，世界 👋\n第二行") fail(`${tag}: Unicode text changed`);
      ok(`${tag}: Unicode text saves`);
    }
    await ctx.close();
  }

  const scenarios = [
    ["slow", "light", 1280],
    ["slow", "light", 390],
    ["load-fail", "light", 1280],
    ["reject", "light", 1280],
    ["conflict", "light", 1280],
    ["unconfirmed", "light", 1280],
    ["keyboard", "light", 1280],
    ["keyboard", "light", 390],
    ["contract", "light", 1280],
    ["invalid", "light", 1280],
    ["invalid", "light", 390],
    ["empty", "light", 1280],
    ["unsupported", "light", 1280],
    ["conflict", "dark", 390],
    ["unconfirmed", "dark", 390],
    ["unsupported", "dark", 390],
    ["empty", "light", 390],
  ];
  for (const [scenario, theme, width] of scenarios) {
    const tag = `showcase-${scenario}-${width}-${theme}`;
    const { ctx, page } = await context(theme, width);
    await open(page, scenario);
    if (await sideways(page)) fail(`${tag}: the scenario scrolls sideways`);
    const drawn = await page.$$eval("[data-showcase-module]", (els) => els.map((e) => e.getAttribute("data-showcase-module")));
    const focused = scenario === "load-fail" ? ["showcase-terminal"]
      : ["reject", "conflict", "unconfirmed"].includes(scenario) ? ["showcase-digest"]
      : scenario === "keyboard" ? ["showcase-editor"]
      : scenario === "invalid" ? ["showcase-recovery"]
      : null;
    if (focused && drawn.join(",") !== focused.join(",")) fail(`${tag}: focused modules ${drawn}`);
    if (focused) ok(`${tag}: focused on ${focused[0]}`);

    if (scenario === "slow") {
      const enabled = field(page, "showcase-digest", "enabled");
      await enabled.waitFor({ timeout: 8000 });
      const start = await geometryOf(enabled);
      await enabled.locator('[role="switch"]').click();
      await enabled.locator("text=Saving").waitFor({ timeout: 1200 }).catch(() => fail(`${tag}: Saving… never showed`));
      const saving = await geometryOf(enabled);
      if (!stable(start, saving)) fail(`${tag}: the pending state moves the row ${showGeometry(start)} vs ${showGeometry(saving)}`);
      await form(page, "showcase-digest").screenshot({ path: join(shots, `${tag}-saving.png`) });
      await enabled.locator("text=Saved").waitFor({ timeout: 6000 }).catch(() => fail(`${tag}: the slow save never finished`));
      const saved = await geometryOf(enabled);
      if (!stable(start, saved)) fail(`${tag}: the saved state moves the row ${showGeometry(start)} vs ${showGeometry(saved)}`);
      ok(`${tag}: a slow save shows Saving…, then Saved without moving the row (${showGeometry(start)})`);
    } else if (scenario === "load-fail") {
      const terminal = form(page, "showcase-terminal");
      if (await terminal.locator("[data-configuration-field]").count()) fail(`${tag}: the failed module already drew fields`);
      const retry = terminal.locator("button", { hasText: "Try again" });
      await retry.waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: no Try again`));
      await terminal.screenshot({ path: join(shots, `${tag}-failed.png`) });
      await retry.click();
      await terminal.locator('[data-configuration-field="renderer"]').waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: Try again did not load the document`));
      ok(`${tag}: a failed first read offers Try again and recovers`);
    } else if (scenario === "reject") {
      const topic = field(page, "showcase-digest", "topic");
      const input = topic.locator("input");
      await input.fill("Another topic");
      await input.press("Enter");
      await topic.locator('[role="alert"]').waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: no rejection message`));
      const said = await topic.locator('[role="alert"]').textContent().catch(() => "");
      if (!said || !said.includes("already used")) fail(`${tag}: rejection said ${said}`);
      await form(page, "showcase-digest").screenshot({ path: join(shots, `${tag}-rejected.png`) });
      if ((await input.inputValue()) !== "Another topic") fail(`${tag}: the rejected draft was lost`);
      await input.press("Enter");
      await topic.locator("text=Saved").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: the second save did not succeed`));
      ok(`${tag}: a rejected field keeps its text and the next save succeeds`);
    } else if (scenario === "conflict") {
      const topic = field(page, "showcase-digest", "topic");
      const input = topic.locator("input");
      await input.fill("Mine");
      await input.press("Enter");
      const banner = form(page, "showcase-digest").locator('[role="alert"]');
      await banner.waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: no conflict banner`));
      if ((await input.inputValue()) !== "Mine") fail(`${tag}: the conflict lost the draft`);
      await form(page, "showcase-digest").screenshot({ path: join(shots, `${tag}-conflict.png`) });
      await form(page, "showcase-digest").locator("button", { hasText: "Reload" }).click();
      await page.waitForTimeout(500);
      if (await form(page, "showcase-digest").locator('[role="alert"]').count()) fail(`${tag}: Reload did not clear the conflict`);
      if ((await input.inputValue()) !== "Mine") fail(`${tag}: Reload lost the draft`);
      await input.press("Enter");
      await topic.locator("text=Saved").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: saving after Reload did not succeed`));
      ok(`${tag}: a conflict keeps the draft, and Reload recovers`);
    } else if (scenario === "unconfirmed") {
      const topic = field(page, "showcase-digest", "topic");
      const input = topic.locator("input");
      await input.fill("Mine");
      await input.press("Enter");
      const banner = form(page, "showcase-digest").locator('[role="alert"]');
      await banner.waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: no unconfirmed banner`));
      const said = await banner.textContent().catch(() => "");
      if (!said || !said.includes("Couldn't confirm")) fail(`${tag}: the banner said ${said}`);
      if (!(await topic.locator("button", { hasText: "Save" }).isDisabled())) fail(`${tag}: Save is enabled while the outcome is unknown`);
      await form(page, "showcase-digest").screenshot({ path: join(shots, `${tag}-unconfirmed.png`) });
      await form(page, "showcase-digest").locator("button", { hasText: "Reload" }).click();
      await page.waitForTimeout(500);
      if (await form(page, "showcase-digest").locator('[role="alert"]').count()) fail(`${tag}: Reload did not clear the unconfirmed state`);
      await input.press("Enter");
      await topic.locator("text=Saved").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: saving after Reload did not succeed`));
      ok(`${tag}: an unconfirmed save keeps the draft and waits for Reload`);
    } else if (scenario === "contract") {
      const fields = await page.locator("[data-showcase-form] [data-configuration-field]").count();
      if (fields !== 9) fail(`${tag}: the contract example drew ${fields} fields`);
      const kinds = await page.$$eval("[data-configuration-field]", (els) => [...new Set(els.map((e) => e.getAttribute("data-kind")))]);
      if (kinds.length !== 5) fail(`${tag}: the contract example kinds ${kinds}`);
      await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
      ok(`${tag}: the shared contract example draws all five kinds`);
    } else if (scenario === "invalid") {
      const recovery = form(page, "showcase-recovery");
      const fieldOf = (id) => recovery.locator(`[data-configuration-field="${id}"]`);
      const toggle = fieldOf("compact");
      const notes = fieldOf("notes");
      const emptyStates = {
        toggle: (await toggle.locator('[role="radio"][aria-checked="false"]').count()) === 2,
        dropdown: (await fieldOf("accent").locator("select").inputValue()) === "",
        segmented: (await fieldOf("motion").locator('[role="radio"][aria-checked="true"]').count()) === 0,
        list: (await fieldOf("layout").locator('[role="radio"][aria-checked="true"]').count()) === 0,
        checks: (await fieldOf("sections").locator('[role="radio"][aria-checked="true"]').count()) === 0,
        text: (await fieldOf("label").locator("input").inputValue()) === "",
        number: (await fieldOf("interval").locator("input").inputValue()) === "",
      };
      for (const [name, empty] of Object.entries(emptyStates)) if (!empty) fail(`${tag}: ${name} does not show its empty state`);
      if (!(await toggle.innerText()).includes("isn't valid here")) fail(`${tag}: no invalid-value hint`);
      // A single-choice group with no valid saved value: no radio is checked, and its first radio is the one Tab stop.
      const sectionRadios = fieldOf("sections").locator('[role="radio"]');
      if ((await sectionRadios.count()) !== 3) fail(`${tag}: the sections group is not a single choice (${await sectionRadios.count()} radios)`);
      await fieldOf("motion").locator("button").last().focus();
      await page.keyboard.press("Tab");
      const sectionEntry = await page.evaluate(() => ({
        field: document.activeElement?.closest("[data-configuration-field]")?.getAttribute("data-configuration-field"),
        role: document.activeElement?.getAttribute("role"),
        checked: document.activeElement?.getAttribute("aria-checked"),
      }));
      if (sectionEntry.field !== "sections" || sectionEntry.role !== "radio" || sectionEntry.checked !== "false") fail(`${tag}: sections' Tab entry ${JSON.stringify(sectionEntry)}`);
      ok(`${tag}: a single choice with no saved value has one Tab stop and no checked radio`);
      await page.screenshot({ path: join(shots, `${tag}-before.png`), fullPage: true });
      // Space chooses the focused first option (the same interaction as a click).
      await page.keyboard.press("Space");
      await page.waitForFunction(() => document.querySelectorAll('[data-showcase-form="showcase-recovery"] [data-configuration-field="sections"] [role="radio"][aria-checked="true"]').length === 1, null, { timeout: 2000 })
        .catch(() => fail(`${tag}: Space did not choose the first section`));
      // The hint itself says "saved", so wait for the exact save-state line, not any text containing it.
      const saved = (fieldLocator, what) => fieldLocator.locator('[aria-live="polite"]', { hasText: /^Saved$/ }).waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: ${what} did not save`));
      // Save Off directly with an On default, and repair the unreadable notes with valid empty text.
      await toggle.getByRole("radio", { name: "Off", exact: true }).click();
      await saved(toggle, "the toggle");
      if ((await toggle.getByRole("switch").getAttribute("aria-checked")) !== "false") fail(`${tag}: the toggle did not take Off`);
      if ((await toggle.innerText()).includes("isn't valid here")) fail(`${tag}: the toggle kept the invalid hint`);
      await notes.locator("textarea").fill("Draft");
      await notes.locator("textarea").fill("");
      await notes.getByRole("button", { name: "Save", exact: true }).click();
      await saved(notes, "the empty text");
      await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
      ok(`${tag}: invalid values show empty states and can be corrected`);
    } else if (scenario === "keyboard") {
      const editor = form(page, "showcase-editor");
      const fieldOf = (id) => editor.locator(`[data-configuration-field="${id}"]`);
      const active = () => page.evaluate(() => ({
        field: document.activeElement?.closest("[data-configuration-field]")?.getAttribute("data-configuration-field"),
        role: document.activeElement?.getAttribute("role"),
        checked: document.activeElement?.getAttribute("aria-checked"),
        choice: document.activeElement?.getAttribute("data-choice"),
      }));
      // The last focusable before the single-choice group is its row's Reset (the footer follows the checks).
      const tabIn = async () => {
        await fieldOf("pinned").locator("button").last().focus();
        await page.keyboard.press("Tab");
        return active();
      };
      const language = fieldOf("snippet_language");
      // A saved value: the chosen radio is the group's only Tab stop, and the next Tab leaves the group.
      let now = await tabIn();
      if (now.field !== "snippet_language" || now.role !== "radio" || now.checked !== "true" || now.choice !== "0") fail(`${tag}: saved-value Tab entry ${JSON.stringify(now)}`);
      await page.keyboard.press("Tab");
      now = await active();
      if (now.field !== "line_height") fail(`${tag}: the single-choice group has more than one Tab stop (${JSON.stringify(now)})`);
      // Cancel returns to the saved choice, which is again the one stop.
      await language.getByRole("radio", { name: "Chinese", exact: true }).click();
      await language.getByRole("button", { name: "Cancel", exact: true }).click();
      now = await tabIn();
      if (now.field !== "snippet_language" || now.checked !== "true" || now.choice !== "0") fail(`${tag}: cancelled Tab entry ${JSON.stringify(now)}`);
      // A refused save keeps the draft shown and still has exactly one Tab stop (the draft's radio).
      await language.getByRole("radio", { name: "Chinese", exact: true }).click();
      await language.getByRole("button", { name: "Save", exact: true }).click();
      await language.locator('[role="alert"]').waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: the refused save said nothing`));
      now = await tabIn();
      if (now.field !== "snippet_language" || now.checked !== "true" || now.choice !== "1") fail(`${tag}: refused-save Tab entry ${JSON.stringify(now)}`);
      await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
      ok(`${tag}: a single-choice group keeps one Tab stop through saved, cancelled and refused states`);
    } else if (scenario === "empty") {
      const fields = await page.locator("[data-showcase-form] [data-configuration-field]").count();
      if (fields !== 0) fail(`${tag}: the empty form drew ${fields} fields`);
      const title = await form(page, "showcase-empty").locator("h2").textContent().catch(() => "");
      if (!title || !title.trim()) fail(`${tag}: the empty form has no title`);
      await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
      ok(`${tag}: an empty document shows its title and no fields`);
    } else if (scenario === "unsupported") {
      const kind = form(page, "showcase-newer-kind");
      await kind.locator("text=needs a newer version").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: the unknown kind was not named`));
      const version = form(page, "showcase-newer-version");
      await version.locator("text=can't be changed").waitFor({ timeout: 5000 }).catch(() => fail(`${tag}: the newer version was not marked read-only`));
      const enabled = version.locator('[data-configuration-field="enabled"] [role="switch"]');
      if (!(await enabled.isDisabled())) fail(`${tag}: a newer version's control is enabled`);
      await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
      ok(`${tag}: an unknown kind and a newer version are shown read-only`);
    }

    if (!["empty", "unsupported", "slow", "reject", "conflict", "unconfirmed", "contract", "invalid", "keyboard"].includes(scenario)) {
      await page.screenshot({ path: join(shots, `${tag}.png`), fullPage: true });
      ok(`${tag}: scenario drawn`);
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
console.log(failed ? "FAIL configuration showcase" : "PASS configuration showcase");
process.exit(failed ? 1 : 0);
