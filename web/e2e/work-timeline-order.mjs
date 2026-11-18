// Run against Vite on loopback: node web/e2e/work-timeline-order.mjs <origin> <screenshot directory>
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [address, output] = process.argv.slice(2);
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname));
assert.ok(!base.username && !base.password && !base.search && !base.hash && base.pathname === "/");
await mkdir(output, { recursive: true });
const MID = "example-node", SID = "synthetic-timeline";
const machine = { id: MID, name: "Example computer", online: true };
const start = Date.now() - 12000;
const item = (seq, second, role, kind, extra) => ({ id: `item-${seq}#0`, seq, ts: new Date(start + second * 1000).toISOString(), role, kind, ...extra });
const prefix = [
  item(1, 0, "user", "text", { text: "Check this example." }),
  item(2, 2, "assistant", "text", { text: "A: I’ll inspect the file." }),
  item(3, 3, "assistant", "tool_use", { tool_name: "Bash", tool_use_id: "first", tool_input: { command: "echo first-check" } }),
  item(4, 4, "user", "tool_result", { tool_use_id: "first", text: "ok" }),
  item(5, 6, "assistant", "text", { text: "B: I found the file. Now I’ll verify it." }),
  item(6, 7, "assistant", "tool_use", { tool_name: "Bash", tool_use_id: "later", tool_input: { command: "echo later-check" } }),
  item(7, 9, "user", "tool_result", { tool_use_id: "later", text: "ok" }),
];
const browser = await chromium.launch({ channel: "chrome", headless: true });
try {
  for (const theme of ["light", "dark"]) for (const textSize of ["standard", "large"]) for (const width of [1280, 390]) {
    let items = [...prefix], socket;
    let live = true;
    const session = { machine_id: MID, session_id: SID, title: "Synthetic work timeline", agent: "codex", status: "working", state: "working", capabilities: { read: true, send: true } };
    const context = await browser.newContext({ viewport: { width, height: 1250 }, colorScheme: theme, serviceWorkers: "block" });
    await context.addInitScript(({ theme, textSize }) => {
      localStorage.setItem("persona.token", "synthetic-only");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.text-size", textSize);
    }, { theme, textSize });
    await context.route("**/*", async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== base.origin) return route.abort("blockedbyclient");
      if (!["GET", "HEAD"].includes(request.method())) return route.fulfill({ json: { ok: true } });
      if (!url.pathname.startsWith("/api/")) return route.continue();
      const path = url.pathname.replace(/^\/api\/v1/, "");
      if (path.endsWith("/items")) return route.fulfill({ json: {
        items: url.searchParams.has("after_seq") ? [] : items, has_more: false, history: "fixture", to_seq: items.at(-1).seq,
      } });
      const json = {
        "/sessions": { sessions: [session], total: 1, has_more: false }, [`/sessions/${MID}/${SID}/info`]: session,
        "/machines": { machines: [machine] }, "/assistant": { machine_id: MID, main: SID, conversations: [session] },
        "/interactions": { interactions: [] }, "/todos": { todos: [] }, "/mutes": { spaces: [], sessions: [], allowed: [] },
        "/pins": { spaces: [], sessions: [], sections: [] }, "/agents/sessions": { sessions: [] }, "/agents/prefs": {},
        "/managers": { managers: [], default_id: "" }, "/personas": { personas: [] }, "/assistant/personas": { personas: [] },
        "/workspaces": { workspaces: [] }, "/extensions": { extensions: [] },
      }[path] ?? {};
      return route.fulfill({ json });
    });
    await context.routeWebSocket("**/*", ws => {
      socket = ws;
      assert.equal(new URL(socket.url()).host, base.host);
      socket.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: [] }));
      socket.send(JSON.stringify({ type: "machines", machines: [machine] }));
      socket.send(JSON.stringify({ type: "sessions", machine_id: MID, sessions: [session] }));
      socket.onMessage(raw => { const event = JSON.parse(String(raw)); if (event.type === "app_ping") socket.send(JSON.stringify({ type: "pong", id: event.id })); });
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    page.setDefaultTimeout(12000);
    const url = new URL(`/s/${MID}/${SID}`, base).href;
    await page.goto(url);
    const count = async text => page.locator("[data-segment]").getByText(text, { exact: true }).count();
    const b = "B: I found the file. Now I’ll verify it.";
    const d = "D: The checks are passing.";
    const final = "Final: The example is verified.";
    for (const state of ["live", "streaming", "persisted", "completed", "reloaded"]) {
      if (state === "streaming") {
        socket.send(JSON.stringify({ type: "delta", machine_id: MID, session_id: SID, text: d }));
      } else if (state === "persisted" || state === "completed") {
        const next = state === "persisted" ? item(8, 10, "assistant", "text", { text: d }) : item(9, 11, "assistant", "text", { text: final });
        items.push(next);
        socket.send(JSON.stringify({ type: "items", machine_id: MID, session_id: SID, items: [next] }));
        if (state === "completed") {
          live = false;
          session.status = session.state = "idle";
          socket.send(JSON.stringify({ type: "sessions", machine_id: MID, sessions: [session] }));
        }
      } else if (state === "reloaded") await page.reload();
      const group = page.locator("[data-work-group]").first();
      const slot = page.locator("[data-latest-text]").last();
      const current = state === "live" ? b : ["streaming", "persisted"].includes(state) ? d : final;
      await slot.getByText(current, { exact: true }).waitFor();
      const button = group.locator(":scope > button");
      if (["live", "completed", "reloaded"].includes(state)) assert.equal(await button.getAttribute("aria-expanded"), "false");
      if (await button.getAttribute("aria-expanded") === "false") await button.press("Enter");
      const detail = group.locator("[data-work-detail]").first();
      await detail.getByText(/echo later-check/).waitFor();
      assert.equal(await count(current), 1, `${state}: latest text appears once`);
      assert.equal(await detail.getByText(current, { exact: true }).count(), 0, `${state}: latest stays outside the trace`);
      const actual = await detail.innerText();
      const expected = ["A: I’ll inspect", "echo first-check", ...(state === "live" ? [] : ["B: I found"]), "echo later-check"];
      let position = -1;
      for (const text of expected) {
        const next = actual.indexOf(text);
        assert.ok(next > position, `${state}: ${text} out of order in ${actual}`);
        position = next;
      }
      assert.match(await button.getAttribute("title"), /start of the exchange/);
      const runs = detail.locator("[data-work-group] > button");
      assert.equal(await runs.count(), 2);
      assert.match(await runs.first().innerText(), /Worked for 4s/);
      if (state === "live") assert.match(await runs.last().innerText(), /Working/);
      if (["completed", "reloaded"].includes(state)) {
        assert.match(await button.innerText(), /Worked for 11s/);
        assert.equal(await count(b), 1);
        assert.equal(await count(d), 1);
      }
      assert.match(await runs.last().getAttribute("title"), /preceding message/);
      await page.waitForFunction(() => {
        const detail = document.querySelector("[data-work-detail]");
        return detail && detail.getBoundingClientRect().height > 100 && Number(getComputedStyle(detail.parentElement).opacity) > .99;
      });
      await slot.evaluate(el => Promise.all(el.getAnimations({ subtree: true }).filter(a => a.effect?.getTiming().iterations !== Infinity).map(a => a.finished.catch(() => {}))));
      assert.deepEqual(errors, [], "no page errors");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      await page.locator("[data-segment]").filter({ has: slot }).screenshot({ path: join(output, `${state}-${theme}-${textSize}-${width}.png`) });
    }
    await context.close();
    console.log(`PASS ${theme}/${textSize}: ${width}px: live → streaming → persisted → completed → reload, unique text and clock boundaries`);
  }
} finally {
  await browser.close();
}
