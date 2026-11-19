// End-to-end check that the sidebar's top-level headings are evenly spaced (#10637): Pages, Persona chats and Agents
// (and Personal Agents when it is there) each sit in one box, `sidebar-heading` (styles.css), whatever the heading
// carries beside its title (Pages a count, Persona chats New chat, Agents its counts, fold all, Filter, ⋯ and +). Before
// the fix each heading's row was as tall as its tallest control, so the title-to-title gaps differed (34.25 / 38 px at
// 1440 × 1000, folded). iOS checks the same in SidebarHeadingGapsUITests (#10561).
//
// Needs the web page against a mock hub that pages its sessions for one assistant (the paged list is drawn only then),
// and Chrome:
//
//   MOCK_SESSIONS_LIST=1 MOCK_MANAGERS=1 PORT=<port> python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:<port> npx vite --port <web port> --strictPort &)
//   node web/e2e/sidebar-heading-spacing.mjs http://127.0.0.1:<web port> [--shots <dir>] [--chromium <executable>]
//
// Both Agents lists have their own heading: the complete list (the mock's pinned space keeps the sidebar on it) and the
// paged one (the runner clears the pins, then puts them back). Each in Simplified Chinese, light and dark, at
// 1440 × 1000 (the wide sidebar, at the standard and the largest text size), 390 × 844 with a mouse and 390 × 844 on a
// touch screen (the drawer, where Pages is a grid and not a heading), with the headings folded and then unfolded. Every heading's row is measured (its content
// box, padding left out) with its title's text:
// - the rows are as tall as each other, and each title is centred as far down its row;
// - folded, title to title is the same distance down the list (three or more headings);
// - unfolded, each title stands as far below the end of the group before it.
// All within 0.5 px. Prints one line per check and a verdict; writes results.json and the screenshots to --shots;
// exits 1 on a failure. A four-minute watchdog ends the run.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const origin = args[0];
const option = (name) => (args.includes(name) ? args[args.indexOf(name) + 1] : undefined);
const shots = option("--shots");
const executable = option("--chromium");
if (!/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin ?? "")) {
  console.log("FAIL sidebar-heading-spacing: give the web page's loopback origin");
  process.exit(1);
}
if (shots) mkdirSync(shots, { recursive: true });

const TOLERANCE = 0.5;
// the top-level headings' titles in Simplified Chinese, top to bottom (Personal Agents only when the hub lists some)
const TITLES = ["页面", "Persona 对话", "个人 Agent", "Agents"];
const VARIANTS = [
  { name: "1440", viewport: { width: 1440, height: 1000 } },
  { name: "1440-large", viewport: { width: 1440, height: 1000 }, large: true },
  { name: "390", viewport: { width: 390, height: 844 } },
  { name: "390-touch", viewport: { width: 390, height: 844 }, touch: true },
];

let failed = 0;
const results = [];
const check = (ok, what, detail) => {
  results.push({ ok: Boolean(ok), what, detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
  if (!ok) failed++;
};
const near = (a, b) => Math.abs(a - b) <= TOLERANCE;
const allNear = (xs) => xs.every((x) => near(x, xs[0]));
const round = (x) => Math.round(x * 100) / 100;
const hub = async (path, init) => (await fetch(`${origin}/api/v1${path}`, { ...init, headers: { Authorization: "Bearer mock", "Content-Type": "application/json" } })).json();

/** Each top-level heading drawn in the sidebar: its row's content box, its title's text box, and the bottom of the
 *  group before it (the heading's own group holds its rows; the paged Agents list's heading stands alone). */
const measure = (page, titles) => page.evaluate((titles) => {
  const nav = [...document.querySelectorAll("[data-sidebar-rows]")].find((n) => n.getClientRects().length);
  const own = (el) => [...el.childNodes].every((c) => c.nodeType === Node.TEXT_NODE);
  const found = [];
  for (const name of titles) {
    const title = [...nav.querySelectorAll("span")].find((s) => own(s) && s.textContent.trim() === name && s.getClientRects().length);
    if (!title) continue;
    const row = title.closest("button")?.parentElement ?? title.parentElement;
    const r = row.getBoundingClientRect(), cs = getComputedStyle(row);
    const top = r.top + parseFloat(cs.borderTopWidth) + parseFloat(cs.paddingTop);
    const height = r.height - parseFloat(cs.borderTopWidth) - parseFloat(cs.borderBottomWidth) - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
    const t = title.getBoundingClientRect();
    const group = row.closest("[data-sidebar-group]") ?? row;
    found.push({ name, top, height, bottom: r.bottom, titleTop: t.top, titleMid: t.top + t.height / 2, group: group.getBoundingClientRect().bottom, box: row.classList.contains("sidebar-heading") });
  }
  return { paged: !nav.querySelector("[data-sidebar-agents-controls]"), headings: found.map((h, i) => ({ ...h, above: i ? h.titleMid - found[i - 1].group : null })) };
}, titles);

const settle = (page, ms = 700) => page.waitForTimeout(ms); // a fold's 250 ms and the observers after it

/** Folds (or opens) every top-level heading that folds: its title's button. */
const foldAll = async (page, folded) => {
  for (const name of TITLES) {
    const button = page.locator("[data-sidebar-rows] button[aria-expanded]", { has: page.locator(`span:text-is("${name}")`) }).first();
    if (!(await button.count()) || !(await button.isVisible())) continue;
    if ((await button.getAttribute("aria-expanded")) === String(folded)) await button.click();
  }
  await settle(page);
};

async function run(browser, list, variant, theme) {
  const context = await browser.newContext({
    viewport: variant.viewport, deviceScaleFactor: 2, colorScheme: theme, locale: "zh-CN", serviceWorkers: "block",
    hasTouch: Boolean(variant.touch), isMobile: Boolean(variant.touch),
  });
  await context.addInitScript(([theme, large]) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
    if (large) localStorage.setItem("persona.text-size", "large");
  }, [theme, Boolean(variant.large)]);
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === origin ? route.continue() : route.abort()));
  const page = await context.newPage();
  await page.goto(`${origin}/?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"));
  await page.waitForLoadState("networkidle");
  const narrow = variant.viewport.width < 640;
  if (narrow) {
    await page.getByRole("button", { name: /侧边栏/ }).first().click();
    await settle(page, 600); // the drawer slides in
  }
  await page.locator("[data-sidebar-rows] span", { hasText: /^Agents$/ }).first().waitFor({ timeout: 20_000 });
  const label = `${list} ${variant.name} ${theme}`;
  if (variant.touch) check(await page.evaluate(() => matchMedia("(pointer: coarse)").matches), `${label}: the touch screen has a coarse pointer`);
  if (variant.large) check(await page.locator("html").getAttribute("data-text-size") === "large", `${label}: the app's largest text is on`);
  for (const folded of [true, false]) {
    await foldAll(page, folded);
    const { paged, headings: hs } = await measure(page, TITLES);
    const state = folded ? "folded" : "unfolded";
    const shown = hs.map((h) => ({ name: h.name, height: round(h.height), titleTop: round(h.titleTop), middle: round(h.titleMid - h.top), above: h.above === null ? null : round(h.above), box: h.box }));
    results.push({ list, variant: variant.name, theme, state, headings: shown });
    const want = narrow ? 2 : 3;
    check(paged === (list === "paged"), `${label} ${state}: the ${list} Agents list is drawn`, { paged });
    check(hs.length >= want && hs.at(-1).name === "Agents", `${label} ${state}: ${want}+ top-level headings, Agents last`, hs.map((h) => h.name));
    check(allNear(hs.map((h) => h.height)), `${label} ${state}: every heading's row is as tall`, shown.map((h) => `${h.name} ${h.height}`));
    check(allNear(hs.map((h) => h.titleMid - h.top)), `${label} ${state}: every title is centred as far down its row`, shown.map((h) => `${h.name} ${h.middle}`));
    if (folded && hs.length >= 3) {
      const pitches = hs.slice(1).map((h, i) => h.titleTop - hs[i].titleTop);
      check(allNear(pitches), `${label} folded: title to title is the same distance`, pitches.map(round));
    }
    if (!folded && hs.length >= 2) {
      const above = hs.slice(1).map((h) => h.above);
      check(allNear(above) && above.every((x) => x > 0), `${label} unfolded: each title stands as far below the group before it`, above.map(round));
    }
    if (shots) {
      const column = await page.locator("[data-sidebar-rows]").filter({ visible: true }).first().boundingBox();
      // the wide sidebar's head down to a little under the Agents heading; the phone's whole drawer
      const clip = narrow ? undefined : { x: 0, y: 0, width: Math.ceil(column.x + column.width + 10), height: Math.min(variant.viewport.height, Math.ceil(hs.at(-1).bottom + (folded ? 120 : 260))) };
      await page.screenshot({ path: join(shots, `headings-${list}-${variant.name}-${theme}-${state}.png`), clip });
    }
  }
  await context.close();
}

const browser = await chromium.launch(executable ? { executablePath: executable, headless: true } : { channel: "chrome", headless: true });
const saved = await hub("/pins");
const deadline = setTimeout(async () => {
  console.log("FAIL sidebar-heading-spacing: timed out after 4 minutes");
  await hub("/pins", { method: "PUT", body: JSON.stringify(saved) }).catch(() => {});
  process.exit(1);
}, 4 * 60_000);
try {
  for (const list of ["complete", "paged"]) {
    // the complete list while something is pinned (the mock pins a space), the paged one with nothing pinned
    await hub("/pins", { method: "PUT", body: JSON.stringify(list === "paged" ? { spaces: [], sessions: [] } : saved) });
    if (list === "complete" && !saved.spaces?.length && !saved.sessions?.length) await hub("/pins", { method: "PUT", body: JSON.stringify({ spaces: ["macbook/weather"], sessions: [] }) });
    for (const variant of VARIANTS) for (const theme of ["light", "dark"]) await run(browser, list, variant, theme);
  }
} catch (e) {
  check(false, `the run stopped: ${e.message.split("\n")[0]}`);
} finally {
  clearTimeout(deadline);
  await hub("/pins", { method: "PUT", body: JSON.stringify(saved) }).catch(() => {});
  await browser.close();
}
if (shots) writeFileSync(join(shots, "results.json"), JSON.stringify(results, null, 2));
console.log(failed ? `FAIL sidebar-heading-spacing: ${failed} check(s) failed` : "PASS sidebar-heading-spacing");
process.exit(failed ? 1 : 0);
