// Module visibility and lazy loading (#13434), plus the permanent switches in General (#14468).
// Run against the local mock hub through Vite. The settings matrix covers both themes, two text sizes and all locales.
// PORT=<hub port> python3 scripts/mock/mock_hub.py; PERSONA_HUB_URL=http://127.0.0.1:<hub port> npx vite --port <web port>
// node web/e2e/module-host.mjs http://127.0.0.1:<web port> <shots directory> [chromium executable]
import assert from "node:assert/strict";
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executablePath] = process.argv.slice(2);
assert.match(base ?? "", /^http:\/\/127\.0\.0\.1:\d+$/);
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch({ executablePath });
console.log(`Chromium ${browser.version()}`);
const watchdog = setTimeout(() => {
  console.error("module-host: TIMEOUT");
  process.exit(2);
}, 240_000);
const moduleKey = "persona.modules.off";
const health = (page) => page.locator("a[href='/health']");
const featureForm = (page) => page.locator('[data-configuration="features"]');
const featureSwitch = (page, id) => featureForm(page).locator(`[data-configuration-field="${id}"]`).getByRole("switch");
const catalog = (lang, ns) => JSON.parse(readFileSync(new URL(`../src/i18n/locales/${lang}/${ns}.json`, import.meta.url), "utf8"));
// the module's own files and strings, as the dev server serves them
const ofModule = (url) => /\/src\/modules\/health\/(Health|HealthReports|reports)\.tsx?/.test(url) || /catalog=module-health|i18n-catalog:module-health/.test(url);

try {
  for (const scheme of ["light", "dark"]) {
    for (const on of [true, false]) {
      const context = await browser.newContext({ viewport: { width: 1280, height: 820 }, colorScheme: scheme, reducedMotion: "reduce", locale: "en-US" });
      await context.addInitScript(({ on }) => {
        localStorage.setItem("persona.token", "mock");
        localStorage.setItem("persona.language", "en");
        if (!on) localStorage.setItem("persona.modules.off", '["health"]');
      }, { on });
      const page = await context.newPage();
      const requested = [];
      page.on("request", (r) => ofModule(r.url()) && requested.push(r.url()));
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));
      page.setDefaultTimeout(30000);
      const tag = `${on ? "on" : "off"}-${scheme}`;

      await page.goto(`${base}/`);
      await page.getByRole("link", { name: "Inbox" }).first().waitFor();
      assert.equal(await health(page).count() > 0, on, `${tag}: sidebar entry`);
      await page.screenshot({ path: join(shots, `${tag}-sidebar.png`) });
      // never fetched at idle (past the idle tier's 5 s); warmed by its entry's hover only; nothing ever for an off module
      await page.waitForTimeout(6500);
      const idled = requested.length;
      assert.equal(idled, 0, `${tag}: nothing of the module is fetched at idle`);
      if (on) {
        await health(page).first().hover();
        await page.waitForTimeout(3000);
      }
      const warmed = requested.length;
      if (on) assert.ok(requested.some((u) => /catalog=module-health|i18n-catalog:module-health/.test(u)) && requested.some((u) => /\/Health\.tsx/.test(u)), `${tag}: hover warms the strings and the chunk: ${requested.join(" ")}`);
      else assert.equal(warmed, 0, `${tag}: an off module is never warmed`);

      await page.goto(`${base}/health/new`);
      if (on) await page.getByText("Health").first().waitFor();
      else await page.getByText(/no page|not found|doesn.t exist/i).first().waitFor();
      await page.waitForTimeout(800);
      await page.screenshot({ path: join(shots, `${tag}-health.png`) });

      await page.goto(`${base}/settings?tab=general`);
      await featureSwitch(page, "health").waitFor();
      assert.equal(await featureSwitch(page, "health").getAttribute("aria-checked"), String(on), `${tag}: saved choice in General`);

      if (on) assert.ok(requested.length > 0, `${tag}: the module's code and strings load when its page opens`);
      else assert.deepEqual(requested, [], `${tag}: nothing of the module is requested`);
      assert.deepEqual(errors, [], `${tag}: page errors`);
      console.log(`${tag}: ok, ${idled} module request(s) at idle, ${warmed} after hovering its entry, ${requested.length} in all`);
      await context.close();
    }
  }
  for (const lang of ["en", "zh-Hans", "ja"]) {
    const sidebar = catalog(lang, "sidebar");
    const features = catalog(lang, "settings-features");
    const settings = catalog(lang, "settings");
    const nav = catalog(lang, "settings-nav");
    const labels = { health: sidebar.nav_health, voice: features.module_voice, recording: sidebar.module_recording };
    for (const colorScheme of ["light", "dark"]) {
      for (const size of ["standard", "large"]) {
        const tag = `${lang}-${colorScheme}-${size}`;
        const context = await browser.newContext({ viewport: { width: 1280, height: 820 }, colorScheme, reducedMotion: "reduce", locale: lang });
        await context.addInitScript(({ lang, size }) => {
          localStorage.setItem("persona.token", "mock");
          localStorage.setItem("persona.language", lang);
          localStorage.setItem("persona.text-size", size);
        }, { lang, size });
        const page = await context.newPage();
        page.setDefaultTimeout(20_000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(String(e)));
        await page.goto(`${base}/settings?tab=labs`);
        await page.locator('[data-configuration-field="renderer"]').waitFor();
        assert.equal(await featureForm(page).count(), 0, `${tag}: Labs has no feature switches`);
        assert.equal(await page.locator("html").getAttribute("lang"), lang);
        assert.equal(await page.locator("html").getAttribute("data-text-size"), size === "large" ? size : null);
        assert.equal(await page.locator("html").evaluate((el) => el.classList.contains("dark")), colorScheme === "dark");
        if (lang === "en") {
          await page.screenshot({ path: join(shots, `${tag}-labs.png`) });
        }

        const search = page.getByRole("textbox", { name: settings.search_settings });
        const navigation = page.getByRole("navigation", { name: settings.categories });
        const landed = async (anchor) => {
          await page.getByRole("region", { name: nav.tab_general }).waitFor();
          await page.waitForFunction((anchor) => {
            const target = document.querySelector(`[data-settings-group="${anchor}"], [data-settings-row="${anchor}"]`);
            return (target?.matches("[data-settings-row]") ? target : target?.firstElementChild)?.style.boxShadow;
          }, anchor);
        };
        const jump = async (label, anchor) => {
          await search.fill(label);
          await navigation.getByRole("button")
            .filter({ has: page.getByText(nav.tab_general, { exact: true }) })
            .filter({ has: page.getByText(label, { exact: true }) })
            .click();
          await landed(anchor);
        };
        for (const [id, label] of Object.entries(labels)) {
          await jump(label, `configuration-features-${id}`);
          assert.equal(await featureSwitch(page, id).getAttribute("aria-label"), label);
          assert.equal(await featureSwitch(page, id).getAttribute("aria-checked"), "true");
        }
        await search.fill(features.area_features);
        await search.press("Enter");
        await landed("configuration-features-modules");
        await page.waitForFunction(() => {
          const group = document.querySelector('[data-settings-group="configuration-features-modules"]');
          return group && !group.firstElementChild.style.boxShadow;
        });
        await featureForm(page).evaluate((el) => el.scrollIntoView({ block: "center" }));
        assert.ok(await featureForm(page).evaluate((el) => el.scrollWidth <= el.clientWidth), `${tag}: feature form fits its column`);
        await page.screenshot({ path: join(shots, `${tag}-general.png`) });

        if (lang === "en" && size === "standard" && colorScheme === "light") {
          const other = await context.newPage();
          other.on("pageerror", (e) => errors.push(String(e)));
          await other.goto(`${base}/settings?tab=general`);
          for (const id of Object.keys(labels)) {
            const toggle = featureSwitch(page, id);
            await toggle.click();
            await featureSwitch(other, id).and(other.locator('[aria-checked="false"]')).waitFor();
            assert.equal(await page.evaluate((key) => localStorage.getItem(key), moduleKey), JSON.stringify([id]));
            // A switched-off module still has its own search result so the user can turn it back on.
            await jump(labels[id], `configuration-features-${id}`);
            await toggle.click();
            await featureSwitch(other, id).and(other.locator('[aria-checked="true"]')).waitFor();
            assert.equal(await page.evaluate((key) => localStorage.getItem(key), moduleKey), null);
          }
          console.log("all three switches persist and sync across two tabs");
        }
        assert.deepEqual(errors, [], `${tag}: page errors`);
        await context.close();
        console.log(`${tag}: localized controls and search destinations, General and Labs verified`);
      }
    }
  }
  console.log("module-host: PASS");
} catch (e) {
  console.log(`module-host: FAIL ${e instanceof Error ? e.message : e}`);
  process.exitCode = 1;
} finally {
  clearTimeout(watchdog);
  await browser.close();
}
