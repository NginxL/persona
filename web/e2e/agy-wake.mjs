// Run against a local Persona Web server: node web/e2e/agy-wake.mjs <base URL> <output directory> [Chrome path].
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright-core";
import { createAgyRuntime } from "../../integrations/antigravity/lib/runtime.js";
import { startFakeNode } from "../../integrations/runtime-sdk/testing/fake-node.js";

const [base, shots, executablePath] = process.argv.slice(2);
assert(base && ["127.0.0.1", "localhost"].includes(new URL(base).hostname) && shots, "local URL and screenshot directory required");
const id = "11111111-2222-4333-8444-555555555555", sid = "agy-" + id;
const steps = readFileSync(new URL("../../integrations/antigravity/test/fixtures/autonomous-wake.jsonl", import.meta.url), "utf8").trim().split("\n").map(line => JSON.parse(line));
const wakeLabel = JSON.parse(readFileSync(new URL("../../server/internal/i18n/locales/en/hub-transcript_text.json", import.meta.url), "utf8")).task_updated;
const chart = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAKAAAAA2CAIAAADPkgUkAAAAmUlEQVR42u3cwQ0AEBREQQ3pTTdadHN01QTJhkleA3auP8qYSw9XTABYgAVYgAVYgAX4SrX14wEGDBiwAAswYMCAAQMGDPgr4JxZAQMGDBgwYMCAAQMGDBgwYMCAAWcBpz0DMGDAgAEDBgwYMGDAgAEDBgwYMGDAgAEDBgwYMGDAgAEDBgwYMGA3WfKNkgALsAADNgFgAVZqG5avnJJOD+9jAAAAAElFTkSuQmCC", "base64");

// Exercise the runtime's public RPC against a changing native export, including the cursor past a pending notification.
async function readPages() {
  const home = mkdtempSync(join(tmpdir(), "persona-agy-wake-"));
  const agyDir = join(home, "agy"), logs = join(agyDir, "brain", id, ".system_generated", "logs");
  mkdirSync(logs, { recursive: true });
  const publish = n => writeFileSync(join(logs, "transcript_full.jsonl"), steps.slice(0, n).map(step => JSON.stringify(step)).join("\n") + "\n");
  publish(3);
  const agyBin = join(home, "fake-agy");
  writeFileSync(agyBin, "#!/bin/sh\nexit 0\n", { mode: 0o700 });
  const node = await startFakeNode();
  const runtime = createAgyRuntime({ home, agyDir, agyBin, personadBin: join(home, "missing-personad"), socketPath: node.path, log: () => {} });
  try {
    runtime.start();
    await node.ready;
    const page = (params = {}) => node.peer.call("session.items", { session_id: sid, ...params });
    const before = await page();
    publish(5);
    const pending = await page();
    publish(6);
    const update = await page({ after_seq: pending.to_seq }), after = await page();
    assert.deepEqual(pending.items, before.items);
    assert.deepEqual(after.items.slice(0, before.items.length), before.items);
    assert.deepEqual(after.items.slice(before.items.length), update.items);
    assert.deepEqual(update.items.map(item => [item.seq, item.kind, !!item.wake]), [[6, "notice", true], [6, "text", false]]);
    const english = page => ({ ...page, items: page.items.map(item => item.wake ? { ...item, text: wakeLabel } : item) });
    return { before: english(before), update: english(update), after: english(after) };
  } finally {
    await runtime.stop();
    node.close();
    rmSync(home, { recursive: true, force: true });
  }
}

async function verify(browser, pages, theme, textSize) {
  const context = await browser.newContext({ viewport: { width: 1100, height: 860 }, colorScheme: theme, reducedMotion: "reduce" });
  try {
    await context.addInitScript(({ theme, textSize }) => {
      localStorage.setItem("persona.token", "synthetic-session");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
      if (textSize) localStorage.setItem("persona.text-size", textSize);
    }, { theme, textSize });
    let backlog = pages.before, socket;
    const session = { machine_id: "example", session_id: sid, title: "Background report", agent: "agy", cwd: "/synthetic", assistant: true, started_by_persona: true, status: "idle", state: "idle", running: true, updated_at: steps[5].created_at, capabilities: { read: true, send: true } };
    await context.route("**/*", async route => {
      const url = new URL(route.request().url());
      if (url.origin !== new URL(base).origin) return route.abort();
      if (!url.pathname.startsWith("/api/v1/")) return route.continue();
      const path = url.pathname.slice("/api/v1".length);
      if (path === "/machines/example/file/thumb") {
        assert.equal(url.searchParams.get("path"), "/synthetic/chart.png");
        return route.fulfill({ contentType: "image/png", body: chart });
      }
      let body = {};
      if (path === "/machines") body = { machines: [{ id: "example", name: "Example computer", online: true, os: "linux" }] };
      else if (path === "/sessions") body = { sessions: [session] };
      else if (path === "/assistant") body = { machine_id: "example", main: sid, conversations: [session], tasks: [], memory: [] };
      else if (path === `/sessions/example/${sid}`) body = { session };
      else if (path.endsWith("/items")) body = backlog;
      else if (path.endsWith("/feed")) body = { items: [] };
      else if (path === "/interactions") body = { interactions: [] };
      else if (path === "/extensions/ui") body = { slots: [] };
      else if (path === "/extensions") body = { extensions: [] };
      else if (path === "/managers") body = { managers: [] };
      await route.fulfill({ json: body });
    });
    await context.routeWebSocket("**/api/v1/ws**", ws => {
      socket = ws;
      ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] }));
      ws.onMessage(raw => { if (JSON.parse(String(raw)).type === "app_ping") ws.send(JSON.stringify({ type: "pong" })); });
    });
    const page = await context.newPage(), errors = [];
    page.on("pageerror", error => errors.push(String(error)));
    await page.goto(base);
    assert.equal(await page.locator("html").getAttribute("data-text-size"), textSize || null);
    const chat = page.locator("[data-chat-scroll]"), slots = chat.locator("[data-latest-text]");
    const answer = slots.filter({ hasText: "Here is the finished report." }), chartImage = answer.getByRole("img", { name: "Chart" });
    const checkFirst = async () => {
      await chartImage.waitFor();
      assert.equal(await chartImage.evaluate(img => img.complete && img.naturalWidth), 160);
      assert(await answer.getByText(/file:.*chart\.png/).isVisible());
    };
    await checkFirst();
    await answer.evaluate(el => { el.dataset.answered = "true"; });
    backlog = pages.after;
    socket.send(JSON.stringify({ ...pages.update, type: "items", machine_id: "example", session_id: sid }));
    const checkBoth = async () => {
      await slots.getByText("The check is complete.", { exact: true }).waitFor();
      await chat.getByText(wakeLabel, { exact: true }).waitFor();
      await checkFirst();
      assert.equal(await slots.count(), 2);
      assert.equal(await chat.locator("[data-work-detail]:visible").count(), 0);
      assert.equal(await chat.getByText("Synthetic private scheduling context.").count(), 0);
    };
    await checkBoth();
    assert.equal(await answer.getAttribute("data-answered"), "true");
    await page.reload();
    await checkBoth();
    assert.deepEqual(errors, []);
    await page.screenshot({ path: join(shots, `web-${theme}${textSize ? "-large" : ""}.png`) });
  } finally { await context.close(); }
}

mkdirSync(shots, { recursive: true });
const pages = await readPages();
const browser = await chromium.launch({ ...(executablePath ? { executablePath } : {}), args: ["--no-sandbox"] });
try {
  for (const theme of ["light", "dark"]) for (const textSize of ["", "large"]) await verify(browser, pages, theme, textSize);
  console.log("PASS native export, live push and cold page in the main chat, in both themes and text sizes");
} finally { await browser.close(); }
