// Run against a loopback mock with MOCK_SKIP_OCCURRENCE=1, or legacy with --legacy.
// node web/e2e/task-skip-occurrence.mjs [--legacy] [--shots <dir>]
// Set TASK_WEB and TASK_HUB to override the loopback ports.
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const web = process.env.TASK_WEB ?? "http://127.0.0.1:5797";
const hub = process.env.TASK_HUB ?? "http://127.0.0.1:18797";
const origins = new Set([web, hub].map((url) => {
  const parsed = new URL(url);
  assert.equal(parsed.hostname, "127.0.0.1", "use only the loopback mock");
  return parsed.origin;
}));
const legacy = process.argv.includes("--legacy");
const shotsArg = process.argv.indexOf("--shots");
const shots = shotsArg === -1 ? undefined : process.argv[shotsArg + 1];
if (shots) await mkdir(shots, { recursive: true });

const title = "Review notes";
const tasks = "/api/v1/assistant/tasks";
const skip = tasks + "/fixture-skip-occurrence/skip-next";
const runs = "/api/v1/assistant/runs";

async function call(path, method = "GET", body, status = 200) {
  const response = await fetch(hub + path, {
    method, headers: { Authorization: "Bearer mock", "Content-Type": "application/json" },
    ...(body && { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(8000),
  });
  assert.equal(response.status, status, `${method} ${path}`);
  return response.json();
}
const read = async () => (await call(tasks)).tasks[0];
const cancellation = (t) => skip + "?" + new URLSearchParams({ at: t.skip_next_at, token: t.skip_next_token });
const browser = await chromium.launch({ channel: "chrome", headless: true });
const errors = [];

async function open(width, theme) {
  const context = await browser.newContext({
    viewport: { width, height: 830 }, colorScheme: theme, reducedMotion: "reduce", hasTouch: width < 600,
  });
  await context.route("**/*", (route) => origins.has(new URL(route.request().url()).origin) ? route.continue() : route.abort());
  await context.addInitScript((appearance) => {
    localStorage.setItem("persona.theme", appearance);
    localStorage.setItem("persona.language", "en");
  }, theme);
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  page.setDefaultTimeout(15000);
  await page.goto(web + "/routines?token=mock");
  await page.getByText(title, { exact: true }).first().waitFor();
  return page;
}

const menu = (page) => page.getByRole("button", { name: `Actions for ${title}`, exact: true });
const item = (page, name) => page.getByRole("menuitem", { name });
const marker = (page) => page.getByText(/^Will skip /).first();
const result = (page, method, path = skip) => page.waitForResponse((response) =>
  new URL(response.url()).pathname === path && response.request().method() === method);
const shot = (page, name) => shots && page.screenshot({ path: join(shots, name + ".png"), animations: "disabled" });

try {
  await call("/mock/skip-occurrence/reset", "POST", {});
  const desktop = await open(1200, "light");
  if (legacy) {
    assert.equal((await read()).can_skip_next, undefined);
    await desktop.getByText(title, { exact: true }).first().click({ button: "right" });
    await item(desktop, /^Run now/).waitFor();
    assert.equal(await item(desktop, /^Skip next run/).count(), 0);
    assert.equal(await item(desktop, /^Cancel skip/).count(), 0);
    console.log("PASS legacy manager keeps its existing schedule actions");
  } else {
    const phone = await open(375, "dark");
    const initial = await read();
    assert.equal(initial.can_skip_next, true);

    await menu(desktop).click();
    await item(desktop, /^Skip next run/).waitFor();
    const { task: retimed } = await call("/mock/skip-occurrence/retime", "POST", {});
    const stale = result(desktop, "POST");
    await item(desktop, /^Skip next run/).click();
    const rejected = await stale;
    assert.equal(rejected.status(), 409);
    assert.equal(rejected.request().postDataJSON().at, initial.next_run);
    await desktop.getByText(/schedule changed/i).first().waitFor();
    assert.equal((await read()).skip_next_at, undefined);
    await desktop.getByRole("button", { name: "Refresh", exact: true }).click();
    await menu(desktop).click();
    const made = result(desktop, "POST");
    await item(desktop, /^Skip next run/).click();
    assert.equal((await made).status(), 200);
    const armed = await read();
    assert.equal(armed.skip_next_at, retimed.next_run);
    assert.equal(typeof armed.skip_next_token, "string");
    await marker(desktop).waitFor();
    await marker(phone).waitFor();
    await shot(desktop, "task-skip-armed-light");

    await menu(phone).click();
    await item(phone, /^Cancel skip/).waitFor();
    await shot(phone, "task-skip-actions-dark");
    const removed = result(phone, "DELETE");
    await item(phone, /^Cancel skip/).click();
    const cancelled = await removed;
    assert.equal(cancelled.status(), 200);
    assert.equal(new URL(cancelled.url()).searchParams.get("token"), armed.skip_next_token);
    await marker(desktop).waitFor({ state: "hidden" });
    assert.equal((await read()).skip_next_at, undefined);

    await menu(desktop).click();
    const remade = result(desktop, "POST");
    await item(desktop, /^Skip next run/).click();
    assert.equal((await remade).status(), 200);
    const rearmed = await read();
    assert.equal(rearmed.skip_next_at, armed.skip_next_at);
    assert.notEqual(rearmed.skip_next_token, armed.skip_next_token);
    await call(cancellation(armed), "DELETE", undefined, 409);
    assert.equal((await read()).skip_next_token, rearmed.skip_next_token);

    await marker(phone).waitFor();
    await menu(phone).click();
    const ran = result(phone, "POST", tasks + "/fixture-skip-occurrence/run");
    await item(phone, /^Run now/).click();
    assert.equal((await ran).status(), 200);
    assert.equal((await read()).skip_next_token, rearmed.skip_next_token, "manual runs leave the scheduled skip waiting");
    await phone.waitForURL((url) => !url.pathname.endsWith("/routines"));
    await phone.goto(web + "/routines");
    await marker(phone).waitFor();
    const { task: after } = await call("/mock/skip-occurrence/consume", "POST", {});
    assert.equal(after.skip_next_at, undefined);
    assert.equal(Date.parse(after.next_run) - Date.parse(rearmed.skip_next_at), 30 * 60 * 1000);
    const history = (await call(runs)).runs;
    assert.deepEqual(history.slice(0, 2).map((run) => [run.status, !!run.owner_skipped]), [["skipped", true], ["ok", false]]);
    assert.equal(history[0].due, rearmed.skip_next_at);
    await marker(phone).waitFor({ state: "hidden" });
    await phone.getByText(/Skipped by you$/).first().waitFor();
    await shot(phone, "task-skip-history-dark");
    console.log("PASS stale requests, two-device skip and cancellation, manual run, cadence and intentional skip history");
  }
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}
