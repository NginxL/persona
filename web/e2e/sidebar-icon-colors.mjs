// Settings › Appearance › Sidebar icons (#10822) in a real browser against the mock hub, in Simplified Chinese.
// Monochrome, the default: every sidebar page's icon is the quiet ink (--ink-3) and the open page's --ink, in the wide
// sidebar, the icon rail and a phone's drawer. The setting is switched to Colored in Settings › General › Appearance,
// then each built-in page's icon is its named color, the made-up extension page that names icon_color (花园) is green,
// the gateway's 用量 (none named) stays neutral, the open page keeps --ink, a colored icon keeps its color under the
// pointer, and every colored icon reads at 3:1 on the sidebar and on the hover fill. Light and dark, 1440 × 900 and
// 390 × 844, screenshots of the sidebar (the drawer opened on a phone), the rail and the setting.
//
// Start the mock with MOCK_EXTENSIONS=1 MOCK_NAV_ICON_COLOR=1, and Vite (or `vite preview`) pointing at it
// (PERSONA_HUB_URL). `--monochrome-only` takes only the Monochrome shots and checks (to set beside another build's,
// pixel for pixel). Only the web origin's requests leave the browser.
// node web/e2e/sidebar-icon-colors.mjs <loopback web origin> <artifact directory> [--monochrome-only] [--chromium <executable>]
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const option = (name) => (args.includes(name) ? args[args.indexOf(name) + 1] : undefined);
const executable = option("--chromium");
const monochromeOnly = args.includes("--monochrome-only");
const [origin, shots] = args.filter((a, i) => !a.startsWith("--") && args[i - 1] !== "--chromium");
if (!/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin ?? "") || !shots) {
  console.log("FAIL sidebar-icon-colors: usage: node web/e2e/sidebar-icon-colors.mjs <loopback web origin> <artifact directory> [--monochrome-only] [--chromium <executable>]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.log("FAIL sidebar-icon-colors: timed out after 5 minutes");
  process.exit(124);
}, 300_000);

const results = [];
let failed = 0;
const check = (ok, what, detail) => {
  results.push({ ok: Boolean(ok), what, detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
  if (!ok) failed++;
};

// Each built-in page's color (web/src/lib/sidebar-icons.ts BUILTIN_PAGE_COLORS), by its address; the mock's extension
// pages by their titles.
const BUILT_IN = { "/overview": "amber", "/inbox": "orange", "/health": "pink", "/todos": "blue", "/routines": "purple", "/kb": "indigo", "/machines": "teal", "/settings": "gray" };
const EXTENSIONS = { 花园: "green", 用量: null };
const OPEN = "/kb"; // the open page: its icon keeps the open row's ink

const luminance = ([r, g, b]) => [r, g, b].map((c) => c / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)).reduce((n, c, i) => n + c * [0.2126, 0.7152, 0.0722][i], 0);
const contrast = (a, b) => {
  const x = luminance(a), y = luminance(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
};

/** The pages drawn in `scope` (a CSS selector): each one's address or title, its icon's color and whether it is open,
 *  and the tokens it is compared with, all as the browser resolved them (rgb triples). */
const read = (page, scope) => page.evaluate((scope) => {
  const probe = document.createElement("span");
  document.body.append(probe);
  const resolve = (value, property = "color") => {
    probe.style[property] = value;
    const m = getComputedStyle(probe)[property].match(/\d+(\.\d+)?/g).map(Number);
    return m.slice(0, 3);
  };
  const tokens = Object.fromEntries(["ink", "ink-3", "canvas", "hover", ...["amber", "orange", "pink", "blue", "purple", "indigo", "teal", "gray", "green", "yellow"].map((c) => `hue-${c}`)]
    .map((t) => [t, resolve(`var(--${t})`, t === "canvas" || t === "hover" ? "backgroundColor" : "color")]));
  const box = document.querySelector(scope);
  const pages = [...(box?.querySelectorAll("a") ?? [])].filter((a) => a.querySelector("svg") && a.getClientRects().length).map((a) => ({
    href: new URL(a.href).pathname,
    // an extension's page is titled "<page> · <extension>" (and its count after it on the rail)
    name: (a.title || a.getAttribute("aria-label") || a.textContent || "").split(" · ")[0].trim(),
    color: getComputedStyle(a.querySelector("svg")).color.match(/\d+(\.\d+)?/g).map(Number).slice(0, 3),
    open: a.getAttribute("aria-current") === "page",
  }));
  probe.remove();
  return { tokens, pages };
}, scope);

const same = (a, b) => a && b && a.every((v, i) => Math.abs(v - b[i]) <= 1);

/** Checks the pages drawn in `scope` against the mode's rule. */
function rule(label, { tokens, pages }, colored) {
  const builtIns = pages.filter((p) => BUILT_IN[p.href]);
  const extensions = pages.filter((p) => p.name in EXTENSIONS);
  check(builtIns.length >= 6, `${label}: the built-in pages are drawn`, builtIns.map((p) => p.href));
  for (const p of [...builtIns, ...extensions]) {
    const want = p.open ? "ink" : colored ? (BUILT_IN[p.href] ? `hue-${BUILT_IN[p.href]}` : EXTENSIONS[p.name] ? `hue-${EXTENSIONS[p.name]}` : "ink-3") : "ink-3";
    check(same(p.color, tokens[want]), `${label}: ${p.href}${p.name ? ` ${p.name}` : ""} is --${want}`, p.color);
    if (colored && !p.open && want.startsWith("hue-")) {
      const low = Math.min(contrast(p.color, tokens.canvas), contrast(p.color, tokens.hover));
      check(low >= 3, `${label}: ${p.href} reads at 3:1 on the canvas and the hover fill`, Math.round(low * 100) / 100);
    }
  }
}

const settle = (page, ms = 600) => page.waitForTimeout(ms);
/** The folded sidebar: the rail's column. */
const railClip = (page) => page.evaluate(() => {
  const r = document.querySelector('aside nav[aria-label="页面"]').closest("aside").getBoundingClientRect();
  return { x: 0, y: 0, width: Math.ceil(r.right), height: Math.ceil(r.bottom) };
});
const browser = await chromium.launch(executable ? { executablePath: executable, headless: true } : { channel: "chrome", headless: true });

async function open({ width, height, theme, colored }) {
  const touch = width < 640;
  const context = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 2, colorScheme: theme, locale: "zh-CN", serviceWorkers: "block", hasTouch: touch, isMobile: touch });
  await context.addInitScript(([theme, colored]) => {
    if (sessionStorage.getItem("e2e-set")) return; // set once: the setting is then the page's own to change
    sessionStorage.setItem("e2e-set", "1");
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
    if (colored) localStorage.setItem("persona.sidebar-icons", "colored");
  }, [theme, colored]);
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === origin ? route.continue() : route.abort()));
  const page = await context.newPage();
  await page.goto(`${origin}${OPEN}?token=mock`);
  await page.waitForFunction(() => localStorage.getItem("persona.token"));
  await page.waitForLoadState("networkidle");
  return { context, page };
}

/** The sidebar's pages on screen: the wide sidebar's Pages group, or the phone's drawer (opened) with its grid. */
async function sidebar(page, width) {
  if (width < 640) {
    await page.getByRole("button", { name: /侧边栏/ }).first().click();
    await settle(page);
    return 'nav[aria-label="页面"]';
  }
  await page.locator("[data-sidebar-rows] a[href='/settings']").first().waitFor({ timeout: 20_000 });
  return "[data-sidebar-rows]";
}

/** The part of the screen showing the sidebar's pages: the column down to the end of the pages (wide), the drawer (phone). */
async function pagesClip(page, width) {
  if (width < 640) return undefined;
  return page.evaluate(() => {
    const rows = document.querySelector("[data-sidebar-rows]").getBoundingClientRect();
    const last = [...document.querySelectorAll("[data-sidebar-rows] a[href='/settings']")].at(-1).getBoundingClientRect();
    return { x: 0, y: 0, width: Math.ceil(rows.right + 8), height: Math.ceil(last.bottom + 12) };
  });
}

try {
  for (const theme of ["light", "dark"]) {
    for (const [width, height] of [[1440, 900], [390, 844]]) {
      const label = `${theme} ${width}`;
      // Monochrome: nothing stored
      const mono = await open({ width, height, theme, colored: false });
      check(await mono.page.evaluate(() => localStorage.getItem("persona.sidebar-icons")) === null, `${label}: nothing is stored by default`);
      const scope = await sidebar(mono.page, width);
      rule(`${label} monochrome`, await read(mono.page, scope), false);
      await mono.page.mouse.move(width - 5, height - 5);
      await settle(mono.page, 300);
      await mono.page.screenshot({ path: join(shots, `sidebar-${theme}-${width}-monochrome.png`), clip: await pagesClip(mono.page, width) });
      if (width >= 640) {
        await mono.page.getByRole("button", { name: "收起侧边栏" }).click();
        await settle(mono.page);
        rule(`${label} monochrome rail`, await read(mono.page, 'aside nav[aria-label="页面"]'), false);
        await mono.page.screenshot({ path: join(shots, `rail-${theme}-${width}-monochrome.png`), clip: await railClip(mono.page) });
        await mono.page.getByRole("button", { name: "展开侧边栏" }).click();
        await settle(mono.page);
      }
      if (monochromeOnly) {
        await mono.context.close();
        continue;
      }
      // Colored, chosen in Settings › General › Appearance
      await mono.page.goto(`${origin}/settings?tab=general`);
      const row = mono.page.locator('[data-settings-row="sidebar-icons"]');
      await row.waitFor({ timeout: 20_000 });
      await row.evaluate((e) => e.scrollIntoView({ block: "center" }));
      check(await row.getByRole("radio", { name: "单色" }).getAttribute("aria-checked") === "true", `${label}: the setting starts on 单色`);
      await row.getByRole("radio", { name: "彩色" }).click();
      check(await mono.page.evaluate(() => localStorage.getItem("persona.sidebar-icons")) === "colored", `${label}: 彩色 is kept in this browser`);
      await settle(mono.page, 300);
      await mono.page.screenshot({ path: join(shots, `setting-${theme}-${width}.png`) });
      await mono.context.close();

      const colored = await open({ width, height, theme, colored: true });
      const shown = await sidebar(colored.page, width);
      const drawn = await read(colored.page, shown);
      rule(`${label} colored`, drawn, true);
      check(drawn.pages.some((p) => p.name === "花园"), `${label}: the extension page naming icon_color is drawn`, drawn.pages.map((p) => p.name || p.href));
      if (width >= 640) {
        // under the pointer the color stays and the hover fill shows
        const inbox = colored.page.locator("[data-sidebar-rows] a[href='/inbox']").first();
        await inbox.hover();
        await settle(colored.page, 400);
        const hovered = await read(colored.page, shown);
        const one = hovered.pages.find((p) => p.href === "/inbox");
        check(same(one.color, hovered.tokens["hue-orange"]), `${label} colored: Inbox keeps its orange under the pointer`, one.color);
        await colored.page.screenshot({ path: join(shots, `sidebar-${theme}-${width}-colored-hover.png`), clip: await pagesClip(colored.page, width) });
      }
      await colored.page.mouse.move(width - 5, height - 5);
      await settle(colored.page, 400);
      await colored.page.screenshot({ path: join(shots, `sidebar-${theme}-${width}-colored.png`), clip: await pagesClip(colored.page, width) });
      if (width >= 640) {
        await colored.page.getByRole("button", { name: "收起侧边栏" }).click();
        await settle(colored.page);
        rule(`${label} colored rail`, await read(colored.page, 'aside nav[aria-label="页面"]'), true);
        await colored.page.screenshot({ path: join(shots, `rail-${theme}-${width}-colored.png`), clip: await railClip(colored.page) });
        await colored.page.getByRole("button", { name: "展开侧边栏" }).click();
      }
      await colored.context.close();
    }
  }
} catch (e) {
  check(false, "the run finished", String(e?.stack ?? e));
} finally {
  writeFileSync(join(shots, "results.json"), JSON.stringify(results, null, 2));
  await browser.close();
  clearTimeout(watchdog);
}
console.log(failed ? `RESULT: sidebar-icon-colors FAIL (${failed} of ${results.length})` : `RESULT: sidebar-icon-colors ok (${results.length} checks)`);
process.exit(failed ? 1 : 0);
