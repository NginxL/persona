// Every shared chat sync case (spec/fixtures/sync, README.md) in a real browser against the mock hub playing it
// (#2777, #1073 step 4): the real web app, its store, socket, composer and cards, with nothing faked in the page. The
// mock (MOCK_SYNC_CASE, scripts/mock/sync_case.py) holds the case's hub state, answers the chat's reads, holds the writes
// until the case replies and sends only the frames the case lists; this runner asks it for each step, does the step's
// user action on the page, moves the page's clock for its `wait`, and checks the README's three rules and the step's
// `expect` (messages, cards, streamed text: the projection of web/src/views/chat/sync/project.ts; requests: what the
// mock saw). At the end it lets every page settle and compares it with the case's `end`. The chat's state (`status`)
// has no mark on the page and is not compared.
//
//   (cd web && npx vite build)
//   MOCK_WEB=web/dist MOCK_SYNC_CASE=sends.json#send_tied_by_echo PORT=19051 python3 scripts/mock/mock_hub.py &
//   node web/e2e/sync-cases.mjs http://127.0.0.1:19051 --hub http://127.0.0.1:19051 [--case <name>] [--out <dir>] [--browser <path>]
//
// The mock serves the built app itself (MOCK_WEB with a case): through a dev server's proxy, a write the case cuts comes
// back as the proxy's own error response, which the page rightly takes as a definite failure, not an unknown one.
//
// One device per browser context, signed in with `mock-<device>` (the mock's device token). One line per case and a
// verdict; results.json in --out with every difference. Exits 1 on a failure KNOWN does not list, or a KNOWN case that
// passes (take it off the list), and after 20 minutes in any case.
import assert from "node:assert/strict";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = new URL(args[0] ?? "");
const hub = new URL(flag("--hub") ?? "");
for (const u of [base, hub]) assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(u.hostname), "loopback servers only");
const only = flag("--case");
const out = resolve(flag("--out") ?? "sync-cases-results");
const SPEC = new URL("../../spec/fixtures/sync/", import.meta.url);
const BOX = "[data-chat-dock] textarea, [data-new-conversation] textarea";

/** Cases the page does not pass yet: the bug, and what every failure of the case must say. A known case that fails for
 *  another reason, or passes, fails the run: the fixed bug comes off this list, and a new one is not hidden. */
const KNOWN = {};

const labels = async (file) => JSON.parse(await readFile(new URL(`../src/i18n/locales/en/${file}.json`, import.meta.url)));
const interactionsText = await labels("chat-interactions");
const outboxText = await labels("chat-outboxbubbles");
const chatText = await labels("chat-chatview");

async function mock(path, body) {
  const r = await fetch(new URL(path, hub), body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  return { status: r.status, body: await r.json() };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/** What a page shows, as a case's view: project.ts in the page. `sent`: the words of this device's sends; one back in
 *  the box counts as a failed copy. */
function project(sent) {
  const text = (el) => (el.textContent ?? "").replace(/\s+/g, " ").trim();
  const messages = [];
  let live = null;
  for (const el of document.querySelectorAll("[data-user-bubble], [data-answer], [data-queue-row]")) {
    if (el.hasAttribute("data-answer")) {
      if (el.dataset.answer === "streaming") live = text(el);
      else messages.push({ from: "agent", text: text(el) });
      continue;
    }
    if (el.hasAttribute("data-queue-row")) {
      const words = el.querySelector("[data-queue-row-body]");
      messages.push({ from: "user", text: text(words), state: "pending" });
      continue;
    }
    const state = el.dataset.sendState;
    const words = el.querySelector("[data-bubble-text]");
    messages.push({ from: "user", text: words ? text(words) : "", state: state === "failed" ? "failed" : state ? "pending" : "shown" });
  }
  const box = document.querySelector("[data-chat-dock] textarea, [data-new-conversation] textarea")?.value ?? "";
  for (const words of sent) if (box.includes(words)) messages.push({ from: "user", text: words, state: "failed" });
  const cards = {};
  for (const el of document.querySelectorAll("[data-answered]")) cards[el.dataset.answered] = "answered";
  for (const el of document.querySelectorAll("[data-card]")) {
    const enabled = [...el.querySelectorAll("[data-card-actions] button, button[data-card-answer]")].some((b) => !b.disabled && !b.closest("fieldset[disabled]"));
    cards[el.dataset.card] = enabled ? "open" : "answering";
  }
  return { messages, cards, live };
}

function differences(got, want) {
  const diffs = [];
  if (want.messages) {
    const listed = (ms) => ms.map((m) => `${m.from}:${m.state ?? ""}:${m.text}`);
    if (JSON.stringify(listed(got.messages)) !== JSON.stringify(listed(want.messages))) diffs.push(`messages ${JSON.stringify(listed(got.messages))}, want ${JSON.stringify(listed(want.messages))}`);
  }
  if (want.cards) {
    const open = (cs) => Object.keys(cs).filter((id) => cs[id] === "open").sort();
    if (JSON.stringify(open(got.cards)) !== JSON.stringify(open(want.cards))) diffs.push(`open cards ${JSON.stringify(open(got.cards))}, want ${JSON.stringify(open(want.cards))}`);
  }
  if (want.live !== undefined && (got.live ?? null) !== want.live) diffs.push(`live ${JSON.stringify(got.live)}, want ${JSON.stringify(want.live)}`);
  return diffs;
}

/** The requests since the last check, matched in order against what the case names (others may come between). */
function matchRequests(want, seen, before) {
  let at = 0;
  // one_of: any one of these, where the protocol allows several ways (#2770); an op's receipt goes by the write's id
  const one = (w, s) => {
    if (w.one_of) return w.one_of.some((o) => one(o, s));
    if (w.get === "/ops") return s.get === "/ops" && s.key === w.id;
    if (w.get) return s.get === w.get && (w.after_seq === undefined || s.after_seq === w.after_seq);
    if (w.ws) return s.ws === w.ws && (w.since_seq === undefined || s.since_seq === w.since_seq);
    if (w.post !== s.post) return false;
    if (w.new_op) return s.op !== undefined && !before.has(s.op);
    return s.key === w.id;
  };
  for (const w of want) {
    const k = seen.findIndex((s, i) => i >= at && one(w, s));
    if (k < 0) return `no ${JSON.stringify(w)} in ${JSON.stringify(seen.slice(at))}`;
    at = k + 1;
  }
  return null;
}

function route(chat) {
  if (chat.new) return "/persona/new";
  if (chat.assistant) return `/persona/${encodeURIComponent(chat.session_id)}`;
  return `/s/${encodeURIComponent(chat.machine_id)}/${encodeURIComponent(chat.session_id)}`;
}

async function loadCases() {
  const cases = [];
  for (const f of (await readdir(SPEC)).filter((n) => n.endsWith(".json")).sort()) {
    for (const c of JSON.parse(await readFile(new URL(f, SPEC))).cases) cases.push({ file: f, ...c });
  }
  return cases;
}

/** Moves every page's clock by `ms` (its timers run), a little at a time with real time between for the network. */
async function pass(pages, ms) {
  for (let left = ms; left > 0; left -= 500) {
    await Promise.all(pages.map((p) => p.clock.runFor(Math.min(500, left))));
    await sleep(20);
  }
  await sleep(150);
}

/** A step from the mock while the pages' clocks run: an `open` waits for a page's reconnect, a `reply` for its write. */
async function nextStep(pages) {
  let done = false;
  const asked = mock("/mock/case/step", {}).finally(() => (done = true));
  for (let i = 0; i < 240 && !done; i++) {
    await Promise.all(pages.map((p) => p.clock.runFor(250).catch(() => {})));
    await sleep(50);
  }
  return asked;
}

async function openChat(page, chat) {
  await page.goto(new URL(route(chat), base).href);
  await page.waitForSelector(chat.new ? "[data-new-conversation]" : "[data-chat-scroll]", { timeout: 20_000 });
}

async function typeAndSend(page, words) {
  const box = page.locator(BOX).first();
  await box.fill(words);
  await box.press("Enter");
}

/** The main conversation mounts its cards in Pending; ordinary chats have no tray. Keep it open for reads and answers. */
async function showCards(page) {
  const pending = page.locator('[data-assistant-actionbar] [data-tray-entry="pending"][aria-expanded="false"]');
  if (await pending.count()) await pending.click();
}

/** Taps a failed bubble's Retry or Delete for the message with these words (by the button's text, as the jsdom
 *  consumer does). */
async function onFailed(page, words, label) {
  // A refusal may have asked something first (the computer's input box has text: "Clear and send" / "Not now"); the
  // user closes it to use the bubble's own controls.
  if (await page.locator('[role="dialog"]').isVisible()) await page.keyboard.press("Escape");
  const entry = page.locator("[data-outbox]").filter({ has: page.locator('[data-user-bubble][data-send-state="failed"] [data-bubble-text]', { hasText: words }) });
  await entry.locator("button").filter({ hasText: new RegExp(`^${label}$`) }).first().click({ timeout: 10_000 });
}

async function play(browser, c) {
  const devices = {};
  try {
    return await playOn(browser, c, devices);
  } catch (e) {
    for (const [name, d] of Object.entries(devices)) {
      await d.page.screenshot({ path: join(out, `${c.name}-${name}.png`) }).catch(() => {});
    }
    throw e;
  } finally {
    // a page left open would connect to the next case as its device and take that device's socket
    for (const d of Object.values(devices)) await d.context.close().catch(() => {});
  }
}

async function playOn(browser, c, devices) {
  const loaded = await mock("/mock/case/load", { case: `${c.file}#${c.name}` });
  assert.equal(loaded.status, 200, JSON.stringify(loaded.body));
  const failures = [];
  for (const name of c.devices) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: "en-US", serviceWorkers: "block" });
    await context.addInitScript((token) => {
      localStorage.setItem("persona.token", token);
      localStorage.setItem("persona.language", "en");
    }, `mock-${name}`);
    await context.clock.install({ time: new Date("2026-01-02T03:05:00Z") });
    const page = await context.newPage();
    page.on("pageerror", (e) => failures.push(`${name}: page error ${e.message}`));
    devices[name] = { context, page, sent: [], ended: new Set(), answering: new Set(), checked: 0 };
    await openChat(page, c.chat);
  }
  const pages = Object.values(devices).map((d) => d.page);
  // The case starts with every device on the chat, loaded the normal way: subscribed (a `new` chat has nothing to
  // subscribe to yet). Its requests count from here.
  for (let i = 0; i < 60; i++) {
    await pass(pages, 250);
    const seen = (await mock("/mock/case")).body.devices;
    if (c.chat.new || c.devices.every((n) => seen[n].connected && seen[n].seen.some((s) => s.ws === "subscribe"))) {
      for (const n of c.devices) devices[n].checked = seen[n].seen.length;
      break;
    }
    if (i === 59) failures.push(`start: not every device subscribed to the chat: ${JSON.stringify(seen)}`);
  }

  const look = async (d) => {
    await showCards(d.page);
    return d.page.evaluate(project, d.sent.filter((s) => !s.discarded).map((s) => s.text));
  };
  const invariants = async (where) => {
    for (const [name, d] of Object.entries(devices)) {
      const v = await look(d);
      for (const s of d.sent) {
        if (s.discarded) continue;
        const n = v.messages.filter((m) => m.from === "user" && m.text === s.text).length;
        if (n !== 1) failures.push(`${where}: ${name}: sent message ${s.id} shows ${n} times`);
      }
      for (const [id, state] of Object.entries(v.cards)) {
        if (state === "open" && (d.ended.has(id) || d.answering.has(id))) failures.push(`${where}: ${name}: card ${id} is answerable again`);
      }
    }
  };

  for (const [i, st] of c.steps.entries()) {
    const where = `step ${i + 1} (${Object.keys(st).filter((k) => k !== "expect").join("+")})`;
    const got = await nextStep(pages);
    if (got.status !== 200) {
      failures.push(`${where}: the mock: ${got.body.error}`);
      break;
    }
    if (st.socket?.open && !st.socket.open.resumed) {
      const d = devices[st.socket.device];
      for (const id of d.answering) if (!(st.frames?.[st.socket.device] ?? []).some((f) => f.interaction?.id === id)) d.ended.add(id);
    }
    for (const [name, frames] of Object.entries(st.frames ?? {})) {
      for (const f of frames) if (f.type === "interaction_resolved") devices[name].ended.add(String(f.id));
    }
    const u = st.user;
    if (u) {
      const d = devices[u.device];
      try {
        if (u.send) {
          d.sent.push({ id: u.send.id, text: u.send.text });
          await typeAndSend(d.page, u.send.text);
        } else if (u.start) {
          d.sent.push({ id: u.start.id, text: u.start.text, start: true });
          await typeAndSend(d.page, u.start.text);
        } else if (u.retry && d.sent.find((s) => s.id === u.retry)?.start && (await d.page.locator("[data-new-conversation]").count())) {
          await typeAndSend(d.page, d.sent.find((s) => s.id === u.retry).text); // a refused start goes again from its page
        } else if (u.answer) {
          d.answering.add(u.answer.id);
          const label = u.answer.resolution.allow === false ? interactionsText.deny : interactionsText.allow;
          await showCards(d.page);
          await d.page.locator(`[data-card="${u.answer.id}"]`).getByRole("button", { name: label, exact: true }).first().click();
        } else if (u.retry) {
          await onFailed(d.page, d.sent.find((s) => s.id === u.retry).text, outboxText.retry);
        } else if (u.discard) {
          const s = d.sent.find((x) => x.id === u.discard);
          s.discarded = true;
          await onFailed(d.page, s.text, outboxText.delete);
        } else if (u.relaunch) {
          // the app starts again from what it stored; the case keeps its socket away until the step that opens it
          await d.page.reload({ waitUntil: "domcontentloaded" });
          // Reload finishes before the app mounts; drive its clock through startup before reading the chat.
          // Wait for the surface and its initial load, never for the message whose retention this case checks.
          const surface = c.chat.new ? "[data-new-conversation]" : "[data-chat-scroll]";
          for (let attempt = 0; attempt < 100; attempt++) {
            await pass([d.page], 100);
            if (await d.page.locator(surface).count() && !await d.page.getByText(chatText.loading_chat, { exact: true }).count()) break;
            if (attempt === 99) throw new Error("the chat did not finish loading after relaunch");
          }
        } else {
          failures.push(`${where}: ${JSON.stringify(u)} is not something this runner does yet`);
        }
      } catch (e) {
        failures.push(`${where}: ${u.device} could not do ${JSON.stringify(u)}: ${e.message.split("\n")[0]}`);
        await d.page.screenshot({ path: join(out, `${c.name}-step${i + 1}-${u.device}.png`) }).catch(() => {});
      }
    }
    const r = st.reply;
    if (r) {
      const d = devices[r.device];
      if (d.answering.has(r.write) && !r.cut) {
        d.answering.delete(r.write);
        if ((r.status ?? 0) < 300 || r.status === 410) d.ended.add(r.write);
      }
    }
    await pass(pages, st.wait ?? 100);
    await invariants(where);
    const state = (await mock("/mock/case")).body;
    for (const [name, want] of Object.entries(st.expect ?? {})) {
      const d = devices[name];
      for (const diff of differences(await look(d), want)) failures.push(`${where}: ${name}: ${diff}`);
      if (want.requests) {
        const seen = state.devices[name].seen;
        const before = new Set(seen.slice(0, d.checked).flatMap((s) => (s.op ? [s.op] : [])));
        const miss = matchRequests(want.requests, seen.slice(d.checked), before);
        if (miss) failures.push(`${where}: ${name}: requests: ${miss}`);
        d.checked = seen.length;
      }
    }
  }

  // the end: every page's timers run until nothing is due
  await pass(pages, 10_000);
  await invariants("end");
  for (const [name, want] of Object.entries(c.end)) {
    for (const diff of differences(await look(devices[name]), want)) failures.push(`end: ${name}: ${diff}`);
  }
  return failures;
}

await mkdir(out, { recursive: true });
const health = await mock("/mock/case");
assert.equal(health.status, 200, "the mock hub needs MOCK_SYNC_CASE (any case: this runner loads each in turn)");
const browserPath = flag("--browser");
const browser = await chromium.launch(browserPath ? { executablePath: browserPath } : { headless: true });
const stop = setTimeout(() => {
  console.log("FAIL: 20 minutes passed");
  process.exit(1);
}, 20 * 60_000);
const results = [];
let bad = 0;
try {
  for (const c of await loadCases()) {
    if (only && c.name !== only) continue;
    let failures;
    try {
      failures = await play(browser, c);
    } catch (e) {
      failures = [`the runner: ${e.message.split("\n")[0]}`];
    }
    const known = KNOWN[c.name];
    let verdict = failures.length ? "fail" : "ok";
    if (known) {
      if (!failures.length) verdict = "fail (passes now: take it off KNOWN)";
      else if (failures.some((f) => !known.because.test(f))) verdict = `fail (not only ${known.issue})`;
      else verdict = `known (${known.issue})`;
    }
    if (verdict.startsWith("fail")) bad++;
    results.push({ case: c.name, file: c.file, verdict, failures });
    console.log(`${verdict.padEnd(6)} ${c.file} ${c.name}${failures.length ? "\n  " + failures.join("\n  ") : ""}`);
  }
} finally {
  clearTimeout(stop);
  await browser.close();
  await writeFile(join(out, "results.json"), JSON.stringify(results, null, 2));
}
console.log(bad ? `FAIL: ${bad} of ${results.length} cases` : `OK: ${results.length} cases`);
process.exit(bad ? 1 : 0);
