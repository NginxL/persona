// Browser regression for the web's neutral project link and setup without star prompts (#6570).
// Use only a disposable mock hub; never switch scenarios or pair with a production hub here.
// node web/e2e/project-source.mjs <web URL> --control <mock control URL> [--cdp <URL>] [--shots <directory>]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => args[args.indexOf(name) + 1];
const base = args[0];
const control = args.includes("--control") ? flag("--control") : null;
const shots = args.includes("--shots") ? flag("--shots") : null;
if (!base || !control) throw new Error("Supply a web URL and a disposable mock scenario-control URL");
if (shots) mkdirSync(shots, { recursive: true });
const browser = args.includes("--cdp") ? await chromium.connectOverCDP(flag("--cdp")) : await chromium.launch({ channel: "chrome" });
const noStar = async (page) => {
  assert.equal(await page.locator('[data-testid="star-pill"], [data-testid="star-prompt"]').count(), 0);
  assert.doesNotMatch(await page.locator("body").innerText(), /去 GitHub 点 Star|Star on GitHub|GitHubでスター|喜欢 Persona 的话|If you like Persona, a star/);
};
const switchTo = async (name) => {
  const response = await fetch(`${control}/scenario?name=readiness-${name}`, { signal: AbortSignal.timeout(35000) });
  assert.equal(response.status, 200);
};
const snapshot = async (page, name) => {
  if (!shots) return;
  await page.bringToFront();
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
  await page.screenshot({ path: join(shots, name + ".png"), timeout: 60000 });
};
try {
  for (const width of [1280, 390]) {
    for (const theme of ["light", "dark"]) {
      await switchTo("not_logged_in");
      const context = await browser.newContext({ viewport: { width, height: 900 }, locale: "zh-CN", colorScheme: theme,
        ...(args.includes("--proxy") ? { proxy: { server: flag("--proxy"), bypass: "127.0.0.1,localhost" } } : {}),
      });
      const page = await context.newPage();
      await page.addInitScript(({ theme }) => {
        localStorage.setItem("persona.theme", theme);
        localStorage.setItem("persona.language", "zh-Hans");
        localStorage.setItem("persona.starPrompt", JSON.stringify({ launches: 20, threshold: 20, done: false, welcomed: false }));
      }, { theme });
      await page.goto(`${base}/?token=mock`);
      await page.waitForSelector("main", { timeout: 20000 });
      await page.getByText("先接上模型", { exact: true }).first().waitFor({ timeout: 20000 });
      await noStar(page);
      await snapshot(page, `setup-before-${width}-${theme}`);
      const ready = page.waitForResponse((r) => r.url().includes("/assistant/readiness") && r.status() === 200 && r.request().method() === "GET");
      await switchTo("full");
      await page.evaluate(() => window.dispatchEvent(new Event("focus")));
      assert.equal((await (await ready).json()).ready, true);
      await page.getByText("先接上模型", { exact: true }).first().waitFor({ state: "hidden", timeout: 20000 });
      await noStar(page);
      // The switch restarted the mock; reload to retire the former scenario's cached model-error turn before capture.
      await page.reload();
      await page.waitForSelector("main", { timeout: 20000 });
      await noStar(page);
      await snapshot(page, `setup-complete-${width}-${theme}`);
      console.log(`PASS setup transition: ${width} ${theme}, no star toast or milestone`);
      await page.goto(`${base}/settings?tab=general`);
      const link = page.locator('[data-settings-group="about-persona"] a');
      await link.waitFor({ timeout: 20000 });
      await link.scrollIntoViewIfNeeded();
      assert.equal(await link.getAttribute("href"), "https://github.com/nwverse/persona");
      assert.equal(await link.getAttribute("target"), "_blank");
      assert.equal(await link.getAttribute("rel"), "noopener noreferrer");
      assert.match(await link.locator("..").locator("..").innerText(), /开源地址/);
      const box = await link.boundingBox();
      assert.ok(box && box.x >= 0 && box.x + box.width <= width && box.y >= 0 && box.y + box.height <= 900);
      await noStar(page);
      await snapshot(page, `settings-source-${width}-${theme}`);
      const popupPromise = page.waitForEvent("popup");
      await link.click();
      const popup = await popupPromise;
      await popup.waitForURL("https://github.com/nwverse/persona", { timeout: 30000, waitUntil: "domcontentloaded" });
      console.log(`PASS settings link: ${width} ${theme}, opened https://github.com/nwverse/persona`);
      if (width === 390) {
        await page.evaluate(() => localStorage.setItem("persona.text-size", "large"));
        await page.reload();
        await link.waitFor({ timeout: 20000 });
        await link.scrollIntoViewIfNeeded();
        const large = await link.boundingBox();
        assert.ok(large && large.x >= 0 && large.x + large.width <= width);
        await snapshot(page, `settings-source-${width}-${theme}-large`);
        console.log(`PASS largest text size: ${width} ${theme}, link fits`);
      }
      // Leave attached-browser pages available for inspection; never touch existing tabs.
      if (!args.includes("--cdp")) await context.close();
    }
  }
  console.log("PROJECT_SOURCE_E2E=PASS");
} catch (error) {
  console.error("PROJECT_SOURCE_E2E=FAIL", error);
  process.exitCode = 1;
} finally {
  if (!args.includes("--cdp")) await browser.close();
  else process.exit(process.exitCode ?? 0);
}
