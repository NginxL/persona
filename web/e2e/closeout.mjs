// End-to-end check of closeouts, orphaned tasks and their settings (#3612, #3618, #3617; docs/web/web.md "What the session
// is waiting for"): a stopped agent's page opens with its closeout as a card above the transcript (each claim confirmed,
// contradicted or not checked), the reply shows without the raw block, list rows end with where it stands on merged and
// deployed, Settings › Session triage has "Ask for a closeout", Settings › Assistant has the agent follow-up settings
// (#4293 added "Tell supervisors about new messages"), and an open to-do nobody works on says so on its row
// and its task card.
//
// Needs the web page against the fake hub, and Chrome (or any Chromium through --executable):
//
//   MOCK_CLOSEOUT=1 MOCK_ORPHANED=1 MOCK_TASKS=1 PORT=18931 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18931 npx vite --port 51931 --strictPort &)
//   node web/e2e/closeout.mjs http://127.0.0.1:51931 [--shots <dir>] [--executable <chromium>]
//
// Runs wide (1280) and phone width (390), light and dark, in English; the three command states also run with the app's
// largest text in every theme and width, and the card runs in Chinese and Japanese. --shots saves each state. Prints one
// line per check and a verdict; exits 1 on a failure and after four minutes.
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
  console.error("usage: node web/e2e/closeout.mjs <web URL> [--shots <dir>] [--executable <chromium>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 4 minutes");
  process.exit(1);
}, 240_000).unref();

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}
const hub = (path, init) => fetch(`${base}/api/v1${path}`, { ...init, headers: { Authorization: "Bearer mock", "Content-Type": "application/json" }, signal: AbortSignal.timeout(5000) }).then((r) => r.json());

const MACHINE = "server";
const DONE = "a9c00000-0000-4000-8000-000000000001";
const DISPROVED = "a9c00000-0000-4000-8000-000000000002";
const BLOCKED = "a9c00000-0000-4000-8000-000000000003";
const NOT_DONE = "a9c00000-0000-4000-8000-000000000004";

const sessions = (await hub("/sessions")).sessions ?? [];
if (!sessions.some((s) => s.session_id === DONE && s.work?.closeout)) fail("the fake hub has no closeout sessions (start it with MOCK_CLOSEOUT=1)");

async function open(browser, theme, width, language = "en", large = false) {
  const ctx = await browser.newContext({ viewport: { width, height: width > 600 ? 820 : 844 }, deviceScaleFactor: 2, colorScheme: theme, locale: "en-US", hasTouch: width <= 600 });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
  await page.evaluate(([t, l, largeText]) => {
    localStorage.setItem("persona.theme", t);
    localStorage.setItem("persona.language", l);
    if (largeText) localStorage.setItem("persona.text-size", "large");
  }, [theme, language, large]);
  return { ctx, page };
}
const shot = async (page, name) => {
  if (shots) await page.screenshot({ path: join(shots, `web-closeout-${name}.png`) });
};

/** Opens a session's page and returns its closeout card, once the transcript's last words are there. */
async function card(page, sid) {
  await page.goto(`${base}/s/${MACHINE}/${sid}`);
  const el = page.locator("[data-closeout]").first();
  await el.waitFor({ state: "visible", timeout: 15000 }).catch(() => fail(`${sid}: no closeout card`));
  await page.locator("[data-segment]").first().waitFor({ state: "attached", timeout: 15000 }).catch(() => fail(`${sid}: no transcript`));
  await page.waitForTimeout(400);
  return el;
}
const claim = (el, what) => el.locator(`[data-closeout-claim="${what}"]`).getAttribute("data-state", { timeout: 2000 }).catch(() => null);
async function commandEvidence(el, state, sid) {
  const line = el.locator(`[data-closeout-command-evidence="${state}"]`);
  await line.waitFor({ state: "visible", timeout: 15000 }).catch(() => fail(`${sid}: no ${state} command evidence`));
  return line;
}
const transcript = (page) => page.locator("[data-chat-content]").first().innerText();

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
try {
  for (const theme of ["light", "dark"]) {
    for (const width of [1280, 390]) {
      const where = `${theme} ${width}px`;
      const size = width > 600 ? "wide" : "narrow";
      const { ctx, page } = await open(browser, theme, width);

      let el = await card(page, DONE);
      if ((await el.getAttribute("data-closeout")) !== "done") fail(`${where}: the done card is not marked done`);
      if ((await claim(el, "merged")) !== "confirmed" || (await claim(el, "deployed")) !== "confirmed") fail(`${where}: done: merged and deployed are not both confirmed`);
      if (await el.getAttribute("data-closeout-contradicted")) fail(`${where}: done: marked contradicted`);
      const passed = await commandEvidence(el, "exit", DONE);
      const passedText = await passed.innerText();
      for (const want of ["go test ./export/...", "Exit code 0", "is_error: false"]) if (!passedText.includes(want)) fail(`${where}: done command evidence lacks "${want}": ${passedText}`);
      const doneText = await el.innerText();
      for (const want of ["The agent's own report", "What changed", "PR #31", "4f2a91c", "Hub confirmed", "Verified"]) if (!doneText.includes(want)) fail(`${where}: done card lacks "${want}": ${doneText}`);
      if ((await el.locator('a[href*="/pull/31"]').count()) !== 1) fail(`${where}: the pull request is not a link`);
      if ((await el.locator("a").count()) !== 1) fail(`${where}: a commit hash became a link`);
      let words = await transcript(page);
      if (words.includes("<closeout") || words.includes("</closeout>") || words.includes("did:") || words.includes("Persona 工作报告") || words.includes("Status: done")) fail(`${where}: done: the raw block is still in the reply`);
      if (!words.includes("94 秒降到 11 秒")) fail(`${where}: done: the reply's own words went with the block`);
      const box = await el.boundingBox();
      const scroll = await page.locator("[data-chat-scroll]").first().boundingBox();
      if (!box || !scroll || box.y + box.height > scroll.y + 1) fail(`${where}: the card is not above the transcript`);
      if (await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)) fail(`${where}: the page scrolls sideways`);
      ok(`${where}: done closeout: card above the transcript, both claims confirmed, PR a link, raw block hidden`);
      await shot(page, `done-${size}-${theme}`);

      el = await card(page, DISPROVED);
      if ((await claim(el, "merged")) !== "contradicted") fail(`${where}: a merged claim the hub disproved is not contradicted`);
      if ((await claim(el, "deployed")) !== "unchecked") fail(`${where}: an unchecked deployed claim is not "unchecked"`);
      if (!(await el.getAttribute("data-closeout-contradicted"))) fail(`${where}: the card of a disproved claim is not marked`);
      const failedRun = await commandEvidence(el, "exit", DISPROVED);
      const failedText = await failedRun.innerText();
      for (const want of ["cd app; npm test", "Exit code 1", "is_error: true"]) if (!failedText.includes(want)) fail(`${where}: failed command evidence lacks "${want}": ${failedText}`);
      const found = el.locator('[data-closeout-claim="merged"] span').filter({ hasText: "Hub found it isn't merged" }).last();
      if (!(await found.count())) fail(`${where}: the contradicted claim does not say what the hub found`);
      const tone = await found.evaluate((n) => [getComputedStyle(n).color, getComputedStyle(n).backgroundColor, getComputedStyle(document.documentElement).getPropertyValue("--orange").trim()]);
      const quiet = await el.locator('[data-closeout-claim="deployed"] span').last().evaluate((n) => getComputedStyle(n).color);
      if (tone[0] === quiet || tone[1] === "rgba(0, 0, 0, 0)") fail(`${where}: the contradicted claim does not stand out (${tone.join(" / ")} vs ${quiet})`);
      if ((await found.locator("svg").count()) !== 1) fail(`${where}: the contradicted claim has no warning mark (color alone)`);
      ok(`${where}: contradicted "merged": caution color on the caution tint with a warning mark; "deployed" not checked`);
      await shot(page, `contradicted-${size}-${theme}`);

      el = await card(page, BLOCKED);
      if ((await el.getAttribute("data-closeout")) !== "blocked") fail(`${where}: the blocked card is not marked blocked`);
      const missing = await commandEvidence(el, "not_found", BLOCKED);
      const missingText = await missing.innerText();
      for (const want of ["./scripts/migrate.sh --env staging", "No matching command in this closeout's work record"]) if (!missingText.includes(want)) fail(`${where}: not-found evidence lacks "${want}": ${missingText}`);
      const blockedText = await el.innerText();
      if (!blockedText.includes("Blocked on: DBA")) fail(`${where}: the blocked card does not say what it waits for: ${blockedText}`);
      if ((await claim(el, "merged")) !== "unchecked" || (await claim(el, "deployed")) !== null) fail(`${where}: blocked: merged should be unchecked and deployed absent`);
      if ((await el.locator("a").count()) !== 0) fail(`${where}: a branch or a file path became a link`);
      if ((await transcript(page)).includes("Persona 工作报告")) fail(`${where}: the blocked report is duplicated beneath its card`);
      ok(`${where}: blocked closeout: what it waits for, a branch and a document as text, merged not checked`);
      await shot(page, `blocked-${size}-${theme}`);

      el = await card(page, NOT_DONE);
      words = await transcript(page);
      if (words.includes("closeout") || words.includes("untested")) fail(`${where}: not done: the raw block is still shown`);
      if (await page.locator("[data-segment]").last().locator("[data-latest-text]").count()) fail(`${where}: a reply that was only the block still leaves an empty slot`);
      if (!(await el.innerText()).includes("Still needs")) fail(`${where}: not done: the card does not say what is missing`);
      // the header folds the details away and brings them back
      await el.getByRole("button").first().click();
      await page.waitForTimeout(400);
      if ((await el.getByRole("button").first().getAttribute("aria-expanded")) !== "false") fail(`${where}: the card does not fold`);
      await shot(page, `not-done-folded-${size}-${theme}`);
      await el.getByRole("button").first().click();
      await page.waitForTimeout(400);
      ok(`${where}: not-done closeout: a reply that was only the block shows nothing; the card folds and unfolds`);
      await shot(page, `not-done-${size}-${theme}`);

      // list rows: the computer's page lists every agent with its line, seen or not (opening them above marked them seen)
      await page.goto(`${base}/machines/${MACHINE}`);
      await page.locator("main").getByText("修复报表导出超时").first().waitFor({ state: "visible", timeout: 15000 });
      const list = await page.locator("main").first().innerText();
      for (const want of ["merged ✓ · deployed ✓", "✗ says merged, isn't · not deployed", "not merged"]) if (!list.includes(want)) fail(`${where}: list rows lack "${want}"`);
      await page.locator("main").getByText("修复报表导出超时").first().scrollIntoViewIfNeeded();
      if (list.includes("<closeout")) fail(`${where}: a list row shows the raw block`);
      if (await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)) fail(`${where}: the list page scrolls sideways`);
      ok(`${where}: list rows end with where the closeout stands; a contradicted claim is marked; no raw block`);
      await shot(page, `list-${size}-${theme}`);

      // Settings › Session triage and Settings › Assistant
      await page.goto(`${base}/settings?tab=persona`);
      const sw = page.getByRole("switch", { name: "Ask for a closeout" });
      await sw.waitFor({ state: "visible", timeout: 15000 }).catch(() => fail(`${where}: no "Ask for a closeout" switch`));
      await sw.scrollIntoViewIfNeeded();
      await shot(page, `settings-triage-${size}-${theme}`);
      const tasks = page.getByRole("switch", { name: "Follow up every agent Persona starts" });
      await tasks.waitFor({ state: "visible", timeout: 15000 }).catch(() => fail(`${where}: no agent tasks switch`));
      await page.getByRole("switch", { name: "Tell supervisors about new messages" }).waitFor({ state: "visible", timeout: 15000 })
        .catch(() => fail(`${where}: no inbox signals switch`));
      await tasks.scrollIntoViewIfNeeded();
      await shot(page, `settings-assistant-${size}-${theme}`);
      ok(`${where}: the closeout switch and the agent follow-up settings are in Settings`);

      // an open to-do nobody works on: its row, and its task card in the main chat
      await page.goto(`${base}/todos`);
      const row = page.locator("[data-orphaned]").first();
      await row.waitFor({ state: "visible", timeout: 15000 }).catch(() => fail(`${where}: no orphaned to-do row (start the fake hub with MOCK_ORPHANED=1)`));
      await shot(page, `orphaned-rows-${size}-${theme}`);
      const rows = await page.locator("[data-orphaned]").allInnerTexts();
      // the rows on screen: the third reason (computer offline) is on a row a folded section holds, and in the unit tests
      for (const want of ["closed after an error", "· closed"]) if (!rows.some((x) => x.includes("Nobody is working on this") && x.includes(want))) fail(`${where}: no to-do row says "${want}": ${rows.join(" | ")}`);
      ok(`${where}: orphaned to-do rows say nobody is working on them, each with its reason`);
      await shot(page, `orphaned-rows-${size}-${theme}`);
      await page.goto(`${base}/overview`);
      const line = page.locator("[data-task] [data-orphaned-line]").first();
      await line.waitFor({ state: "attached", timeout: 15000 }).catch(() => fail(`${where}: no orphaned line on a task card of the Overview`));
      await line.scrollIntoViewIfNeeded();
      if (!(await line.innerText()).includes("Nobody is working on this")) fail(`${where}: the task card's line does not say it`);
      ok(`${where}: a task card on the Overview says nobody is working on it`);
      await shot(page, `orphaned-card-${size}-${theme}`);
      await ctx.close();
    }
  }

  // the switches save, and the hub's answer is what stays
  const { ctx, page } = await open(browser, "light", 1280);
  await page.goto(`${base}/settings?tab=persona`);
  const sw = page.getByRole("switch", { name: "Ask for a closeout" });
  await sw.waitFor({ state: "visible", timeout: 15000 });
  const before = (await hub("/agents/triage")).closeout === true;
  await sw.click();
  await page.waitForFunction((was) => document.querySelector('[role="switch"][aria-label="Ask for a closeout"]')?.getAttribute("aria-checked") === String(!was), before, { timeout: 5000 });
  const after = await hub("/agents/triage");
  if (after.closeout !== !before || after.enabled !== true) fail(`the closeout switch did not save, or changed another switch: ${JSON.stringify(after)}`);
  await sw.click();
  await page.waitForTimeout(400);
  ok("Ask for a closeout saves and leaves the other switches alone");
  const days = page.getByRole("spinbutton", { name: "Archive closed sessions after" });
  await days.fill("7");
  await days.press("Enter");
  await page.waitForTimeout(500);
  if ((await hub("/assistant")).archive_closed_days !== 7) fail("archive_closed_days did not save");
  await days.fill("99999");
  await days.press("Enter");
  await page.waitForTimeout(300);
  if ((await days.inputValue()) !== "7" || (await hub("/assistant")).archive_closed_days !== 7) fail("an out-of-range number of days was kept");
  await days.fill("3");
  await days.press("Enter");
  await page.waitForTimeout(400);
  const tasks = page.getByRole("switch", { name: "Follow up every agent Persona starts" });
  await tasks.click();
  await page.waitForTimeout(400);
  if ((await hub("/assistant")).agent_tasks !== true) fail("agent_tasks did not save");
  await tasks.click();
  await page.waitForTimeout(400);
  const signals = page.getByRole("switch", { name: "Tell supervisors about new messages" });
  if ((await signals.getAttribute("aria-checked")) !== "false" || (await hub("/assistant")).inbox_signals !== false) fail("inbox_signals is not off by default");
  await signals.click();
  await page.waitForTimeout(400);
  const saved = await hub("/assistant");
  if (saved.inbox_signals !== true || saved.agent_tasks !== false) fail(`inbox_signals did not save alone: ${JSON.stringify(saved)}`);
  await signals.click();
  await page.waitForTimeout(400);
  ok("the days field saves 7, refuses 99999, and the agent tasks and inbox signals switches save");
  await ctx.close();

  // Chinese and Japanese: the card's labels fit on a phone
  for (const language of ["zh-Hans", "ja"]) {
    const { ctx: c2, page: p2 } = await open(browser, "light", 390, language);
    const el = await card(p2, DISPROVED);
    if (await p2.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)) fail(`${language}: the page scrolls sideways`);
    if ((await el.innerText()).includes("chat-chatview.")) fail(`${language}: a string is missing`);
    ok(`${language}: the contradicted card fits at 390px`);
    await shot(p2, `contradicted-narrow-light-${language}`);
    await c2.close();
  }

  // The app's largest text, not a browser zoom: every evidence state stays readable in both themes and widths.
  for (const theme of ["light", "dark"]) {
    for (const width of [1280, 390]) {
      const { ctx, page } = await open(browser, theme, width, "en", true);
      const size = width > 600 ? "wide" : "narrow";
      for (const [sid, name, state] of [[DONE, "done", "exit"], [DISPROVED, "contradicted", "exit"], [BLOCKED, "blocked", "not_found"]]) {
        const el = await card(page, sid);
        if (await page.locator("html").getAttribute("data-text-size") !== "large") fail(`${theme} ${width}px: the app's largest text is not active`);
        const line = await commandEvidence(el, state, sid);
        await line.scrollIntoViewIfNeeded();
        if (await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)) fail(`${theme} ${width}px largest text: the page scrolls sideways`);
        await shot(page, `${name}-${size}-${theme}-large-text`);
        ok(`${theme} ${width}px largest text: ${name} command evidence fits`);
      }
      await ctx.close();
    }
  }
} finally {
  await browser.close();
}
console.log("PASS web closeout e2e");
process.exit(0);
