// The health module on and off (#13434, docs/web/web.md "Modules"), against the mock hub through the dev server: the
// sidebar entry, the page, Settings › Labs › Features, and which requests the browser made. Light and dark.
// PORT=<hub port> python3 scripts/mock/mock_hub.py; PERSONA_HUB_URL=http://127.0.0.1:<hub port> npx vite --port <web port>
// node web/e2e/module-host.mjs http://127.0.0.1:<web port> <shots directory>
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots] = process.argv.slice(2);
assert.match(base ?? "", /^http:\/\/127\.0\.0\.1:\d+$/);
mkdirSync(shots, { recursive: true });
const browser = await chromium.launch();
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
      const health = () => page.locator("a[href='/health']");

      await page.goto(`${base}/`);
      await page.getByRole("link", { name: "Inbox" }).first().waitFor();
      assert.equal(await health().count() > 0, on, `${tag}: sidebar entry`);
      await page.screenshot({ path: join(shots, `${tag}-sidebar.png`) });
      // never fetched at idle (past the idle tier's 5 s); warmed by its entry's hover only; nothing ever for an off module
      await page.waitForTimeout(6500);
      const idled = requested.length;
      assert.equal(idled, 0, `${tag}: nothing of the module is fetched at idle`);
      if (on) {
        await health().first().hover();
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

      await page.goto(`${base}/settings?tab=labs`);
      await page.locator('[data-configuration-field="health"]').waitFor();
      const checked = await page.locator('[data-configuration-field="health"] [role="switch"]').getAttribute("aria-checked");
      assert.equal(checked, String(on), `${tag}: the Labs switch`);
      await page.locator('[data-configuration-field="health"]').scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(shots, `${tag}-labs.png`) });

      if (on) assert.ok(requested.length > 0, `${tag}: the module's code and strings load when its page opens`);
      else assert.deepEqual(requested, [], `${tag}: nothing of the module is requested`);
      assert.deepEqual(errors, [], `${tag}: page errors`);
      console.log(`${tag}: ok, ${idled} module request(s) at idle, ${warmed} after hovering its entry, ${requested.length} in all`);
      await context.close();
    }
  }
  console.log("module-host: PASS");
} catch (e) {
  console.log(`module-host: FAIL ${e instanceof Error ? e.message : e}`);
  process.exitCode = 1;
} finally {
  await browser.close();
}
