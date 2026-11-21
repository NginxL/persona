// Run with MOCK_STARRED=1 MOCK_STARRED_BACKUP=1 and Vite pointed at the loopback mock.
// node web/e2e/starred-backup.mjs [--shots <dir>] [--legacy]
// Omit MOCK_STARRED_BACKUP for --legacy. Set STARRED_WEB and STARRED_HUB for different ports.
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const web = process.env.STARRED_WEB ?? "http://127.0.0.1:5797";
const hub = process.env.STARRED_HUB ?? "http://127.0.0.1:18797";
const allowed = new Set([web, hub].map((url) => {
  const parsed = new URL(url);
  assert.equal(parsed.hostname, "127.0.0.1", "only a loopback mock is allowed");
  return parsed.origin;
}));
const legacy = process.argv.includes("--legacy");
const shotsIndex = process.argv.indexOf("--shots");
const shots = shotsIndex === -1 ? undefined : process.argv[shotsIndex + 1];
if (shots) await mkdir(shots, { recursive: true });
const path = "/api/v1/assistant/starred";
const textA = "Save this retirement checklist.";
const textB = "Remember the documentation follow-up.";

async function request(method, suffix = "", body, status = 200) {
  const response = await fetch(hub + path + suffix, {
    method, headers: { Authorization: "Bearer mock", "Content-Type": "application/json" },
    ...(body && { body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(8000),
  });
  assert.equal(response.status, status, `${method} ${path}${suffix}`);
  if (!response.headers.get("content-type")?.includes("application/json") && status !== 200) return {};
  return response.json();
}

const seed = (item, text, conversation = "assistant") => request("POST", "", {
  machine_id: "macbook", session_id: "synthetic-chat", item_id: item, conversation,
  role: "assistant", title: "Review notes", speaker: "Reviewer", text,
  written_at: "2026-09-12T08:15:00Z",
});

const chrome = await chromium.launch({ headless: true, channel: "chrome" });
const errors = [];
async function open(theme, width) {
  const context = await chrome.newContext({
    viewport: { width, height: 840 }, colorScheme: theme, acceptDownloads: true, reducedMotion: "reduce",
    permissions: ["clipboard-read", "clipboard-write"],
  });
  await context.route("**/*", (route) => allowed.has(new URL(route.request().url()).origin) ? route.continue() : route.abort());
  await context.addInitScript((choice) => {
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", choice);
  }, theme);
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(web + "/starred?token=mock");
  await page.getByRole("heading", { name: "Starred", exact: true }).waitFor();
  return page;
}

async function download(page, dialog, kind) {
  const incoming = page.waitForEvent("download");
  await dialog.getByRole("button", { name: `Download ${kind}`, exact: true }).click();
  const file = await incoming;
  return readFile(await file.path(), "utf8");
}

function nextImport(page, kind) {
  return page.waitForResponse((res) => res.request().method() === "POST" && new URL(res.url()).pathname === path + kind);
}

async function preview(page, dialog, json) {
  if (json !== undefined) {
    await dialog.getByLabel("Backup file", { exact: true }).setInputFiles({
      name: "saved-messages.json", mimeType: "application/json", buffer: Buffer.from(json),
    });
  }
  const response = nextImport(page, "/import/preview");
  await dialog.getByRole("button", { name: "Preview restore", exact: true }).click();
  const rendered = await response;
  assert.equal(rendered.status(), 200);
  await dialog.getByText("Messages in file", { exact: true }).waitFor();
  return rendered.json();
}

const shot = (dialog, filename) => shots && dialog.screenshot({ path: join(shots, filename + ".png"), animations: "disabled" });
let clear = false;
try {
  assert.deepEqual((await request("GET")).stars, [], "start with an empty mock");
  clear = true;
  assert.equal((await request("GET", "/import", undefined, legacy ? 405 : 200)).can_import, legacy ? undefined : true);
  const { star: first } = await seed("a-original-item", textA);
  const { star: second } = await seed("b-original-item", textB, "agent");
  const desktop = await open("light", 1200);
  await desktop.getByText(textB, { exact: true }).waitFor();
  await desktop.getByRole("button", { name: "Backup", exact: true }).click();
  const dialog = desktop.getByRole("dialog", { name: "Starred backup", exact: true });
  const json = await download(desktop, dialog, "JSON");
  const backup = JSON.parse(json);
  assert.deepEqual([backup.format, backup.version], ["persona.starred", 1]);
  assert.deepEqual(backup.messages.map((item) => item.key).sort(), [first.id, second.id].sort());
  for (const item of backup.messages) {
    for (const field of ["machine_id", "session_id", "item_id", "source_manager", "source_fingerprint"]) assert.ok(!(field in item));
  }
  const markdown = await download(desktop, dialog, "Markdown");
  assert.ok(markdown.includes(textA) && markdown.includes(textB));
  assert.ok(!markdown.includes("synthetic-chat"));

  if (legacy) {
    assert.equal(await dialog.getByLabel("Backup file", { exact: true }).count(), 0);
    console.log("PASS legacy manager keeps both export formats");
  } else {
    await dialog.getByRole("button", { name: "Preview restore", exact: true }).waitFor();
    const mobile = await open("dark", 375);
    await request("DELETE", `/${second.id}`);
    await mobile.getByText(textB, { exact: true }).waitFor({ state: "hidden" });
    // JSON.parse alone would keep only the second value, so preserve the
    // owner's raw file through preview and let the server reject ambiguity.
    const duplicateField = json.replace('"version": 1', '"version": 99, "version": 1');
    assert.notEqual(duplicateField, json);
    await dialog.getByLabel("Backup file", { exact: true }).setInputFiles({
      name: "duplicates.json", mimeType: "application/json", buffer: Buffer.from(duplicateField),
    });
    const rejected = nextImport(desktop, "/import/preview");
    await dialog.getByRole("button", { name: "Preview restore", exact: true }).click();
    const invalidPreview = await rejected;
    assert.equal(invalidPreview.request().postData(), duplicateField, "the preview must send the actual file text");
    assert.equal(invalidPreview.status(), 400);
    await dialog.getByRole("alert").waitFor();
    const firstPreview = await preview(desktop, dialog, json);
    assert.deepEqual([firstPreview.total, firstPreview.to_add, firstPreview.already_saved], [2, 1, 1]);
    assert.equal((await request("GET")).stars.length, 1, "preview is read-only");
    await shot(dialog, "starred-backup-preview-light");

    await seed("concurrent-save", "Saved on the other device.");
    const conflicted = nextImport(desktop, "/import");
    await dialog.getByRole("button", { name: "Restore messages", exact: true }).click();
    assert.equal((await conflicted).status(), 409);
    assert.equal((await request("GET")).stars.length, 2, "the stale restore adds nothing");
    const renewed = await preview(desktop, dialog);
    assert.ok(renewed.revision > firstPreview.revision);
    const committed = nextImport(desktop, "/import");
    await dialog.getByRole("button", { name: "Restore messages", exact: true }).click();
    const response = await committed;
    assert.equal(response.status(), 200);
    assert.deepEqual(await response.json(), { revision: renewed.revision + 1, added: 1, already_saved: 1 });
    await dialog.getByRole("button", { name: "Done", exact: true }).click();

    const archived = mobile.getByRole("article").filter({ hasText: textB });
    await archived.getByText("Restored from backup", { exact: true }).waitFor();
    assert.equal(await archived.getByRole("link", { name: "Open original" }).count(), 0);
    await archived.getByRole("button", { name: "Copy", exact: true }).click();
    assert.equal(await mobile.evaluate(() => navigator.clipboard.readText()), textB);
    await mobile.getByRole("textbox", { name: "Search starred messages" }).fill(textB);
    await archived.waitFor();
    await shot(mobile, "starred-backup-archived-dark");

    await desktop.getByRole("button", { name: "Backup", exact: true }).click();
    const duplicate = await preview(desktop, dialog, json);
    assert.deepEqual([duplicate.to_add, duplicate.already_saved], [0, 2]);
    const restore = dialog.getByRole("button", { name: "Restore messages", exact: true });
    assert.ok(!await restore.count() || await restore.isDisabled());
    const exportedAgain = JSON.parse(await download(desktop, dialog, "JSON"));
    assert.equal(exportedAgain.messages.find((item) => item.text === textB)?.key, second.id);
    await mobile.getByRole("button", { name: "Backup", exact: true }).click();
    const mobileDialog = mobile.getByRole("dialog", { name: "Starred backup", exact: true });
    await preview(mobile, mobileDialog, json);
    await shot(mobileDialog, "starred-backup-preview-dark");
    console.log("PASS JSON and Markdown export, atomic restore, stale revision, cross-device archive, and duplicate handling");
  }
  assert.deepEqual(errors, []);
} finally {
  await chrome.close();
  if (clear) for (const star of (await request("GET")).stars) await request("DELETE", `/${star.id}`);
}
