// Load the real served Web assets, but answer every API/socket request with browser-only fixtures.
// No credentials or writes reach the hub; typing a draft never submits an answer.
// Usage: node web/e2e/needs-you-regressions.mjs <loopback-origin> <artifact-dir> [--chromium <executable>]
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const executablePath = args.includes("--chromium") ? args.splice(args.indexOf("--chromium"), 2)[1] : undefined;
const [address, artifacts, ...extra] = args;
assert.ok(address && artifacts && !extra.length, "Supply the loopback Web origin and artifact directory");
const base = new URL(address);
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(base.hostname), "Use a loopback Web server");
const output = resolve(artifacts);
await mkdir(output, { recursive: true });
const health = await fetch(new URL("/api/v1/health", base)).then((r) => r.json());
const at = new Date().toISOString();
const main = "regression-main";
const machine = { id: "example", name: "Browser-only fixture", online: true, os: "linux" };
const primary = { machine_id: machine.id, session_id: main, title: "Persona", state: "idle", status: "idle", cwd: "/example", updated_at: at, assistant: true, capabilities: { send: true, approve: true } };
const worker = { machine_id: machine.id, session_id: "off-page", title: "Regression Agent (browser-only fixture)", state: "needs_you", status: "blocked", cwd: "/example", updated_at: at, source_kind: "runtime", capabilities: { read: true, send: true, approve: true }, pending_interactions: 1, waiting: true };
const todo = { id: "regression-task", title: "Regression task (browser-only fixture)", origin: main, created_at: at, updated_at: at, agents: [{ machine_id: machine.id, session_id: worker.session_id }], supervision: { supervisor: main } };
const question = { id: "regression-question", machine_id: machine.id, session_id: worker.session_id, kind: "question", agent: "codex", created_at: at, questions: [{ question: "Type a draft only. Do not submit this browser-only check.", options: null }] };
const result = { status: "running", servedCommit: health.commit, runs: [] };
const browser = await chromium.launch({ ...(executablePath ? { executablePath } : { channel: "chrome" }), args: ["--no-sandbox"] });
try {
  for (const [width, height] of [[1365, 900], [390, 844]]) for (const theme of ["light", "dark"]) {
    const name = `${width}x${height}-${theme}`;
    const context = await browser.newContext({ viewport: { width, height }, locale: "en-US", colorScheme: theme, reducedMotion: "reduce", isMobile: width < 500, hasTouch: width < 500, serviceWorkers: "block" });
    await context.addInitScript(({ theme }) => {
      localStorage.setItem("persona.token", "browser-only-fixture");
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("agent.open", "page");
    }, { theme });
    const writes = [];
    const lookups = [];
    const assets = [];
    await context.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== base.origin) return route.abort();
      if (!url.pathname.startsWith("/api/v1/")) return route.continue();
      if (request.method() !== "GET") {
        writes.push({ method: request.method(), path: url.pathname });
        return route.fulfill({ json: {} });
      }
      const path = url.pathname.slice("/api/v1".length);
      let data = {};
      if (path === "/health") data = { ...health, features: ["sessions_list", "sessions_lookup"] };
      else if (path === "/machines") data = { machines: [machine] };
      else if (path === "/assistant") data = { machine_id: machine.id, main, conversations: [primary], tasks: [], memory: [] };
      else if (path === "/todos") data = { todos: [todo] };
      else if (path === "/interactions") data = { interactions: [question] };
      else if (path.endsWith("/feed")) data = { items: [], plans: {}, task_cards: [] };
      else if (path === "/sessions" && url.searchParams.has("counts")) data = { total: 0, machine: {}, project: {}, group: {}, state: {}, revision: "r1" };
      else if (path === "/sessions") {
        const exact = url.searchParams.get("machine_id") === machine.id && url.searchParams.get("session_id") === worker.session_id;
        if (exact) lookups.push(url.search);
        data = { sessions: exact ? [worker] : [], total: exact ? 1 : 0, has_more: false, revision: "r1" };
      } else if (path.endsWith("/items")) data = { items: [{ id: "fixture-line", seq: 1, role: "assistant", kind: "text", text: "Browser-only verification. No answer has been submitted.", ts: at }], has_more: false };
      else if (path === `/sessions/${machine.id}/${main}`) data = { session: primary };
      else if (path === `/sessions/${machine.id}/${worker.session_id}`) data = { session: worker };
      return route.fulfill({ json: data });
    });
    await context.routeWebSocket("**/api/v1/ws**", (socket) => {
      socket.send(JSON.stringify({ type: "hello", features: ["sessions_list", "sessions_lookup"], hub_name: "Browser-only fixture" }));
      socket.onMessage((message) => {
        if (String(message).includes("app_ping")) socket.send(JSON.stringify({ type: "pong" }));
      });
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(String(error)));
    page.on("response", (response) => {
      if (new URL(response.url()).pathname.startsWith("/assets/")) assets.push(response.url());
    });
    await page.goto(new URL("/", base).href);
    const tray = page.locator("[data-assistant-actionbar]");
    await tray.locator('[data-tray-entry="needs"]').click();
    const row = tray.locator(`[data-needs-row="task:${todo.id}"]`);
    await row.click();
    const input = row.locator(`[data-card="${question.id}"] input`);
    await input.waitFor();
    const enabled = await input.isEnabled();
    if (enabled) {
      await input.pressSequentially("Draft only - not submitted", { delay: 25 });
      assert.equal(await input.inputValue(), "Draft only - not submitted");
      assert.ok(await input.evaluate((el) => el === document.activeElement));
    }
    await page.screenshot({ path: `${output}/${name}-typing.png`, fullPage: true });
    if (enabled) await input.fill("");
    await row.getByRole("button").filter({ hasText: todo.title }).first().click();
    await page.waitForTimeout(200);
    const navigated = new URL(page.url()).pathname === `/s/${machine.id}/${worker.session_id}`;
    if (navigated) await page.getByText("Browser-only verification. No answer has been submitted.", { exact: true }).waitFor();
    await page.screenshot({ path: `${output}/${name}-navigation.png`, fullPage: true });
    const answerWrites = writes.filter((w) => /\/(resolve|accept|dismiss|send|message)$/.test(w.path));
    result.runs.push({ name, enabled, navigated, url: page.url(), lookups, answerWrites, interceptedWrites: writes, errors, assets });
    await context.close();
  }
  result.status = result.runs.every((r) => r.enabled && r.navigated && r.lookups.length > 0 && !r.answerWrites.length && !r.errors.length) ? "passed" : "failed";
} catch (error) {
  result.status = "error";
  result.error = String(error);
} finally {
  await browser.close();
  await writeFile(`${output}/result.json`, JSON.stringify(result, null, 2));
}
console.log(JSON.stringify({ status: result.status, servedCommit: result.servedCommit, runs: result.runs.map(({ name, enabled, navigated, answerWrites, errors }) => ({ name, enabled, navigated, answerWrites, errors })), artifactDir: output }, null, 2));
assert.equal(result.status, "passed", result.error ?? "See result.json for the regression evidence");
