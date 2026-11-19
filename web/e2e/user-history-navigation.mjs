// Actual web-app acceptance with an invented paginated conversation. No real hub or conversation is read.
// Usage: node web/e2e/user-history-navigation.mjs <loopback origin> <artifact directory>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const [address, directory, count = "678", conversation = "main", initialPage = "2", textSize = "standard", mode] = process.argv.slice(2);
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname));
assert.ok(!base.username && !base.password && !base.search && !base.hash && base.pathname === "/");
const output = resolve(directory); await mkdir(output, { recursive: true });
const COUNT = Number(count), MID = "example-node", SID = "synthetic-history", RAIL = "[data-nav-rail]";
assert.ok(Number.isSafeInteger(COUNT) && COUNT >= 2);
const INITIAL_MARKS = Math.min(COUNT, 50);
const INITIAL_BODIES = Math.min(COUNT, Number(initialPage));
assert.ok(Number.isSafeInteger(INITIAL_BODIES) && INITIAL_BODIES >= 2);
const inputs = Array.from({ length: COUNT }, (_, i) => ({ id: `test-input-${String(i).padStart(3, "0")}#0`, seq: i * 4 + 1 }));
const items = inputs.flatMap((input, i) => [
  { ...input, role: "user", kind: "text", client_msg_id: `test-send-${i}`, queued: i % 8 === 0,
    text: `Navigation input ${i + 1}: Keep the synthetic layout readable.${i % 3 === 0 ? '\n<persona-context kind="context" title="Fixture">Invented context only.</persona-context>' : ""}` },
  { id: `test-answer-${i}#0`, seq: input.seq + 1, role: "assistant", kind: "text", text: `Answer ${i + 1}.\n\n${Array.from({ length: 7 }, (_, n) => `Synthetic paragraph ${n + 1}. This invented test conversation supplies enough content to scroll. No account, private conversation or real project is used.`).join("\n\n")}` },
  { id: `test-auto-${i}#0`, seq: input.seq + 2, role: "user", kind: "text", text: "Synthetic automatic supervision", injected: { kind: "supervision", line: "Synthetic automatic supervision" }, source: { kind: "supervision" } },
  { id: `test-compact-${i}#0`, seq: input.seq + 3, role: "system", kind: "notice", text: "Synthetic compaction boundary" },
]);
const index = { total: COUNT, inputs, history: "synthetic-history-v1" };
const session = { machine_id: MID, session_id: SID, title: "Synthetic full-history navigation", agent: "claude", status: "idle", state: "idle", capabilities: { read: true, send: true, terminal: false } };
const machine = { id: MID, name: "Example computer", online: true };
const results = { status: "running", realConversationUsed: false, messagesSent: 0, forwardedWrites: 0, runs: [], errors: [] };
let browser;
const deadline = setTimeout(() => { console.error("TIMEOUT full-history navigation"); process.exit(1); }, 180_000);

function pageFor(before = 0) {
  const eligible = inputs.filter((input) => !before || input.seq < before);
  const pageSize = before ? 50 : INITIAL_BODIES;
  const start = eligible.length > pageSize ? eligible.at(-pageSize).seq : eligible[0]?.seq ?? 0;
  return { items: items.filter((item) => item.seq >= start && (!before || item.seq < before)), has_more: eligible.length > pageSize, history: index.history, to_seq: before ? before - 1 : items.at(-1).seq };
}

try {
  browser = await chromium.launch({ channel: "chrome", headless: true, ignoreDefaultArgs: ["--hide-scrollbars"] });
  for (const theme of ["light", "dark"]) for (const size of ["wide", "narrow"]) {
    const run = { name: `${size}-${theme}`, serverTotal: COUNT, initialBodies: 0, beforeTicks: 0, afterTicks: 0, olderReads: 0, pageErrors: [], screenshots: [] };
    results.runs.push(run);
    const context = await browser.newContext({ viewport: size === "wide" ? { width: 1600, height: 900 } : { width: 1024, height: 820 }, colorScheme: theme, locale: "en-US", hasTouch: false, serviceWorkers: "block" });
    await context.addInitScript(({ theme, textSize }) => { localStorage.setItem("persona.token", "synthetic-only"); localStorage.setItem("persona.language", "en"); localStorage.setItem("persona.theme", theme); localStorage.setItem("persona.text-size", textSize); }, { theme, textSize });
    let release;
    const ready = new Promise((done) => { release = done; });
    let olderGate;
    let olderStarted;
    let failNextOlder = false;
    await context.route("**/*", async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== base.origin) return route.abort("blockedbyclient");
      if (!["GET", "HEAD"].includes(request.method())) return route.fulfill({ json: { ok: true } });
      if (!url.pathname.startsWith("/api/")) return route.continue();
      const path = url.pathname.replace(/^\/api\/v1/, "");
      if (path.endsWith("/items") && url.searchParams.get("user_inputs") === "1") { await ready; return route.fulfill({ json: index }); }
      if (path.endsWith("/items")) {
        const before = Number(url.searchParams.get("before_seq") ?? 0);
        if (before) {
          run.olderReads++;
          olderStarted?.();
          if (olderGate) await olderGate;
          if (failNextOlder) {
            failNextOlder = false;
            return route.fulfill({ status: 400, json: { code: "bad_request", error: "Synthetic older-page failure" } });
          }
        }
        const after = Number(url.searchParams.get("after_seq") ?? 0);
        return route.fulfill({ json: after ? { items: [], history: index.history, to_seq: items.at(-1).seq, more_after: false } : pageFor(before) });
      }
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
    await context.routeWebSocket("**/*", socket => {
      assert.equal(new URL(socket.url()).host, base.host);
      socket.send(JSON.stringify({ type: "hello", server_version: "mock", protocol: 1, features: [] }));
      socket.send(JSON.stringify({ type: "machines", machines: [machine] }));
      socket.send(JSON.stringify({ type: "sessions", machine_id: MID, sessions: [session] }));
      socket.onMessage(raw => { const event = JSON.parse(String(raw)); if (event.type === "app_ping") socket.send(JSON.stringify({ type: "pong", id: event.id })); });
    });
    const page = await context.newPage(); page.setDefaultTimeout(20_000);
    page.on("pageerror", error => run.pageErrors.push(error.message));
    await page.goto(new URL(conversation === "main" ? "/" : `/s/${MID}/${SID}`, base).href);
    await page.locator(`${RAIL} button[data-tick]`).first().waitFor();
    await page.waitForFunction(count => document.querySelectorAll("[data-user-bubble]").length === count, INITIAL_BODIES);
    const chat = page.locator("[data-chat-scroll]"); const box = await chat.boundingBox();
    await page.mouse.move(box.x + 40, box.y + box.height / 2); await page.mouse.wheel(0, -80);
    await page.waitForFunction(() => Number(getComputedStyle(document.querySelector("[data-nav-rail]")).opacity) > 0.95);
    run.initialBodies = await page.locator("[data-user-bubble]").count();
    run.beforeTicks = await page.locator(`${RAIL} button[data-tick]`).count(); assert.equal(run.beforeTicks, Math.min(INITIAL_BODIES, 50));
    const shot = async state => { const name = `history-${run.name}-${state}.png`; await page.screenshot({ path: join(output, name) }); run.screenshots.push(name); };
    await shot("before-two-loaded");
    release();
    await page.waitForFunction(([rail, total]) => document.querySelector(rail)?.dataset.navTotal === String(total) && document.querySelector(rail)?.dataset.navSource === "server", [RAIL, COUNT]);
    run.afterTicks = await page.locator(`${RAIL} button[data-tick]`).count(); assert.equal(run.afterTicks, INITIAL_MARKS);
    assert.equal(await page.locator("[data-user-bubble]").count(), INITIAL_BODIES, "Index loading must not load every transcript body");
    assert.equal(run.olderReads, 0);
    const widths = await page.locator(`${RAIL} [data-nav-mark]`).evaluateAll(marks => marks.map(mark => parseFloat(mark.style.width)));
    assert.ok(widths.every(width => width === 6), "All idle marks stay equally short");
    await shot("after-server-total");
    const first = page.locator(`${RAIL} [data-tick='0']`);
    const firstOrdinal = COUNT - INITIAL_MARKS + 1;
    assert.ok((await first.getAttribute("aria-label")).includes(String(firstOrdinal)), "Labels use full-history ordinals");
    await first.hover(); assert.equal(run.olderReads, 0, "Hover does not fetch old transcript bodies");
    await page.locator("[data-nav-preview]").waitFor(); await shot("hover");
    assert.ok((await page.locator("[data-nav-preview]").textContent()).includes(String(firstOrdinal)));
    const started = Date.now();
    if (mode === "slow") {
      failNextOlder = true;
      let resume;
      olderGate = new Promise(resolve => { resume = resolve; });
      await page.evaluate(() => {
        window.navFrameGaps = [];
        let last = performance.now();
        const frame = now => {
          window.navFrameGaps.push(now - last);
          last = now;
          window.navFrame = requestAnimationFrame(frame);
        };
        window.navFrame = requestAnimationFrame(frame);
      });
      await first.click();
      await page.locator("[data-nav-loading]").waitFor();
      run.feedbackMs = Date.now() - started;
      await first.locator("[data-nav-mark^='current']").waitFor();
      assert.equal(await first.getAttribute("aria-current"), null, "The pending mark is highlighted without moving the current position");
      await shot("loading");
      resume();
      olderGate = undefined;
      await page.getByText("This input could not be loaded. Try again.", { exact: true }).waitFor();
      await page.locator("[data-nav-loading]").waitFor({ state: "hidden" });
      // Both selections arrive during backoff, before the server receives another page request.
      const next = page.locator(`${RAIL} [data-tick='1']`);
      const readsBefore = run.olderReads;
      olderGate = new Promise(resolve => { resume = resolve; });
      const retryRequest = new Promise(resolve => { olderStarted = resolve; });
      const retryStartedAt = Date.now();
      await first.click();
      await page.locator("[data-nav-loading]").waitFor();
      await next.click();
      await next.locator("[data-nav-mark^='current']").waitFor();
      assert.equal(run.olderReads, readsBefore, "Both selections precede a retry");
      await retryRequest;
      olderStarted = undefined;
      assert.equal(run.olderReads, readsBefore + 1, "Overlapping selections share the retry");
      resume();
      olderGate = undefined;
      await page.waitForFunction(id => {
        const row = document.querySelector(`[data-line='seg:${id}']`)?.firstElementChild;
        const sc = document.querySelector("[data-chat-scroll]");
        return row && sc && Math.abs(row.getBoundingClientRect().top - sc.getBoundingClientRect().top - 12) <= 2;
      }, inputs[COUNT - INITIAL_MARKS + 1].id);
      run.retryLandingMs = Date.now() - retryStartedAt;
      run.maxFrameGapMs = await page.evaluate(() => {
        cancelAnimationFrame(window.navFrame);
        return Math.max(...window.navFrameGaps.slice(1));
      });
      await page.locator("[data-nav-loading]").waitFor({ state: "hidden" });
      assert.equal(run.olderReads, readsBefore + 1, "The latest target is in the shared page");
    }
    const warmStarted = Date.now();
    const readsBeforeRepeat = run.olderReads;
    if (mode === "slow") {
      await page.getByRole("button", { name: new RegExp(`^Jump to user input ${firstOrdinal}:`) }).click();
    } else await first.click();
    await page.waitForFunction(([id]) => {
      const row = document.querySelector(`[data-line='seg:${id}']`)?.firstElementChild;
      const sc = document.querySelector("[data-chat-scroll]");
      return row && sc && row.getBoundingClientRect().top - sc.getBoundingClientRect().top >= -2 && row.getBoundingClientRect().top - sc.getBoundingClientRect().top <= 32;
    }, [inputs[COUNT - INITIAL_MARKS].id]);
    if (mode === "slow") {
      run.repeatLandingMs = Date.now() - warmStarted;
      assert.equal(run.olderReads, readsBeforeRepeat, "Loaded repeat requires no older read");
    }
    if (INITIAL_BODIES < INITIAL_MARKS) assert.ok(run.olderReads > 0, "The unloaded target must fetch earlier pages before positioning");
    await page.mouse.move(box.x + 40, box.y + box.height / 2);
    await page.waitForFunction(() => !document.querySelector("[data-nav-preview]"));
    if (COUNT > 50) {
      await chat.evaluate(el => { el.scrollTop = 1; });
      await page.mouse.wheel(0, -500);
      await page.waitForFunction(() => document.querySelectorAll("[data-nav-rail] button[data-tick]").length > 50);
      run.afterScrollTicks = await page.locator(`${RAIL} button[data-tick]`).count();
      assert.ok(run.afterScrollTicks > INITIAL_MARKS && run.afterScrollTicks <= COUNT);
    } else {
      assert.equal(await page.locator(`${RAIL} button[data-tick]`).count(), COUNT);
    }
    await page.waitForFunction(() => [...document.querySelectorAll("[data-nav-mark]")].every(mark => parseFloat(getComputedStyle(mark).width) === 6));
    await shot("earlier-loaded");
    assert.deepEqual(run.pageErrors, []);
    run.selectedInputPositioned = true;
    console.log(`PASS ${run.name}: initial user bodies=${run.initialBodies}, marks ${run.beforeTicks}->${run.afterTicks}->${run.afterScrollTicks ?? COUNT}, global total=${run.serverTotal}, selected input=${firstOrdinal}, backward reads=${run.olderReads}`);
    await context.close();
  }
  results.status = "pass";
} catch (error) { results.status = "fail"; results.errors.push(error.message); process.exitCode = 1; }
finally {
  clearTimeout(deadline); await browser?.close();
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2));
  console.log(`${results.status.toUpperCase()} full-history user navigation`);
}
