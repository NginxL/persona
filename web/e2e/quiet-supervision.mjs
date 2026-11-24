// Real app browser regression with synthetic HTTP/WS only; no live hub or main conversation.
// node web/e2e/quiet-supervision.mjs <web URL> --shots <directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, "");
const shots = flag("--shots");
if (!base || !shots) {
  console.error("usage: node web/e2e/quiet-supervision.mjs <web URL> --shots <directory> [--chromium <executable>]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT quiet-supervision browser verification (300s)"); process.exit(124); }, 300_000);
const AT = "2026-11-01T10:00:00Z";
const at = (seq) => new Date(Date.parse(AT) + seq * 60_000).toISOString();
const item = (seq, role, kind, extra) => ({ id: `demo-${seq}#0`, seq, ts: at(seq), role, kind, ...extra });
// i18n-ignore: fixed injected protocol markers.
const raw = '📋 督办：你派出去的任务有 1 个要你处理\n<persona-supervision level="triage" />\n这是 Persona 的督办消息\nInspect the fictional release evidence and check the example task.';
const input = item(1, "user", "text", { text: raw });
const thought = item(2, "assistant", "thinking", { text: "Checking the fictional evidence against the acceptance criteria." });
const read = item(3, "assistant", "tool_use", { tool_name: "Read", tool_use_id: "demo-read", tool_input: { file_path: "/tmp/fictional-evidence.txt" } });
const readResult = item(4, "user", "tool_result", { tool_use_id: "demo-read", text: "The synthetic checks passed." });
const finish = item(5, "assistant", "tool_use", { tool_name: "supervision_finish", tool_use_id: "demo-finish", tool_input: { visibility: "silent", summary: "Checked the example task." } });
const finishResult = item(6, "user", "tool_result", { tool_use_id: "demo-finish", text: '{"visibility":"silent","summary":"Checked the example task.","level":"triage"}' });
const agentSend = item(7, "assistant", "tool_use", { tool_name: "agent_send", tool_use_id: "demo-agent-send", tool_input: { session_id: "demo-worker", text: "Inspect the synthetic release." } });
const agentSendResult = item(8, "user", "tool_result", { tool_use_id: "demo-agent-send", text: "Sent to the example worker." });
const conversationSend = item(9, "assistant", "tool_use", { tool_name: "conversation_send", tool_use_id: "demo-conversation-send", tool_input: { conversation: "demo-worker", text: "Continue the synthetic check." } });
const conversationSendResult = item(10, "user", "tool_result", { tool_use_id: "demo-conversation-send", text: "Sent to the example conversation." });
const interactionAnswer = item(11, "assistant", "tool_use", { tool_name: "interaction_answer", tool_use_id: "demo-interaction-answer", tool_input: { interaction_id: "demo-old-question", answer: "Synthetic answer" } });
const interactionAnswerResult = item(12, "user", "tool_result", { tool_use_id: "demo-interaction-answer", text: "Recorded the synthetic answer." });
const answer = item(13, "assistant", "text", { text: "The fictional release evidence is complete. No user action is needed." });
const next = item(14, "user", "text", { text: "Now check the next example task.", queued: true, client_msg_id: "demo-next" });
const nextAnswer = item(15, "assistant", "text", { text: "I will check the next example task separately." });
const folded = (promoted) => item(2, "assistant", "fold", { id: "fold:demo-2#0", fold: { items: 3, tools: 1, thoughts: 1, through_seq: 4, until: at(4), supervision_user: promoted } });
const questions = [{ question: "Which fictional rollout should we use?", header: "Rollout", options: [{ label: "Small batch", description: "Start with the demo group" }, { label: "All examples", description: "Use the full demo set" }] }];
const pending = item(3, "assistant", "tool_use", { tool_name: "mcp__persona__ask_user", tool_use_id: "demo-question", tool_input: { questions } });

async function run(browser, size, theme, scenario) {
  const sid = `demo-${scenario}`;
  const live = scenario === "running" || scenario === "ask";
  const session = { machine_id: "demo", session_id: sid, title: "Example supervision", cwd: "/tmp/fictional-project", assistant: true, started_by_persona: true, state: live ? "working" : "idle", status: live ? "working" : "idle", updated_at: AT, state_since: AT, capabilities: { send: true, read: true } };
  const items = scenario === "waiting" ? [] : scenario === "ask" ? [input, thought, pending]
    : scenario === "running" ? [input, thought, read]
    : [input, folded(scenario === "promoted"), finish, scenario === "user" ? { ...finishResult, text: '{"visibility":"user","summary":"A fictional user decision is needed.","level":"user"}' } : finishResult, agentSend, agentSendResult, conversationSend, conversationSendResult, interactionAnswer, interactionAnswerResult, answer, next, nextAnswer];
  const context = await browser.newContext({ viewport: size === "wide" ? { width: 1280, height: 1000 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce" });
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
  const interactions = scenario === "ask" ? [{ id: "demo-question-card", machine_id: "demo", session_id: sid, session_title: "Persona", kind: "question", tool_name: "ask_user", questions, created_at: AT }] : [];
  const todo = { id: "demo-task", title: "Check the example release", origin: sid, created_at: AT, updated_at: AT, agents: [{ machine_id: "demo", session_id: "demo-worker", linked_at: AT }] };
  const worker = { machine_id: "demo", session_id: "demo-worker", title: "Example worker", cwd: "/tmp/fictional-project", state: "idle", status: "done", updated_at: AT, state_since: AT, capabilities: { send: true, read: true } };
  const waitingTodos = [
    { id: "demo-expired", title: "Answer the current question", origin: sid, created_at: AT, updated_at: AT, agents: [{ machine_id: "demo", session_id: "demo-worker", linked_at: AT }], ask: { verdict: "Which rollout?", asked_at: AT, questions: [{ question: "Which rollout?", header: "Rollout", options: [{ label: "Small batch" }, { label: "Full" }] }] } },
    { id: "demo-decision", title: "Choose the rollout", origin: sid, created_at: AT, updated_at: AT, agents: [{ machine_id: "demo", session_id: "demo-worker", linked_at: AT }], ask: { verdict: "Choose the rollout", outcome: "decide", asked_at: AT } },
    { id: "demo-delivery", title: "Accept the delivery", origin: sid, created_at: AT, updated_at: AT, agents: [], delivery: { state: "off", unblock: "user" } },
    { id: "demo-failed", title: "A task that did not start", origin: sid, created_at: AT, updated_at: AT, agents: [], start_error: "Synthetic start failure" },
    { id: "demo-unstarted", title: "An unstarted stale question", origin: sid, created_at: AT, updated_at: AT, agents: [], ask: { verdict: "An unstarted stale question", outcome: "decide", asked_at: AT } },
    { id: "demo-ack", title: "An ordinary acknowledgement", origin: sid, created_at: AT, updated_at: AT, agents: [], ask: { verdict: "Finished", outcome: "done", asked_at: AT } },
  ];
  const waitingPlan = { id: "demo-plan", conversation: sid, state: "open", tasks: [{ title: "Unstarted work", prompt: "Synthetic prompt", machine: "demo", cwd: "/tmp/fictional-project" }] };
  const todos = scenario === "waiting" ? waitingTodos : scenario === "receipt" || scenario === "silent" ? [todo] : [];
  const feed = scenario === "waiting" ? [
    { id: "demo-draft", conversation: sid, kind: "message", ref: "demo-inbox-draft", at: at(1), body: { draft: "A draft only the user may send", text: "A draft only the user may send" } },
    { id: "demo-plan-card", conversation: sid, kind: "plan", ref: waitingPlan.id, at: at(2), body: { plan: waitingPlan } },
  ] : scenario === "silent" ? [
    { id: "demo-task-card", conversation: sid, kind: "task", ref: todo.id, at: at(7), body: { wake: "created" } },
    { id: "demo-routed", conversation: sid, kind: "routed", ref: todo.id, at: at(9), body: { from: "assistant", text: "Continue the synthetic check." } },
    { id: "demo-answered", conversation: sid, kind: "answered", ref: todo.id, at: at(11), body: { items: [{ question: "Synthetic old question", answer: "Synthetic answer" }] } },
    { id: "demo-notice", conversation: sid, kind: "notice", at: at(12), body: { text: "This ordinary notice stays in the main timeline." } },
  ] : scenario === "receipt" ? [
    { id: "demo-progress", conversation: sid, kind: "report", ref: todo.id, at: AT, body: { kind: "progress", text: "Checked two of three fictional criteria.", step: 2, steps: 3, evidence: ["https://example.com/evidence"] } },
    { id: "demo-checking", conversation: sid, kind: "receipt", ref: todo.id, at: AT, body: { state: "done", title: todo.title, summary: "The example agent submitted evidence for Persona to follow up.", review: { state: "checking", criteria: 3 } } },
  ] : [];
  let seenWrites = 0;
  const seenKinds = [];
  await context.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace(/^\/api\/v1/, "");
    if (path.endsWith("/seen") && route.request().method() !== "GET") { seenWrites++; seenKinds.push("http"); }
    let body = {};
    if (path === "/machines") body = { machines: [{ id: "demo", name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") body = { sessions: [session, worker] };
    else if (path === "/interactions") body = { interactions };
    else if (path === "/assistant") body = { machine_id: "demo", main: sid, conversations: [session], tasks: [], memory: [] };
    else if (path === "/todos") body = { todos };
    else if (path === `/sessions/demo/${sid}`) body = { session };
    else if (path.endsWith("/feed")) body = { items: feed };
    else if (path.endsWith("/items")) body = { items: url.searchParams.has("from_seq") ? [thought, read, readResult] : url.searchParams.has("after_seq") ? [] : items, has_more: false };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
    else if (path === "/managers") body = { managers: [] };
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
    ws.onMessage((rawMessage) => {
      const message = JSON.parse(String(rawMessage));
      if (message.type === "seen") {
        seenWrites++;
        seenKinds.push("ws");
        ws.send(JSON.stringify({ type: "sessions", machine_id: "demo", sessions: [{ ...session, state: "idle", status: "idle", seen_at: at(20) }] }));
      }
      if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
      if (message.type === "subscribe" && scenario === "running") ws.send(JSON.stringify({ type: "delta", machine_id: "demo", session_id: sid, text: "Checking the fictional task quietly." }));
    });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.setDefaultTimeout(12_000);
  await page.goto(`${base}/`);
  await page.locator("[data-chat-scroll]").waitFor();
  const chat = page.locator("[data-chat-scroll]");
  const quiet = chat.locator("[data-supervision]");
  const tag = `${size}-${theme}-${scenario}`;
  const shot = async (name) => {
    assert.equal(await page.locator("vite-error-overlay").count(), 0, "no Vite overlay");
    assert.deepEqual(errors, [], "no page errors");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no sideways page overflow");
    await page.screenshot({ path: join(shots, `${tag}-${name}.png`) });
  };
  if (scenario === "running") {
    await quiet.waitFor();
    assert.equal(await quiet.locator(":scope > button").getAttribute("aria-expanded"), "false");
    assert(!(await chat.textContent()).includes(answer.text), "running answer is not written yet");
    await shot("folded");
    await quiet.locator(":scope > button").click();
    await quiet.locator("[data-supervision-input]").waitFor();
    assert.equal(await quiet.locator("[data-supervision-input] pre").textContent(), raw);
    await shot("expanded");
  } else if (scenario === "silent") {
    await page.getByText(next.text, { exact: true }).waitFor();
    assert.equal(await quiet.count(), 0, "a completed silent round adds no main-timeline row");
    for (const name of ["Read", "supervision_finish", "agent_send", "conversation_send", "interaction_answer"]) {
      assert.equal(await chat.locator(`[data-action-row="${name}"]`).count(), 0, `${name} stays out of the main timeline`);
    }
    assert(!(await chat.textContent()).includes(answer.text), "silent answer is hidden");
    assert(!(await chat.textContent()).includes("Synthetic old question"), "answered feed card is hidden");
    assert(!(await chat.textContent()).includes("Continue the synthetic check."), "routed feed card is hidden");
    await page.getByText("This ordinary notice stays in the main timeline.", { exact: true }).waitFor();
    await shot("main-without-silent-round");

    const mainHeader = page.locator("header").filter({ has: page.locator("[data-chat-title]") }).first();
    // i18n-ignore: wide uses the title menu; a phone uses its visible More actions button in that same header.
    if (size === "wide") await mainHeader.locator("[data-assistant-name-menu]").click();
    else await mainHeader.getByRole("button", { name: "更多操作", exact: true }).last().click();
    // i18n-ignore: the browser runs in zh-Hans and finds the localized menu item.
    const activityItem = page.getByRole("menuitem", { name: "后台活动", exact: true });
    await activityItem.waitFor();
    await page.getByRole("menu").evaluate((menu) => Promise.all(menu.getAnimations({ subtree: true }).map((animation) => animation.finished.catch(() => {}))));
    // Opening the menu can reveal the existing chat layout and repeat its own seen registration. Measure the read-only
    // activity action itself, after that unrelated transition has settled.
    const seenBeforeActivity = seenWrites;
    await activityItem.click();
    const activity = page.locator("[data-background-activity]");
    await activity.waitFor();
    const backgroundQuiet = activity.locator("[data-supervision]");
    await backgroundQuiet.waitFor();
    assert.equal(seenWrites, seenBeforeActivity, `opening Background activity marks nothing seen (${seenKinds.join(",")})`);
    assert.equal(await backgroundQuiet.locator(":scope > button").getAttribute("aria-expanded"), "false");
    assert.equal(await activity.locator('[data-activity-feed="task"]').count(), 1);
    assert.equal(await activity.locator('[data-activity-feed="routed"]').count(), 1);
    assert.equal(await activity.locator('[data-activity-feed="answered"]').count(), 1);
    await shot("background-folded");
    await backgroundQuiet.locator(":scope > button").click();
    await backgroundQuiet.locator("[data-supervision-input]").waitFor();
    assert.equal(await backgroundQuiet.locator("[data-supervision-input] pre").textContent(), raw);
    await backgroundQuiet.locator("[data-work-group] > button").first().click();
    await backgroundQuiet.locator('[data-action-row="Read"]').waitFor();
    for (const name of ["supervision_finish", "agent_send", "conversation_send", "interaction_answer"]) {
      await backgroundQuiet.locator(`[data-action-row="${name}"]`).waitFor();
    }
    assert((await backgroundQuiet.textContent()).includes(answer.text));
    assert(!(await backgroundQuiet.textContent()).includes(next.text));
    await shot("background-expanded");
    if (size === "wide") {
      // a thought that fits on its row says it there; one that does not opens in place. Rows come in their own chunk (#5155)
      await backgroundQuiet.locator('[data-thinking="words"]').waitFor();
      const thoughtRow = backgroundQuiet.locator('[data-thinking="words"] > [role="button"]');
      if (await thoughtRow.count()) await thoughtRow.click();
      await backgroundQuiet.locator('[data-action-row="Read"] > button').click();
      await backgroundQuiet.locator('[data-action-row="supervision_finish"] > button').click();
      assert((await backgroundQuiet.textContent()).includes(thought.text), "original thoughts are inspectable");
      assert((await backgroundQuiet.textContent()).includes(readResult.text), "Read shows its historical result, not today's file");
      assert((await backgroundQuiet.textContent()).includes(finishResult.text), "original finish receipt is inspectable");
      for (const result of [agentSendResult, conversationSendResult, interactionAnswerResult]) {
        await backgroundQuiet.locator(`[data-action-row="${result.tool_use_id === "demo-agent-send" ? "agent_send" : result.tool_use_id === "demo-conversation-send" ? "conversation_send" : "interaction_answer"}"] > button`).click();
        assert((await backgroundQuiet.textContent()).includes(result.text), "original tool receipt is inspectable");
      }
      await page.setViewportSize({ width: 1280, height: 1500 });
      await shot("background-trace-details");
    }
    await page.getByRole("dialog").getByRole("button").first().click();
    await activity.waitFor({ state: "hidden" });
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.waitForFunction(() => !(history.state && history.state.personaOverlay));
    await page.goto(`${base}/inbox`);
    // i18n-ignore: the Inbox entry opens the same localized Background activity list.
    const inboxEntry = page.getByRole("button", { name: "后台活动", exact: true });
    await inboxEntry.waitFor();
    const seenBeforeInbox = seenWrites;
    await inboxEntry.click();
    await activity.waitFor();
    await activity.locator("[data-background-round]").waitFor();
    assert.equal(await activity.locator("[data-background-round]").count(), 1, "Inbox opens the same quiet-round list");
    assert.equal(seenWrites, seenBeforeInbox, "the Inbox activity reader also marks nothing seen");
    await shot("background-from-inbox");
  } else if (scenario === "promoted" || scenario === "user") {
    const work = chat.locator("[data-work-group] > button").first();
    await work.waitFor();
    await work.click();
    await chat.locator('[data-action-row="supervision_finish"]').waitFor();
    assert.equal(await quiet.count(), 0, "authoritative folded promotion survives a later silent finish");
    assert((await chat.textContent()).includes(answer.text));
    await shot("visible");
  } else if (scenario === "ask") {
    await page.getByText(questions[0].question, { exact: true }).waitFor();
    assert.equal(await quiet.count(), 0, "ask_user is visible while waiting for its result");
    assert(await page.getByRole("button", { name: "Small batch", exact: false }).count() > 0, "pending answer options are visible");
    await shot("pending-question");
  } else if (scenario === "waiting") {
    const entry = page.locator('[data-tray-entry="needs"]');
    await entry.waitFor();
    await page.waitForFunction(() => document.querySelector('[data-tray-entry="needs"] span:last-child')?.textContent === "4");
    assert.equal(await entry.locator("span").last().textContent(), "4", "only direct questions, owner decisions, Send authorization and user acceptance count");
    await shot("waiting-count");
    await entry.click();
    const tray = page.locator("[data-tray-list]");
    await tray.locator("[data-needs-row]").first().waitFor();
    const rows = await tray.locator("[data-needs-row]").evaluateAll((els) => els.map((el) => el.getAttribute("data-needs-row")).sort());
    assert.deepEqual(rows, ["message:demo-draft", "task:demo-decision", "task:demo-delivery", "task:demo-expired"], "the count opens exactly the counted rows");
    assert(!(await tray.textContent()).includes("A task that did not start"));
    assert(!(await tray.textContent()).includes("An unstarted stale question"));
    assert(!(await tray.textContent()).includes("An ordinary acknowledgement"));
    assert(!(await tray.textContent()).includes("Unstarted work"));
    await shot("waiting-list");
    if (size === "wide") {
      await entry.click();
      await tray.waitFor({ state: "hidden" });
      await page.locator('[data-main-waiting="4"]').click();
      await tray.waitFor();
      const fromSidebar = await tray.locator("[data-needs-row]").evaluateAll((els) => els.map((el) => el.getAttribute("data-needs-row")).sort());
      assert.deepEqual(fromSidebar, rows, "the conversation-list count opens exactly the same rows");
    }
  } else {
    await page.getByText(next.text, { exact: true }).waitFor();
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    assert.equal(await page.locator("[data-receipt]").count(), 0, "no receipt card in the chat");
    assert.equal(await page.locator('[data-testid="progress-report"]').count(), 0, "no report line in the chat");
    await shot("no-receipt-or-report");
  }
  console.log(`PASS ${tag}: real app, synthetic data, quiet/visible boundaries verified`);
  await context.close();
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") ?? process.env.PERSONA_E2E_CHROMIUM ?? "/usr/bin/google-chrome", args: ["--no-sandbox"] });
  const only = flag("--only");
  for (const size of ["wide", "narrow"]) for (const theme of ["light", "dark"]) for (const scenario of ["silent", "running", "promoted", "user", "ask", "receipt", "waiting"]) {
    if (!only || only === `${size}-${theme}-${scenario}`) await run(browser, size, theme, scenario);
  }
  console.log("PASS quiet-supervision browser verification");
} catch (error) {
  console.error("FAIL quiet-supervision browser verification", error);
  process.exitCode = 1;
} finally {
  await browser?.close();
  clearTimeout(watchdog);
}
