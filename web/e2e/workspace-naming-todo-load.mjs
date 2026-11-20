// Real Todo page + isolated Manager fixture; abort the actual lazy module request.
// node web/e2e/workspace-naming-todo-load.mjs <loopback origin> <shots directory> <Chromium executable>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [origin, shots, executablePath] = process.argv.slice(2);
assert(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin), "Use an isolated loopback origin");
assert(shots && executablePath, "Provide screenshot output and the installed browser");
await mkdir(shots, { recursive: true });
const deadline = setTimeout(() => { console.error("FAIL Todo naming import browser deadline"); process.exit(124); }, 240_000);
const browser = await chromium.launch({ executablePath, env: { ...process.env, TMPDIR: process.env.WORKSPACE_NAMING_BROWSER_TMP || process.env.TMPDIR }, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const results = [];
let activePage;
try {
  for (const scheme of ["light", "dark"]) for (const width of [1440, 390]) {
    const context = await browser.newContext({ viewport: { width, height: 1200 }, colorScheme: scheme, locale: "en-US", reducedMotion: "reduce", ...(width === 390 ? { isMobile: true, hasTouch: true } : {}) });
    await context.addInitScript(({ scheme }) => {
      localStorage.setItem("persona.token", "synthetic-naming-token");
      localStorage.setItem("persona.theme", scheme);
      localStorage.setItem("persona.language", "en");
      // Preserve the existing recovery contract: test a persistent failure after
      // PageBoundary's one-time automatic reload, then actually use its Reload.
      sessionStorage.setItem("persona.chunkReload", String(Date.now()));
    }, { scheme });
    const headers = { "X-Persona-Manager": "assistant", Authorization: "Bearer synthetic-naming-token" };
    const todo = await (await context.request.get(origin + "/api/v1/_qa/todo")).json();
    const todoURL = `${origin}/api/v1/todos/${encodeURIComponent(todo.id)}`;
    assert((await context.request.patch(todoURL, { headers, data: { title: "Saved example task", notes: "Saved example notes" } })).ok());
    let requested, release;
    const chunkRequested = new Promise((resolve) => { requested = resolve; });
    const failChunk = new Promise((resolve) => { release = resolve; });
    let blocking = true, imports = 0;
    const failedURLs = [], writes = [];
    await context.route("**/*", async (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) return route.abort();
      if (request.url() === todoURL && request.method() === "PATCH") writes.push(request.postDataJSON());
      if (/\/(?:src\/ui\/WorkspaceNaming\.tsx|assets\/WorkspaceNaming-[^/]+\.js)$/.test(url.pathname)) {
        imports++;
        if (blocking) { failedURLs.push(url.pathname); requested(); await failChunk; return route.abort("failed"); }
      }
      return route.continue();
    });
    const page = await context.newPage(); activePage = page;
    page.setDefaultTimeout(15_000);
    const pageErrors = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await page.goto(origin + "/todos?id=" + encodeURIComponent(todo.id));
    const detail = page.getByTestId("todos-detail-column");
    const title = detail.getByPlaceholder("To-do title", { exact: true });
    const notes = detail.locator("textarea").first();
    await title.waitFor(); await chunkRequested;
    const titleNode = await title.elementHandle(), notesNode = await notes.elementHandle();
    const draftTitle = `Title kept through failure (${scheme}, ${width})`;
    const draftNotes = `Notes kept through failure (${scheme}, ${width})`;
    // Change both real controlled fields without blurring either: no save can
    // hide a remount by repopulating the draft from the server.
    for (const [node, value] of [[title, draftTitle], [notes, draftNotes]]) {
      await node.evaluate((el, value) => {
        const proto = el.tagName === "INPUT" ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
        Object.getOwnPropertyDescriptor(proto, "value").set.call(el, value);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      }, value);
    }
    assert.equal(await title.inputValue(), draftTitle);
    assert.equal(await notes.inputValue(), draftNotes);
    assert.deepEqual(writes, []);
    release();
    const failure = page.getByRole("alert").filter({ hasText: "This page didn't load" });
    await failure.waitFor();
    assert.equal(await detail.getByRole("alert").filter({ hasText: "This page didn't load" }).count(), 1, "only the naming subtree failed");
    assert(await titleNode.evaluate((el) => el.isConnected), "the title editor did not remount");
    assert(await notesNode.evaluate((el) => el.isConnected), "the notes editor did not remount");
    assert.equal(await title.inputValue(), draftTitle);
    assert.equal(await notes.inputValue(), draftNotes);
    assert.deepEqual(writes, [], "both drafts remain unsubmitted after the import failure");
    await detail.screenshot({ path: join(shots, `todo-chunk-failed-${scheme}-${width}.png`) });
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
      await detail.screenshot({ path: join(shots, `todo-chunk-failed-${scheme}-${width}-large.png`) });
    }
    const savedTitle = draftTitle + " edited", savedNotes = draftNotes + " edited";
    for (const [node, key, value] of [[title, "title", savedTitle], [notes, "notes", savedNotes]]) {
      await node.fill(value);
      const saved = page.waitForResponse((response) => response.url() === todoURL && response.request().method() === "PATCH" && response.request().postDataJSON()[key] === value);
      await node.blur();
      assert((await saved).ok(), "the other Todo editor saves while naming is unavailable");
      await page.waitForFunction(() => [...document.querySelectorAll('[data-testid="todos-detail-column"] input, [data-testid="todos-detail-column"] textarea')].every((el) => el.getAttribute("aria-busy") !== "true"));
    }
    const persisted = (await (await context.request.get(origin + "/api/v1/todos", { headers })).json()).todos.find((item) => item.id === todo.id);
    assert.equal(persisted.title, savedTitle); assert.equal(persisted.notes, savedNotes);
    blocking = false;
    await Promise.all([page.waitForEvent("load"), failure.getByRole("button", { name: "Reload", exact: true }).click()]);
    await page.getByRole("button", { name: "Workspace naming", exact: true }).click();
    const naming = detail.locator("[data-workspace-naming]");
    await naming.getByLabel("Task folder template", { exact: true }).waitFor();
    assert.equal(await title.inputValue(), savedTitle);
    assert.equal(await notes.inputValue(), savedNotes);
    assert.equal(await failure.count(), 0);
    const namingDraft = "local/{task-id}";
    await naming.getByLabel("Task folder template", { exact: true }).fill(namingDraft);
    await notes.fill("A new notes draft after recovery");
    assert.equal(await naming.getByLabel("Task folder template", { exact: true }).inputValue(), namingDraft);
    assert.equal(await notes.inputValue(), "A new notes draft after recovery");
    await detail.screenshot({ path: join(shots, `todo-chunk-recovered-${scheme}-${width}.png`) });
    if (width === 390) {
      await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
      await page.setViewportSize({ width, height: 6000 });
      await detail.screenshot({ path: join(shots, `todo-chunk-recovered-${scheme}-${width}-large.png`) });
    }
    assert.equal(pageErrors.length, 0, "the real import error was caught");
    assert(imports >= 2 && failedURLs.length > 0, "the failed module was fetched again on actual Reload");
    results.push({ scheme, width, imports, failedURLs, draftTitle, draftNotes, savedTitle, savedNotes, writes, pageErrors });
    await context.close();
    console.log(`PASS Todo naming import isolation, drafts, save and Reload: ${scheme} ${width}px`);
  }
  await writeFile(join(shots, "results.json"), JSON.stringify(results, null, 2) + "\n");
  console.log("PASS real Todo lifecycle across failed import and recovery");
} catch (error) {
  if (activePage && !activePage.isClosed()) {
    await activePage.screenshot({ path: join(shots, "failure.png"), fullPage: true });
    await writeFile(join(shots, "failure.txt"), await activePage.locator("body").innerText());
  }
  throw error;
} finally { clearTimeout(deadline); await browser.close(); }
