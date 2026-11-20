// Overview's own sections arranged with the extensions' cards (#10905) in a real browser against the mock hub, in
// Simplified Chinese: with nothing arranged the sections come first in their order and the cards last (#11147); Arrange
// Overview draws each section as a bar (Needs you and Stuck without a switch, "always shown"); Tasks in progress
// dragged onto Needs you takes the top, and after Done it is right under the greeting, above Needs you; the hub
// keeps it, a reload and a second browser (another device) draw the same; Working hidden is not drawn; Reset puts
// Overview back. Light and dark screenshots at 1440 and 390 wide of Overview as it was, the edit state, and Tasks in
// progress moved first.
//
// Start the mock with MOCK_EXTENSIONS=1 MOCK_OVERVIEW_CARDS=1, and Vite (or `vite preview`) pointing at it
// (PERSONA_HUB_URL). The run sets the mock's arrangement.
// node web/e2e/overview-sections.mjs <loopback web URL> <artifact directory> [Chromium executable]
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executable] = process.argv.slice(2);
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/overview-sections.mjs <loopback web URL> <artifact directory> [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.error("RESULT: overview-sections timed out (300s)");
  process.exit(124);
}, 300_000);
const root = base.replace(/\/$/, "");
const results = [];
const check = (name, ok, detail) => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};

// the sections' headings in zh-Hans, in their default order
const SECTION_TITLES = ["需要你", "卡住了，要你去看", "进行中的任务", "工作中", "完成未看", "电脑"];
const IN_PROGRESS = "进行中的任务";

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });

async function device({ width, height, theme = "light" }) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, deviceScaleFactor: 2, serviceWorkers: "block", hasTouch: width < 640 });
  await context.addInitScript((theme) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
  }, theme);
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort()));
  const p = await context.newPage();
  await p.goto(`${root}/?token=mock`);
  await p.waitForFunction(() => localStorage.getItem("persona.token"));
  await overview(p);
  return { context, p };
}

async function overview(p) {
  await p.goto(`${root}/overview`);
  await p.waitForLoadState("networkidle");
  await p.locator("[data-extension-card]").first().waitFor({ timeout: 20_000 });
  await p.getByRole("heading", { name: IN_PROGRESS }).first().waitFor({ timeout: 20_000 });
}

const navigation = (p, method, body) =>
  p.evaluate(async ([method, body]) => {
    const r = await fetch("/api/v1/navigation", { method, headers: { Authorization: `Bearer ${localStorage.getItem("persona.token")}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
    return r.status === 200 ? r.json() : { status: r.status };
  }, [method, body]);

/** What Overview draws under the greeting, top to bottom: `card` for each run of cards (its own heading, #11148, left
 *  out), a section by its heading, a section's bar while arranging as `bar:<id>`. */
const drawn = (p) =>
  p.evaluate(() => {
    const out = [];
    for (const el of document.querySelectorAll("[data-extension-card], [data-overview-section], section > div > h2")) {
      if (el.closest("[data-extension-card]") && !el.matches("[data-extension-card]")) continue;
      if (el.matches("h2") && el.closest("[data-overview-cards]")) continue;
      const item = el.matches("[data-extension-card]") ? "card" : el.matches("[data-overview-section]") ? `bar:${el.getAttribute("data-overview-section")}` : el.textContent;
      if (item !== "card" || out.at(-1) !== "card") out.push(item);
    }
    return out;
  });
const arrangeButton = (p) => p.getByRole("button", { name: "整理总览" });
const head = (p) => p.locator("[data-overview-arrange]");
async function startArranging(p) {
  await arrangeButton(p).click();
  await head(p).waitFor({ timeout: 20_000 });
  await p.locator("[data-overview-section]").first().waitFor({ timeout: 20_000 });
}
const done = (p) => head(p).getByRole("button", { name: "完成" }).click();
const bar = (p, id) => p.locator(`[data-overview-section="${id}"]`);
// the cards' and sections' entrance is over (a working agent's spinner never is)
const settle = (p) => p.waitForTimeout(900);

let failed = false;
try {
  // tall enough for every card and section bar at once: a drag that scrolls the page on its way is not one
  const laptop = await device({ width: 1440, height: 1600 });
  const { p } = laptop;
  await navigation(p, "PUT", { overview: [] });
  await overview(p);
  let now = await drawn(p);
  const present = SECTION_TITLES.filter((s) => now.includes(s));
  check("nothing arranged: the sections in their order, then the cards (#11147)", now.at(-1) === "card" && JSON.stringify(now.filter((x) => x !== "card")) === JSON.stringify(present) && present.includes(IN_PROGRESS), now);

  await startArranging(p);
  now = await drawn(p);
  check("arranging: every section is a bar in its place, above the cards", JSON.stringify(now) === JSON.stringify(["bar:needs_you", "bar:waiting_on_computer", "bar:in_progress", "bar:working", "bar:unread", "bar:computers", "card"]), now);
  const needs = await bar(p, "needs_you").textContent();
  check("arranging: Needs you and Stuck have no switch and say they are always shown", (await bar(p, "needs_you").getByRole("switch").count()) === 0 && (await bar(p, "waiting_on_computer").getByRole("switch").count()) === 0 && needs.includes("始终显示"), needs);
  check("arranging: the heading names Overview", (await head(p).textContent()).includes("整理总览"));

  // Tasks in progress to the top: dragged onto Needs you
  await bar(p, "in_progress").dragTo(bar(p, "needs_you"), { sourcePosition: { x: 16, y: 18 } });
  await p.waitForFunction(() => document.querySelector("[data-overview-section], [data-extension-card]")?.getAttribute("data-overview-section") === "in_progress");
  check("Tasks in progress dropped on Needs you takes the top", (await drawn(p))[0] === "bar:in_progress", await drawn(p));
  // Working hidden with its switch
  await bar(p, "working").getByRole("switch").click();
  await p.waitForFunction(() => document.querySelector('[data-overview-section="working"]')?.className.includes("shadow-hairline"));
  await done(p);
  await p.waitForFunction(() => !document.querySelector("[data-overview-arrange]"));
  now = await drawn(p);
  check("done: Tasks in progress right under the greeting, the cards still last; Working not drawn", now[0] === IN_PROGRESS && now.at(-1) === "card" && !now.includes("工作中"), now);
  const hub = await navigation(p, "GET");
  check("the hub keeps it: Tasks in progress first, every section named, Working hidden", JSON.stringify(hub.overview?.[0]) === JSON.stringify({ section: "in_progress" }) && hub.overview.filter((e) => e.section).length === 6 && hub.overview.some((e) => e.section === "working" && e.hidden), hub.overview);
  const h1 = await p.evaluate(() => {
    const greeting = document.querySelector("h1");
    const first = [...document.querySelectorAll("section > div > h2")][0];
    return Boolean(greeting && first && greeting.compareDocumentPosition(first) & Node.DOCUMENT_POSITION_FOLLOWING) && first.textContent;
  });
  check("the greeting stays first", h1 === IN_PROGRESS, h1);

  await overview(p);
  now = await drawn(p);
  check("after a reload: the same", now[0] === IN_PROGRESS && now.at(-1) === "card" && !now.includes("工作中"), now);
  const other = await device({ width: 1440, height: 900 });
  await other.p.waitForFunction((t) => [...document.querySelectorAll("section > div > h2")][0]?.textContent === t, IN_PROGRESS, { timeout: 20_000 });
  check("another device draws the same", JSON.stringify(await drawn(other.p)) === JSON.stringify(now), await drawn(other.p));
  await other.context.close();

  // Reset puts Overview back
  await startArranging(p);
  await head(p).getByRole("button", { name: "恢复默认" }).click();
  await p.waitForFunction(() => document.querySelector("[data-overview-section], [data-extension-card]")?.getAttribute("data-overview-section") === "needs_you");
  const reset = await drawn(p);
  check("Reset: the sections in their order, then the cards", reset[0] === "bar:needs_you" && reset.at(-1) === "card", reset);
  await done(p);
  await laptop.context.close();

  // screenshots: as it was, arranging, Tasks in progress first
  const FIRST = [{ section: "in_progress" }, ...hub.overview.filter((e) => e.section !== "in_progress").map((e) => (e.section === "working" ? { section: "working" } : e))];
  for (const theme of ["light", "dark"]) {
    for (const [width, height] of [[1440, 1300], [390, 1600]]) {
      const d = await device({ width, height, theme });
      await navigation(d.p, "PUT", { overview: [] });
      await overview(d.p);
      await settle(d.p);
      await d.p.screenshot({ path: join(shots, `overview-default-${theme}-${width}.png`) });
      await startArranging(d.p);
      await settle(d.p);
      await d.p.screenshot({ path: join(shots, `overview-editing-${theme}-${width}.png`) });
      await done(d.p);
      await navigation(d.p, "PUT", { overview: FIRST });
      await overview(d.p);
      await d.p.waitForFunction((t) => [...document.querySelectorAll("section > div > h2")][0]?.textContent === t, IN_PROGRESS);
      await settle(d.p);
      check(`${theme} ${width}: Tasks in progress first`, (await drawn(d.p))[0] === IN_PROGRESS);
      await d.p.screenshot({ path: join(shots, `overview-in-progress-first-${theme}-${width}.png`) });
      await navigation(d.p, "PUT", { overview: [] });
      await d.context.close();
    }
  }
} catch (e) {
  failed = true;
  console.error(e);
}
clearTimeout(watchdog);
await browser.close();
writeFileSync(join(shots, "results-sections.json"), JSON.stringify(results, null, 2));
const ok = !failed && results.length > 0 && results.every((r) => r.ok);
console.log(`RESULT: overview-sections ${ok ? "passed" : "failed"} (${results.filter((r) => r.ok).length}/${results.length})`);
process.exit(ok ? 0 : 1);
