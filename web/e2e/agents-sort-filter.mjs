// Settings › Agents: "Sort Agents by recent activity" and "Show the Filter on the Agents list" (#13727), against the fake
// hub's own answers, light and dark, wide and 390 px: the two switches (off, then on, through the hub's PUT), the paged
// list's Filter button with its menu and chips (the computer goes to the hub as machine_id), the Filter gone while its
// switch is off, and the activity order drawn without errors.
//
//   MOCK_SESSIONS_LIST=1 MOCK_SESSIONS_CATALOG=1 MOCK_MANAGERS=1 MOCK_AGENT_ARCHIVES=1 PORT=18891 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18891 npx vite --port 5391 --strictPort &)
//   node web/e2e/agents-sort-filter.mjs http://127.0.0.1:5391 <artifact directory> [--channel chrome]
//
// Prints one line per check and a verdict; exits 1 on a failed check and 2 on its three-minute timeout.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name, fallback) => { const i = args.indexOf(name); return i >= 0 ? args[i + 1] : fallback; };
const base = (args[0] ?? "").replace(/\/$/, "");
const out = args[1];
const channel = flag("--channel", "");
if (!base || !out || out.startsWith("--")) { console.error("usage: node web/e2e/agents-sort-filter.mjs <web URL> <artifact directory> [--channel chrome]"); process.exit(2); }
mkdirSync(out, { recursive: true });
const watchdog = setTimeout(() => { console.log("VERDICT TIMEOUT agents sort filter"); process.exit(2); }, 180_000);
const failures = [];
const check = (ok, what, detail = "") => { console.log(`${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`); if (!ok) failures.push(what); };
const hub = async (path, init) => (await fetch(`${base}/api/v1${path}`, { ...init, headers: { Authorization: "Bearer mock", "Content-Type": "application/json", ...init?.headers } })).json();
const put = (path, body) => hub(path, { method: "PUT", body: JSON.stringify(body) });
const savedPins = await hub("/pins");
const resetHub = async () => { await put("/pins", { spaces: [], sessions: [] }); await put("/agents/display-settings", { sort_by_activity: false }); await put("/agents/display-settings", { show_filter: false }); };

const browser = await chromium.launch({ ...(channel ? { channel } : {}), ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}), headless: true });
try {
  for (const theme of ["light", "dark"]) for (const width of [1280, 390]) {
    const tag = `${theme}-${width}`;
    await resetHub();
    const ctx = await browser.newContext({ viewport: { width, height: width > 500 ? 1100 : 844 }, deviceScaleFactor: 2, locale: "zh-CN", colorScheme: theme, reducedMotion: "reduce", hasTouch: width < 500, isMobile: width < 500 });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    const requests = [];
    page.on("request", (r) => { if (r.url().includes("/sessions")) requests.push(r.url()); });
    await page.goto(`${base}/?token=mock`);
    await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15_000 });
    await page.evaluate((theme) => {
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.language", "zh-Hans");
      localStorage.setItem("persona.agentGrouping", "machine");
      for (const key of Object.keys(localStorage)) if (key.startsWith("fold:") || key.endsWith("BrowseBy")) localStorage.removeItem(key);
    }, theme);

    // the two switches, off and then on
    const row = (anchor) => page.locator(`[data-settings-row="${anchor}"]`);
    await page.goto(`${base}/settings?tab=agents#agents-sort-by-activity`);
    await row("agents-sort-by-activity").waitFor({ timeout: 15_000 });
    check(await row("agents-sort-by-activity").locator('[role="switch"]').getAttribute("aria-checked") === "false", `${tag}: the sort switch is off by default`);
    check(await row("agents-show-filter").locator('[role="switch"]').getAttribute("aria-checked") === "false", `${tag}: the Filter switch is off by default`);
    await row("agents-sort-by-activity").scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(out, `settings-off-${tag}.png`) });
    await row("agents-sort-by-activity").locator('[role="switch"]').click();
    await page.waitForFunction(() => document.querySelector('[data-settings-row="agents-sort-by-activity"] [role="switch"]')?.getAttribute("aria-checked") === "true");
    await row("agents-show-filter").locator('[role="switch"]').click();
    await page.waitForFunction(() => document.querySelector('[data-settings-row="agents-show-filter"] [role="switch"]')?.getAttribute("aria-checked") === "true");
    const stored = await hub("/agents/display-settings");
    check(stored.sort_by_activity === true && stored.show_filter === true, `${tag}: both switches reached the hub`, JSON.stringify(stored));
    await row("agents-sort-by-activity").scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(out, `settings-on-${tag}.png`) });

    // the sidebar: wide beside the page, narrow through its button
    const openSidebar = async () => {
      if (width < 500) { await page.goto(`${base}/settings`); await page.getByRole("button", { name: "侧边栏", exact: true }).click(); }
      else await page.goto(`${base}/`);
      await page.locator("aside").first().waitFor();
      await page.waitForTimeout(1500);
    };
    const sidebarShot = async (name) => {
      await page.waitForTimeout(800);
      const box = await page.locator("aside").first().boundingBox();
      await page.screenshot({ path: join(out, `${name}-${tag}.png`), clip: { x: Math.max(0, box.x), y: Math.max(0, box.y), width: Math.min(box.width, width), height: Math.min(box.height, width > 500 ? 1100 : 844) }, animations: "disabled" });
    };
    // closes the open menu; on a narrow screen Escape would also close the drawer, so a click outside the menu does it
    const dismiss = async () => {
      if (width < 500) await page.mouse.click(150, 700);
      else await page.keyboard.press("Escape");
      await page.waitForTimeout(500);
    };
    await openSidebar();
    const paged = page.locator("[data-paged-sessions='runtime']").first();
    await paged.waitFor({ timeout: 20_000 });
    const button = page.locator('aside [data-testid="agent-filter-button"]').first();
    check(await button.count() === 1, `${tag}: the paged list has the Filter button on its title row`);
    await page.mouse.move(width - 5, 5);
    await sidebarShot("sidebar-sort-on-filter-on");
    await button.click();
    const menu = page.locator('[role="menu"]').first();
    await menu.waitFor({ timeout: 5_000 });
    const items = (await menu.locator('[role^="menuitem"]').evaluateAll((els) => els.map((el) => el.textContent ?? ""))).map((s) => s.replace(/\s+/g, " ").trim());
    const wanted = ["搜索范围", "已置顶", "Agent 类型", "项目", "模型", "机器", "对话列表", "代码检索"];
    check(wanted.every((w) => items.some((i) => i.includes(w))), `${tag}: the paged Filter menu offers every option`, items.join(" / "));
    await page.screenshot({ path: join(out, `filter-menu-${tag}.png`), animations: "disabled" });
    const machineRow = menu.locator('[role^="menuitem"]', { hasText: "机器" }).first();
    await machineRow.click();
    const option = page.locator('[role="menu"] [role^="menuitem"]').nth(2);
    const optionName = ((await option.innerText()) ?? "").split("\n")[0].trim();
    await option.click();
    await dismiss();
    await page.waitForTimeout(1200);
    check(requests.some((u) => u.includes("machine_id=")), `${tag}: the computer chosen goes to the hub as machine_id`, requests.filter((u) => u.includes("machine_id=")).at(-1) ?? "");
    check(await page.locator('aside [data-testid="agent-filter-chips"]').count() === 1, `${tag}: its chip shows (${optionName})`);
    check(await page.locator("[data-paged-sessions='runtime']").count() > 0, `${tag}: still the paged list`);
    await page.mouse.move(width - 5, 5);
    await sidebarShot("sidebar-paged-filter-chip");
    // a choice the hub cannot express: the complete list answers, with the same chips
    await page.locator('aside [data-testid="agent-filter-button"]').first().click();
    await page.locator('[role="menu"] [role^="menuitem"]', { hasText: "已置顶" }).first().click();
    await dismiss();
    await page.waitForTimeout(1200);
    check(await page.locator("[data-paged-sessions='runtime']").count() === 0, `${tag}: Pinned (no paged parameter) is answered by the complete list`);
    await sidebarShot("sidebar-complete-fallback");
    await page.locator('aside [data-testid="scoped-filter-reset"]').first().click();
    await page.waitForTimeout(1000);
    check(await page.locator("[data-paged-sessions='runtime']").count() > 0, `${tag}: Reset brings the paged list back`);

    // the Filter switch off: no button, no chips, on both lists
    await put("/agents/display-settings", { show_filter: false });
    await openSidebar();
    await paged.waitFor({ timeout: 20_000 });
    check(await page.locator('aside [data-testid="agent-filter-button"]').count() === 0, `${tag}: no Filter button while its switch is off`);
    await page.mouse.move(width - 5, 5);
    await sidebarShot("sidebar-filter-off");
    check(errors.length === 0, `${tag}: no page errors`, errors.join(" | "));
    await ctx.close();
  }
} finally {
  await put("/pins", savedPins);
  await put("/agents/display-settings", { sort_by_activity: false });
  await put("/agents/display-settings", { show_filter: false });
  await browser.close();
  clearTimeout(watchdog);
}
console.log(failures.length ? `VERDICT FAIL ${failures.length} check(s): ${failures.join("; ")}` : "VERDICT PASS agents sort filter");
process.exit(failures.length ? 1 : 0);
