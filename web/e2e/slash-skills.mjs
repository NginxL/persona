// Synthetic #6171 acceptance: / palette skills in Claude Code, Codex and Persona chats.
// Start scripts/mock/skill_mentions.py and Vite (or vite preview) on free loopback ports, then:
// node web/e2e/slash-skills.mjs <web URL> --hub <mock URL> --out <capture directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const web = args[0]?.replace(/\/$/, "");
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const hub = flag("--hub")?.replace(/\/$/, "");
const out = flag("--out");
assert(web && hub && out, "usage: node web/e2e/slash-skills.mjs <web URL> --hub <mock URL> --out <directory>");
await mkdir(out, { recursive: true });
const executable = flag("--chromium");
const browser = await chromium.launch({ ...(executable ? { executablePath: executable } : { channel: "chrome" }), headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });

const CHATS = [
  { name: "claude", path: "/s/macbook/c1111111-0000-4000-8000-000000000001", catalog: "/sessions/macbook/c1111111-0000-4000-8000-000000000001/skills" },
  { name: "codex", path: "/s/macbook/d2222222-0000-4000-8000-000000000002", catalog: "/sessions/macbook/d2222222-0000-4000-8000-000000000002/skills" },
  { name: "persona", path: "/persona/b2e4d6f8-0000-4000-8000-000000000001", catalog: "/assistant/skills" },
];
const state = async (value) => {
  const response = await fetch(`${hub}/mock/skills?state=${value}`);
  assert.equal(response.status, 200);
};
const sent = async () => (await (await fetch(`${hub}/mock/skill-sends`)).json()).sends;
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function waitFor(run, label, timeout = 12000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try {
      const value = await run();
      if (value) return value;
    } catch (error) { last = error; }
    await sleep(60);
  }
  throw new Error(`Timed out: ${label}${last ? ` (${last.message})` : ""}`);
}
async function initialize(context) {
  await context.addInitScript(() => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.language", "en");
  });
}
async function openChat(page, route) {
  await page.goto(`${web}${route}`, { waitUntil: "domcontentloaded" });
  await page.locator("textarea").waitFor({ state: "visible" });
  await waitFor(() => page.locator("textarea").evaluate((el) => !el.disabled), "composer enabled");
}
const options = (page) => page.locator("[data-slash-palette] [role=option]");
const skill = (page, text) => options(page).filter({ hasText: text });
async function openSlash(page, query = "/") {
  await page.locator("textarea").fill(query);
  await page.locator("[data-slash-palette]").waitFor({ state: "visible" });
}
async function shot(page, name) {
  await sleep(200);
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false });
}
async function theme(page, dark) {
  await page.evaluate((value) => document.documentElement.classList.toggle("dark", value), dark);
}
async function pickAndSend(page, chat, query, description, reference, keyboard, message) {
  await openSlash(page, query);
  const row = skill(page, description);
  await row.waitFor({ state: "visible" });
  if (keyboard) await page.locator("textarea").press("Enter");
  else await row.click();
  await waitFor(() => page.locator("textarea").inputValue().then((value) => value === ""), "slash token removed");
  const expectedName = reference === "fixture-design" ? "interface-design" : "review";
  await waitFor(async () => (await page.locator("body").innerText()).includes(expectedName), "skill chip visible");
  assert(await page.locator("textarea").inputValue() === "");
  const before = (await sent()).length;
  await page.locator("textarea").fill(message);
  await page.locator('button[aria-label="Send"]').click();
  await waitFor(async () => (await sent()).length > before, `${chat} send received`);
  const body = (await sent()).at(-1).body;
  assert(body.text.includes(`kind="skill"`), `${chat} carries a skill context`);
  assert(body.text.includes(`ref="${reference}"`), `${chat} carries exact ${reference}`);
  assert(body.text.endsWith(`\n\n${message}`), `${chat} keeps ordinary message text`);
  assert(!body.text.includes(`@${reference}`) && !body.text.endsWith(`/${reference}`), `${chat} does not type an invocation`);
}

try {
  const context = await browser.newContext({ viewport: { width: 1365, height: 900 }, locale: "en-US", reducedMotion: "reduce" });
  await initialize(context);
  for (const chat of CHATS) {
    await state("ready");
    const page = await context.newPage();
    const catalogs = [];
    page.on("request", (request) => {
      if (/\/assistant\/skills|\/sessions\/[^/]+\/[^/]+\/skills/.test(new URL(request.url()).pathname)) catalogs.push({ url: request.url(), manager: request.headers()["x-persona-manager"] });
    });
    await openChat(page, chat.path);
    assert.equal(catalogs.length, 0, `${chat.name}: no page-load catalog request`);
    await openSlash(page);
    await skill(page, "Inspect security boundaries").waitFor({ state: "visible" });
    await skill(page, "Refine accessible, responsive interfaces").waitFor({ state: "visible" });
    const texts = await options(page).allTextContents();
    assert(texts.length >= 4, `${chat.name}: commands and skills together`);
    assert(texts[texts.length - 1].includes("interface-design"), `${chat.name}: skills follow commands`);
    assert.equal(catalogs.length, 1, `${chat.name}: one catalog request per opening`);
    await theme(page, false);
    await shot(page, `${chat.name}-wide-light-commands-skills`);
    await theme(page, true);
    await shot(page, `${chat.name}-wide-dark-commands-skills`);
    await openSlash(page, "/interface");
    await waitFor(async () => (await options(page).count()) === 1, `${chat.name}: skill-only filter`);
    assert((await options(page).allTextContents())[0].includes("interface-design"));
    await shot(page, `${chat.name}-wide-dark-skill-only`);
    await pickAndSend(page, chat.name, "/interface", "Refine accessible, responsive interfaces", "fixture-design", false, `${chat.name} pointer pick`);
    await pickAndSend(page, chat.name, "/security", "Inspect security boundaries", "fixture-project", true, `${chat.name} keyboard pick`);
    assert.equal(catalogs.length, 2, `${chat.name}: one read for each of the two openings, none per keystroke`);
    for (const record of catalogs) {
      const url = new URL(record.url);
      assert(url.pathname.endsWith(chat.catalog), `${chat.name}: own catalog route, got ${url.pathname}`);
      assert.equal(record.manager, "assistant", `${chat.name}: catalog manager`);
      if (chat.name === "persona") {
        assert.equal(url.searchParams.get("machine_id"), "server", "Persona: catalog machine");
        assert.equal(url.searchParams.get("session_id"), "b2e4d6f8-0000-4000-8000-000000000001", "Persona: catalog session");
      } else assert.equal(url.search, "", `${chat.name}: session environment comes from the route, not client overrides`);
    }
    await page.close();
  }

  // The Persona starter has no built-in command rows: a skill still opens the palette.
  const starter = await context.newPage();
  await state("ready");
  await openChat(starter, "/persona/new");
  await openSlash(starter, "/interface");
  await skill(starter, "Refine accessible, responsive interfaces").waitFor({ state: "visible" });
  assert.equal(await options(starter).count(), 1);
  await shot(starter, "persona-new-wide-light-skill-only");
  await starter.close();

  // Commands and the active row survive a delayed answer, then a failed catalog.
  const loading = await context.newPage();
  await openChat(loading, CHATS[0].path);
  await state("ready");
  const skillPath = /\/skills(?:\?|$)/;
  let heldCatalog;
  const holdCatalog = (route) => { heldCatalog = route; };
  await loading.route(skillPath, holdCatalog);
  await openSlash(loading);
  const commandRows = await options(loading).count();
  assert(commandRows > 0, "loading: commands render immediately");
  await waitFor(async () => (await loading.locator("body").innerText()).includes("Loading skills"), "catalog loading status");
  await waitFor(() => heldCatalog, "catalog response held by the browser driver");
  await loading.locator("textarea").press("ArrowDown");
  const activeBefore = await loading.locator('[data-slash-palette] [aria-selected="true"]').count();
  const selectedBefore = await loading.locator('[data-slash-palette] [aria-selected="true"]').textContent();
  await theme(loading, false);
  assert.equal(await skill(loading, "Inspect security boundaries").count(), 0, "loading capture: skills are still absent");
  await shot(loading, "claude-wide-light-loading-commands");
  assert.equal(await skill(loading, "Inspect security boundaries").count(), 0, "loading capture completed before the response");
  const list = loading.locator("[data-slash-palette] [role=listbox]");
  await list.evaluate((el) => { el.scrollTop = el.scrollHeight; });
  const scrolled = await list.evaluate((el) => el.scrollTop);
  assert(scrolled > 0, "loading: the command list was manually scrolled");
  await heldCatalog.continue();
  await loading.unroute(skillPath, holdCatalog);
  await skill(loading, "Inspect security boundaries").waitFor({ state: "visible" });
  assert.equal(await list.evaluate((el) => el.scrollTop), scrolled, "catalog arrival preserves manual scrolling");
  assert.equal(await loading.locator('[data-slash-palette] [aria-selected="true"]').textContent(), selectedBefore, "catalog arrival does not move the active row");
  assert.equal(activeBefore, 1, "one row is active before catalog arrival");
  await theme(loading, true);
  await shot(loading, "claude-wide-dark-loaded-stable-active");
  await state("partial");
  await loading.locator("textarea").press("Escape");
  await openSlash(loading, "/security");
  await skill(loading, "Inspect security boundaries").waitFor({ state: "visible" });
  await loading.getByText("Verified local skills only", { exact: false }).waitFor();
  await shot(loading, "claude-wide-dark-partial");
  await state("error");
  await loading.locator("textarea").press("Escape");
  await openSlash(loading);
  await waitFor(async () => (await loading.locator("body").innerText()).includes("Skills unavailable here"), "catalog failure status");
  assert.equal(await options(loading).count(), commandRows, "failure: commands remain and no skills are listed");
  await theme(loading, false);
  await shot(loading, "claude-wide-light-unavailable-commands");
  await theme(loading, true);
  await shot(loading, "claude-wide-dark-unavailable-commands");
  await loading.close();

  // Coarse pointer: every command and skill row is at least 44 px, including large text.
  const touchContext = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 1, isMobile: true, hasTouch: true, locale: "en-US", reducedMotion: "reduce" });
  await initialize(touchContext);
  const touch = await touchContext.newPage();
  await state("ready");
  await openChat(touch, CHATS[0].path);
  await openSlash(touch);
  await skill(touch, "Inspect security boundaries").waitFor({ state: "visible" });
  await touch.evaluate(() => document.documentElement.setAttribute("data-text-size", "large"));
  // pop-in starts at scale(.95): measure the settled rows, not the entrance's transient 41.8 px rectangles.
  await waitFor(() => touch.locator("[data-slash-palette]").evaluate((el) => el.getAnimations({ subtree: true }).every((animation) => animation.playState === "finished")), "palette entrance settled");
  for (const dark of [false, true]) {
    await theme(touch, dark);
    const heights = await options(touch).evaluateAll((rows) => rows.map((row) => row.getBoundingClientRect().height));
    assert(heights.length >= 4 && heights.every((height) => height >= 44), `touch rows 44px: ${heights.join(",")}`);
    assert(await touch.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "narrow palette has no horizontal page overflow");
    await shot(touch, `claude-narrow-${dark ? "dark" : "light"}-large-commands-skills`);
  }
  await openSlash(touch, "/interface");
  await skill(touch, "Refine accessible, responsive interfaces").waitFor({ state: "visible" });
  await shot(touch, "claude-narrow-dark-large-skill-only");
  await theme(touch, false);
  await shot(touch, "claude-narrow-light-large-skill-only");
  await theme(touch, true);
  await skill(touch, "Refine accessible, responsive interfaces").tap();
  assert.equal(await touch.locator("textarea").inputValue(), "", "touch pick removes slash token");
  await shot(touch, "claude-narrow-dark-skill-chip");
  await touchContext.close();
  await context.close();
  console.log(`PASS slash skills: ${CHATS.length} chat types, pointer/keyboard/touch picks, exact hub references, one on-open catalog read, skill-only filtering, Persona starter, loading/failure/partial, light/dark wide/narrow 44px rows. Captures: ${out}`);
} finally {
  await browser.close();
}
