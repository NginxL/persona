// Isolated synthetic browser evidence: no live hub, credentials or Agent actions.
// Usage: node web/e2e/hidden-agent-dirs.mjs <loopback-web-origin> <artifact-directory> <chromium-executable>
// Captures go in the PR, not the repository (CLAUDE.md, "Workflow").
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { chromium } from "playwright-core";
const [base, shots, executablePath] = process.argv.slice(2);
assert(["127.0.0.1", "localhost"].includes(new URL(base).hostname));
mkdirSync(shots, { recursive: true });
const timer = setTimeout(() => { console.error("TIMEOUT hidden directory browser checks"); process.exit(124); }, 180000);
const browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox"] });
try {
 for (const theme of ["light", "dark"]) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: theme, reducedMotion: "reduce" });
  await ctx.addInitScript((theme) => { localStorage.setItem("persona.token", "synthetic-token"); localStorage.setItem("persona.language", "en"); localStorage.setItem("persona.theme", theme); }, theme);
  const visible = { machine_id: "example", session_id: "visible", title: "Visible project", cwd: "/example/projects", state: "idle", status: "idle", updated_at: "2026-01-01T00:00:00Z" };
  const hidden = { ...visible, session_id: "hidden", title: "Background workspace", cwd: "/example/workspaces/demo", hidden_by_directory: true };
  let rules = [{ pattern: "~/workspaces/*", enabled: true }];
  await ctx.route("**/api/v1/**", async (route) => {
   const req = route.request(), url = new URL(req.url()), path = url.pathname.replace(/^\/api\/v1/, "");
   let body = {};
   if (path === "/machines/example/agent-dirs/hidden") {
    if (req.method() === "PUT") rules = req.postDataJSON().rules;
    body = { rules, home: "/example" };
   } else {
    assert.equal(req.method(), "GET", `unexpected write ${path}`);
    if (path === "/machines") body = { machines: [{ id: "example", name: "Example computer", online: true, os: "linux" }] };
    else if (path === "/sessions") { const sessions = url.searchParams.get("include_hidden") === "1" ? [visible, hidden] : [visible]; body = { sessions, remove: [], total: sessions.length, has_more: false, revision: "example" }; }
    else if (path === "/interactions") body = { interactions: [] };
    else if (path === "/assistant") body = { conversations: [], tasks: [], memory: [] };
    else if (path === "/todos") body = { todos: [] };
    else if (path === "/extensions/ui") body = { slots: [] };
    else if (path === "/extensions") body = { extensions: [] };
    else if (path === "/managers") body = { managers: [] };
    else if (path.endsWith("/claude-dirs")) body = { dirs: [], blocked: [], live: true };
    else if (path.endsWith("-settings")) body = { settings: [] };
   }
   await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await ctx.routeWebSocket("**/api/v1/ws**", (ws) => { ws.send(JSON.stringify({ type: "hello", server_version: "synthetic", hub_name: "Example hub", features: [] })); });
  const page = await ctx.newPage();
  const errors = []; page.on("console", (m) => { if (m.type() === "error") console.error(m.text()); }); page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(`${base}/settings?tab=agents`);
  await page.getByRole("heading", { name: "Agent folders" }).waitFor().catch(async (e) => { console.error(await page.locator("body").innerText(), errors); await page.screenshot({path: `${shots}/failure.png`}); throw e; });
  const rule = page.getByRole("switch", { name: "~/workspaces/*", exact: true });
  assert.equal(await rule.getAttribute("aria-checked"), "true");
  await rule.click();
  await page.getByText("Rule off · sessions visible", { exact: true }).waitFor();
  await rule.click();
  await page.getByText("Hidden from Agents and Assistant", { exact: true }).waitFor();
  // #10053: a new rule blocks unless Hide is chosen; an existing rule's action can be switched.
  assert.equal(await page.getByRole("combobox", { name: "Action", exact: true }).inputValue(), "block");
  await page.getByRole("textbox", { name: "Folder or glob", exact: true }).fill("~/.automation/runs");
  await page.getByRole("button", { name: "Add rule", exact: true }).click();
  await page.getByText("Blocked · not collected", { exact: true }).waitFor();
  assert.deepEqual(rules.at(-1), { pattern: "~/.automation/runs", enabled: true, action: "block" });
  await page.screenshot({ path: `${shots}/settings-${theme}.png` });
  await page.getByRole("combobox", { name: "Action: ~/.automation/runs", exact: true }).selectOption("hide");
  await page.getByText("Hidden from Agents and Assistant", { exact: true }).nth(1).waitFor();
  assert.equal(rules.at(-1).action, "hide");
  rules = rules.slice(0, 1);
  await page.goto(`${base}/agents`);
  await page.getByRole("button", { name: "Include hidden sessions", exact: true }).waitFor();
  assert.equal(await page.getByText("Background workspace", { exact: true }).count(), 0, "default list hides the workspace");
  await page.getByRole("button", { name: "Include hidden sessions", exact: true }).click();
  await page.getByRole("link", { name: "Background workspace", exact: true }).waitFor();
  await page.screenshot({ path: `${shots}/inspection-${theme}.png` });
  await page.getByRole("button", { name: "Close", exact: true }).last().click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(await page.getByText("Background workspace", { exact: true }).count(), 0, "inspection does not change the default list");
  assert.deepEqual(errors, []);
  await ctx.close();
 }
 console.log("PASS Agent folder settings (toggle, block / hide action) and temporary inspection, light/dark");
} finally { await browser.close(); clearTimeout(timer); }
