// End-to-end check of the main chat's profile for new conversations on the web (#4633, docs/assistant/model-config.md
// "Main chat's profile for new conversations"): Model settings › Main chat offers "New chats' Claude profile" next to
// Runtime, Persona's own first and chosen; choosing this computer's and saving sends config_dir "legacy", which the
// editor shows chosen when opened again; Persona's own goes back to none.
//
// Needs the web page against the fake hub with the model defaults, and Chrome (or any Chromium through --executable):
//
//   MOCK_MODEL_DEFAULTS=1 PORT=18983 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18983 npx vite --port 5983 --strictPort &)
//   node web/e2e/main-chat-profile.mjs http://127.0.0.1:5983 [--shots <dir>] [--executable <chromium>]
//
// Runs wide (1280) in light and dark in English, and once each in 简体中文 and 日本語 for the strings; --shots saves the
// editor in each. Prints one line per check and a verdict; exits 1 on a failure and after three minutes.
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
if (!base) {
  console.error("usage: node web/e2e/main-chat-profile.mjs <web URL> [--shots <dir>] [--executable <chromium>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 3 minutes");
  process.exit(1);
}, 180_000).unref();

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}
const check = (cond, msg) => {
  if (!cond) fail(msg);
};

const hub = (path, init) => fetch(`${base}/api/v1${path}`, { ...init, headers: { Authorization: "Bearer mock", "Content-Type": "application/json" }, signal: AbortSignal.timeout(5000) }).then((r) => r.json());
const mainPurpose = async () => (await hub("/model-config/purposes/assistant")).purpose;
if (!(await mainPurpose())?.config_dirs) fail("the fake hub offers no profile choice (start it with MOCK_MODEL_DEFAULTS=1)");

const WORDS = {
  en: { label: "New chats' Claude profile", own: "Persona's own", computer: "This computer's", save: "Save" },
  "zh-Hans": { label: "新对话的 Claude 配置", own: "Persona 专用", computer: "这台电脑的", save: "保存" },
  ja: { label: "新しい会話の Claude プロファイル", own: "Persona 専用", computer: "このコンピューターの", save: "保存" },
};

async function open(browser, theme, lang) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: "reduce" });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  await page.evaluate(([t, l]) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", l);
  }, [theme, lang]);
  return { ctx, page };
}
async function openEditor(page) {
  await page.goto(`${base}/settings?tab=model-config`);
  await page.waitForSelector('[data-model-purpose="assistant"]', { timeout: 15000 });
  await page.locator('[data-model-purpose="assistant"] button').first().click();
  await page.waitForSelector('[role="dialog"]', { timeout: 10000 });
  await page.waitForTimeout(600); // the launcher's model list loads
}
const choiceOf = (page, w) => page.locator(`[role="dialog"] [role="radiogroup"][aria-label="${w.label}"]`);
const shot = async (page, name, w) => {
  if (!shots) return;
  await choiceOf(page, w).scrollIntoViewIfNeeded(); // the editor scrolls: the choice is under Runtime
  await page.waitForTimeout(350); // fonts and the segment's thumb settle
  await page.locator('[role="dialog"]').screenshot({ path: join(shots, `${name}.png`) });
};

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
try {
  for (const [theme, lang] of [["light", "en"], ["dark", "en"], ["light", "zh-Hans"], ["light", "ja"]]) {
    const w = WORDS[lang];
    const where = `${theme} ${lang}`;
    const { ctx, page } = await open(browser, theme, lang);
    await openEditor(page);
    const choice = choiceOf(page, w);
    check(await choice.count() === 1, `${where}: no "${w.label}" in the Main chat editor`);
    const radios = choice.locator('[role="radio"]');
    check(JSON.stringify(await radios.allTextContents()) === JSON.stringify([w.own, w.computer]), `${where}: choices ${await radios.allTextContents()}`);
    check(await radios.nth(0).getAttribute("aria-checked") === "true", `${where}: Persona's own is not the one chosen`);
    const labels = await page.locator('[role="dialog"] span').allTextContents();
    check(labels.indexOf(w.label) > labels.findIndex((s) => /Runtime|运行方式|実行方式/.test(s)), `${where}: the choice is not after Runtime`);
    ok(`${where}: Main chat offers ${w.own} (chosen) and ${w.computer}, after Runtime`);
    await shot(page, `editor-${theme}-${lang}`, w);

    if (theme === "light" && lang === "en") {
      await radios.nth(1).click();
      await page.locator('[role="dialog"] button', { hasText: w.save }).last().click();
      await page.waitForSelector('[role="dialog"]', { state: "detached", timeout: 10000 });
      const saved = await mainPurpose();
      check(saved.override?.config_dir === "legacy", `saved ${JSON.stringify(saved.override)}`);
      await openEditor(page);
      check(await choiceOf(page, w).locator('[aria-checked="true"]').textContent() === w.computer, "reopened: this computer's is not chosen");
      ok(`${where}: choosing ${w.computer} saves config_dir "legacy" and shows it chosen when opened again`);
      await shot(page, `editor-${theme}-${lang}-legacy`, w);
      await choiceOf(page, w).locator('[role="radio"]').nth(0).click();
      await page.locator('[role="dialog"] button', { hasText: w.save }).last().click();
      await page.waitForSelector('[role="dialog"]', { state: "detached", timeout: 10000 });
      const back = await mainPurpose();
      check(!back.override?.config_dir, `back to Persona's own: ${JSON.stringify(back.override)}`);
      ok(`${where}: choosing ${w.own} again stores none`);
    }
    await ctx.close();
  }
  console.log("PASS main chat profile");
} finally {
  await browser.close();
}
