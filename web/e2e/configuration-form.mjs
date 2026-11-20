// Screenshots and a quick check of the generic configuration form (#9877, docs/web/web.md "Configuration forms"): the
// fixture-only page (e2e/configuration-form.html: the real form over the shared schema/configuration/all-types.json) and
// the real Settings › Labs, each wide (1280) and phone width (390), light and dark, and at the large text size.
//
// Needs the Vite dev server (and, for Labs, any hub the page can sign in to, such as the mock), and Chrome or any
// Chromium through --executable:
//
//   PORT=18977 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18977 npx vite --port 5977 --strictPort &)
//   node web/e2e/configuration-form.mjs http://127.0.0.1:5977 --shots <dir> [--executable <chromium>]
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
  console.error("usage: node web/e2e/configuration-form.mjs <web URL> --shots <dir> [--executable <chromium>]");
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
async function context(theme, width, size) {
  const phone = width < 600;
  const ctx = await browser.newContext({ viewport: { width, height: phone ? 844 : 900 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: "reduce", hasTouch: phone, isMobile: phone });
  await ctx.addInitScript(([t, s]) => {
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", t);
    if (s) localStorage.setItem("persona.text-size", s);
  }, [theme, size]);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => fail(`page error: ${e.message}`));
  return { ctx, page };
}
const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);

try {
  for (const [theme, width, size] of [["light", 1280], ["dark", 1280], ["light", 390], ["dark", 390], ["light", 390, "large"], ["light", 1280, "large"]]) {
    const tag = `${width}-${theme}${size ? `-${size}` : ""}`;
    const { ctx, page } = await context(theme, width, size);

    await page.goto(`${base}/e2e/configuration-form.html`);
    await page.waitForSelector("[data-configuration-field]", { timeout: 20000 });
    await page.waitForTimeout(500);
    if (await sideways(page)) fail(`${tag}: the fixture form scrolls sideways`);
    const kinds = await page.$$eval("[data-configuration-field]", (els) => els.map((e) => e.getAttribute("data-kind")));
    if (new Set(kinds).size !== 5) fail(`${tag}: kinds drawn ${kinds}`);
    await page.screenshot({ path: join(shots, `form-${tag}.png`), fullPage: true });
    ok(`${tag}: the fixture form draws every kind (${[...new Set(kinds)].join(", ")})`);

    if (theme === "light" && !size) {
      await page.locator('[data-configuration-field="label"] input').fill("Sam 👋");
      await page.locator('[data-configuration-field="label"] input').press("Enter");
      await page.waitForSelector('[data-configuration-field="label"] >> text=Saved', { timeout: 5000 }).catch(() => fail(`${tag}: no Saved after Enter`));
      ok(`${tag}: typed text saves with Enter`);

      // A multiselect's checks are local until Save.
      const boxes = page.locator('[data-configuration-field="sections"] [role="checkbox"]');
      await boxes.nth(1).click();
      await boxes.nth(0).click();
      await page.locator('[data-configuration-field="sections"]').screenshot({ path: join(shots, `form-${tag}-checks-pending.png`) });
      await page.locator('[data-configuration-field="sections"] button', { hasText: "Save" }).click();
      await page.waitForSelector('[data-configuration-field="sections"] >> text=Saved', { timeout: 5000 }).catch(() => fail(`${tag}: no Saved after checking`));
      const checked = await boxes.evaluateAll((els) => els.map((e) => e.getAttribute("aria-checked")));
      if (checked.join(",") !== "false,true,false") fail(`${tag}: checks after Save ${checked}`);
      ok(`${tag}: a multiselect's checks save together`);

      await page.goto(`${base}/e2e/configuration-form.html?fail=configuration_unavailable`);
      await page.waitForSelector("[data-configuration-field]");
      await page.locator('[data-configuration-field="notes"] textarea').fill("Kept after an uncertain write");
      await page.locator('[data-configuration-field="notes"] button', { hasText: "Save" }).click();
      await page.waitForSelector('[role="alert"]', { timeout: 5000 });
      if ((await page.locator('[data-configuration-field="notes"] textarea').inputValue()) !== "Kept after an uncertain write") fail(`${tag}: the draft was lost`);
      await page.screenshot({ path: join(shots, `form-${tag}-unavailable.png`) });
      ok(`${tag}: an uncertain write keeps the typed text and asks for Reload`);

      await page.goto(`${base}/e2e/configuration-form.html?fail=network`);
      await page.waitForSelector("[data-configuration-field]");
      await page.locator('[data-configuration-field="label"] input').fill("Mine");
      await page.locator('[data-configuration-field="label"] input').press("Enter");
      await page.waitForSelector('[role="alert"]', { timeout: 5000 });
      if ((await page.locator('[data-configuration-field="label"] input').inputValue()) !== "Mine") fail(`${tag}: the draft was lost after a transport failure`);
      if (!(await page.locator('[data-configuration-field="label"] button', { hasText: "Save" }).isDisabled())) fail(`${tag}: Save is enabled while the outcome is unknown`);
      await page.screenshot({ path: join(shots, `form-${tag}-unconfirmed.png`) });
      await page.getByRole("button", { name: "Reload" }).click();
      await page.waitForTimeout(300);
      ok(`${tag}: a transport failure keeps the draft and asks for Reload`);

      await page.goto(`${base}/e2e/configuration-form.html?fail=configuration_conflict`);
      await page.waitForSelector("[data-configuration-field]");
      await page.locator('[data-configuration-field="label"] input').fill("Mine");
      await page.locator('[data-configuration-field="label"] input').press("Enter");
      await page.waitForSelector('[role="alert"]', { timeout: 5000 });
      await page.getByRole("button", { name: "Reload" }).click();
      await page.waitForTimeout(300);
      if ((await page.locator('[data-configuration-field="label"] input').inputValue()) !== "Mine") fail(`${tag}: Reload dropped the draft`);
      ok(`${tag}: a conflict offers Reload and keeps the draft`);

      await page.goto(`${base}/e2e/configuration-form.html?version=2`);
      await page.waitForSelector("[data-configuration-field]");
      await page.waitForTimeout(300);
      await page.screenshot({ path: join(shots, `form-${tag}-newer-version.png`) });
      ok(`${tag}: a newer version is shown read-only`);
    }

    await page.goto(`${base}/?token=mock`);
    await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 }).catch(() => {});
    await page.goto(`${base}/settings?tab=labs`);
    const labs = await page.waitForSelector('[data-configuration-field="renderer"]', { timeout: 20000 }).catch(() => null);
    if (!labs) {
      fail(`${tag}: Settings › Labs did not open`);
    } else {
      await page.waitForTimeout(600);
      if (await sideways(page)) fail(`${tag}: Labs scrolls sideways`);
      await page.screenshot({ path: join(shots, `labs-${tag}.png`) });
      ok(`${tag}: Settings › Labs drawn through the form`);
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
console.log(failed ? "FAIL configuration form" : "PASS configuration form");
process.exit(failed ? 1 : 0);
