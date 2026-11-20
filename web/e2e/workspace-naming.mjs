// Real isolated manager/PostgreSQL fixture; synthetic repository and computer data.
// node web/e2e/workspace-naming.mjs <loopback web origin> <shots directory> <Chromium executable>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [origin, shots, executablePath] = process.argv.slice(2);
assert(/^http:\/\/(127\.0\.0\.1|localhost):\d+$/.test(origin), "Use an isolated loopback origin");
assert(shots && executablePath, "Provide screenshot output and the installed browser");
await mkdir(shots, { recursive: true });
const deadline = setTimeout(() => { console.error("FAIL workspace naming browser deadline"); process.exit(124); }, 240_000);
const browser = await chromium.launch({ executablePath, env: { ...process.env, TMPDIR: process.env.WORKSPACE_NAMING_BROWSER_TMP || process.env.TMPDIR }, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
const errors = [];
const results = [];
let activePage;
try {
  for (const scheme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      const context = await browser.newContext({ viewport: { width, height: 1600 }, colorScheme: scheme, locale: "en-US", reducedMotion: "reduce", ...(width === 390 ? { isMobile: true, hasTouch: true } : {}) });
      await context.addInitScript(({ scheme }) => { localStorage.setItem("persona.token", "synthetic-naming-token"); localStorage.setItem("persona.theme", scheme); localStorage.setItem("persona.language", "en"); }, { scheme });
      await context.route("**/*", (route) => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
      const page = await context.newPage(); activePage = page; page.on("pageerror", (error) => errors.push(error.message));
      const fixtureHeaders = { "X-Persona-Manager": "assistant", Authorization: "Bearer synthetic-naming-token" };
      const cleared = await context.request.put(origin + "/api/v1/assistant", { headers: fixtureHeaders, data: { task_workspace_preference: "prefer-existing", workspace_rules: { base_directory: null, task_directory_template: null, branch_template: null } } });
      assert(cleared.ok(), "reset only this isolated fixture's naming rules");
      await page.goto(origin + "/settings?tab=persona");
      const preference = page.getByLabel("When dispatching tasks, prefer", { exact: true });
      await preference.selectOption("prefer-new");
      const dispatch = page.locator('[data-settings-group="dispatch-tasks"]');
      const group = page.locator('[data-settings-group="workspace-naming"]');
      await group.waitFor();
      assert.equal(await dispatch.locator('[data-workspace-naming]').count(), 1, "naming belongs to Dispatch tasks");
      await dispatch.scrollIntoViewIfNeeded();
      await dispatch.screenshot({ path: join(shots, `collapsed-${scheme}-${width}.png`) });
      assert((await group.boundingBox()).height <= 50, "collapsed entry is one settings row");
      const trigger = group.getByRole("button", { name: "Workspace naming", exact: true });
      await trigger.focus(); await page.keyboard.press("Enter");
      const task = group.getByLabel("Task folder template", { exact: true });
      await task.waitFor();
      assert.equal(await task.inputValue(), "", "unset default is an empty input");
      assert.equal(await task.getAttribute("placeholder"), "System default: task-{task-id}");
      assert(await group.getByText("Fields: {task-id}, {task-slug}, {yyyy}, {mmdd}. Relative subfolders are allowed.", { exact: true }).isVisible(), "help is visible without disclosure");
      assert.equal(await group.getByRole("button", { name: /inherited|inheritance/i }).count(), 0, "no reset controls");
      await dispatch.screenshot({ path: join(shots, `expanded-${scheme}-${width}.png`) });
      await task.fill("{yyyy}{mmdd}/task-{task-id}-{task-slug}");
      await group.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(() => !document.body.textContent.includes("Unsaved changes") && [...document.querySelectorAll('[data-settings-group="workspace-naming"] button')].some((b) => b.textContent.trim() === "Save" && b.disabled && b.getAttribute("aria-busy") !== "true"));
      await page.reload();
      await group.getByRole("button", { name: "Workspace naming", exact: true }).click();
      await task.waitFor(); assert.equal(await task.inputValue(), "{yyyy}{mmdd}/task-{task-id}-{task-slug}", "settings persisted through reload");
      await group.getByText("Preview options", { exact: true }).click();
      if (width === 1440) {
        const [computerBox, layoutBox, previewBox] = await Promise.all([
          group.getByLabel("Preview on computer", { exact: true }).boundingBox(),
          group.getByLabel("Folder layout", { exact: true }).boundingBox(),
          group.getByRole("button", { name: "Preview naming", exact: true }).boundingBox(),
        ]);
        assert(computerBox && layoutBox && previewBox, "preview controls are visible");
        assert.equal(previewBox.height, computerBox.height, "preview action matches the select height");
        assert(Math.abs(previewBox.y + previewBox.height - (computerBox.y + computerBox.height)) <= 1, "preview action aligns with the selects");
        assert(previewBox.x >= layoutBox.x + layoutBox.width, "preview action follows the layout select");
      }
      if (width === 390) {
        const [layoutBox, previewBox] = await Promise.all([
          group.getByLabel("Folder layout", { exact: true }).boundingBox(),
          group.getByRole("button", { name: "Preview naming", exact: true }).boundingBox(),
        ]);
        assert(previewBox && layoutBox && previewBox.y >= layoutBox.y + layoutBox.height, "preview action stacks below the selects on phones");
        assert(previewBox.width >= layoutBox.width - 1, "preview action spans the narrow form");
      }
      await group.getByRole("button", { name: "Preview naming", exact: true }).click();
      await group.locator("[data-naming-root]").waitFor();
      assert.match(await group.locator("[data-naming-root]").innerText(), /^\/home\/example\/workspace\/\d{8}\/task-todo_example-example-task$/);
      assert.deepEqual(await group.locator("[data-naming-repo]").evaluateAll((rows) => rows.map((row) => [row.querySelector("[data-naming-folder]")?.textContent, row.querySelector("[data-naming-branch]")?.textContent]).sort((a, b) => a[0].localeCompare(b[0]))),
        [["api", "persona/todo_example"], ["web", "persona/todo_example"]], "container previews each repository's folder and branch");
      await group.getByLabel("Folder layout", { exact: true }).selectOption("root");
      await group.getByRole("button", { name: "Preview naming", exact: true }).click();
      await group.locator("[data-naming-root]").waitFor();
      const rootLayout = await group.locator("[aria-live]").innerText();
      const rootPath = await group.locator("[data-naming-root]").innerText();
      assert.equal(await group.locator("[data-naming-repo]").count(), 0, "the internal dot is not a row");
      assert.equal(await group.locator("[data-naming-branch]").innerText(), "persona/todo_example", "the root branch is labeled");
      assert.equal(rootLayout.split(rootPath).length - 1, 1, "the root path appears once");
      assert(!rootLayout.split("\n").some((line) => line.trim() === "."), "no standalone dot");
      assert.equal(await group.locator("[aria-live] details").count(), 0, "no routine details disclosure");
      assert(!rootLayout.includes("Names only"), "no routine disclaimer");
      assert(!rootLayout.includes("UTC"), "no routine timestamp or time zone");
      await group.scrollIntoViewIfNeeded();
      assert(await group.evaluate((el) => el.scrollWidth <= el.clientWidth), "no horizontal overflow");
      await group.screenshot({ type: "jpeg", quality: 80, path: join(shots, `settings-${scheme}-${width}.jpg`) });
      await task.fill("");
      await group.getByRole("button", { name: "Preview naming", exact: true }).click();
      await group.locator("[data-naming-root]").waitFor();
      assert.equal(await group.locator("[data-naming-root]").innerText(), "/home/example/workspace/task-todo_example", "blank draft previews the system fallback");
      await group.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(() => !document.body.textContent.includes("Unsaved changes"));
      await page.reload(); await group.getByRole("button", { name: "Workspace naming", exact: true }).click();
      await task.waitFor(); assert.equal(await task.inputValue(), "", "cleared override reloads as blank");
      assert.equal(await task.getAttribute("placeholder"), "System default: task-{task-id}");
      await task.fill("{yyyy}{mmdd}/task-{task-id}-{task-slug}");
      await group.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(() => !document.body.textContent.includes("Unsaved changes"));
      await group.getByText("Preview options", { exact: true }).click();
      await task.fill("../unsafe");
      await group.getByRole("button", { name: "Preview naming", exact: true }).click();
      await group.getByRole("alert").first().waitFor();
      assert.equal(await task.inputValue(), "../unsafe", "failed preview preserves draft");
      await group.screenshot({ type: "jpeg", quality: 80, path: join(shots, `invalid-${scheme}-${width}.jpg`) });

      await preference.selectOption("prefer-existing");
      await group.waitFor({ state: "hidden" });
      await preference.selectOption("prefer-new");
      await group.getByRole("button", { name: "Workspace naming", exact: true }).click();
      assert.equal(await task.inputValue(), "{yyyy}{mmdd}/task-{task-id}-{task-slug}", "hiding preserves saved rules");
      if (width === 390) {
        await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
        await group.getByText("Preview options", { exact: true }).click();
        await group.getByRole("button", { name: "Preview naming", exact: true }).click();
        await group.locator("[data-naming-root]").waitFor();
        assert.equal(await group.locator("[data-naming-repo]").count(), 2, "the enlarged preview lists both repositories");
        assert((await group.locator("[data-naming-root]").innerText()).length > 0, "the enlarged preview shows the root path");
        assert.equal(await group.locator("[aria-live] details").count(), 0, "the enlarged preview has no routine details");
        assert(await group.evaluate((el) => el.scrollWidth <= el.clientWidth), "settings preview fits at 200% text");
        await group.locator("details").first().screenshot({ path: join(shots, `large-text-${scheme}-${width}.png`) });
      }

      // Direct API state is supplied by the real fixture, not browser interception.
      const todo = await (await page.request.get(origin + "/api/v1/_qa/todo")).json();
      await page.goto(origin + "/todos?id=" + encodeURIComponent(todo.id));
      await page.getByRole("button", { name: "Workspace naming", exact: true }).click();
      const field = page.getByLabel("Task branch template", { exact: true }); await field.waitFor();
      const taskFolder = page.getByLabel("Task folder template", { exact: true });
      assert.equal(await taskFolder.inputValue(), "", "task fallback stays empty");
      assert.equal(await taskFolder.getAttribute("placeholder"), "Assistant default: {yyyy}{mmdd}/task-{task-id}-{task-slug}");
      await field.fill("custom/{task-id}/{repo}");
      // Scope to the advanced editor so unrelated Todo buttons cannot save instead.
      const editor = page.getByRole("button", { name: "Workspace naming", exact: true }).locator("..");
      await editor.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(() => [...document.querySelectorAll("input")].some((el) => el.value === "custom/{task-id}/{repo}" && !document.body.textContent.includes("Unsaved changes")));
      await page.reload(); await page.getByRole("button", { name: "Workspace naming", exact: true }).click();
      await field.waitFor(); assert.equal(await field.inputValue(), "custom/{task-id}/{repo}", "task override persisted");
      await editor.getByText("Preview options", { exact: true }).click();
      await editor.getByRole("button", { name: "Preview naming", exact: true }).click();
      await editor.locator("[data-naming-root]").waitFor();
      const taskPreview = await editor.locator("[aria-live]").innerText();
      const taskRoot = await editor.locator("[data-naming-root]").innerText();
      assert.equal(taskPreview.split(taskRoot).length - 1, 1, "the task root appears once");
      assert.equal(await editor.locator("[aria-live] dl").count(), 0, "no raw dot mapping is shown");
      const taskRepos = await editor.locator("[data-naming-repo]").evaluateAll((rows) => rows.map((row) => [row.querySelector("[data-naming-folder]")?.textContent, row.querySelector("[data-naming-branch]")?.textContent]).sort((a, b) => a[0].localeCompare(b[0])));
      assert.deepEqual(taskRepos, [["api", `custom/${todo.id}/api`], ["web", `custom/${todo.id}/web`]], "each repository folder maps to its task branch");
      await field.fill("");
      await editor.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(() => !document.body.textContent.includes("Unsaved changes"));
      await page.reload(); await page.getByRole("button", { name: "Workspace naming", exact: true }).click();
      await field.waitFor(); assert.equal(await field.inputValue(), "", "cleared task override reloads blank");
      assert.equal(await field.getAttribute("placeholder"), "Assistant default: persona/{task-id}");
      await editor.getByText("Preview options", { exact: true }).click();
      await editor.getByRole("button", { name: "Preview naming", exact: true }).click();
      await editor.locator("[data-naming-root]").waitFor();
      for (const branch of await editor.locator("[aria-live] [data-naming-branch]").allTextContents()) assert.equal(branch, `persona/${todo.id}`, "cleared task branch previews assistant fallback");
      if (width === 390) await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
      assert(await editor.evaluate((el) => el.scrollWidth <= el.clientWidth), "task editor fits at enlarged text");
      if (width === 390) {
        await editor.locator("details").first().screenshot({ type: "jpeg", quality: 80, path: join(shots, `task-${scheme}-${width}-options.jpg`) });
        await editor.locator("[aria-live]").screenshot({ type: "jpeg", quality: 80, path: join(shots, `task-${scheme}-${width}-preview.jpg`) });
      } else await editor.screenshot({ type: "jpeg", quality: 80, path: join(shots, `task-${scheme}-${width}.jpg`) });
      // Repository associations can change in another editor while this panel stays open.
      const repositoriesURL = `${origin}/api/v1/todos/${encodeURIComponent(todo.id)}/repositories`;
      const bindings = await (await context.request.get(repositoriesURL, { headers: fixtureHeaders })).json();
      const catalog = await (await context.request.get(origin + "/api/v1/assistant/repositories", { headers: fixtureHeaders })).json();
      const inputRows = bindings.repositories.map(({ repo_id, directory_alias, requested_ref }) => ({ repo_id, directory_alias, requested_ref }));
      const retained = bindings.repositories[1];
      const remove = await context.request.put(repositoriesURL, { headers: fixtureHeaders, data: {
        expected_revision: bindings.revision, expected_catalog_revision: catalog.catalog_revision, repositories: inputRows.slice(1),
      } });
      assert(remove.ok(), "remove one repository in the isolated fixture");
      const removed = await remove.json();
      await field.fill("recovered/{task-id}");
      const previewPath = `/todos/${encodeURIComponent(todo.id)}/workspace-naming/preview`;
      const failedPreview = page.waitForResponse((response) => response.url().endsWith(previewPath));
      await editor.getByRole("button", { name: "Preview naming", exact: true }).click();
      assert.equal((await failedPreview).status(), 404, "stale repository selection is rejected by the real manager");
      await editor.getByRole("button", { name: "Reload saved settings", exact: true }).click();
      await editor.getByRole("checkbox", { name: bindings.repositories[0].source_snapshot.identity.path, exact: true }).waitFor({ state: "detached" });
      assert.equal(await field.inputValue(), "recovered/{task-id}", "reload preserves the unsaved rule draft");
      assert(await editor.getByRole("checkbox", { name: retained.source_snapshot.identity.path, exact: true }).isChecked(), "reload keeps the remaining explicit selection");
      const recoveredPreview = page.waitForResponse((response) => response.url().endsWith(previewPath));
      await editor.getByRole("button", { name: "Preview naming", exact: true }).click();
      const recovered = await recoveredPreview;
      assert.equal(recovered.status(), 200, "preview recovers after reload");
      assert.deepEqual(recovered.request().postDataJSON().selected_repo_ids, [retained.repo_id], "removed repository is absent from the retry");
      await editor.locator("[data-naming-root]").waitFor();
      assert.equal(await editor.locator("[data-naming-repo]").count(), 1);
      assert.equal(await editor.locator("[data-naming-branch]").innerText(), `recovered/${todo.id}`);
      await editor.screenshot({ type: "jpeg", quality: 80, path: join(shots, `recovered-${scheme}-${width}.jpg`) });

      // Another settings editor changes the fallback while this task remains open.
      const changedDefault = await context.request.put(origin + "/api/v1/assistant", { headers: fixtureHeaders, data: {
        workspace_rules: { task_directory_template: "fresh/{task-id}" },
      } });
      assert(changedDefault.ok(), "change only this fixture's assistant fallback");
      await field.fill("saved/{task-id}");
      await editor.getByRole("button", { name: "Save", exact: true }).click();
      await page.waitForFunction(() => [...document.querySelectorAll('[data-workspace-naming] input')].some((el) => el.placeholder === "Assistant default: fresh/{task-id}"));
      assert.equal(await taskFolder.inputValue(), "", "refresh keeps the inherited field blank");
      assert.equal(await field.inputValue(), "saved/{task-id}", "refresh preserves the saved draft");
      assert.equal(await editor.getByLabel("Preview on computer", { exact: true }).inputValue(), "server");
      const freshPreview = page.waitForResponse((response) => response.url().endsWith(previewPath));
      await editor.getByRole("button", { name: "Preview naming", exact: true }).click();
      const fresh = await freshPreview;
      assert.equal(fresh.status(), 200);
      assert.equal(fresh.request().postDataJSON().machine_id, "server");
      const freshResult = await fresh.json();
      assert.equal(freshResult.plan.root_path, `/home/example/workspace/fresh/${todo.id}`, "the displayed fallback matches the actual manager/Node preview");
      if (width === 390) {
        await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "1"));
        await editor.scrollIntoViewIfNeeded();
      }
      await editor.screenshot({ type: "jpeg", quality: 80, path: join(shots, `fresh-default-${scheme}-${width}.jpg`) });
      if (width === 390) {
        await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "2"));
        // Paint the whole enlarged element; the task page virtualizes offscreen content.
        await page.setViewportSize({ width, height: 6000 });
        await editor.scrollIntoViewIfNeeded();
        await editor.screenshot({ type: "jpeg", quality: 80, path: join(shots, `fresh-default-${scheme}-${width}-large.jpg`) });
        await page.evaluate(() => document.documentElement.style.setProperty("--text-scale", "1"));
      }

      const computer = editor.getByLabel("Preview on computer", { exact: true });
      await computer.selectOption("");
      await field.fill("../invalid");
      await editor.getByRole("button", { name: "Save", exact: true }).click();
      await editor.getByRole("button", { name: "Reload saved settings", exact: true }).click();
      await page.waitForFunction(() => !document.querySelector('[data-workspace-naming] [role=alert]'));
      assert.equal(await computer.inputValue(), "", "recovery preserves an explicit empty computer choice");
      assert.equal(await field.inputValue(), "../invalid", "recovery preserves the failed draft");
      assert(await editor.getByRole("button", { name: "Preview naming", exact: true }).isDisabled(), "Reload does not silently choose a preview target");
      results.push({ scheme, width, previewInput: fresh.request().postDataJSON(), root: freshResult.plan.root_path,
        placeholder: await taskFolder.getAttribute("placeholder"), recoveredComputer: await computer.inputValue(), recoveredDraft: await field.inputValue() });
      await editor.screenshot({ type: "jpeg", quality: 80, path: join(shots, `retained-selection-${scheme}-${width}.jpg`) });
      const restored = await context.request.put(repositoriesURL, { headers: fixtureHeaders, data: {
        expected_revision: removed.revision, expected_catalog_revision: catalog.catalog_revision, repositories: inputRows,
      } });
      assert(restored.ok(), "restore this fixture's repositories for the next viewport");
      assert.equal(await page.locator("vite-error-overlay").count(), 0);
      await context.close();
      console.log(`PASS workspace naming ${scheme} ${width}px: real settings/override reload, blank defaults, clear-save fallback, task fallback, root/container preview, invalid draft, overflow`);
    }
  }
  assert.deepEqual(errors, [], "no browser page errors");
  await writeFile(join(shots, "browser-results.json"), JSON.stringify({ results, pageErrors: errors }, null, 2) + "\n");
  console.log("PASS isolated workspace naming browser journey");
  await fetch(origin + "/api/v1/_qa/stop");
  await new Promise((resolve) => setTimeout(resolve, 500));
} catch (error) {
  if (activePage && !activePage.isClosed()) { await activePage.screenshot({ type: "jpeg", quality: 80, path: join(shots, "failure.jpg"), fullPage: true }); await writeFile(join(shots, "failure.txt"), await activePage.locator("body").innerText()); }
  throw error;
} finally { clearTimeout(deadline); await browser.close(); }
