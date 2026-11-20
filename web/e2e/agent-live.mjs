// End-to-end check that an agent's chat streams its reply the way a Claude session Persona started sends it (#1309,
// docs/integrations/claude-integration.md "Live replies (MessageDisplay)"): the live text grows a batch of whole lines at
// a time, stays while the tool after it runs, is replaced by the reply's item without showing it twice (a delta that
// comes after the item included), and the words after the tool stream as a message of their own.
//
// Needs the web page against the fake hub with MOCK_AGENT_LIVE=1, and Chrome (playwright-core drives it):
//
//   MOCK_AGENT_LIVE=1 PORT=18783 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18783 npx vite --port 5183 --strictPort &)
//   node web/e2e/agent-live.mjs http://127.0.0.1:5183 --hub http://127.0.0.1:18783 [--shots <dir>]
//
// Runs wide (1280) and phone width (390), light and dark, each from the agent as the fake hub started it (step reset);
// --shots saves the reply streaming, live while the tool runs (the live work row above it), the moment its item replaces
// it, the message after the tool, and the finished exchange once the run bar has faded. The chat keeps the live text's
// end in view, also on a page opened by its address (the chat's frame changes once the session arrives). Prints one line
// per check and a verdict; exits 1 on a failure.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const hub = (flag("--hub") ?? "").replace(/\/$/, "");
const shots = flag("--shots");
if (!base || !hub) {
  console.error("usage: node web/e2e/agent-live.mjs <web URL> --hub <fake hub URL> [--shots <dir>]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });

const REVIEW = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000006"; // the PR #42 review agent (MOCK_AGENT_LIVE)
const CHAT = "[data-chat-scroll]";
const LIVE = '[data-answer="streaming"]';
const WORKING = '[data-work-group="live"]';
// the run bar stays this long after the turn ends, then fades (web/src/ui/motion.ts LINGER_MS + FADE_OUT_MS)
const BAR_GONE_MS = 1000 + 400 + 200;
const SIZES = { wide: { width: 1280, height: 820 }, narrow: { width: 390, height: 844 } };
const RUNS = [["wide", "light"], ["wide", "dark"], ["narrow", "light"], ["narrow", "dark"]];
// The mock's words (scripts/mock/mock_hub.py AGENT_LIVE_FIRST / AGENT_LIVE_SECOND).
const FIRST = ["两条命名建议都改好了：", "loadForecast", "hourlyEntries", "接下来跑一遍单元测试"];
const SECOND = ["测试全部通过", "可以再看一眼"];

const ok = (msg) => console.log(`ok  ${msg}`);
function fail(msg) {
  console.log(`FAIL ${msg}`);
  process.exit(1);
}

async function step(name, n) {
  const q = new URLSearchParams({ step: name, ...(n === undefined ? {} : { n: String(n) }) });
  const res = await fetch(`${hub}/mock/agent_live?${q}`, { signal: AbortSignal.timeout(5000) });
  const body = await res.json();
  if (!body.ok) fail(`the fake hub refused ${name}: ${JSON.stringify(body)} (is MOCK_AGENT_LIVE=1 set?)`);
}

async function until(page, what, tag, fn, arg, timeout = 8000) {
  try {
    await page.waitForFunction(fn, arg, { timeout });
  } catch {
    fail(`${tag}: ${what} (timed out after ${timeout / 1000}s)`);
  }
}

const liveText = (page) => page.evaluate((sel) => document.querySelector(sel)?.textContent ?? null, LIVE);
// how often words appear in the conversation (the sidebar and header repeat the latest line)
const times = (page, words) =>
  page.evaluate(([chat, w]) => (document.querySelector(chat)?.textContent ?? "").split(w).length - 1, [CHAT, words]);
const sideways = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
// the chat follows the end: nothing of the live text is left under the composer
const followsEnd = (page, what, tag) =>
  until(page, `${what}: the chat keeps its end in view`, tag, (chat) => {
    const s = document.querySelector(chat);
    return !!s && s.scrollHeight - s.scrollTop - s.clientHeight <= 1;
  }, CHAT, 3000);

async function shot(page, name, tag) {
  if (shots) await page.screenshot({ path: join(shots, `agent-live-${tag}-${name}.png`) });
}

// The live work row ("Working…") stays above an agent's live words: they come in batches and stay while a tool runs.
async function workingAboveWords(page, tag) {
  const where = await page.evaluate(([live, working]) => {
    const l = document.querySelector(live), w = document.querySelector(working);
    return !l || !w ? `${!!l} ${!!w}` : l.compareDocumentPosition(w) & Node.DOCUMENT_POSITION_FOLLOWING ? "under" : "above";
  }, [LIVE, WORKING]);
  if (where !== "above") fail(`${tag}: the live work row is not above the live words (${where})`);
}

async function run(browser, size, theme) {
  const tag = `${size}-${theme}`;
  await step("reset"); // each run starts from the agent as the fake hub started it
  const ctx = await browser.newContext({ viewport: SIZES[size], deviceScaleFactor: 2, hasTouch: size === "narrow", colorScheme: theme });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForSelector("main textarea", { timeout: 15000 }); // paired
  await page.evaluate((t) => localStorage.setItem("persona.theme", t), theme);
  await page.reload();
  // Open the session after pairing; the sidebar can start with its Agents section collapsed.
  await page.waitForSelector("main textarea", { timeout: 15000 });
  await page.evaluate((path) => {
    history.pushState(null, "", path);
    dispatchEvent(new PopStateEvent("popstate"));
  }, REVIEW);
  await page.waitForSelector(CHAT, { timeout: 15000 });
  await page.waitForTimeout(800); // subscribed

  // the earlier runs' exchanges are in the conversation too: each check counts what this run added
  const before = { first: await times(page, FIRST[3]), second: await times(page, SECOND[1]) };
  await step("prompt");
  await until(page, "the new message shows", tag, (w) => document.querySelector("[data-chat-scroll]")?.textContent.includes(w), "把命名的两条建议改掉");
  if (await liveText(page)) fail(`${tag}: live text before any delta`);

  await step("lines", 1);
  await until(page, "the first line streams", tag, ([sel, w]) => document.querySelector(sel)?.textContent.includes(w), [LIVE, FIRST[0]]);
  await step("lines", 3); // a blank line, then the list's first item
  await until(page, "the next batch adds its lines", tag, ([sel, w]) => document.querySelector(sel)?.textContent.includes(w), [LIVE, FIRST[1]]);
  await followsEnd(page, "streaming", tag);
  await workingAboveWords(page, tag);
  await page.waitForTimeout(400);
  await shot(page, "streaming", tag);
  ok(`${tag}: an agent's reply streams a batch of whole lines at a time`);

  await step("lines", 99); // all of them: the final batch
  await until(page, "the final batch brings the last line", tag, ([sel, w]) => document.querySelector(sel)?.textContent.includes(w), [LIVE, FIRST[3]]);
  await page.waitForTimeout(1200); // the tool runs: nothing new comes
  if (!(await liveText(page))?.includes(FIRST[3])) fail(`${tag}: the live text went while the tool ran`);
  await workingAboveWords(page, tag);
  await followsEnd(page, "the last line", tag);
  await shot(page, "while-tool", tag);
  ok(`${tag}: the words before the tool call stay live while it runs, the live work row above them`);

  await step("land");
  await until(page, "the item replaces the live text", tag, (sel) => !document.querySelector(sel), LIVE);
  await page.waitForTimeout(300);
  if ((await times(page, FIRST[3])) !== before.first + 1) fail(`${tag}: after the item landed, its words show ${await times(page, FIRST[3]) - before.first} times`);
  await followsEnd(page, "the item in place of the live text", tag);
  await shot(page, "handoff", tag);
  await step("late");
  await page.waitForTimeout(700);
  if (await liveText(page)) fail(`${tag}: a delta that came after its item was shown again`);
  if ((await times(page, FIRST[3])) !== before.first + 1) fail(`${tag}: a late delta doubled the words (${await times(page, FIRST[3]) - before.first})`);
  ok(`${tag}: the reply's item replaces the live copy, and a late delta of the same words is not shown again`);

  await step("second", 1);
  await until(page, "the message after the tool streams", tag, ([sel, w]) => document.querySelector(sel)?.textContent.includes(w), [LIVE, SECOND[0]]);
  await page.waitForTimeout(400);
  if ((await liveText(page))?.includes(FIRST[0])) fail(`${tag}: the message after the tool carries the one before`);
  await shot(page, "second", tag);
  ok(`${tag}: the words after the tool stream as a message of their own`);

  await step("done");
  await until(page, "the last item replaces the live text", tag, (sel) => !document.querySelector(sel), LIVE);
  await until(page, "the reply is in the chat", tag, ([chat, w]) => document.querySelector(chat)?.textContent.includes(w), [CHAT, SECOND[1]]);
  await page.waitForTimeout(BAR_GONE_MS);
  if (await page.$('[data-running-status]')) fail(`${tag}: the run bar is still up after the turn ended`);
  if ((await times(page, SECOND[1])) !== before.second + 1) fail(`${tag}: the last words show ${await times(page, SECOND[1]) - before.second} times`);
  if ((await sideways(page)) > 0) fail(`${tag}: the page scrolls sideways`);
  await shot(page, "done", tag);
  ok(`${tag}: the finished exchange shows each reply once`);
  await ctx.close();
}

// An agent's page opened by its address: the chat is drawn before the session arrives and again after (a terminal to
// switch to), and still keeps the live text's end in view.
async function byAddress(browser) {
  const tag = "narrow-by-address";
  await step("reset");
  const ctx = await browser.newContext({ viewport: SIZES.narrow, deviceScaleFactor: 2, hasTouch: true });
  const page = await ctx.newPage();
  await page.goto(`${base}/?token=mock`);
  await page.waitForSelector("main textarea", { timeout: 15000 });
  await page.goto(base + REVIEW);
  await page.waitForSelector(CHAT, { timeout: 15000 });
  await page.waitForTimeout(1500);
  await step("prompt");
  for (const n of [1, 3, 99]) {
    await step("lines", n);
    await page.waitForTimeout(500);
    await followsEnd(page, `${n} lines live`, tag);
  }
  await step("land");
  await step("second", 1);
  await page.waitForTimeout(500);
  await followsEnd(page, "the next message", tag);
  await step("done");
  ok(`${tag}: the chat keeps the live text's end in view`);
  await ctx.close();
}

const browser = await chromium.launch({ channel: "chrome", headless: true });
const deadline = setTimeout(() => {
  console.log("FAIL agent-live: timed out after 4 minutes");
  process.exit(1);
}, 4 * 60_000);
try {
  for (const [size, theme] of RUNS) await run(browser, size, theme);
  await byAddress(browser);
  await step("reset");
} finally {
  clearTimeout(deadline);
  await browser.close();
}
console.log("PASS agent-live");
