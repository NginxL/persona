// Capture an ended turn with a real background command, and the background-waiting control.
// Start the mock with MOCK_BG_WAITING=1 and Vite with PERSONA_HUB_URL, then run:
//   node web/e2e/background-running.mjs http://127.0.0.1:5261 --phase before --shots <directory>
//   node web/e2e/background-running.mjs http://127.0.0.1:5261 --phase after --shots <directory>
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => (args.includes(name) ? args.splice(args.indexOf(name), 2)[1] : undefined);
const phase = flag("--phase");
const shots = flag("--shots");
const executable = flag("--executable");
const base = (args[0] ?? "").replace(/\/$/, "");
if (!base || !["before", "after"].includes(phase)) {
  console.log("usage: node web/e2e/background-running.mjs <web URL> --phase before|after [--shots <directory>] [--executable <chromium>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

const RELEASE = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000008";
const WAITING = "/s/studio/a1f3c2d4-0000-4000-8000-000000000003";
const SIZES = { desktop: { width: 1280, height: 820 }, phone: { width: 390, height: 844 } };
const LABELS = { en: "Background task", zh: "后台任务" }; // i18n-ignore: localized text asserted against the web catalogs
const runs = [];
for (const locale of ["en-US", "zh-CN"]) {
  for (const size of Object.keys(SIZES)) {
    for (const theme of ["light", "dark"]) runs.push({ locale, size, theme });
  }
}

function fail(message) {
  throw new Error(message);
}

async function openChat(context, path, theme) {
  const page = await context.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForSelector("main textarea", { timeout: 15000 });
  await page.evaluate((value) => localStorage.setItem("persona.theme", value), theme);
  await page.reload();
  await page.waitForSelector("main textarea", { timeout: 15000 });
  await page.goto(`${base}${path}`);
  await page.waitForSelector("main textarea", { timeout: 15000 });
  await page.locator('[data-running-status="on"]').first().waitFor({ timeout: 15000 });
  return page;
}

async function shot(page, name) {
  if (!shots) return;
  await page.waitForTimeout(500);
  await page.screenshot({ path: join(shots, `${name}.png`) });
}

async function noHorizontalOverflow(page, tag) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  if (overflow) fail(`${tag}: horizontal page overflow`);
}

async function captureRelease(browser, { locale, size, theme }) {
  const lang = locale.startsWith("en") ? "en" : "zh";
  const tag = `release-${size}-${theme}-${lang}`;
  const context = await browser.newContext({ viewport: SIZES[size], deviceScaleFactor: 2, hasTouch: size === "phone", colorScheme: theme, locale });
  try {
    const page = await openChat(context, RELEASE, theme);
    const bar = page.locator("[data-running-status]").first();
    const live = page.locator('main [data-work-group="live"]');
    if (phase === "before") {
      await live.first().waitFor({ timeout: 10000 });
      if (await bar.getAttribute("data-running-background")) fail(`${tag}: old turn unexpectedly has a background kind`);
      await bar.locator("[data-running-elapsed]").waitFor();
    } else {
      await page.waitForFunction(() => !document.querySelector('main [data-work-group="live"]'));
      await page.locator('main [data-work-group="done"]').first().waitFor();
      await page.waitForSelector('[data-running-background="command"]');
      const words = (await bar.locator("[data-running-verb]").textContent()) ?? "";
      if (!words.includes(LABELS[lang])) fail(`${tag}: missing ${LABELS[lang]} in ${words}`);
      if (!words.includes("打包发布产物")) fail(`${tag}: missing the command description in ${words}`); // i18n-ignore: mock fixture text
      const separator = bar.locator("[data-running-command-separator]");
      if (await separator.evaluate((element) => getComputedStyle(element).whiteSpace) !== "pre") fail(`${tag}: the description separator loses its leading space`);
      await bar.locator("[data-running-elapsed]").waitFor();
      if (await bar.locator("[data-running-tokens], [data-running-speed]").count()) fail(`${tag}: turn metrics leaked into the background line`);
    }
    const transcript = (await page.locator("main").textContent()) ?? "";
    if (!transcript.includes("发布说明整理好了")) fail(`${tag}: the finished exchange is not visible`); // i18n-ignore: mock fixture text
    await noHorizontalOverflow(page, tag);
    await shot(page, tag);
    console.log(`ok ${phase} ${tag}`);
  } finally {
    await context.close();
  }
}

async function captureWaiting(browser) {
  const tag = "waiting-desktop-light-en";
  const context = await browser.newContext({ viewport: SIZES.desktop, deviceScaleFactor: 2, colorScheme: "light", locale: "en-US" });
  try {
    const page = await openChat(context, WAITING, "light");
    await page.waitForSelector('[data-running-background="waiting"]');
    if (await page.locator('main [data-work-group="live"]').count()) fail(`${tag}: waiting turn became live`);
    const words = (await page.locator("[data-running-verb]").textContent()) ?? "";
    if (!words.includes("等 PR #7 的检查跑完")) fail(`${tag}: wait wording changed: ${words}`); // i18n-ignore: mock fixture text
    await noHorizontalOverflow(page, tag);
    await shot(page, tag);
    console.log(`ok ${phase} ${tag}`);
  } finally {
    await context.close();
  }
}

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
try {
  for (const run of runs) await captureRelease(browser, run);
  await captureWaiting(browser);
} finally {
  await browser.close();
}
console.log(`PASS ${phase}: 8 background-command captures and 1 background-waiting capture`);
