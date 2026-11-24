// Long tasks while a long conversation opens and a reply streams (#9795): the real app against a synthetic hub.
// node web/e2e/chat-long-tasks.mjs <web origin> <artifact-directory> [--items <n>] [--secs <s>] [--chromium <executable>]
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name, fallback) => { const i = args.indexOf(name); return i < 0 ? fallback : args[i + 1]; };
const origin = args[0]?.replace(/\/$/, "");
const output = args[1];
if (!origin || !output || output.startsWith("--")) {
  console.error("usage: node web/e2e/chat-long-tasks.mjs <web origin> <artifact-directory> [--items <n>] [--secs <s>] [--chromium <executable>]");
  process.exit(2);
}
mkdirSync(output, { recursive: true });
const ITEMS = Number(flag("--items", 2000));
const SECS = Number(flag("--secs", 30));
const LONG_TASK_LIMIT_MS = 1000; // past this the page is stuck for the person using it
const PROBE_CAP_MS = 5000; // a probe the page has not answered by then counts as a hang
const results = { origin, items: ITEMS, secs: SECS, scenarios: [], errors: [], status: "running" };
const finish = (status) => {
  results.status = status;
  writeFileSync(join(output, "results.json"), JSON.stringify(results, null, 2) + "\n");
  const worst = Math.max(0, ...results.scenarios.map((s) => s.longestTask ?? 0));
  console.log(`${status.toUpperCase()} chat-long-tasks: longest task ${worst} ms; ${results.scenarios.map((s) => `${s.name} ${s.verdict}`).join(", ")}`);
};
const watchdog = setTimeout(() => { finish("timeout"); process.exit(124); }, 4 * 60_000 + SECS * 1000);

// Invented text shaped like real chats: names without an extension (branches, repositories), a deep quote, a table.
const AT = Date.parse("2026-11-01T10:00:00Z");
const ts = (seq) => new Date(AT + seq * 1000).toISOString();
const say = (t) => [
  `Pushed \`fix/${1000 + t}-attendance-followups\` and opened the PR; example/payments-service-internal is unchanged.`,
  `| step | result |\n|---|---|\n| build | ok |\n| tests | ${t} passed |`,
  `${"> ".repeat(4)}quoted from the thread`,
  `Next: rebase provider-api/large-model-thinking-pro onto main.`,
].join("\n\n");
function conversation(count) {
  const items = [];
  for (let seq = 1, t = 0; items.length < count; t++) {
    items.push({ id: `u${seq}#0`, seq, ts: ts(seq), role: "user", kind: "text", text: `Turn ${t}: check fix/${1000 + t}-attendance-followups`, client_msg_id: `m${t}` });
    seq++;
    for (let k = 0; k < 6 && items.length < count; k++, seq += 2) {
      const call = `c${seq}`;
      items.push({ id: `${call}#u`, seq, ts: ts(seq), role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: call, tool_input: { command: `git log --oneline fix/${1000 + t}-attendance-followups` } });
      items.push({ id: `${call}#r`, seq: seq + 1, ts: ts(seq + 1), role: "user", kind: "tool_result", tool_use_id: call, text: `${call} example/payments-service-internal ok\n`.repeat(20) });
    }
    items.push({ id: `a${seq}#0`, seq, ts: ts(seq), role: "assistant", kind: "text", text: say(t) });
    seq++;
  }
  const last = items[count - 1];
  return [...items.slice(0, count - 1), { id: "last#0", seq: last.seq, ts: last.ts, role: "assistant", kind: "text", text: LAST }];
}
const LAST = "That is every branch checked.";

async function scenario(browser, name, { path, assistant, items, last, stream }) {
  const sid = assistant ? "demo-main" : "demo-agent";
  const session = { machine_id: "demo", session_id: sid, title: assistant ? "Persona" : "Example Agent", cwd: "/tmp/example", assistant, started_by_persona: true, state: "working", status: "working", updated_at: ts(items.length), state_since: ts(items.length), capabilities: { send: true, read: true } };
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, serviceWorkers: "block" });
  const outside = [];
  context.on("request", (r) => { if (!r.url().startsWith(origin) && !/^(data|blob):/.test(r.url())) outside.push(r.url()); });
  await context.addInitScript(() => {
    localStorage.setItem("persona.token", "synthetic-token");
    window.__longTasks = [];
    new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__longTasks.push(Math.round(e.duration)); }).observe({ type: "longtask", buffered: true });
  });
  // Every hub read is answered here and every other origin refused: nothing reaches a hub, whichever serves the page.
  await context.route((url) => !url.href.startsWith(origin), (route) => route.abort());
  await context.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname.replace(/^.*\/api\/v1/, "");
    let body = {};
    if (p === "/machines") body = { machines: [{ id: "demo", name: "Example computer", online: true, os: "linux" }] };
    else if (p === "/sessions") body = { sessions: [session] };
    else if (p === "/assistant") body = { machine_id: "demo", main: assistant ? sid : "other", conversations: assistant ? [session] : [], tasks: [], memory: [] };
    else if (p === `/sessions/demo/${sid}`) body = { session };
    else if (p.endsWith(`/${sid}/items`)) body = url.searchParams.has("after_seq") || url.searchParams.has("before_seq") ? { items: [], has_more: false } : { items, has_more: true };
    else if (p.endsWith("/items")) body = { items: [], has_more: false };
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  let socket;
  await context.routeWebSocket((url) => url.pathname.endsWith("/api/v1/ws"), (ws) => {
    socket = ws;
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
    ws.onMessage((raw) => { if (JSON.parse(String(raw)).type === "app_ping") ws.send(JSON.stringify({ type: "pong" })); });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  // How long the page takes to run a trivial script: what someone clicking or typing waits.
  const probe = () => {
    const started = Date.now();
    return Promise.race([
      page.evaluate((last) => !!document.querySelector("[data-chat-scroll]")?.textContent?.includes(last), last).then((drawn) => ({ ms: Date.now() - started, drawn })),
      new Promise((done) => setTimeout(() => done({ ms: PROBE_CAP_MS, hung: true }), PROBE_CAP_MS)),
    ]);
  };
  const samples = [];
  const started = Date.now();
  await page.goto(origin + path);
  let drawnAfter = null;
  // Until the newest message is on the page, then on for a while: a task still running is not in the list yet.
  for (let end = Date.now() + 30_000; Date.now() < end; ) {
    const s = await probe();
    samples.push(s.ms);
    if (s.drawn && drawnAfter === null) {
      drawnAfter = Date.now() - started;
      end = Date.now() + (stream ? 2000 : 10_000);
    }
    if (!s.hung) await page.waitForTimeout(250);
  }
  let deltas = 0;
  if (stream && drawnAfter !== null) {
    const reply = stream();
    let seq = items.at(-1).seq + 1;
    for (const end = Date.now() + SECS * 1000; Date.now() < end; ) {
      const written = Math.min(reply.length, ++deltas * 120);
      socket?.send(JSON.stringify({ type: "delta", machine_id: "demo", session_id: sid, text: reply.slice(0, written) }));
      if (deltas % 12 === 0) {
        const call = `live${seq}`;
        socket?.send(JSON.stringify({ type: "items", machine_id: "demo", session_id: sid, to_seq: seq + 1, items: [
          { id: `${call}#u`, seq, ts: ts(seq), role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: call, tool_input: { command: "git status" } },
          { id: `${call}#r`, seq: seq + 1, ts: ts(seq + 1), role: "user", kind: "tool_result", tool_use_id: call, text: "clean" },
        ] }));
        seq += 2;
      }
      const s = await probe();
      samples.push(s.ms);
      if (!s.hung) await page.waitForTimeout(80);
    }
  }
  // A page still stuck gets half a minute to finish the task in hand, so its length can be told.
  const tasks = await Promise.race([page.evaluate(() => window.__longTasks), new Promise((done) => setTimeout(() => done(null), 30_000))]);
  const dom = await Promise.race([
    page.evaluate(() => ({
      totalElements: document.querySelectorAll("*").length,
      chatElements: document.querySelectorAll("[data-chat-scroll] *").length,
      segments: document.querySelectorAll("[data-segment]").length,
      placeholders: document.querySelectorAll("[data-segment-placeholder]").length,
    })),
    new Promise((done) => setTimeout(() => done(null), 30_000)),
  ]);
  const shot = await page.screenshot({ path: join(output, `${name}.png`), timeout: 10_000 }).then(() => `${name}.png`, () => null); // a stuck page draws nothing
  const hung = samples.filter((ms) => ms >= PROBE_CAP_MS).length;
  const longestTask = tasks ? Math.max(0, ...tasks) : null;
  const ok = drawnAfter !== null && tasks !== null && hung === 0 && longestTask <= LONG_TASK_LIMIT_MS && errors.length === 0 && outside.length === 0;
  const row = { name, items: items.length, drawnAfter, deltas, longestTask, tasksOver1s: tasks?.filter((d) => d > LONG_TASK_LIMIT_MS).length ?? null,
    longTaskTotal: tasks?.reduce((a, d) => a + d, 0) ?? null, slowestProbe: Math.max(0, ...samples), probesOver5s: hung, dom, errors, outside: outside.slice(0, 5), screenshot: shot, verdict: ok ? "pass" : "fail" };
  results.scenarios.push(row);
  console.log(JSON.stringify(row));
  await context.close();
  return ok;
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") ?? process.env.PERSONA_E2E_CHROMIUM ?? "/usr/bin/google-chrome", args: ["--no-sandbox", "--no-proxy-server"] });
  const agent = await scenario(browser, "agent-open", { path: "/s/demo/demo-agent", assistant: false, items: conversation(ITEMS), last: LAST });
  const main = await scenario(browser, "main-stream", {
    path: "/",
    assistant: true,
    items: [...conversation(240), { id: "ask#0", seq: 100000, ts: ts(100000), role: "user", kind: "text", text: "Where are the branches?", client_msg_id: "ask" }],
    last: "Where are the branches?",
    stream: () => `## Branches\n\n${Array.from({ length: 80 }, (_, i) => `- fix/${2000 + i}-attendance-followups is pushed`).join("\n")}\n`,
  });
  finish(agent && main ? "pass" : "fail");
  if (!(agent && main)) process.exitCode = 1;
} catch (e) {
  results.errors.push(String(e?.stack ?? e));
  finish("fail");
  process.exitCode = 1;
} finally {
  clearTimeout(watchdog);
  await browser?.close();
}
