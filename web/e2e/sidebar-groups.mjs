// Run against mock_worktree_migration.py with dedicated loopback origins.
// node web/e2e/sidebar-groups.mjs <web origin> <mock origin> <artifacts>
import assert from "node:assert/strict";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [web, hub, output] = process.argv.slice(2);
assert([web, hub].every((url) => /^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(url ?? "")) && output);
assert.equal((await (await fetch(`${hub}/mock/worktree-migration`)).json()).fixture, "worktree-migration");
mkdirSync(output, { recursive: true });
const browser = await chromium.launch({ channel: "chrome" });
const results = [];
const expandSessions = (sessions) => sessions.flatMap((s) => s.assistant ? [s] : [s, ...Array.from({ length: 24 }, (_, i) => ({ ...s, session_id: `${s.session_id}-copy-${i}`, title: `Synthetic Agent ${i + 1}`, pane_id: `${s.pane_id}-copy-${i}` }))]);
const expandTodos = (todos) => todos.map((todo) => ({ ...todo, agents: (todo.agents ?? []).flatMap((a) => [a, ...Array.from({ length: 24 }, (_, i) => ({ ...a, session_id: `${a.session_id}-copy-${i}` }))]) }));
try {
  for (const width of (process.env.SIDEBAR_WIDTHS?.split(",").map(Number) ?? [1440, 390])) for (const theme of ["light", "dark"]) for (const textSize of ["standard", "large"]) {
    const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: width === 390, colorScheme: theme, reducedMotion: "reduce" });
    await context.addInitScript(({ theme, textSize }) => {
      localStorage.setItem("persona.token", "synthetic-worktree-token");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.agentGrouping", "repo");
      localStorage.setItem("persona.text-size", textSize);
    }, { theme, textSize });
    // Keep every original identity, and make each synthetic group long enough to scroll through.
    await context.route("**/api/v1/sessions", async (route) => {
      const response = await route.fetch();
      if (response.status() === 304) return route.fulfill({ response });
      const text = await response.text();
      if (!text.trim()) return route.fulfill({ response });
      const body = JSON.parse(text);
      if (body.sessions) body.sessions = expandSessions(body.sessions);
      await route.fulfill({ response, json: body });
    });
    await context.route("**/api/v1/todos", async (route) => {
      const response = await route.fetch();
      if (response.status() === 304) return route.fulfill({ response });
      const text = await response.text();
      if (!text.trim()) return route.fulfill({ response });
      const body = JSON.parse(text);
      if (body.todos) body.todos = expandTodos(body.todos);
      await route.fulfill({ response, json: body });
    });
    await context.routeWebSocket(/.*/, (socket) => {
      const server = socket.connectToServer();
      server.onMessage((message) => {
        const body = JSON.parse(String(message));
        if (body.type === "sessions" && body.sessions) body.sessions = expandSessions(body.sessions);
        if (body.type === "todos" && body.todos) body.todos = expandTodos(body.todos);
        socket.send(JSON.stringify(body));
      });
    });
    const page = await context.newPage();
    page.setDefaultTimeout(15000);
    const failures = [];
    page.on("pageerror", (error) => failures.push(String(error)));
    await page.goto(`${web}/agents`);
    const sidebar = width === 390 ? page.getByRole("main") : page.locator("aside:visible");
    const grouping = sidebar.getByRole("radiogroup", { name: "Group agents by" });
    await grouping.waitFor();
    await page.screenshot({ path: join(output, `${width}-${theme}-${textSize}-controls.png`) });
    for (const mode of (process.env.SIDEBAR_GROUPINGS?.split(",") ?? ["To-do", "Machine", "Repo", "Project", "Status"])) {
      const segment = grouping.getByRole("radio").filter({ hasText: new RegExp(`^${mode}$`) });
      assert.equal(await segment.count(), 1, `${mode} grouping is offered`);
      await segment.click();
      const open = sidebar.getByRole("button", { name: "Expand all", exact: true });
      if (await open.count()) await open.click();
      await page.waitForTimeout(500); // Collapse releases its animation clipping after settling.
      const check = await grouping.evaluate((bar) => {
        const content = bar.closest("[data-sidebar-agents]");
        const scroller = bar.closest("[data-sidebar-rows]") ?? bar.closest("main").querySelector(".overflow-y-auto");
        scroller.scrollTop = 0;
        const groups = [...content.querySelectorAll("[data-sidebar-group]")].filter((el) => el.parentElement.closest("[data-sidebar-group]")?.parentElement?.hasAttribute("data-sidebar-agents"));
        const long = groups.filter((el) => el.getBoundingClientRect().height > 160);
        const target = long[0];
        if (!target) return { long: 0 };
        const heading = target.querySelector(":scope > [data-sidebar-group-heading]");
        const top = Number.parseFloat(getComputedStyle(heading).top);
        scroller.scrollTop += heading.getBoundingClientRect().top - scroller.getBoundingClientRect().top - top + 60;
        return { long: long.length, pinned: Math.abs(heading.getBoundingClientRect().top - scroller.getBoundingClientRect().top - top), personaBottom: scroller.querySelector("[data-sidebar-sticky]")?.getBoundingClientRect().bottom ?? scroller.getBoundingClientRect().top, headingTop: heading.getBoundingClientRect().top };
      });
      assert(check.long > 0, `${mode} fixture has a scrollable group`);
      assert(check.pinned < 2, `${mode} heading pins to the scroller: ${JSON.stringify(check)}`);
      assert(check.headingTop >= check.personaBottom - 1, `${mode} preserves Persona`);
      const nested = await grouping.evaluate((bar) => {
        const scroller = bar.closest("[data-sidebar-rows]") ?? bar.closest("main").querySelector(".overflow-y-auto");
        const child = [...bar.closest("[data-sidebar-agents]").querySelectorAll("[data-sidebar-group] [data-sidebar-group]")].find((el) => el.getBoundingClientRect().height > scroller.clientHeight + 120);
        if (!child) return { checked: false };
        const parent = child.parentElement.closest("[data-sidebar-group]").querySelector(":scope > [data-sidebar-group-heading]");
        const heading = child.querySelector(":scope > [data-sidebar-group-heading]");
        const top = Number.parseFloat(getComputedStyle(heading).top);
        scroller.scrollTop += heading.getBoundingClientRect().top - scroller.getBoundingClientRect().top - top + 60;
        return { checked: true, pinned: Math.abs(heading.getBoundingClientRect().top - scroller.getBoundingClientRect().top - top) < 2, stacked: heading.getBoundingClientRect().top >= parent.getBoundingClientRect().bottom - 1 };
      });
      if (nested.checked) assert(nested.pinned && nested.stacked, `Nested headings stack and pin: ${JSON.stringify(nested)}`);
      const toolbar = await grouping.evaluate((bar) => {
        const root = bar.closest("[data-sidebar-agents]");
        const header = root.querySelector("[data-sidebar-agents-controls]").parentElement;
        const scroller = bar.closest("[data-sidebar-rows]") ?? bar.closest("main").querySelector(".overflow-y-auto");
        const top = Number.parseFloat(getComputedStyle(header).top);
        const rect = header.getBoundingClientRect();
        const element = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
        return { pinned: Math.abs(rect.top - scroller.getBoundingClientRect().top - top) < 2, unobscured: header.contains(element), onlyOneAction: root.querySelectorAll("[data-sidebar-agents-controls] [data-testid=agents-fold-all]").length === 1 };
      });
      assert(toolbar.pinned && toolbar.unobscured && toolbar.onlyOneAction, `Toolbar stays pinned, clear and has one action: ${JSON.stringify(toolbar)}`);
      await page.screenshot({ path: join(output, `${width}-${theme}-${textSize}-${mode.toLowerCase()}.png`) });
      const transition = await grouping.evaluate((bar) => {
        const scroller = bar.closest("[data-sidebar-rows]") ?? bar.closest("main").querySelector(".overflow-y-auto");
        const groups = [...bar.closest("[data-sidebar-agents]").querySelectorAll("[data-sidebar-group]")].filter((el) => el.parentElement.closest("[data-sidebar-group]")?.parentElement?.hasAttribute("data-sidebar-agents") && el.getBoundingClientRect().height > 160);
        if (groups.length < 2) return { siblings: false };
        const saved = scroller.scrollTop;
        const first = groups[0].querySelector(":scope > [data-sidebar-group-heading]");
        const second = groups[1].querySelector(":scope > [data-sidebar-group-heading]");
        const top = Number.parseFloat(getComputedStyle(second).top);
        scroller.scrollTop += second.getBoundingClientRect().top - scroller.getBoundingClientRect().top - top + 30;
        const replaced = first.getBoundingClientRect().bottom <= second.getBoundingClientRect().top + 1;
        const actualTop = second.getBoundingClientRect().top;
        const desiredTop = scroller.getBoundingClientRect().top + top;
        const pinned = Math.abs(actualTop - desiredTop) < 2;
        scroller.scrollTop = saved;
        const restored = Math.abs(first.getBoundingClientRect().top - scroller.getBoundingClientRect().top - Number.parseFloat(getComputedStyle(first).top)) < 2;
        return { siblings: true, replaced, pinned, restored, actualTop, desiredTop, scrollTop: scroller.scrollTop, maxScroll: scroller.scrollHeight - scroller.clientHeight };
      });
      if (transition.siblings) assert(transition.replaced && transition.pinned && transition.restored, `${mode} switches siblings and restores on reverse scroll: ${JSON.stringify(transition)}`);
      await sidebar.getByRole("button", { name: "Collapse all", exact: true }).click();
      await page.waitForTimeout(500);
      assert.equal(await grouping.evaluate((bar) => [...bar.closest("[data-sidebar-agents]").querySelectorAll("[data-sidebar-group-heading] button[aria-expanded]")].filter((b) => !b.closest("[data-sidebar-agents-controls]")).every((b) => b.getAttribute("aria-expanded") === "false")), true);
      results.push({ width, theme, textSize, mode, ...check, ...transition, nested, toolbar });
    }
    assert.deepEqual(failures, []);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    // Finish intercepted polling responses before closing the context aborts their bodies.
    await context.unrouteAll({ behavior: "wait" });
    await context.close();
  }
} catch (error) {
  for (const context of browser.contexts()) for (const page of context.pages()) {
    await page.screenshot({ path: join(output, "failure.png") });
    console.error(await page.locator("aside").evaluateAll((els) => els.map((el) => ({ rect: el.getBoundingClientRect().toJSON(), text: el.textContent.slice(0, 150) }))));
  }
  throw error;
} finally { await browser.close(); }
writeFileSync(join(output, "results.json"), JSON.stringify(results, null, 2));
console.log(`PASS ${results.length} grouping / viewport / theme combinations`);
