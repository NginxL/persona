// #10477: real-browser drag, keyboard, persistence, responsive bounds and deep-file count layout.
// Requires MOCK_TURN_CHANGES=1; use independent loopback Vite/mock ports.
// node web/e2e/changes-tree-resize.mjs <web origin> <mock origin> <artifact directory>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { chromium } from "playwright-core";
const [webAddress, hubAddress, directory] = process.argv.slice(2);
const web = new URL(webAddress), hub = new URL(hubAddress);
for (const base of [web, hub]) assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname));
assert.equal((await (await fetch(new URL("/api/v1/health", hub))).json()).version, "mock");
const output = resolve(directory);
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ channel: "chrome", headless: true });
const results = [];
const errors = [];
try {
  for (const theme of ["light", "dark"]) for (const size of ["standard", "large"]) {
    await fetch(new URL("/mock/reset", hub));
    const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, colorScheme: theme, locale: "en-US" });
    try {
      await context.addInitScript(({ theme, size }) => {
        // Initialize once; a reload must exercise the actual saved tree preference.
        if (sessionStorage.getItem("tree-test")) return;
        sessionStorage.setItem("tree-test", "1");
        localStorage.setItem("persona.theme", theme);
        localStorage.setItem("persona.language", "en");
        localStorage.setItem("persona.text-size", size);
        localStorage.setItem("persona.panelWidth", "800");
        localStorage.setItem("persona.sidebarCollapsed", "1");
        localStorage.removeItem("persona.changesTreeWidth");
      }, { theme, size });
      const page = await context.newPage();
      page.setDefaultTimeout(20_000);
      page.on("pageerror", error => errors.push(error.message));
      await page.goto(new URL("/s/macbook/a1f3c2d4-0000-4000-8000-000000005157?token=mock", web).href);
      async function openChanges() {
        if (await page.locator("[data-changes-panel] nav").count()) return;
        const edit = page.locator("[data-action-row=Edit] > button").first();
        if (!await edit.count()) await page.locator("[data-chat-scroll] button").filter({ hasText: "Worked for" }).first().click();
        await edit.click();
        await page.locator("[data-change-call]").first().waitFor();
      }
      await openChanges();
      const deep = "/Users/me/code/weather/src/app/services/feature/contract/very/deep/native-feature-flags.test.ts";
      for (let n = 0; n < 19; n++) {
        const url = new URL("/mock/say", hub);
        url.searchParams.set("edit", deep);
        assert.equal((await fetch(url)).status, 200);
      }
      await page.locator('[data-change-scope="session"]').click();
      const file = page.locator(`[data-change-file="${deep}"]`);
      await file.waitFor();
      await page.waitForFunction(path => document.querySelector(`[data-change-file="${path}"]`)?.textContent?.includes("-19"), deep);
      await file.click();
      const handle = page.locator('[data-changes-panel] [role="separator"]');
      const value = async () => Number(await handle.getAttribute("aria-valuenow"));
      assert.equal(await value(), 192);
      const box = await handle.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width / 2 + 128, box.y + box.height / 2, { steps: 8 });
      await page.mouse.up();
      const dragged = await value();
      assert.equal(dragged, 320);
      assert.equal(await page.evaluate(() => localStorage.getItem("persona.changesTreeWidth")), "320");
      await page.reload();
      await openChanges();
      assert.equal(await value(), 320);
      await handle.focus();
      await page.keyboard.press("Home");
      assert.equal(await value(), 160);
      await file.scrollIntoViewIfNeeded();
      const geometry = await file.evaluate(row => {
        const counts = row.lastElementChild, nav = row.closest("nav"), name = row.firstElementChild;
        const r = counts.getBoundingClientRect(), n = nav.getBoundingClientRect();
        return { text: counts.textContent, left: r.left, right: r.right, navLeft: n.left, navRight: n.right,
          nameClient: name.clientWidth, nameScroll: name.scrollWidth, countsClient: counts.clientWidth, countsScroll: counts.scrollWidth,
          labelClient: name.firstElementChild.lastElementChild.clientWidth, labelScroll: name.firstElementChild.lastElementChild.scrollWidth };
      });
      assert.ok(geometry.left >= geometry.navLeft && geometry.right <= geometry.navRight, JSON.stringify(geometry));
      assert.equal(geometry.countsClient, geometry.countsScroll);
      assert.ok(geometry.labelClient >= 16 && geometry.labelScroll > geometry.labelClient, JSON.stringify(geometry));
      assert.equal(await file.getAttribute("title"), deep);
      assert.ok(geometry.text.includes("+19") && geometry.text.includes("-19"));
      await handle.dblclick();
      assert.equal(await value(), 192);
      assert.equal(await page.evaluate(() => localStorage.getItem("persona.changesTreeWidth")), null);
      await handle.focus();
      await page.keyboard.press("End");
      const max = await value();
      const diffWidth = await page.locator("[data-change-diff]").evaluate(el => el.clientWidth);
      assert.ok(diffWidth >= 160);
      // A smaller outer panel clamps the displayed width, but expanding restores the preference.
      await page.evaluate(() => { document.querySelector("[data-changes-panel]").closest("aside").style.setProperty("--panel-w", "360px"); });
      await page.waitForFunction(() => Number(document.querySelector('[data-changes-panel] [role="separator"]')?.getAttribute("aria-valuenow")) <= 200);
      await page.evaluate(() => { document.querySelector("[data-changes-panel]").closest("aside").style.setProperty("--panel-w", "800px"); });
      await page.waitForFunction(w => Number(document.querySelector('[data-changes-panel] [role="separator"]')?.getAttribute("aria-valuenow")) === w, max);
      await handle.focus();
      await page.keyboard.press("Enter");
      await file.click();
      await file.scrollIntoViewIfNeeded();
      await page.mouse.move(500, 50);
      await page.screenshot({ path: join(output, `tree-${theme}-${size}.png`) });
      await page.setViewportSize({ width: 390, height: 844 });
      assert.equal(await handle.isVisible(), false);
      const narrow = await page.locator("[data-changes-panel]").evaluate(panel => {
        const nav = panel.querySelector("nav").getBoundingClientRect(), diff = panel.querySelector("[data-change-diff]").getBoundingClientRect();
        return { treeBottom: nav.bottom, diffTop: diff.top, treeWidth: nav.width, diffWidth: diff.width };
      });
      assert.ok(narrow.diffTop >= narrow.treeBottom - 1 && narrow.treeWidth >= 388 && narrow.diffWidth >= 388, JSON.stringify(narrow));
      results.push({ theme, size, dragged, max, diffWidth, geometry, narrow });
      console.log(`PASS ${theme}/${size}: ${JSON.stringify(results.at(-1))}`);
    } finally { await context.close(); }
  }
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
  await writeFile(join(output, "results.json"), JSON.stringify({ results, errors }, null, 2) + "\n");
}
