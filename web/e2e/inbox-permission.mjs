// Settings › Model configuration › AI twin runs: the real web editor against synthetic in-browser hub answers.
// Verifies that an Agents Full access default is not inherited by Inbox, an explicit choice saves, and reset restores
// Default permissions. Screenshots cover desktop/phone, light/dark and standard/Large text; nothing touches a live hub.
import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const base = new URL(process.env.BASE_URL || "http://127.0.0.1:5173");
assert.equal(base.hostname, "127.0.0.1", "Use an isolated loopback preview");
const output = resolve(process.env.SHOTS_DIR || "inbox-permission-shots");
await mkdir(output, { recursive: true });
const executablePath = process.env.PERSONA_E2E_CHROMIUM ||
  (process.platform === "darwin" ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" : "/usr/bin/google-chrome");
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const deadline = setTimeout(() => { console.error("TIMEOUT Inbox permission settings"); process.exit(1); }, 180_000);
const modes = JSON.parse(await readFile(new URL("../src/i18n/locales/en/lib-session-actions.json", import.meta.url), "utf8"));
const common = JSON.parse(await readFile(new URL("../src/i18n/locales/en/common.json", import.meta.url), "utf8"));
const modelText = JSON.parse(await readFile(new URL("../src/i18n/locales/en/settings-modelconfiguration.json", import.meta.url), "utf8"));
const results = [];

function fixture() {
  const providers = [{ id: "launcher.claude", name: "Claude Code", kind: "launcher", agent: "claude" }];
  const parent = {
    id: "agent.new", name: "Agents", group: "Agents", kind: "launcher", launch: "engine", category_id: "agent.new",
    override: { provider: "launcher.claude", permission_mode: "bypassPermissions" },
    effective: { provider: "launcher.claude", permission_mode: "bypassPermissions" }, revision: 3,
    fields: ["provider", "model", "effort", "permission_mode"], provider_ids: ["launcher.claude"],
  };
  const inbox = {
    id: "inbox.agent", name: "AI twin runs", group: "Agents", kind: "launcher", launch: "headless",
    parent: "agent.new", category_id: "agent.new", override: {},
    effective: { provider: "launcher.claude", permission_mode: "bypassPermissions" }, revision: 0,
    inherited_from: "Agents", fields: ["provider", "model", "effort", "permission_mode"], provider_ids: ["launcher.claude"],
  };
  return { providers, parent, inbox, writes: [] };
}

async function run(theme, width, textSize) {
  const tag = `${width}-${theme}-${textSize}`;
  const state = fixture();
  const context = await browser.newContext({ viewport: { width, height: width < 500 ? 844 : 1000 }, colorScheme: theme,
    reducedMotion: "reduce", deviceScaleFactor: 2 });
  await context.addInitScript(({ theme, textSize }) => {
    localStorage.setItem("persona.token", "fixture-token");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.text-size", textSize);
  }, { theme, textSize });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/api/v1/model-config", (route) => route.fulfill({
    status: 200, contentType: "application/json", body: JSON.stringify({ purposes: [state.parent, state.inbox], providers: state.providers }),
  }));
  await page.route("**/api/v1/model-config/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    if (path === "/api/v1/model-profiles") return json({ error: "not found" }, 404);
    if (path === "/api/v1/model-config") return json({ purposes: [state.parent, state.inbox], providers: state.providers });
    if (path.endsWith("/models")) return json({ agent: "claude", models: [{ id: "default", name: "Default", default: true }] });
    if (path === "/api/v1/model-config/purposes/inbox.agent") {
      if (route.request().method() === "PATCH") {
        const body = route.request().postDataJSON();
        state.writes.push(body);
        for (const key of ["provider", "model", "effort", "permission_mode"]) {
          if (!(key in body)) continue;
          if (body[key] == null) delete state.inbox.override[key];
          else state.inbox.override[key] = String(body[key]);
        }
        state.inbox.revision++;
        state.inbox.effective = { ...state.parent.effective, ...state.inbox.override };
      }
      return json({ purpose: state.inbox, providers: state.providers });
    }
    return json({ error: `unexpected ${path}` }, 404);
  });
  try {
    await page.goto(new URL("/settings?tab=models#settings-model-config", base).href);
    const row = page.locator('[data-model-purpose="inbox.agent"]');
    await row.waitFor();
    assert.match(await row.textContent(), new RegExp(modes.mode_default));
    assert.doesNotMatch(await row.textContent(), new RegExp(modes.mode_bypass_permissions));
    await row.locator("button").first().click();
    const dialog = page.getByRole("dialog");
    const select = dialog.locator("[data-permission-mode-select]");
    await select.waitFor();
    assert.match(await select.locator("option").first().textContent(), new RegExp(modes.mode_default));
    assert.doesNotMatch(await select.locator("option").first().textContent(), new RegExp(modes.mode_bypass_permissions));
    assert.equal(await select.locator('option[value="plan"]').count(), 0, `${tag}: Inbox must not offer Plan`);
    assert.match(await dialog.textContent(), /never inherit Agents' Full access/);
    assert.equal(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), true, `${tag}: dialog overflows`);
    await page.screenshot({ path: join(output, `${tag}-default.png`) });
    const screenshots = [`${tag}-default.png`];
    if (width < 500) {
      const hint = dialog.getByText(modelText.permission_mode_hint_inbox, { exact: true });
      await hint.evaluate((el) => el.scrollIntoView({ block: "center" }));
      const box = await hint.boundingBox();
      assert.ok(box && box.y >= 0 && box.y + box.height <= 844, `${tag}: the full permission hint is visible after scrolling`);
      await page.screenshot({ path: join(output, `${tag}-hint.png`) });
      screenshots.push(`${tag}-hint.png`);
    }

    await select.selectOption("bypassPermissions");
    assert.match(await dialog.textContent(), new RegExp(modes.bypass_warning));
    await dialog.getByRole("button", { name: common.save, exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    assert.equal(state.writes[0].permission_mode, "bypassPermissions");

    await row.locator("button").first().click();
    assert.equal(await select.inputValue(), "bypassPermissions");
    await dialog.getByRole("button", { name: modelText.follow_agents, exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    assert.equal(state.writes[1].permission_mode, null);
    await row.locator("button").first().click();
    assert.match(await select.locator("option").first().textContent(), new RegExp(modes.mode_default));
    assert.doesNotMatch(await select.locator("option").first().textContent(), new RegExp(modes.mode_bypass_permissions));
    assert.deepEqual(errors, []);
    results.push({ tag, writes: state.writes, screenshots, pageErrors: errors });
    console.log(`PASS ${tag}: default does not inherit Agents bypass; explicit bypass saves; reset restores default`);
  } catch (error) {
    await writeFile(join(output, `${tag}-failure.txt`), await page.locator("body").innerText());
    await page.screenshot({ path: join(output, `${tag}-failure.png`), fullPage: true });
    throw error;
  } finally {
    await context.close();
  }
}

try {
  await run("light", 1280, "standard");
  await run("dark", 1280, "standard");
  await run("light", 390, "large");
  await run("dark", 390, "large");
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2) + "\n");
  console.log(`PASS Inbox permission settings: ${results.length} browser cases in ${output}`);
} catch (e) {
  console.error("FAIL Inbox permission settings:", e);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
