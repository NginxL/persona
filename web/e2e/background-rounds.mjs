// Real app browser check of background rounds in the main conversation (#4314), with synthetic HTTP/WS only.
// node web/e2e/background-rounds.mjs <web URL> --shots <directory> [--folded on|off] [--expect before|after] [--chromium <executable>]
// --folded: the hub's main_background_folded (on by default). --expect before: only take screenshots (a build without
// the feature); after: also assert the folded and opened states.
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, "");
const shots = flag("--shots");
const folded = (flag("--folded") ?? "on") === "on";
const expect = flag("--expect") ?? "after";
if (!base || !shots) {
  console.error("usage: node web/e2e/background-rounds.mjs <web URL> --shots <directory> [--folded on|off] [--expect before|after] [--chromium <executable>]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT background-rounds browser verification (180s)"); process.exit(124); }, 180_000);
const AT = "2026-11-01T10:00:00Z";
const sid = "demo-main";
const item = (seq, role, kind, extra) => ({ id: `demo-${seq}#0`, seq, ts: AT, role, kind, ...extra });
const source = (meta) => `<persona-context kind="message-source">${JSON.stringify(meta)}</persona-context>\n`;
const prompt = "Read every example inbox, list what changed since yesterday and draft a three-line brief.";
const items = [
  item(1, "user", "text", { text: "Plan the example launch for Friday.", client_msg_id: "demo-1" }),
  item(2, "assistant", "text", { text: "Here is the plan for the example launch on Friday." }),
  item(3, "user", "text", { text: source({ kind: "conversation", conversation_id: "demo-side", title: "Release desk" }) + "Check that the fictional release notes mention the new export button." }),
  item(4, "assistant", "tool_use", { tool_name: "mcp__persona__notify_user", tool_use_id: "demo-notify-failed", tool_input: { text: "The export button is listed." } }),
  item(5, "user", "tool_result", { tool_use_id: "demo-notify-failed", text: "Unavailable", is_error: true }),
  item(6, "assistant", "text", { text: "The fictional release notes mention the export button." }),
  item(7, "user", "text", { text: prompt, injected: { kind: "followup", state: "daily", title: "Morning brief", line: "Scheduled task “Morning brief” started", detail: prompt }, source: { kind: "patrol", title: "Morning brief" } }),
  item(8, "assistant", "text", { text: "Checking the example inboxes.", narration: true }),
  item(9, "user", "text", { text: prompt, injected: { kind: "followup", state: "daily", title: "Evening brief", line: "Scheduled task “Evening brief” started", detail: prompt }, source: { kind: "patrol", title: "Evening brief" } }),
  item(10, "assistant", "tool_use", { tool_name: "mcp__persona__notify_user", tool_use_id: "demo-notify", tool_input: { text: "Two example invoices are due tomorrow." } }),
  item(11, "user", "tool_result", { tool_use_id: "demo-notify", text: "sent" }),
  item(12, "assistant", "text", { text: "I told you about the two example invoices due tomorrow." }),
  item(13, "user", "text", { text: "Thanks, what is left for the launch?", client_msg_id: "demo-13" }),
  item(14, "assistant", "text", { text: "Only the example screenshots are left." }),
];

async function run(browser, size, theme) {
  const session = { machine_id: "demo", session_id: sid, title: "Persona", cwd: "/tmp/fictional-project", assistant: true, started_by_persona: true, state: "idle", status: "idle", updated_at: AT, state_since: AT, capabilities: { send: true, read: true } };
  const side = { ...session, session_id: "demo-side", title: "Release desk" };
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
    if (path === "/machines") body = { machines: [{ id: "demo", name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") body = { sessions: [session, side] };
    else if (path === "/interactions") body = { interactions: [] };
    else if (path === "/assistant") body = { machine_id: "demo", main: sid, conversations: [session, side], tasks: [], memory: [], main_background_folded: folded };
    else if (path === "/todos") body = { todos: [] };
    else if (path === `/sessions/demo/${sid}`) body = { session };
    else if (path.endsWith("/feed")) body = { items: [] };
    else if (path.endsWith("/items")) body = { items: url.searchParams.has("after_seq") ? [] : items, has_more: false };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
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
  await page.goto(`${base}/`);
  const chat = page.locator("[data-chat-scroll]");
  await chat.waitFor();
  await page.getByText("Only the example screenshots are left.", { exact: false }).waitFor();
  const tag = `${size}-${theme}`;
  const shot = async (name) => {
    assert.equal(await page.locator("vite-error-overlay").count(), 0, "no Vite overlay");
    assert.deepEqual(errors, [], "no page errors");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no sideways page overflow");
    await page.screenshot({ path: join(shots, `${tag}-${name}.png`) });
  };
  await shot("chat");
  if (expect === "after") {
    const rounds = chat.locator("[data-background-round]");
    const text = await chat.textContent();
    assert(text.includes("Only the example screenshots are left."), "the user's own rounds stay shown");
    assert(text.includes("Two example invoices") || text.includes("two example invoices"), "a round that told the user stays shown");
    if (folded) {
      assert.equal(await rounds.count(), 1, "only the round without a final reply folds");
      assert(!text.includes(prompt), "no scheduled prompt in the main chat");
      const answer = chat.getByText("The fictional release notes mention the export button.", { exact: true });
      assert.equal(await answer.count(), 1, "the completed reply is visible despite the failed notification");
      assert.equal(await answer.locator("xpath=ancestor::*[@data-supervision]").count(), 0);
      assert.equal(await chat.locator("[data-work-group] > button[aria-expanded=false]").count(), 2, "both tool traces stay closed");
      await rounds.locator(":scope > button").click();
      const opened = await chat.textContent();
      assert(opened.includes("Checking the example inboxes."), "the scheduled round's work shows when opened");
      assert(!opened.includes(prompt), "the scheduled prompt stays hidden when opened");
      assert(!opened.includes("persona-context"), "an opened round draws its inputs as source blocks, not raw model input");
      await shot("opened");
    } else {
      assert.equal(await rounds.count(), 0, "nothing folds with the switch off");
      assert(text.includes("The fictional release notes mention the export button."));
    }
  }
  if (expect === "after") {
    // Settings › Assistant: the switch shows the hub's value
    await page.goto(`${base}/assistant`);
    const toggle = page.getByRole("switch", { name: "后台轮次默认折叠" }); // i18n-ignore: the zh-Hans label under test
    await toggle.waitFor();
    assert.equal(await toggle.getAttribute("aria-checked"), String(folded));
    await toggle.scrollIntoViewIfNeeded();
    await shot("settings");
  }
  console.log(`PASS ${tag} (${expect}, folded ${folded ? "on" : "off"})`);
  await context.close();
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") ?? process.env.PERSONA_E2E_CHROMIUM ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
  for (const size of ["wide", "narrow"]) for (const theme of ["light", "dark"]) await run(browser, size, theme);
  console.log("PASS background-rounds browser verification");
} catch (error) {
  console.error("FAIL background-rounds browser verification", error);
  process.exitCode = 1;
} finally {
  await browser?.close();
  clearTimeout(watchdog);
}
