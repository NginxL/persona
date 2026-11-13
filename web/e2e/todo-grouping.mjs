// The Agents list "By to-do" on made-up data (docs/web/web.md "By to-do"): to-dos at the top level with no repository
// rows, then "Not on a to-do" and "Recent (not running)" as rows of the same kind, each with its count and most urgent
// state; "Not on a to-do" also holds the Agents opened by hand in a terminal, each row ending with the Terminal mark
// (#6591), and has an ⓘ that tells what it holds; a to-do says the To-dos page's state word.
//
// Every session and to-do comes from the fixture below (the mock hub's own are replaced, over REST and the socket), so
// the screenshots hold no real data. Needs the web page against the fake hub, and Chrome:
//
//   PORT=18871 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18871 npx vite --port 5871 --strictPort &)
//   node web/e2e/todo-grouping.mjs http://127.0.0.1:5871 [--shots <dir>] [--prefix before-] [--no-checks]
//
// Light and dark, 1280 wide. --no-checks only takes the screenshots (for the code before this change). Prints one line per
// check and a verdict; exits 1 on a failure and after three minutes.
import assert from "node:assert/strict";
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
const prefix = flag("--prefix") ?? "";
const checks = !args.includes("--no-checks");
if (!base) {
  console.error("usage: node web/e2e/todo-grouping.mjs <web URL> [--shots <dir>] [--prefix <name>] [--no-checks]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.log("FAIL timed out after 3 minutes");
  process.exit(1);
}, 180_000);
watchdog.unref();

const now = Date.now();
const ago = (minutes) => new Date(now - minutes * 60_000).toISOString();
let n = 0;
function session(machine, title, cwd, state, minutes, extra = {}) {
  n += 1;
  const id = String(n).padStart(12, "0");
  return {
    machine_id: machine, session_id: `c0ffee00-0000-4000-8000-${id}`, title, cwd, state, status: state === "working" ? "working" : state === "offline" ? "offline" : "idle",
    agent: "claude", launcher: "claude", model: "claude-opus-5-5", started_by_persona: true, assistant: false, pending_interactions: state === "needs_you" ? 1 : 0,
    space_kind: "herdr", space_id: `w${n}`, space_name: cwd.split("/").pop(), space_index: n, slot_id: `w${n}:t1`, slot_name: "claude", slot_index: 1,
    pane_id: `w${n}:p1`, tab_id: `w${n}:t1`, workspace_id: `w${n}`, updated_at: ago(minutes), state_since: ago(minutes), rev: 1, manager: "assistant", ...extra,
  };
}
const todoRef = (id, title) => ({ todo: { id, title } });
// Two to-dos in one repository and two in others: "By to-do" used to put a repository row over each.
const weather = [
  session("server", "天气 App 主屏幕小组件", "/Users/me/code/weather", "working", 2, { ...todoRef("t_weather", "天气 App 发 1.3 版"), last_text: "正在跑小组件的快照测试。" }),
  session("server", "天气 App 降雨提醒文案", "/Users/me/code/weather", "idle", 40, { ...todoRef("t_weather", "天气 App 发 1.3 版"), last_text: "文案改好了。" }),
];
// Waiting: its agents ended their turns and one waits on CI (a to-do the To-dos page calls 等待中).
const release = [
  session("server", "等 CI 跑完再发 TestFlight", "/Users/me/code/weather", "idle", 4, { ...todoRef("t_release", "天气 App 提交审核"), work: { phase: "blocked", on: "external", line: "等 CI 跑完" }, last_text: "CI 还在跑，跑完我再上传。" }),
  session("server", "整理发版说明", "/Users/me/code/weather", "idle", 19, { ...todoRef("t_release", "天气 App 提交审核"), last_text: "发版说明写好了。" }),
  session("server", "截图更新", "/Users/me/code/weather", "idle", 22, { ...todoRef("t_release", "天气 App 提交审核"), last_text: "六张截图都换好了。" }),
];
const photos = [session("studio", "照片同步支持 HEIC", "/Users/me/code/photo-sync", "needs_you", 6, { ...todoRef("t_photos", "照片同步支持 HEIC"), last_text: "要不要把旧照片也转一遍？" })];
const orders = [session("studio", "排查 /orders 变慢", "/Users/me/code/shop-api", "idle", 55, { ...todoRef("t_orders", "排查 /orders 接口变慢"), last_text: "慢在没走索引，已经加上。" })];
// opened by hand in the terminal, on no to-do
const manual = [
  session("server", "对比两个图表库的包大小", "/Users/me/code/sandbox", "working", 1, { started_by_persona: false, last_text: "正在对比。" }),
  session("server", "整理下载文件夹", "/Users/me/Downloads", "idle", 30, { started_by_persona: false, last_text: "按类型分好了。" }),
];
// started from elsewhere (a phone, a group chat), on no to-do
const unassigned = [session("studio", "周报里要的数据", "/Users/me/code/report", "needs_you", 3, { space_kind: "botmux", pane_id: "botmux:report", last_text: "这周的数据要按部门拆开吗？" })];
const offline = [
  session("server", "给博客换字体", "/Users/me/code/blog", "offline", 300),
  session("studio", "清理旧的构建缓存", "/Users/me/code/photo-sync", "offline", 600),
  session("server", "试一下新的测试框架", "/Users/me/code/sandbox", "offline", 1500),
];
const todo = (id, title, sessions, minutes) => ({
  id, title, notes: "", done_at: null, created_at: ago(600), updated_at: ago(minutes), spaces: [],
  agents: sessions.map((s) => ({ machine_id: s.machine_id, session_id: s.session_id, linked_at: ago(300), released_at: null, title: s.title, cwd: s.cwd, agent: "claude", start_error: "", source: "manual", via: "" })),
});
const todos = [todo("t_weather", "天气 App 发 1.3 版", weather, 2), todo("t_release", "天气 App 提交审核", release, 4), todo("t_photos", "照片同步支持 HEIC", photos, 6), todo("t_orders", "排查 /orders 接口变慢", orders, 55)];
const FIXTURES = {
  // every group holds agents
  full: [...weather, ...release, ...photos, ...orders, ...manual, ...unassigned, ...offline],
  // "Not on a to-do" has none: its open row says what would be there
  empty: [...weather, ...release, ...photos, ...orders, ...offline],
};

async function open(browser, theme, fixture, path = "/overview") {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 1500 }, deviceScaleFactor: 2, colorScheme: theme, locale: "zh-CN", reducedMotion: "reduce" });
  const sessions = FIXTURES[fixture];
  await ctx.addInitScript((theme) => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
    localStorage.setItem("persona.agentGrouping", "todo");
    // every group at its default: no fold kept from an earlier run
    for (const key of Object.keys(localStorage)) if (key.startsWith("fold:")) localStorage.removeItem(key);
  }, theme);
  await ctx.route("**/api/v1/sessions", (route) => route.fulfill({ json: { sessions } }));
  await ctx.route("**/api/v1/todos", (route) => route.fulfill({ json: { todos } }));
  await ctx.routeWebSocket(/\/api\/v1\/ws/, (socket) => {
    const server = socket.connectToServer();
    socket.onMessage((m) => server.send(m));
    server.onMessage((m) => {
      let body;
      try { body = JSON.parse(String(m)); } catch { return socket.send(m); }
      if (body.type === "sessions_delta") return;
      if (body.type === "sessions") body.sessions = sessions.filter((s) => s.machine_id === body.machine_id);
      if (body.type === "todos") body.todos = todos;
      socket.send(JSON.stringify(body));
    });
  });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(`${base}${path}`);
  const agents = page.locator("[data-sidebar-agents]");
  await agents.waitFor();
  await agents.getByText("天气 App 提交审核").first().waitFor();
  await page.waitForTimeout(600);
  return { ctx, page, agents, errors };
}

const ok = (msg) => console.log(`ok  ${msg}`);
const shot = async (locator, name) => {
  if (!shots) return;
  await locator.screenshot({ path: join(shots, `${prefix}${name}.png`), animations: "disabled" });
  console.log(`shot ${prefix}${name}.png`);
};

const browser = await chromium.launch({ channel: "chrome" });
let failed = false;
try {
  for (const theme of ["light", "dark"]) {
    // the list as it opens
    {
      const { ctx, page, agents, errors } = await open(browser, theme, "full");
      await shot(agents, `agents-todo-${theme}`);
      if (checks) {
        const text = await agents.innerText();
        for (const repo of ["weather", "photo-sync", "shop-api", "sandbox", "Downloads"]) assert.ok(!new RegExp(`^${repo}$`, "m").test(text), `no "${repo}" repository row in By to-do`);
        ok(`${theme}: no repository rows`);
        // the to-dos and the two groups are rows of one kind, at one level, each with its count and state
        const rows = await agents.locator("[data-tree-branch]").evaluateAll((els) => els.map((el) => ({
          name: el.getAttribute("data-tree-branch"),
          level: el.closest("[data-tree-branch-list]") ? "nested" : "top",
          text: el.querySelector("[data-tree-branch-row]")?.textContent ?? "",
          expanded: el.querySelector("[data-tree-branch-row] button[aria-expanded]")?.getAttribute("aria-expanded"),
        })));
        const names = rows.map((r) => r.name);
        assert.deepEqual(names.slice(-2), ["todo:none", "recent"], `the two groups come last: ${names.join(", ")}`);
        assert.ok(!names.includes("todo:manual"), "no group of its own for the Agents opened in a terminal (#6591)");
        assert.ok(rows.every((r) => r.level === "top"), "every to-do and group is top level");
        const group = (key) => rows.find((r) => r.name === key);
        // the count's spoken words sit between the number and the state in the text: "2" + "2 个 Agent" + "工作中"
        assert.match(group("todo:none").text, /^没挂待办的.*3 个 Agent需要你$/, "Not on a to-do: 3 (2 opened in a terminal), Needs you");
        assert.match(group("recent").text, /^最近（未运行）.*3 个 Agent已完成$/, "Recent: 3, Done");
        // folded by default, unless it holds an agent that needs you (DESIGN_SYSTEM 3.4)
        assert.equal(group("recent").expanded, "false");
        assert.equal(group("todo:none").expanded, "true");
        ok(`${theme}: two groups after the to-dos, top level, with count and most urgent state`);
        // where each came from stays on its row: the two opened in a terminal end with the Terminal mark, the Lark one with Lark's
        const marks = await agents.locator('[data-tree-branch="todo:none"] [data-tree-branch-list] [data-source]').evaluateAll((els) => els.map((el) => el.getAttribute("data-source")).sort());
        assert.deepEqual(marks, ["feishu", "terminal", "terminal"]);
        ok(`${theme}: the rows opened in a terminal keep the Terminal mark`);
        // a to-do says the To-dos page's word: Waiting, Follow up
        assert.match(rows.find((r) => r.name === "todo:t_release").text, /3 个 Agent等待中$/);
        assert.match(rows.find((r) => r.name === "todo:t_orders").text, /1 个 Agent待跟进$/);
        ok(`${theme}: to-do rows say 等待中 / 待跟进 as the To-dos page`);
        // the explanation is behind the ⓘ, not under the title
        assert.equal(await agents.getByText("右键一行").count(), 0, "no explanation shown by default");
        const info = agents.getByRole("button", { name: /关于「没挂待办的」/ });
        await info.hover();
        const tip = page.getByRole("tooltip");
        await tip.waitFor();
        assert.match(await tip.innerText(), /终端[\s\S]*飞书/);
        assert.doesNotMatch(await tip.innerText(), /herdr/i, "no engine name (DESIGN_SYSTEM 5.6)");
        ok(`${theme}: ⓘ shows the explanation on hover`);
        // the sidebar and the tip beside it, which is drawn over the page; the page itself is left out
        if (shots) {
          const at = await info.boundingBox();
          const side = await page.locator("aside").first().boundingBox();
          const tipAt = await tip.boundingBox();
          const top = Math.max(0, at.y - 520);
          await page.screenshot({ path: join(shots, `${prefix}agents-todo-info-${theme}.png`), clip: { x: 0, y: top, width: Math.max(side.width, tipAt.x + tipAt.width) + 12, height: Math.min(700, 1500 - top) }, animations: "disabled" });
          console.log(`shot ${prefix}agents-todo-info-${theme}.png`);
        }
        await page.mouse.move(900, 700);
        await tip.waitFor({ state: "detached" });
        await info.click();
        await tip.waitFor();
        await page.keyboard.press("Escape");
        await tip.waitFor({ state: "detached" });
        ok(`${theme}: ⓘ opens on click and closes with Escape`);
        // a press-and-drag over the group rows selects nothing (the grey blocks of the report)
        const from = await agents.getByText("最近（未运行）", { exact: true }).boundingBox();
        const to = await agents.getByText("没挂待办的", { exact: true }).boundingBox();
        await page.mouse.move(from.x + from.width - 2, from.y + from.height / 2);
        await page.mouse.down();
        await page.mouse.move(to.x + 1, to.y + to.height / 2, { steps: 10 });
        await page.mouse.up();
        assert.equal(await page.evaluate(() => String(getSelection()).trim()), "", "dragging over group rows selects no text");
        ok(`${theme}: dragging over the group rows selects no text`);
        assert.deepEqual(errors, []);
      }
      await ctx.close();
    }
    // the To-dos page beside it: the same to-do, the same word
    {
      const { ctx, page } = await open(browser, theme, "full", "/todos");
      await page.getByText("等待中").first().waitFor();
      // the sidebar and the To-dos list beside it (the open to-do's details left out)
      if (shots) {
        const list = await page.getByText("等待中").first().boundingBox();
        await page.screenshot({ path: join(shots, `${prefix}todos-page-${theme}.png`), clip: { x: 0, y: 0, width: Math.min(1280, list.x + 520), height: 1500 }, animations: "disabled" });
        console.log(`shot ${prefix}todos-page-${theme}.png`);
      }
      await ctx.close();
    }
    // an empty group, opened: its explanation is the empty state
    {
      const { ctx, page, agents, errors } = await open(browser, theme, "empty");
      const row = agents.locator('[data-tree-branch="todo:none"]');
      if (checks) {
        await row.locator("[data-tree-branch-row] button[aria-expanded]").first().click();
        await row.getByText(/你自己在终端里开的/).waitFor();
        assert.match(await row.locator("[data-tree-branch-row]").innerText(), /^没挂待办的[\s\S]*0 个 Agent$/);
        ok(`${theme}: an empty group says 0 and, opened, what would be there`);
        assert.deepEqual(errors, []);
      } else {
        // before: open the groups of agents on no to-do there are, to show what they say
        for (const key of ["todo:manual", "todo:none"]) {
          const button = agents.locator(`[data-tree-branch="${key}"] [data-tree-branch-row] button[aria-expanded]`).first();
          if ((await button.count()) && (await button.getAttribute("aria-expanded")) === "false") await button.click();
        }
      }
      await page.waitForTimeout(500);
      await shot(agents, `agents-todo-empty-open-${theme}`);
      await ctx.close();
    }
  }
  console.log(checks ? "PASS todo-grouping (synthetic data only)" : "DONE screenshots only");
} catch (error) {
  failed = true;
  console.log(`FAIL ${error.message}`);
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
process.exit(failed ? 1 : 0);
