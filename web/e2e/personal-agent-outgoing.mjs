// Personal Agent full-text recovery in the real app, with synthetic HTTP/WS only and no live hub.
// node web/e2e/personal-agent-outgoing.mjs <loopback-web URL> --shots <artifact-directory> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => { const i = args.indexOf(name); return i < 0 ? undefined : args[i + 1]; };
const base = args[0]?.replace(/\/$/, "");
const shots = flag("--shots");
if (!base || !shots) {
  console.error("usage: node web/e2e/personal-agent-outgoing.mjs <loopback-web URL> --shots <artifact-directory> [--chromium <executable>]");
  process.exit(2);
}
const origin = new URL(base);
assert(origin.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(origin.hostname), "only an owned loopback HTTP web server is allowed");
assert(!origin.username && !origin.password && !origin.search && !origin.hash, "the web URL must not contain credentials");
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => { console.error("TIMEOUT personal-agent-outgoing browser verification (180s)"); process.exit(124); }, 180_000);

const id = "example:recovery";
const fixture = { id, source: "example", sessionKey: "recovery", accountId: "example-account", provider: "example", title: "Recovery Agent", canSend: true, unreadCount: 0, rankTimeMs: 1 };
const text = `Browser recovery keeps this line\n${"x".repeat(240)}\nand this final line`;
const failureText = "Synthetic browser send failure";
const results = [];

async function run(browser, theme, textSize, width) {
  const context = await browser.newContext({ viewport: width === "wide" ? { width: 1440, height: 900 } : { width: 390, height: 844 }, colorScheme: theme, reducedMotion: "reduce", locale: "en-US" });
  await context.addInitScript(({ theme, textSize }) => {
    localStorage.setItem("persona.token", "synthetic-browser-credential");
    localStorage.setItem("persona.tokenId", "synthetic-browser-device");
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.text-size", textSize);
  }, { theme, textSize });
  const sends = [];
  const requests = [];
  let clientId = "";
  await context.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api\/v1/, "");
    requests.push(`${request.method()} ${path}`);
    if (path === `/agents/sessions/${encodeURIComponent(id)}/messages` || path === `/agents/sessions/${id}/messages`) {
      if (request.method() === "POST") {
        const body = request.postDataJSON();
        sends.push(body);
        clientId = body.clientId;
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: failureText, delivered: "none" }) });
      } else {
        await route.fulfill({ json: { session: fixture, messages: [], hasMore: false } });
      }
      return;
    }
    if (path.endsWith("/read")) {
      await route.fulfill({ json: {} });
      return;
    }
    if (path === "/agents/outbox") {
      const outbox = clientId ? [{ clientId, sessionId: id, preview: text.slice(0, 200), length: text.length, origin: "app", status: "failed", error: failureText, attempts: sends.length, createdAt: "2026-11-21T00:00:00Z", updatedAt: "2026-11-21T00:00:00Z" }] : [];
      await route.fulfill({ json: { outbox } });
      return;
    }
    if (request.method() !== "GET") throw new Error(`unexpected synthetic write: ${request.method()} ${path}`);
    let body = {};
    if (path === "/agents/sessions") {
      body = url.searchParams.has("counts")
        ? { total: 1, revision: "1", state: { idle: 1 }, machine: {}, project: {}, group: {} }
        : { sessions: [fixture], items: [], total: 1, has_more: false, revision: "1", remove: [] };
    } else if (path === "/agents") body = { sources: [{ source: "example", enabled: true, accounts: [{ id: "example-account" }] }] };
    else if (path === "/agents/prefs") body = {};
    else if (path === "/agents/display-settings") body = { show_botmux: true, configured: true };
    else if (path === "/agents/hidden") body = { sessions: [] };
    else if (path === "/machines" || path === "/sessions" || path === "/extensions" || path === "/skills") body = { [path.slice(1)]: [] };
    else if (path === "/managers") body = { managers: [], default_id: "" };
    else if (path === "/assistant") body = { machine_id: "", conversations: [] };
    else if (path === "/extensions/ui") body = { slots: [] };
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await context.routeWebSocket("**/api/v1/ws**", (ws) => {
    ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: ["sessions_list"] }));
    ws.onMessage((raw) => {
      const message = JSON.parse(String(raw));
      if (message.type === "app_ping") ws.send(JSON.stringify({ type: "pong" }));
    });
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.setDefaultTimeout(15_000);
  try {
    await page.goto(`${base}/a/${encodeURIComponent(id)}`);
    const composer = page.locator("textarea");
    await composer.waitFor();
    await composer.fill(text);
    await page.getByRole("button", { name: "Send", exact: true }).click();
    const failure = page.getByText(/Synthetic browser send failure/);
    await failure.waitFor();
    assert.equal(await composer.inputValue(), "");
    assert.equal(sends.length, 1);
    assert(clientId);

    await page.reload();
    await failure.waitFor();
    const recoveredBubble = page.getByText(text, { exact: true });
    await recoveredBubble.waitFor();
    assert(await recoveredBubble.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), "the full restored line wraps inside its bubble");
    assert.equal(sends.length, 1, "reload must not resend");
    const restored = await page.evaluate(() => Object.entries(localStorage).filter(([key]) => key.includes(":agent-outgoing-v1:")).map(([, value]) => JSON.parse(value)));
    assert.equal(restored.length, 1);
    assert.deepEqual(restored[0].entries.map((entry) => [entry.clientId, entry.text, entry.status]), [[clientId, text, "failed"]]);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth), false, "no horizontal overflow");
    await page.screenshot({ path: join(shots, `personal-agent-outgoing-${theme}-${textSize}.png`), fullPage: true });

    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await failure.waitFor();
    assert.equal(sends.length, 2);
    assert.deepEqual(sends[1], sends[0], "Retry preserves full text, origin and client id");
    await page.getByRole("button", { name: "Discard", exact: true }).click();
    await page.getByText(text, { exact: true }).waitFor({ state: "detached" });
    await page.reload();
    await composer.waitFor();
    assert.equal(await page.getByText(text, { exact: true }).count(), 0, "Discard survives reload and outbox metadata");
    assert.equal(await page.getByRole("button", { name: "Retry", exact: true }).count(), 0);
    assert.equal(sends.length, 2, "Discard and reload never resend");
    assert.deepEqual(errors, []);
    results.push({ theme, textSize, width, clientIdPreserved: sends[0].clientId === sends[1].clientId, fullTextPreserved: sends[0].text === text && sends[1].text === text, sends: sends.length, discarded: true, errors });
    console.log(`PASS ${theme} ${textSize} ${width}: full text restored; same-id retry; durable discard; no automatic resend`);
  } catch (error) {
    console.error(`Fixture failure at ${page.url()}\nBody: ${await page.locator("body").textContent()}\nRequests: ${requests.join(", ")}\nPage errors: ${errors.join("; ")}`);
    await page.screenshot({ path: join(shots, `personal-agent-outgoing-failure-${theme}-${textSize}.png`), fullPage: true }).catch(() => {});
    throw error;
  } finally {
    await context.close();
  }
}

let browser;
try {
  browser = await chromium.launch({ executablePath: flag("--chromium") || "/usr/bin/google-chrome", headless: true });
  for (const theme of ["light", "dark"]) {
    await run(browser, theme, "standard", "wide");
    await run(browser, theme, "large", "narrow");
  }
  await writeFile(join(shots, "results.json"), JSON.stringify({ fixture: "synthetic personal Agent send failure; no live hub", results }, null, 2));
  console.log("PASS personal-agent-outgoing browser verification");
} catch (error) {
  console.error("FAIL personal-agent-outgoing browser verification", error);
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  clearTimeout(watchdog);
}
