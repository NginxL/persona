// Finding a Persona chat by what was said in it, against the mock hub (#6283): ⌘K and "All chats" show the sentence
// that matched and open the chat at that message, paging back for an early one; thoughts and tools are never found.
// Synthetic data only. Start the mock with MOCK_CHAT_SEARCH=1 and Vite pointing at it (PERSONA_HUB_URL), then:
// node web/e2e/assistant-search.mjs <loopback web URL> <screenshot directory> [Chromium executable]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executable] = process.argv.slice(2);
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/assistant-search.mjs <loopback web URL> <screenshot directory> [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.error("RESULT: assistant-search timed out (300s)");
  process.exit(124);
}, 300_000);
const root = base.replace(/\/$/, "");

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
let failed = false;
try {
  for (const scheme of ["light", "dark"]) {
    for (const [width, height, tag] of [[1280, 860, "desktop"], [390, 844, "phone"]]) {
      const page = await (await browser.newContext({ viewport: { width, height }, colorScheme: scheme, locale: "en-US", deviceScaleFactor: 2 })).newPage();
      await page.goto(`${root}/overview?token=mock`);
      await page.waitForFunction(() => localStorage.getItem("persona.token"));

      // ⌘K: the user's words
      await page.goto(`${root}/overview`);
      await page.waitForTimeout(1200);
      await page.keyboard.press("ControlOrMeta+k");
      const box = page.getByPlaceholder("Search sessions, inbox");
      await box.waitFor();
      await box.fill("BlueLane");
      const said = page.locator("[data-said]", { hasText: "BlueLane" }).first();
      await said.waitFor({ timeout: 10_000 });
      assert.equal(await said.locator("mark").first().innerText(), "BlueLane", "the match is marked");
      await page.screenshot({ path: join(shots, `${scheme}-${tag}-palette.png`) });
      // never a thought or a tool's output
      for (const word of ["ThinkWord", "CopperKey"]) {
        await box.fill(word);
        await page.waitForTimeout(700);
        assert.equal(await page.locator("[data-said]").count(), 0, `${word} is not found`);
      }
      await box.fill("BlueLane");
      await said.waitFor({ timeout: 10_000 });
      await said.click();
      await page.waitForURL(/[?&]at=/, { timeout: 10_000 });
      await page.locator("[data-search-hit]", { hasText: "BlueLane" }).waitFor({ timeout: 15_000 });
      // the ring is on the bubble itself, not on its row (#6426)
      assert.equal(await page.locator("[data-search-hit][data-hit-target]").count(), 1, "the ring hugs the bubble");
      await page.screenshot({ path: join(shots, `${scheme}-${tag}-palette-landed.png`) });

      // All chats: early history, paged back for
      await page.goto(`${root}/history`);
      const field = page.getByPlaceholder("Search chats");
      await field.waitFor({ timeout: 15_000 });
      await field.fill("GlacierPost");
      const early = page.locator("[data-said]", { hasText: "GlacierPost" }).first();
      await early.waitFor({ timeout: 10_000 });
      await page.screenshot({ path: join(shots, `${scheme}-${tag}-history.png`) });
      await early.click();
      await page.waitForURL(/[?&]at=/, { timeout: 10_000 });
      await page.locator("[data-search-hit]", { hasText: "GlacierPost" }).waitFor({ timeout: 30_000 });
      await page.screenshot({ path: join(shots, `${scheme}-${tag}-history-landed.png`) });

      // A completed turn's narration is folded on the newest page: the global hit opens only that fold (#7965).
      await page.goto(`${root}/overview`);
      await page.waitForTimeout(1200);
      await page.keyboard.press("ControlOrMeta+k");
      const foldedBox = page.getByPlaceholder("Search sessions, inbox");
      await foldedBox.fill("FoldedNeedle");
      const folded = page.locator("[data-said]", { hasText: "FoldedNeedle" }).first();
      await folded.waitFor({ timeout: 10_000 });
      await folded.click();
      await page.waitForURL(/[?&]at=narration-target/, { timeout: 10_000 });
      await page.locator("[data-search-hit]", { hasText: "FoldedNeedle" }).waitFor({ timeout: 15_000 });
      await page.screenshot({ path: join(shots, `${scheme}-${tag}-folded-narration-landed.png`) });
      await page.close();
    }
  }
  console.log(`RESULT: assistant-search passed (screenshots in ${shots})`);
} catch (e) {
  failed = true;
  console.error(`RESULT: assistant-search failed: ${e instanceof Error ? e.message.split("\n")[0] : e}`);
} finally {
  clearTimeout(watchdog);
  await browser.close();
}
process.exit(failed ? 1 : 0);
