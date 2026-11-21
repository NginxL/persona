// Run with MOCK_STARRED=1 PORT=18797 python3 scripts/mock/mock_hub.py and
// PERSONA_HUB_URL=http://127.0.0.1:18797 vite --host 127.0.0.1 --port 5797.
// node web/e2e/starred-messages.mjs [--shots tmp/starred-shots] [--nested-sources team|hierarchy]
// For team set MOCK_TEAM=1 in the mock. For hierarchy set MOCK_SIDE_SESSIONS=1 MOCK_CHILD_SESSIONS=1 MOCK_ENGINE_SWITCH=done.
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const web = process.env.STARRED_WEB ?? "http://127.0.0.1:5797";
const hub = process.env.STARRED_HUB ?? "http://127.0.0.1:18797";
const allowed = new Set([web, hub].map((url) => {
  const parsed = new URL(url);
  assert.equal(parsed.hostname, "127.0.0.1", "only a loopback mock is allowed");
  return parsed.origin;
}));
const shotsArg = process.argv.indexOf("--shots");
const shots = shotsArg < 0 ? undefined : process.argv[shotsArg + 1];
const nestedArg = process.argv.indexOf("--nested-sources");
const nestedSources = nestedArg < 0 ? undefined : process.argv[nestedArg + 1];
assert.ok(nestedArg < 0 || nestedSources === "team" || nestedSources === "hierarchy", "choose a nested mock fixture: team or hierarchy");
if (shots) mkdirSync(shots, { recursive: true });
const shot = (page, name) => shots && page.screenshot({ path: join(shots, name + ".png"), animations: "disabled" });

const review = "/s/macbook/a1f3c2d4-0000-4000-8000-000000000006?view=chat";
const path = "/api/v1/assistant/starred";
const onHub = async (method = "GET", suffix = "", json) => {
  const response = await fetch(hub + path + suffix, {
    method,
    headers: { Authorization: "Bearer mock", "Content-Type": "application/json" },
    ...(json && { body: JSON.stringify(json) }),
    signal: AbortSignal.timeout(8000),
  });
  assert.ok(response.ok, `${method} ${path} returned ${response.status}`);
  return await response.json();
};

const navigation = async (start) => {
  const response = await fetch(hub + "/api/v1/navigation", {
    method: start === undefined ? "GET" : "PUT",
    headers: { Authorization: "Bearer mock", "Content-Type": "application/json" },
    ...(start === undefined ? {} : { body: JSON.stringify({ start }) }),
    signal: AbortSignal.timeout(8000),
  });
  assert.ok(response.ok, `mock navigation returned ${response.status}`);
  return await response.json();
};

const sourcePhase = async (phase) => {
  const response = await fetch(hub + "/mock/starred-source", {
    method: "POST",
    headers: { Authorization: "Bearer mock", "Content-Type": "application/json" },
    body: JSON.stringify({ phase }),
    signal: AbortSignal.timeout(8000),
  });
  assert.ok(response.ok, `setting the positional source phase returned ${response.status}`);
};

async function fixtureReply(mid, sid) {
  const source = `/api/v1/sessions/${encodeURIComponent(mid)}/${encodeURIComponent(sid)}/items?turns=2`;
  const response = await fetch(hub + source, {
    headers: { Authorization: "Bearer mock" },
    signal: AbortSignal.timeout(8000),
  });
  assert.ok(response.ok, `reading the nested source returned ${response.status}`);
  const reply = (await response.json()).items.findLast((item) => item.role === "assistant" && item.kind === "text");
  assert.ok(reply?.id, `missing reply in the nested source ${sid}`);
  return reply;
}

const browser = await chromium.launch({ headless: true, ...(process.env.STARRED_BROWSER ? { executablePath: process.env.STARRED_BROWSER } : { channel: "chrome" }) });
async function device(theme, phone = false, { width = phone ? 390 : 1440, large = false, reducedMotion = "no-preference" } = {}) {
  const ctx = await browser.newContext({
    viewport: { width, height: phone ? 844 : 900 },
    isMobile: phone, hasTouch: phone, colorScheme: theme, reducedMotion, locale: "en-US",
    permissions: ["clipboard-read", "clipboard-write"],
  });
  await ctx.route("**/*", (route) => allowed.has(new URL(route.request().url()).origin) ? route.continue() : route.abort());
  await ctx.addInitScript(({ theme, large }) => {
    localStorage.setItem("persona.language", "en");
    localStorage.setItem("persona.theme", theme);
    if (large) localStorage.setItem("persona.text-size", "large");
  }, { theme, large });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.setDefaultTimeout(15000);
  return { page, errors };
}

async function rowCount(page, n) {
  await page.waitForFunction((count) => document.querySelectorAll('ol[aria-label="Starred"] li > article').length === count, n);
}

let cleanup = false;
let previousStart = "";
try {
  assert.deepEqual((await onHub()).stars, [], "use an empty mock");
  previousStart = (await navigation()).start ?? "";
  cleanup = true;
  const desktop = await device("light");
  await desktop.page.goto(web + review + "&token=mock");
  const desktopUser = desktop.page.locator("[data-user-bubble]").filter({ hasText: "PR #42" }).first();
  await desktopUser.waitFor();
  await desktop.page.getByRole("link", { name: "Starred", exact: true }).first().waitFor();
  await desktopUser.getByText("PR #42", { exact: false }).first().click({ button: "right" });
  await desktop.page.getByRole("menuitem", { name: "Star", exact: true }).click();
  await desktopUser.locator('[data-star-mark="saved"]').waitFor();
  await shot(desktop.page, "agent-message-light");

  const mobile = await device("dark", true);
  await mobile.page.goto(web + "/starred?token=mock");
  await mobile.page.getByRole("heading", { name: "Starred", exact: true }).waitFor();
  await rowCount(mobile.page, 1);
  // The mobile list is open while a separate device stars an Assistant reply.
  await desktop.page.goto(web + "/persona");
  const reply = desktop.page.locator("[data-answer=true]").last();
  await reply.waitFor();
  await reply.locator("p").first().click({ button: "right" });
  await desktop.page.getByRole("menuitem", { name: "Star", exact: true }).click();
  await rowCount(mobile.page, 2);
  const stored = (await onHub()).stars;
  assert.deepEqual(stored.map((star) => [star.conversation, star.role]), [["assistant", "assistant"], ["agent", "user"]]);

  await desktop.page.getByRole("link", { name: /Starred/ }).first().click();
  await rowCount(desktop.page, 2);
  await shot(desktop.page, "starred-desktop-light");
  await shot(mobile.page, "starred-phone-dark");
  const search = mobile.page.getByRole("textbox", { name: "Search starred messages" });
  await search.fill("You PR #42");
  await rowCount(mobile.page, 1);
  const card = mobile.page.getByRole("article");
  await card.getByText("You", { exact: true }).waitFor();
  await card.getByRole("button", { name: "Copy", exact: true }).click();
  assert.match(await mobile.page.evaluate(() => navigator.clipboard.readText()), /PR #42/);
  await card.getByRole("button", { name: "Unstar" }).click();
  await rowCount(desktop.page, 1);
  await mobile.page.getByRole("button", { name: "Undo" }).click();
  await rowCount(desktop.page, 2);

  await card.getByRole("link", { name: "Open original" }).click();
  await mobile.page.waitForFunction(() => new URLSearchParams(location.search).has("at"));
  await mobile.page.locator("[data-search-hit]").waitFor();
  await shot(mobile.page, "agent-message-phone-dark");

  await desktop.page.locator(`a[href*="${encodeURIComponent(stored[0].item_id)}"]`).click();
  await desktop.page.locator("[data-search-hit]").waitFor();
  assert.equal(new URL(desktop.page.url()).searchParams.get("at"), stored[0].item_id);
  await shot(desktop.page, "assistant-message-light");
  await desktop.page.getByRole("link", { name: /Starred/ }).first().click();

  const source = await onHub("POST", "", {
    machine_id: "macbook", session_id: stored[1].session_id, item_id: "removed-example",
    conversation: "agent", role: "assistant", title: "Former conversation", speaker: "Agent",
    text: "Saved before the original went away.",
  });
  await desktop.page.getByText("Saved before the original went away.").waitFor();
  await desktop.page.reload();
  await desktop.page.getByText("Saved before the original went away.").waitFor();
  const orphan = desktop.page.getByRole("article").filter({ hasText: "Former conversation" });
  await orphan.getByRole("link", { name: "Open original" }).click();
  assert.equal(new URL(desktop.page.url()).searchParams.get("at"), source.star.item_id);

  await desktop.page.getByRole("link", { name: /Starred/ }).first().click();
  await rowCount(desktop.page, 3);
  await onHub("POST", "", {
    machine_id: "macbook", session_id: stored[1].session_id, item_id: "long-example",
    conversation: "agent", role: "assistant", title: "Long review snapshot", speaker: "Reviewer",
    text: "## Findings\n\n" + Array.from({ length: 9 }, (_, i) => `- Note ${i + 1} for the documentation review.\n`).join("") +
      "\nA unique note after the folded preview.",
    omitted_chars: 23,
  });
  const largeWide = await device("light", false, { large: true });
  const largeNarrow = await device("dark", true, { width: 320, large: true });
  for (const [name, layout] of [["desktop", largeWide], ["phone-320", largeNarrow]]) {
    await layout.page.goto(web + "/starred?token=mock");
    const long = layout.page.getByRole("article").filter({ hasText: "Long review snapshot" });
    await long.getByText("23 more characters in the original message").waitFor();
    assert.equal(await long.getByText("A unique note after the folded preview.").count(), 0);
    await shot(layout.page, `starred-${name}-large-folded`);
    await long.getByRole("button", { name: "Show saved text" }).click();
    await long.getByText("A unique note after the folded preview.").waitFor();
    assert.equal(await long.evaluate((card) => [...card.querySelectorAll("button, a")].every((action) => {
      const box = action.getBoundingClientRect();
      return box.left >= 0 && box.right <= innerWidth && box.height > 0;
    })), true, "all actions fit at the largest supported text size");
    await shot(layout.page, `starred-${name}-large-expanded`);
  }

  const old = await device("light", true, { width: 320, large: true });
  await old.page.route("**/api/v1/assistant/starred**", (route) => route.fulfill({
    status: 404, contentType: "application/json", body: '{"error":"Not found"}',
  }));
  await old.page.goto(web + "/starred?token=mock");
  await old.page.getByRole("heading", { name: "Starred messages are unavailable" }).waitFor();
  assert.equal(await old.page.getByRole("link", { name: "Starred", exact: true }).count(), 0);
  await shot(old.page, "starred-older-assistant-320-large");

  // A Claude/Codex fallback line ID denotes a position. Rewriting that line
  // must bracket the historical snapshot away from the replacement's marker and link.
  const reused = "line900#0";
  const originalWords = "Original words from the reused line";
  const replacementWords = "Replacement words from the reused line";
  const bubble = (words) => desktop.page.locator("[data-user-bubble]").filter({ hasText: words });
  const cardFor = (words) => desktop.page.getByRole("article").filter({ hasText: words });
  const openStarred = async () => {
    await desktop.page.getByRole("link", { name: /Starred/ }).first().click();
    await desktop.page.getByRole("heading", { name: "Starred", exact: true }).waitFor();
  };
  const menu = async (words, action) => {
    await bubble(words).getByText(words, { exact: true }).click({ button: "right" });
    await desktop.page.getByRole("menuitem", { name: action, exact: true }).click();
  };

  await sourcePhase("original");
  await desktop.page.goto(web + review);
  await bubble(originalWords).waitFor();
  await desktop.page.getByRole("link", { name: /Starred/ }).first().waitFor();
  await menu(originalWords, "Star");
  await bubble(originalWords).locator('[data-star-mark="saved"]').waitFor();
  await openStarred();
  const originalHref = await cardFor(originalWords).getByRole("link", { name: "Open original" }).getAttribute("href");
  assert.match(new URL(originalHref, web).searchParams.get("at"), /^star:a{32}:line900#0$/);

  // A same-size in-place edit can leave the page's record stale even after reloading.
  await sourcePhase("stale");
  await desktop.page.goto(new URL(originalHref, web).href);
  await bubble(originalWords).waitFor();
  await desktop.page.getByText("The original message is unavailable.").waitFor();
  assert.equal(await desktop.page.locator("[data-search-hit]").count(), 0);
  await openStarred();

  await sourcePhase("replacement");
  await cardFor(originalWords).getByRole("link", { name: "Open original" }).click();
  await cardFor(originalWords).getByText("The original message is unavailable.").waitFor();
  await shot(desktop.page, "starred-reconstructed-source");
  assert.equal(new URL(desktop.page.url()).pathname, "/starred");

  // Direct links also verify on landing, even without the snapshot-page preflight.
  await desktop.page.goto(new URL(originalHref, web).href);
  await bubble(replacementWords).waitFor();
  await desktop.page.getByText("The original message is unavailable.").waitFor();
  assert.equal(await desktop.page.locator("[data-search-hit]").count(), 0);
  await desktop.page.getByRole("link", { name: /Starred/ }).first().waitFor();
  assert.equal(await bubble(replacementWords).locator('[data-star-mark="saved"]').count(), 0);

  await menu(replacementWords, "Star");
  await bubble(replacementWords).locator('[data-star-mark="saved"]').waitFor();
  const copies = (await onHub("GET", "?index=1")).refs.filter((ref) => ref.item_id === reused);
  assert.deepEqual(copies.map((ref) => ref.source_fingerprint), ["b".repeat(32), "a".repeat(32)]);
  await openStarred();
  await cardFor(replacementWords).getByRole("link", { name: "Open original" }).click();
  await desktop.page.locator("[data-search-hit]").waitFor();
  assert.match(new URL(desktop.page.url()).searchParams.get("at"), /^star:b{32}:line900#0$/);
  const [removed] = await Promise.all([
    desktop.page.waitForResponse((response) => response.request().method() === "DELETE" &&
      new URL(response.url()).pathname === `${path}/${copies[0].id}`),
    menu(replacementWords, "Unstar"),
  ]);
  assert.ok(removed.ok());
  await bubble(replacementWords).locator('[data-star-mark="saved"]').waitFor({ state: "hidden" });
  assert.deepEqual((await onHub("GET", "?index=1")).refs.filter((ref) => ref.item_id === reused).map((ref) => ref.id), [copies[1].id]);

  await sourcePhase("reset");
  await openStarred();
  await cardFor(originalWords).getByRole("link", { name: "Open original" }).click();
  await cardFor(originalWords).getByText("The original message is unavailable.").waitFor();
  assert.equal(new URL(desktop.page.url()).pathname, "/starred");
  await desktop.page.goto(new URL(originalHref, web).href);
  await desktop.page.getByText("The original message is unavailable.").waitFor();
  assert.equal(await desktop.page.locator("[data-search-hit]").count(), 0);

  await sourcePhase("long-user");
  await desktop.page.goto(web + review);
  const longUser = desktop.page.locator('[data-user-bubble][data-items~="fixture-long-user#0"]');
  await longUser.locator(".group\\/msg > div").first().click({ button: "right" });
  await desktop.page.getByRole("menuitem", { name: "Star", exact: true }).click();
  await longUser.locator('[data-star-mark="saved"]').waitFor();
  const fullUserText = [...("User-visible long note " + "🐈".repeat(6500))];
  const userStar = (await onHub()).stars.find((star) => star.item_id === "fixture-long-user#0");
  assert.equal(userStar.text, fullUserText.slice(0, 4000).join(""));
  assert.equal(userStar.omitted_chars, fullUserText.length - 4000);

  const startup = await device("light");
  await startup.page.addInitScript(() => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.startPage", "starred");
  });
  let allowIndex;
  const delayed = new Promise((resolve) => { allowIndex = resolve; });
  await startup.page.route("**/api/v1/assistant/starred*", async (route) => {
    await delayed;
    await route.continue();
  });
  try {
    await Promise.all([
      startup.page.waitForRequest((request) => new URL(request.url()).searchParams.has("index") && request.url().includes("/assistant/starred?")),
      startup.page.goto(web + "/"),
    ]);
    assert.equal(new URL(startup.page.url()).pathname, "/starred");
  } finally {
    allowIndex();
  }
  await startup.page.getByRole("heading", { name: "Starred", exact: true }).waitFor();
  await startup.page.getByRole("link", { name: /Starred/ }).first().waitFor();
  assert.equal(new URL(startup.page.url()).pathname, "/starred");

  const nestedErrors = [];
  if (nestedSources) {
    await sourcePhase("reset");
    const cases = nestedSources === "team" ? [
      ["worker", "server", "7ab5e000-0000-4000-8000-000000000003", true],
    ] : [
      ["side", "macbook", "a1f3c2d4-0000-4000-8000-0000000059a2", true],
      ["child", "server", "paseo-9a5e0000-0000-4000-8000-000000000007", false],
      ["earlier-engine", "macbook", "019a5054-0000-7000-8000-000000000002", false],
    ];
    for (const [kind, mid, sid, embedded] of cases) {
      const item = await fixtureReply(mid, sid);
      const title = `Saved message in ${kind}`;
      await onHub("POST", "", {
        machine_id: mid, session_id: sid, item_id: item.id, conversation: "agent", role: "assistant",
        title, speaker: "Agent", text: item.text,
      });
      const nested = await device(kind === "side" ? "dark" : "light", false, { reducedMotion: "reduce" });
      nestedErrors.push(nested.errors);
      await nested.page.goto(web + "/starred?token=mock");
      const card = nested.page.getByRole("article").filter({ hasText: title });
      await card.getByRole("link", { name: "Open original" }).click();
      const target = nested.page.locator(embedded ? '[role="tabpanel"] [data-search-hit]' : "[data-search-hit]");
      try {
        await target.waitFor();
      } catch (error) {
        await shot(nested.page, `starred-${kind}-landing-failure`);
        throw new Error(`${kind} message ${item.id} did not land at ${new URL(nested.page.url()).pathname}`, { cause: error });
      }
      assert.equal((await target.textContent()).trim(), item.text.trim());
      if (!embedded) assert.equal(new URL(nested.page.url()).pathname, `/s/${mid}/${encodeURIComponent(sid)}`);
      if (embedded) await shot(nested.page, `starred-${kind}-original`);
    }
    console.log(`ok: saved messages land in their exact ${cases.map(([kind]) => kind).join(", ")} transcripts`);
  }

  assert.deepEqual(desktop.errors.concat(mobile.errors, largeWide.errors, largeNarrow.errors, old.errors, startup.errors, ...nestedErrors), []);
  console.log("ok: both message menus and exact links, two-device push, filter, copy, undo, orphaned snapshot, older assistant, largest text at 320 px, positional-ID reuse and removal, displayed-speaker search, delayed start-page availability, and accurate full user-message snapshots");
} finally {
  await browser.close();
  if (cleanup) {
    await sourcePhase("reset");
    await navigation(previousStart);
    for (const star of (await onHub()).stars) await onHub("DELETE", "/" + encodeURIComponent(star.id));
  }
}
