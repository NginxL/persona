// More's own visibility (#13798), with synthetic extension pages and hub navigation, never a live account.
// Start the mock with MOCK_EXTENSIONS=1 MOCK_EXTENSION_COLUMNS=1 and a web origin pointed at it.
// node web/e2e/sidebar-more.mjs <loopback web URL> <artifact directory> [Chromium executable]
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executable] = process.argv.slice(2);
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/sidebar-more.mjs <loopback web URL> <artifact directory> [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.error("RESULT: sidebar-more timed out (180s)");
  process.exit(124);
}, 180_000);
const root = base.replace(/\/$/, "");
const results = [];
const check = (name, ok, detail) => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};
const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });

async function device({ width = 1440, height = 900, theme = "light", size = "standard" } = {}) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, deviceScaleFactor: 2, serviceWorkers: "block", hasTouch: width < 640 });
  await context.addInitScript(({ theme, size }) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
    if (size !== "standard") localStorage.setItem("persona.text-size", size);
  }, { theme, size });
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort()));
  const p = await context.newPage();
  await p.goto(`${root}/?token=mock`);
  await p.waitForFunction(() => localStorage.getItem("persona.token"));
  await p.locator('[data-sidebar-rows]').waitFor();
  return { context, p };
}

const navigation = (p, method, body) => p.evaluate(async ([method, body]) => {
  const r = await fetch("/api/v1/navigation", { method, headers: { Authorization: `Bearer ${localStorage.getItem("persona.token")}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
  return r.status === 200 ? r.json() : { status: r.status };
}, [method, body]);
const prefs = (p) => p.evaluate(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}"));
const more = (p) => p.locator('[data-sidebar-rows] button[aria-expanded]').filter({ hasText: "更多" });
const moreSwitch = (p) => p.locator('[data-settings-group="sidebar-pages"] [data-sidebar-more] [role="switch"]');
async function editor(p, width) {
  await p.goto(root + (width < 640 ? "/settings?tab=general" : "/settings"));
  await moreSwitch(p).waitFor();
  return moreSwitch(p);
}

let failed = false;
try {
  const laptop = await device();
  const { p } = laptop;
  await navigation(p, "PUT", { pages: { order: [], shown: [], hidden: ["kb"] } });
  await p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").hidden?.includes("kb"));
  await p.reload();
  await p.locator('[data-sidebar-rows]').waitFor();
  await p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").hidden?.includes("kb"));
  await more(p).waitFor();
  check("More is visible by default", await more(p).getAttribute("aria-expanded") === "false" && (await more(p).textContent()).includes("更多"), await more(p).textContent());

  const sw = await editor(p, 1440);
  await sw.click();
  await p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").more === false);
  check("the switch hides the sidebar row immediately", await more(p).count() === 0);
  check("Settings stays fixed", await p.locator('[data-sidebar-rows] a').filter({ hasText: "设置" }).count() > 0);
  await p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").more === false);
  const saved = await navigation(p, "GET");
  check("the hub stores More off without dropping its pages", saved.pages?.more === false && saved.pages?.hidden?.includes("kb"), saved.pages);
  const second = await device();
  await second.p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").more === false);
  check("a second browser follows the hub", await more(second.p).count() === 0);
  await second.context.close();

  await p.reload();
  await p.locator('[data-sidebar-rows]').waitFor();
  check("the choice survives a reload", await more(p).count() === 0);
  await p.getByRole("button", { name: "收起侧边栏" }).click();
  await p.getByRole("button", { name: "展开侧边栏" }).waitFor();
  check("the icon rail has no More menu", await p.locator('button[aria-label^="更多（"]').count() === 0);
  await p.screenshot({ path: join(shots, "rail-light.png") });
  await p.getByRole("button", { name: "展开侧边栏" }).click();
  await editor(p, 1440);
  await sw.click();
  await p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").more === true);
  await more(p).click();
  await p.locator('[data-sidebar-rows] a[href="/kb"]').waitFor();
  check("restoring More restores its page", await p.locator('[data-sidebar-rows] a[href="/kb"]').textContent() === "知识库");
  await p.goto(`${root}/kb`);
  await p.waitForURL(`${root}/kb`);
  await more(p).click();
  await editor(p, 1440);
  await sw.click();
  await p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").more === false);
  await p.goto(`${root}/kb`);
  await p.waitForURL(`${root}/kb`);
  check("hiding More does not redirect an open page", new URL(p.url()).pathname === "/kb");
  await laptop.context.close();

  for (const theme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      const d = await device({ width, height: width === 1440 ? 900 : 844, theme, size: width === 1440 && theme === "dark" ? "large" : "standard" });
      await d.p.waitForFunction(() => JSON.parse(localStorage.getItem("sidebar.pages") ?? "{}").more === false);
      if (width === 390) {
        await d.p.getByRole("button", { name: /侧边栏/ }).first().click();
        await d.p.locator('nav[aria-label="页面"]').waitFor();
        check(`${theme} phone: More's grid button is absent`, await d.p.locator('nav[aria-label="页面"] button[aria-haspopup="menu"]').count() === 0);
      } else {
        check(`${theme} wide: More's row is absent`, await more(d.p).count() === 0);
      }
      await d.p.screenshot({ path: join(shots, `sidebar-${theme}-${width}.png`) });
      await editor(d.p, width);
      await d.p.locator('[data-sidebar-more]').scrollIntoViewIfNeeded();
      await d.p.screenshot({ path: join(shots, `setting-${theme}-${width}.png`) });
      await d.context.close();
    }
  }
} catch (e) {
  failed = true;
  console.error(e);
}
clearTimeout(watchdog);
await browser.close();
writeFileSync(join(shots, "results.json"), JSON.stringify(results, null, 2));
const ok = !failed && results.length > 0 && results.every((r) => r.ok);
console.log(`RESULT: sidebar-more ${ok ? "passed" : "failed"} (${results.filter((r) => r.ok).length}/${results.length})`);
process.exit(ok ? 0 : 1);
