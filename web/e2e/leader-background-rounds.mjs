// Real browser check of background-round folding in a leader conversation (#6595), synthetic HTTP/WS only.
// Cloned from background-rounds.mjs (#4314): the same fold mechanism, but the chat is one of the hub's leaders.
// node web/e2e/leader-background-rounds.mjs <web URL> --shots <directory> [--expect before|after] [--chromium <executable>]
// before: the mock names no leader, so the chat is an ordinary side conversation and patrol/relay rounds stay expanded
// (supervision rounds still fold, as they always have in Persona chats). after: the chat is a leader and folds them.
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, "");
const shots = flag("--shots");
const expect = flag("--expect") ?? "after";
if (!base || !shots) {
  console.error("usage: node web/e2e/leader-background-rounds.mjs <web URL> --shots <directory> [--expect before|after] [--chromium <executable>]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT leader-background-rounds browser verification (180s)"); process.exit(124); }, 180_000);
const AT = "2026-11-13T10:00:00Z";
const machine = "demo";
const main = "demo-main";
const leader = "demo-leader";
const item = (seq, role, kind, extra) => ({ id: `demo-${seq}#0`, seq, ts: AT, role, kind, ...extra });
const source = (meta) => `<persona-context kind="message-source">${JSON.stringify(meta)}</persona-context>\n`;
const patrolPrompt = "Run the fictional half-hour patrol and say whether anything changed.";
const interject =
  '<persona-context kind="context" title="插话" url="">\n用户在你这一轮还没做完时发来下面这条消息。\n</persona-context>\n';

// The leader's transcript: one owner exchange, a relayed round, a patrol round, a supervision digest and a round that
// asks the owner something. Only the owner exchange and the asking round may stay open after #6595.
const leaderItems = [
  item(1, "user", "text", { text: interject + "把这周的验收口径定一下。", client_msg_id: "owner-1" }),
  item(2, "assistant", "text", { text: "本周按三条口径验收。" }),
  item(3, "user", "text", { text: source({ kind: "conversation", conversation_id: "demo-worker", title: "Release desk" }) + "Check that the fictional release notes mention the new export button." }),
  item(4, "assistant", "tool_use", { tool_name: "Read", tool_use_id: "demo-read", tool_input: { file_path: "/tmp/fictional-notes.md" } }),
  item(5, "user", "tool_result", { tool_use_id: "demo-read", text: "The export button is listed." }),
  item(6, "assistant", "text", { text: "The fictional release notes mention the export button." }),
  item(7, "user", "text", { text: patrolPrompt, injected: { kind: "followup", state: "daily", title: "Half-hour patrol", line: "Scheduled task “Half-hour patrol” started", detail: patrolPrompt }, source: { kind: "patrol", title: "Half-hour patrol" } }),
  item(8, "assistant", "text", { text: "The patrol found nothing new." }),
  item(9, "user", "text", { text: "📋 督办：你派出去的任务有 1 个要你处理\n这是 Persona 的督办消息，不是用户发的。", injected: { kind: "system", state: "supervise", level: "triage", line: "Supervision: 1 task to handle" } }),
  item(10, "assistant", "text", { text: "Handled the supervised task silently." }),
  item(11, "user", "text", { text: source({ kind: "conversation", conversation_id: "demo-worker-2", title: "Rollout desk" }) + "Decide whether the fictional rollout goes to 100%." }),
  item(12, "assistant", "tool_use", { tool_name: "mcp__persona__ask_user", tool_use_id: "demo-ask", tool_input: { question: "Roll the fictional release out to everyone?" } }),
  item(13, "user", "tool_result", { tool_use_id: "demo-ask", text: "waiting" }),
];
const mainItems = [
  item(1, "user", "text", { text: "Plan the example launch for Friday.", client_msg_id: "main-1" }),
  item(2, "assistant", "text", { text: "Here is the plan for the example launch on Friday." }),
];

async function run(browser, size, theme) {
  const session = (sid, title) => ({ machine_id: machine, session_id: sid, title, cwd: "/tmp/fictional-project", assistant: true, started_by_persona: true, state: "idle", status: "idle", updated_at: AT, state_since: AT, capabilities: { send: true, read: true } });
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1280, height: 1100 } : { width: 390, height: 1100 }, colorScheme: theme, reducedMotion: "reduce" });
  await context.addInitScript(({ theme, now }) => {
    const NativeDate = Date;
    globalThis.Date = class extends NativeDate {
      constructor(...args) { super(...(args.length ? args : [now])); }
      static now() { return now; }
    };
    localStorage.setItem("persona.token", "synthetic-demo-token");
    localStorage.setItem("persona.language", "zh-Hans");
    localStorage.setItem("persona.theme", theme);
  }, { theme, now: Date.parse(AT) + 1000 });
  await context.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace(/^\/api\/v1/, "");
    let body = {};
    if (path === "/machines") body = { machines: [{ id: machine, name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") body = { sessions: [session(main, "Persona"), session(leader, "产品主管")] };
    else if (path === "/interactions") body = { interactions: [] };
    else if (path === "/assistant") body = { machine_id: machine, main, conversations: [session(main, "Persona"), session(leader, "产品主管")], tasks: [], memory: [], main_background_folded: true, ...(expect === "after" ? { leaders: [leader] } : {}) };
    else if (path === `/sessions/${machine}/${leader}`) body = { session: session(leader, "产品主管") };
    else if (path === `/sessions/${machine}/${main}`) body = { session: session(main, "Persona") };
    else if (path.endsWith("/feed")) body = { items: [] };
    else if (path.endsWith("/items")) body = { items: url.searchParams.has("after_seq") ? [] : (path.includes(leader) ? leaderItems : mainItems), has_more: false };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
    else if (path === "/todos") body = { todos: [] };
    else if (path === "/managers") body = { managers: [] };
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
    ws.onMessage((rawMessage) => {
      const message = JSON.parse(String(rawMessage));
      if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
    });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.setDefaultTimeout(12_000);
  await page.goto(`${base}/persona/${leader}`);
  const chat = page.locator("[data-chat-scroll]");
  await chat.waitFor();
  await page.getByText("本周按三条口径验收。", { exact: false }).waitFor();
  const tag = `${size}-${theme}`;
  const shot = async (name) => {
    assert.equal(await page.locator("vite-error-overlay").count(), 0, "no Vite overlay");
    assert.deepEqual(errors, [], "no page errors");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no sideways page overflow");
    await page.screenshot({ path: join(shots, `${tag}-${name}.png`) });
  };
  await shot("chat");
  const rounds = chat.locator("[data-supervision]");
  const text = await chat.textContent();
  // the owner's own exchange stays shown, and the interject note never becomes a quote chip
  assert(text.includes("把这周的验收口径定一下。"), "the owner's own message stays shown");
  assert(!text.includes("插话"), "the interject note is not rendered as an owner-bubble quote");
  assert(text.includes("本周按三条口径验收。"), "the leader's reply to the owner stays shown");
  // the asking round is promoted open even in a leader chat: its input stays readable rather than being folded away
  // (the synthetic hub serves no interaction card, so assert on the round's own text, not the card's chrome)
  assert(text.includes("Decide whether the fictional rollout goes to 100%."), "a round that asks the user stays shown");
  if (expect === "after") {
    // relayed + patrol + supervision: three folded rounds; the asking round and the owner exchange are not among them.
    // Pure supervision rows carry data-supervision without data-background-round (no message source), so count the former.
    assert.equal(await rounds.count(), 3, "the relayed, patrol and supervision rounds fold in a leader chat");
    assert.equal(await chat.locator("[data-background-round]").count(), 2, "the two sourced rounds carry their source kind");
    assert(!text.includes(patrolPrompt), "no scheduled patrol prompt in the leader chat");
    assert(!text.includes("The fictional release notes mention the export button."), "the folded relayed answer is hidden");
    assert(!text.includes("The patrol found nothing new."), "the folded patrol answer is hidden");
    await chat.locator("[data-background-round]").nth(0).locator(":scope > button").click();
    await page.getByText("The fictional release notes mention the export button.", { exact: false }).waitFor();
    const opened = await chat.textContent();
    assert(opened.includes("Check that the fictional release notes mention the new export button."), "the relayed message shows when opened");
    assert(!opened.includes("persona-context"), "an opened round draws source blocks, not raw model input");
    await shot("opened");
  } else {
    assert.equal(await rounds.count(), 1, "before #6595 only the supervision round folds in a side conversation");
    assert(text.includes("The patrol found nothing new."), "the patrol round stays expanded before #6595");
    assert(text.includes("The fictional release notes mention the export button."), "the relayed round stays expanded before #6595");
  }
  console.log(`PASS ${tag} (${expect})`);
  await context.close();
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") ?? process.env.PERSONA_E2E_CHROMIUM ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
  for (const size of ["wide", "narrow"]) for (const theme of ["light", "dark"]) await run(browser, size, theme);
  console.log("PASS leader-background-rounds browser verification");
} catch (error) {
  console.error("FAIL leader-background-rounds browser verification", error);
  process.exitCode = 1;
} finally {
  await browser?.close();
  clearTimeout(watchdog);
}
