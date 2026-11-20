// Real-Chrome evidence for provider request headers on Add / Edit Connect (#11075). The script starts a loopback Vite
// server, answers every hub HTTP request and WebSocket in the browser, exercises the real Settings UI, and writes
// screenshots and results outside the repository. Light / dark, desktop / phone, standard / large text.
// Usage: node web/e2e/provider-headers.mjs <artifactDir> [--chromium <executable>]
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdir, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const chromiumAt = args.includes("--chromium") ? args.splice(args.indexOf("--chromium"), 2)[1] : undefined;
const [artifacts, ...extra] = args;
assert.ok(artifacts && !extra.length, "Supply the artifact directory");
const output = resolve(artifacts);
const worktree = fileURLToPath(new URL("../../", import.meta.url));
const outside = relative(worktree, output);
assert.ok(outside === ".." || outside.startsWith(`..${"/"}`) || resolve(output) === resolve(worktree, ".."), "Save artifacts outside the worktree");
await mkdir(output, { recursive: true });
const webRoot = fileURLToPath(new URL("../", import.meta.url));
const viteBin = fileURLToPath(new URL("../node_modules/vite/bin/vite.js", import.meta.url));

const HEADER = "X-Custom-Header: browser-value";
const REPLACEMENT = "X-Custom-Header: replacement-value\nX-Team: blue";
const LEGACY_VALUES = ["saved-header-secret", "other-saved-secret"];
const KEY = "sk-browser-evidence-123456";
const AUTO = 700;
const WATCHDOG_MS = 8 * 60_000;
const RUNTIMES = [
  { id: "claude", label: "Claude Code", kind: "launcher", agent: "claude", api: "messages", model: "model-one", hint: "Claude Code sends these headers on connection tests, model discovery and model calls." },
  { id: "codex", label: "Codex", kind: "launcher", agent: "codex", api: "messages", model: "gpt-5.4", hint: "Codex sends these headers through env_http_headers on connection tests, model discovery and model calls." },
  { id: "messages", label: "Anthropic Messages", kind: "api", agent: "", api: "messages", model: "model-one", hint: "Direct API sends these headers on connection tests, model discovery and model calls." },
  { id: "responses", label: "OpenAI Responses", kind: "api", agent: "", api: "responses", model: "model-one", hint: "Direct API sends these headers on connection tests, model discovery and model calls." },
  { id: "chat", label: "Chat Completions", kind: "api", agent: "", api: "chat", model: "model-one", hint: "Direct API sends these headers on connection tests, model discovery and model calls." },
];
const AGY = { id: "agy", label: "Antigravity", kind: "launcher", agent: "agy", api: "messages", model: "model-one", hint: "Antigravity cannot send custom request headers. Clear them before testing or using this provider." };
const VARIANTS = [
  { name: "desktop-light-standard", width: 1280, height: 900, theme: "light", textSize: "standard", runtime: "claude", full: true },
  { name: "desktop-light-large", width: 1280, height: 900, theme: "light", textSize: "large", runtime: "codex" },
  { name: "desktop-dark-standard", width: 1280, height: 900, theme: "dark", textSize: "standard", runtime: "messages" },
  { name: "desktop-dark-large", width: 1280, height: 900, theme: "dark", textSize: "large", runtime: "responses" },
  { name: "phone-light-standard", width: 390, height: 844, theme: "light", textSize: "standard", runtime: "chat" },
  { name: "phone-light-large", width: 390, height: 844, theme: "light", textSize: "large", runtime: "claude" },
  { name: "phone-dark-standard", width: 390, height: 844, theme: "dark", textSize: "standard", runtime: "codex" },
  { name: "phone-dark-large", width: 390, height: 844, theme: "dark", textSize: "large", runtime: "chat", antigravity: true },
];
const ACTIVE_VARIANTS = VARIANTS.filter((variant) => !process.env.PROVIDER_HEADERS_ONLY || variant.name === process.env.PROVIDER_HEADERS_ONLY);
const LAPTOP = { id: "laptop", name: "Laptop", online: true, os: "darwin" };
const result = { status: "running", browser: "", origin: "", assertions: [], runs: [], screenshots: [], layouts: [] };
let browser;
let vite;
let viteOutput = "";
let activeTag = "startup";
let step = "start";
let timedOut = false;

function mark(message) {
  result.assertions.push(message);
  console.log(`PASS ${message}`);
}
async function eventually(read, message, timeout = 12_000) {
  const until = Date.now() + timeout;
  let value;
  while (Date.now() < until) {
    value = read();
    if (value) return value;
    await delay(50);
  }
  throw new Error(`${message}; last value: ${JSON.stringify(value)}`);
}
const settle = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
async function freePort() {
  const server = createServer();
  await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}
async function startVite() {
  const port = await freePort();
  vite = spawn(process.execPath, [viteBin, "--host", "127.0.0.1", "--port", String(port), "--strictPort"], { cwd: webRoot, stdio: ["ignore", "pipe", "pipe"] });
  vite.stdout.on("data", (chunk) => { viteOutput += String(chunk); });
  vite.stderr.on("data", (chunk) => { viteOutput += String(chunk); });
  const origin = `http://127.0.0.1:${port}`;
  const until = Date.now() + 30_000;
  while (Date.now() < until) {
    if (vite.exitCode !== null || vite.signalCode) throw new Error(`Vite exited before ready:\n${viteOutput}`);
    try {
      const response = await fetch(origin, { signal: AbortSignal.timeout(1000) });
      if (response.ok) return origin;
    } catch { /* Vite is still starting. */ }
    await delay(100);
  }
  throw new Error(`Vite did not become ready:\n${viteOutput}`);
}
async function stopVite() {
  if (!vite || vite.exitCode !== null || vite.signalCode) return;
  const exited = once(vite, "exit");
  vite.kill("SIGTERM");
  await Promise.race([exited, delay(3000)]);
  if (vite.exitCode === null && !vite.signalCode) {
    const killed = once(vite, "exit");
    vite.kill("SIGKILL");
    await killed;
  }
}
function provider(runtime) {
  return {
    name: "saved-provider",
    display_name: `Saved ${runtime.label}`,
    source: "persona",
    kind: runtime.kind,
    ...(runtime.kind === "launcher" ? { agent: runtime.agent } : {}),
    api: runtime.api,
    base_url: "https://gateway.example.com/v1",
    has_key: true,
    auth: runtime.kind === "api" && runtime.api !== "messages" ? "api_key" : "bearer",
    model: runtime.model,
    models: [{ id: runtime.model, name: "Saved model", context_window: 200000 }],
    has_headers: true,
    header_names: ["X-Custom-Header", "X-Team"],
    // Deliberately emulate a legacy / buggy hub. The web editor must never put these values in the DOM.
    headers: `X-Custom-Header: ${LEGACY_VALUES[0]}\nX-Team: ${LEGACY_VALUES[1]}`,
  };
}

const watchdog = setTimeout(() => {
  timedOut = true;
  result.status = "timeout";
  result.error = `Watchdog expired at ${step}`;
  void browser?.close().catch(() => {});
  void stopVite().catch(() => {});
}, WATCHDOG_MS);

try {
  step = "start Vite";
  result.origin = await startVite();
  step = "launch Chrome";
  browser = await chromium.launch(chromiumAt ? { executablePath: chromiumAt, args: ["--no-sandbox"] } : { channel: "chrome" });
  result.browser = `Chrome ${browser.version()}`;

  for (const variant of ACTIVE_VARIANTS) {
    step = `${variant.name} create context`;
    const touch = variant.width < 500;
    const context = await browser.newContext({ viewport: { width: variant.width, height: variant.height }, locale: "en-US", colorScheme: variant.theme, reducedMotion: "reduce", isMobile: touch, hasTouch: touch, deviceScaleFactor: 1 });
    await context.addInitScript(({ theme, textSize }) => {
      localStorage.setItem("persona.token", "browser-evidence");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.text-size", textSize);
    }, { theme: variant.theme, textSize: variant.textSize });
    const requests = [];
    let saved = provider(RUNTIMES[0]);
    const external = [];
    await context.route(/^https?:\/\/(?!127\.0\.0\.1|localhost|\[::1\])/, (route) => {
      external.push(route.request().url());
      return route.abort();
    });
    await context.route("**/api/v1/**", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const path = url.pathname.replace("/api/v1", "");
      const method = request.method();
      let body;
      if (request.postData()) {
        try { body = JSON.parse(request.postData()); } catch { body = request.postData(); }
      }
      requests.push({ tag: activeTag, method, path, body });
      const json = (data, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(data) });
      if (method === "POST" && path === "/launchers/discover") return json({ models: [{ id: "model-one", name: "Discovered model", context_window: 200000 }] });
      if (method === "POST" && path === "/launchers/test") return json({ ok: true, ms: 12, model: body?.model || "model-one", auth: "bearer" });
      if (method === "POST" && path === "/launchers") return json({ launcher: { name: "created-provider", display_name: "Created provider" } });
      if (method === "PUT" && path === `/launchers/${saved.name}`) return json({ launcher: saved });
      if (method !== "GET") return json({});
      if (path === "/machines") return json({ machines: [LAPTOP] });
      if (path === "/launchers") return json({ launchers: [saved], sync: [] });
      if (path === "/launchers/presets") return json({ presets: [] });
      if (path === `/launchers/${saved.name}`) return json({ launcher: saved });
      if (path === `/launchers/${saved.name}/uses`) return json({ uses: [] });
      if (path === `/launchers/${saved.name}/quota`) return json({ unsupported: true });
      if (path === "/sessions") return json({ sessions: [] });
      if (path === "/interactions") return json({ interactions: [] });
      if (path === "/assistant") return json({ machine_id: LAPTOP.id, conversations: [], tasks: [], memory: [] });
      if (path === "/todos") return json({ todos: [] });
      return json({ error: "not_found" }, 404);
    });
    await context.routeWebSocket("**/api/v1/ws**", (ws) => {
      ws.send(JSON.stringify({ type: "hello", server_version: "browser-evidence", hub_name: "Mock hub", features: ["builtin_presets", "launcher_auto_name"] }));
      ws.onMessage((message) => { if (String(message).includes("app_ping")) ws.send(JSON.stringify({ type: "pong" })); });
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(String(error)));
    const writes = (tag) => requests.filter((r) => r.tag === tag && r.method !== "GET");
    const probes = (tag) => writes(tag).filter((r) => r.path === "/launchers/test" || r.path === "/launchers/discover");
    const saves = (tag, method) => writes(tag).filter((r) => r.method === method && (method === "POST" ? r.path === "/launchers" : r.path === `/launchers/${saved.name}`));
    const headersBox = () => page.locator('[data-provider-field="headers"] textarea');
    const headerField = () => page.locator('[data-provider-field="headers"]');
    const urlBox = () => page.locator('input[inputmode="url"]');
    const keyBox = () => page.locator('input[type="password"]');
    const testButton = () => page.getByRole("button", { name: "Test connection", exact: true });
    const discoverButton = () => page.getByRole("button", { name: "Fetch models", exact: true });

    async function shot(name, scrollTo = headerField()) {
      const before = await page.evaluate(() => {
        const field = document.querySelector('[data-provider-field="headers"]');
        const scroller = field?.closest(".overflow-y-auto");
        return { left: field?.getBoundingClientRect().left, scrollLeft: scroller?.scrollLeft, scrollWidth: scroller?.scrollWidth, clientWidth: scroller?.clientWidth };
      });
      await scrollTo.scrollIntoViewIfNeeded();
      // Playwright centers both axes. This dialog's wide stepper can make that scroll the body horizontally; restore the
      // user's normal left origin before judging the header itself, and record the before/after evidence.
      await page.evaluate(() => {
        const scroller = document.querySelector('[data-provider-field="headers"]')?.closest(".overflow-y-auto");
        if (scroller) scroller.scrollLeft = 0;
      });
      await settle(page);
      const path = `${output}/${name}.png`;
      await page.screenshot({ path });
      result.screenshots.push(path);
      const layout = await page.evaluate(() => {
        const dialog = document.querySelector('[role="dialog"]')?.getBoundingClientRect();
        const field = document.querySelector('[data-provider-field="headers"]')?.getBoundingClientRect();
        const fieldElement = document.querySelector('[data-provider-field="headers"]');
        const offenders = [...fieldElement.querySelectorAll('*')].map((el) => ({ tag: el.tagName, class: String(el.className), left: el.getBoundingClientRect().left, right: el.getBoundingClientRect().right })).filter((r) => r.left < field.left - 1 || r.right > field.right + 1);
        return { viewport: { width: innerWidth, height: innerHeight }, dialog: dialog && { left: dialog.left, right: dialog.right, top: dialog.top, bottom: dialog.bottom }, field: field && { left: field.left, right: field.right, top: field.top, bottom: field.bottom, scrollWidth: fieldElement.scrollWidth, clientWidth: fieldElement.clientWidth, overflowsOwnBox: fieldElement.scrollWidth > fieldElement.clientWidth + 1, offenders }, textSize: document.documentElement.dataset.textSize ?? "standard", rootHorizontalOverflow: document.documentElement.scrollWidth > innerWidth + 1 };
      });
      result.layouts.push({ screenshot: path, before, ...layout });
      assert.equal(layout.textSize, variant.textSize, `${variant.name}: root text size`);
      assert.ok(layout.dialog && layout.dialog.left >= -1 && layout.dialog.right <= variant.width + 1, `${name}: dialog clips horizontally`);
      assert.ok(layout.field && layout.field.left >= -1 && layout.field.right <= variant.width + 1, `${name}: header field clips horizontally`);
      assert.equal(layout.field.offenders.length, 0, `${name}: visible header content overflows its own box`);
      assert.equal(layout.rootHorizontalOverflow, false, `${name}: horizontal page overflow`);
    }
    async function choose(runtime) {
      if (runtime.kind === "api") {
        await page.getByRole("radio", { name: "Direct API", exact: true }).click();
        if (runtime.api !== "messages") await page.getByRole("radio", { name: runtime.label, exact: true }).click();
      } else if (runtime.agent !== "claude") {
        await page.getByRole("radio", { name: runtime.label, exact: true }).click();
      }
      await headerField().waitFor();
    }
    async function startAdd(runtime, tag) {
      activeTag = tag;
      step = tag;
      await page.goto(`${result.origin}/settings?tab=models&add=1`);
      await page.getByText("Custom compatible gateway", { exact: true }).click();
      await choose(runtime);
      assert.equal(await headersBox().inputValue(), "", `${tag}: new headers start empty`);
      assert.equal(probes(tag).length, 0, `${tag}: editor appears before checks`);
      assert.ok((await headerField().textContent()).includes(runtime.hint), `${tag}: runtime hint before checks`);
    }
    async function fillConnection(header = HEADER) {
      if (header !== null) await headersBox().fill(header);
      await urlBox().fill("https://gateway.example.com/v1");
      await keyBox().fill(KEY); // also leaves the URL, permitting the automatic check
    }
    async function waitAutomatic(tag, expectedHeaders) {
      await eventually(() => probes(tag).length >= 2 && page.getByText("Connected (12 ms)", { exact: true }).isVisible(), `${tag}: automatic discovery and test`);
      const automatic = probes(tag).slice(0, 2);
      assert.deepEqual(automatic.map((r) => r.path), ["/launchers/discover", "/launchers/test"], `${tag}: discovery then test`);
      for (const call of automatic) {
        if (expectedHeaders === undefined) assert.ok(!Object.hasOwn(call.body, "headers"), `${tag}: no-header automatic probe changed default`);
        else assert.equal(call.body.headers, expectedHeaders, `${tag}: automatic ${call.path} headers`);
      }
      return automatic;
    }
    async function manualChecks(tag, expectedHeaders) {
      const before = probes(tag).length;
      await testButton().click();
      await discoverButton().click();
      await eventually(() => probes(tag).length >= before + 2, `${tag}: manual checks`);
      const manual = probes(tag).slice(before, before + 2);
      assert.deepEqual(manual.map((r) => r.path), ["/launchers/test", "/launchers/discover"], `${tag}: manual operations`);
      for (const call of manual) {
        if (expectedHeaders === undefined) assert.ok(!Object.hasOwn(call.body, "headers"), `${tag}: no-header manual probe changed default`);
        else assert.equal(call.body.headers, expectedHeaders, `${tag}: manual ${call.path} headers`);
      }
      return manual;
    }
    async function exerciseAdd(runtime, screenshot) {
      const tag = `${variant.name} add ${runtime.id}`;
      await startAdd(runtime, tag);
      await fillConnection();
      const automatic = await waitAutomatic(tag, HEADER);
      for (const call of automatic) {
        assert.equal(call.body.api, runtime.api, `${tag}: automatic protocol`);
        assert.equal(call.body.agent, runtime.agent, `${tag}: automatic agent`);
      }
      mark(`${tag}: Request headers precede checks; automatic discovery/test carry X-Custom-Header for ${runtime.label}`);
      if (screenshot) await shot(`${variant.name}-add-${runtime.id}`, page.getByText("Connected (12 ms)", { exact: true }));
      await manualChecks(tag, HEADER);
      await page.getByRole("button", { name: "Next", exact: true }).click();
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await eventually(() => saves(tag, "POST").length === 1, `${tag}: POST save`);
      const body = saves(tag, "POST")[0].body;
      assert.equal(body.headers, HEADER, `${tag}: POST headers`);
      assert.equal(body.kind, runtime.kind, `${tag}: POST kind`);
      assert.equal(body.api, runtime.api, `${tag}: POST protocol`);
      if (runtime.kind === "launcher" && runtime.agent !== "claude") assert.equal(body.agent, runtime.agent, `${tag}: POST agent`);
      mark(`${tag}: manual test/discovery and Add POST carry the same headers`);
    }
    async function openEdit(runtime, tag) {
      saved = provider(runtime);
      activeTag = tag;
      step = tag;
      await page.goto(`${result.origin}/settings?tab=models&launcher=${saved.name}`);
      await page.getByRole("button", { name: "Edit", exact: true }).first().click();
      await headerField().waitFor();
      assert.equal(await headersBox().inputValue(), "", `${tag}: saved values stay out of the editor`);
      const text = await headerField().textContent();
      assert.ok(text.includes("Configured: X-Custom-Header, X-Team (values hidden)."), `${tag}: saved names shown`);
      assert.ok(text.includes(runtime.hint), `${tag}: edit runtime hint`);
      const dom = await page.evaluate(() => document.documentElement.outerHTML);
      for (const secret of LEGACY_VALUES) assert.ok(!dom.includes(secret), `${tag}: legacy raw header value entered the DOM`);
      assert.equal(probes(tag).length, 0, `${tag}: opening Edit sends no probe`);
    }
    async function replaceOpenEdit(runtime, tag) {
      await headersBox().fill(REPLACEMENT);
      const manual = await manualChecks(tag, REPLACEMENT);
      for (const call of manual) assert.equal(call.body.name, saved.name, `${tag}: stored provider named on replacement probe`);
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await eventually(() => saves(tag, "PUT").length === 1, `${tag}: replacement PUT`);
      assert.equal(saves(tag, "PUT")[0].body.headers, REPLACEMENT, `${tag}: replacement PUT headers`);
      mark(`${tag}: saved names shown without saved values; replacement reaches probes and PUT`);
    }
    async function exerciseEdit(runtime, mode, screenshot = false) {
      const tag = `${variant.name} edit ${mode} ${runtime.id}`;
      await openEdit(runtime, tag);
      if (screenshot) await shot(`${variant.name}-edit-${runtime.id}`);
      if (mode === "replacement") return replaceOpenEdit(runtime, tag);
      if (mode === "unchanged") {
        await delay(AUTO + 300);
        assert.equal(probes(tag).length, 0, `${tag}: unchanged provider checks itself`);
        await page.getByRole("button", { name: "Save", exact: true }).click();
        await eventually(() => saves(tag, "PUT").length === 1, `${tag}: unchanged PUT`);
        assert.ok(!Object.hasOwn(saves(tag, "PUT")[0].body, "headers"), `${tag}: unchanged Save dropped stored headers`);
        mark(`${tag}: unchanged Save keeps sealed headers and still exposes no values`);
        return;
      }
      await page.getByRole("button", { name: "Clear saved headers", exact: true }).click();
      assert.ok((await headerField().textContent()).includes("Saving clears the saved headers."), `${tag}: explicit removal said`);
      await manualChecks(tag, "");
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await eventually(() => saves(tag, "PUT").length === 1, `${tag}: removal PUT`);
      assert.ok(Object.hasOwn(saves(tag, "PUT")[0].body, "headers"), `${tag}: removal must be explicit`);
      assert.equal(saves(tag, "PUT")[0].body.headers, "", `${tag}: removal PUT headers`);
      mark(`${tag}: explicit removal reaches manual probes and PUT as headers: ""`);
    }
    async function exerciseNoHeader() {
      const runtime = RUNTIMES[0];
      const tag = `${variant.name} add no-header default`;
      await startAdd(runtime, tag);
      await fillConnection(null);
      await waitAutomatic(tag, undefined);
      await manualChecks(tag, undefined);
      await page.getByRole("button", { name: "Next", exact: true }).click();
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await eventually(() => saves(tag, "POST").length === 1, `${tag}: POST save`);
      assert.ok(!Object.hasOwn(saves(tag, "POST")[0].body, "headers"), `${tag}: POST changed no-header default`);
      mark(`${tag}: automatic/manual probes and Add POST omit headers by default`);
    }
    async function exerciseAntigravity(mode, screenshot) {
      const tag = `${variant.name} ${mode} antigravity`;
      if (mode === "add") {
        await startAdd(AGY, tag);
        await fillConnection();
      } else {
        await openEdit(AGY, tag);
      }
      assert.ok((await headerField().textContent()).includes(AGY.hint), `${tag}: limitation shown`);
      assert.equal(await testButton().isDisabled(), true, `${tag}: header-dependent test must be disabled`);
      assert.equal(await discoverButton().isDisabled(), true, `${tag}: header-dependent discovery must be disabled`);
      await delay(AUTO + 400);
      assert.equal(probes(tag).length, 0, `${tag}: Antigravity sent a header-dependent check`);
      if (screenshot) await shot(`${variant.name}-antigravity-${mode}`, testButton());
      mark(`${tag}: limitation is visible and no header-dependent check is sent`);
    }

    const visualRuntime = RUNTIMES.find((runtime) => runtime.id === variant.runtime);
    await exerciseAdd(visualRuntime, true);
    await exerciseEdit(visualRuntime, "replacement", true);
    if (variant.full) {
      for (const runtime of RUNTIMES.filter((item) => item.id !== visualRuntime.id)) await exerciseAdd(runtime, false);
      for (const runtime of RUNTIMES.filter((item) => item.id !== visualRuntime.id)) await exerciseEdit(runtime, "replacement");
      for (const runtime of RUNTIMES) await exerciseEdit(runtime, "removal");
      await exerciseEdit(RUNTIMES[0], "unchanged");
      await exerciseNoHeader();
      await exerciseAntigravity("add", true);
      await exerciseAntigravity("edit", true);
    }
    if (variant.antigravity) await exerciseAntigravity("add", true);
    assert.deepEqual(errors, [], `${variant.name}: page errors`);
    result.runs.push({ name: variant.name, textSize: variant.textSize, runtime: variant.runtime, pageErrors: errors, externalRequestsBlocked: external.length });
    await context.close();
  }
  if (!timedOut) result.status = "pass";
} catch (error) {
  result.status = timedOut ? "timeout" : "fail";
  result.error = `${step}: ${String(error?.stack || error)}`;
} finally {
  clearTimeout(watchdog);
  if (browser) await browser.close().catch(() => {});
  await stopVite().catch((error) => {
    result.status = "fail";
    result.error = `${result.error ? `${result.error}\n` : ""}Vite cleanup: ${String(error)}`;
  });
  await writeFile(`${output}/results.json`, JSON.stringify(result, null, 2));
  console.log(`${result.status.toUpperCase()} provider headers: ${result.assertions.length} assertion groups, ${result.screenshots.length} screenshots${result.error ? `\n${result.error}` : ""}`);
  process.exit(result.status === "pass" ? 0 : 1);
}
