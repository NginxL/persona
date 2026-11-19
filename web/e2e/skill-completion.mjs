// Start scripts/mock/skill_mentions.py and the Web app on loopback, then run
// node web/e2e/skill-completion.mjs <web origin> <evidence dir> [before|after]
// CHROME_BIN optionally selects an installed Chromium executable.
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { chromium } from "playwright-core";

const [origin, output, phase = "after"] = process.argv.slice(2);
assert.match(origin, /^http:\/\/(localhost|127\.0\.0\.1):\d+$/);
assert.ok(output && ["before", "after"].includes(phase));
await mkdir(output, { recursive: true });
const after = phase === "after";
const agent = "/s/macbook/d2222222-0000-4000-8000-000000000002";
const examples = [
  ["document-review", "Review a draft for clarity and accuracy. Identify ambiguous passages, explain their impact, and suggest precise edits while preserving the author’s voice."],
  ["document-review", "Apply this project’s editorial guidelines to a document."],
  ["accessibility-audit", "Check contrast, focus order and keyboard navigation."],
  ["diagram-notes", "Turn a diagram into a short written explanation."],
  ["release-summary", "Summarize changes for a release announcement."],
  ["chapter-review-with-editorial-guidelines-for-long-form-technical-documents", "Prepare a review of a long document."],
];
const skills = Array.from({ length: 56 }, (_, i) => {
  const [name, description] = examples[i] ?? [`chapter-${i}`, `Prepare notes for chapter ${i}.`];
  return { id: `example-${i}`, name, description, source: ["user · ~/example/skills", "project · ./example/skills", "system · /example/system"][i % 3] };
});
const report = { phase, delayMs: 450, timings: [], screenshots: [] };
const browser = await chromium.launch(process.env.CHROME_BIN ? { executablePath: process.env.CHROME_BIN } : { channel: "chrome" });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, reducedMotion: "reduce", serviceWorkers: "block" });
  await page.addInitScript(() => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.language", "en");
  });
  let state = "ready";
  const held = Promise.withResolvers();
  const release = Promise.withResolvers();
  const settled = Promise.withResolvers();
  const posts = [];
  page.on("request", r => { if (r.method() === "POST" && r.url().endsWith("/messages")) posts.push(r.postDataJSON()); });
  await page.route(/\/api\/v1\/(assistant\/skills\?|sessions\/[^/]+\/[^/]+\/skills$)/, async route => {
    const requested = state;
    const url = route.request().url();
    const isAgent = url.includes("/sessions/");
    if (requested === "held") {
      held.resolve();
      await release.promise;
    }
    await delay(report.delayMs);
    const catalog = requested === "empty" ? [] : skills.map(s => ({ ...s, name: url.includes("/studio/") ? "writing-" + s.name : s.name, id: (isAgent ? "agent-" : "assistant-") + s.id }));
    await route.fulfill(requested === "error" ? { status: 503, json: { error: "Example catalog unavailable" } }
      : { json: { catalog_version: 1, skills: catalog, complete: true } }).catch(() => {});
    if (requested === "held") settled.resolve();
  });
  const field = page.locator("textarea:visible").last();
  const options = page.getByRole("option");
  const reviews = options.filter({ hasText: "document-review" });
  const selected = page.locator('[role="option"][aria-selected="true"]');
  async function measure(label, token) {
    await field.fill("");
    await options.first().waitFor({ state: "hidden" });
    // Observe DOM commits from the input event. Locator polling would inflate cold timings.
    await page.evaluate(() => {
      window.pickerMeasurement = new Promise(resolve => {
        document.addEventListener("input", () => {
          const start = performance.now();
          let local;
          let sawLoading = false;
          const observer = new MutationObserver(check);
          function check() {
            const rows = [...document.querySelectorAll('[role="option"]')];
            sawLoading ||= [...document.querySelectorAll('[role="status"]')].some(el => el.textContent.includes("Loading skills"));
            if (rows.length && local === undefined) local = Math.round(performance.now() - start);
            if (rows.some(r => r.textContent.includes("document-review"))) {
              observer.disconnect();
              resolve({ sawLoading, firstOptionMs: local, firstSkillMs: Math.round(performance.now() - start),
                skills: rows.filter(r => /document-review|accessibility-audit|diagram-notes|release-summary|chapter-/.test(r.textContent)).length });
            }
          }
          observer.observe(document.body, { subtree: true, childList: true, characterData: true });
          check();
        }, { capture: true, once: true });
      });
    });
    const refreshed = page.waitForResponse(r => /\/skills(\?|$)/.test(r.url()) && r.status() === 200);
    await field.fill(token);
    const [result] = await Promise.all([page.evaluate(() => window.pickerMeasurement), refreshed]);
    report.timings.push({ label, ...result });
    console.log(label, result);
    if (after) {
      assert.equal(result.skills, skills.length);
      if (label.includes("cold") || label.startsWith("Switch")) assert.ok(result.sawLoading);
    }
  }
  await page.goto(origin + "/persona/new");
  await measure("Assistant cold @", "@");
  await measure("Assistant warm @", "@");
  await measure("Assistant warm /", "/");
  await page.goto(origin + agent);
  await page.getByText(/Started in .*project/).waitFor();
  await measure("Switch to Agent @", "@");
  if (after) {
    await field.fill("@chapter-55");
    assert.match(await selected.innerText(), /chapter-55/);
    await field.dispatchEvent("keydown", { key: "Enter", isComposing: true });
    assert.equal(await field.inputValue(), "@chapter-55");
    await field.press("Escape");
    await options.first().waitFor({ state: "hidden" });
    await field.fill("/");
    await reviews.first().waitFor();
    await field.press("ArrowUp"); // wrap from the first command to the last skill
    assert.match(await selected.innerText(), /chapter-55/);
    assert.ok(await page.getByRole("listbox").evaluate(el => el.scrollTop > 0));
    await field.press("Tab");
    assert.equal(await field.inputValue(), "");
    await field.fill("Summarize the example");
    const receipt = page.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith("/messages"));
    await page.getByRole("button", { name: "Send", exact: true }).last().click();
    assert.ok((await receipt).ok());
    assert.match(posts.at(-1).text, /kind="skill"[^>]+ref="agent-example-55"/);
    await field.fill("/compact");
    await options.filter({ hasText: "compact" }).first().click();
    assert.equal((await field.inputValue()).trim(), "/compact");
    const cdp = await page.context().newCDPSession(page);
    for (const [device, width] of [["desktop", 1440], ["phone", 390]]) {
      await page.setViewportSize({ width, height: 844 });
      await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: device === "phone" });
      await page.evaluate(size => document.documentElement.dataset.textSize = size, device === "phone" ? "large" : "standard");
      for (const [menu, trigger] of [["at", "@"], ["slash", "/"]]) {
        await field.fill(trigger + "chapter-review-with");
        await options.first().hover();
        const longTitle = page.locator("[data-picker-detail]").getByText(examples[5][0], { exact: true });
        assert.ok(await longTitle.isVisible());
        assert.ok(await longTitle.evaluate(el => el.scrollWidth <= el.clientWidth + 1));
        await field.fill(trigger);
        await reviews.first().hover();
        const detail = page.locator("[data-picker-detail]");
        assert.match(await detail.innerText(), /user · ~\/example\/skills/);
        assert.ok(!(await options.allInnerTexts()).some(text => text.includes("example/skills")));
        assert.ok(await page.locator("[data-picker-group]").count() >= 2);
        assert.equal(await field.evaluate(el => el === document.activeElement), true);
        const popup = page.locator("[data-slash-palette],[data-composer-panel]").last();
        const bounds = await popup.boundingBox();
        const card = await detail.boundingBox();
        assert.ok(bounds.y >= 0 && bounds.x >= 0 && bounds.x + bounds.width <= width + 1);
        assert.ok(card.y >= 0 && card.x >= 0 && card.x + card.width <= width + 1);
        if (device === "phone") assert.ok(card.x >= bounds.x && card.x + card.width <= bounds.x + bounds.width + 1);
        else assert.ok(card.x >= bounds.x + bounds.width || card.x + card.width <= bounds.x);
        const box = await field.boundingBox();
        const x = Math.max(0, Math.floor(Math.min(bounds.x, card.x, box.x) - 12));
        const y = Math.max(0, Math.floor(Math.min(bounds.y, card.y) - 12));
        const right = Math.min(width, Math.ceil(Math.max(bounds.x + bounds.width, card.x + card.width, box.x + box.width) + 12));
        const bottom = Math.min(844, Math.ceil(box.y + box.height + 54));
        for (const theme of ["light", "dark"]) {
          await page.evaluate(value => document.documentElement.classList.toggle("dark", value === "dark"), theme);
          const name = `${device}-${menu}-${theme}.png`;
          await page.screenshot({ path: join(output, name), clip: { x, y, width: right - x, height: bottom - y }, animations: "disabled" });
          report.screenshots.push(name);
        }
        await reviews.nth(1).click();
        assert.equal(await field.inputValue(), "");
        const chip = page.getByRole("button", { name: "Remove this context" }).last().locator("..");
        assert.ok(!(await chip.innerText()).includes("example/skills"));
        assert.ok(!(await chip.getAttribute("title")).includes("example/skills"));
        if (device === "desktop" && menu === "at") {
          await field.fill("Check the wording");
          const sent = page.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith("/messages"));
          await page.getByRole("button", { name: "Send", exact: true }).last().click();
          assert.ok((await sent).ok());
          assert.match(posts.at(-1).text, /kind="skill"[^>]+ref="agent-example-1"/);
        } else {
          await page.getByRole("button", { name: "Remove this context" }).last().click();
        }
      }
    }
    // Change node, session, launcher and project while an old refresh is in flight.
    state = "held";
    await field.fill("@document");
    await held.promise;
    state = "ready";
    const target = "/s/studio/e3333333-0000-4000-8000-000000000003";
    await page.evaluate(path => { history.pushState({}, "", path); dispatchEvent(new PopStateEvent("popstate")); }, target);
    await page.getByText(/Started in .*writing/).waitFor();
    await field.fill("@writing-document");
    await options.filter({ hasText: "writing-document-review" }).first().waitFor();
    release.resolve();
    await settled.promise;
    await field.fill("@document-review");
    assert.equal(await options.count(), 2);
    assert.ok((await options.allInnerTexts()).every(text => text.includes("writing-document-review")));
    await field.press("Escape");
    state = "error";
    await field.fill("again");
    await field.fill("@document");
    await page.getByText("Skills unavailable here.", { exact: false }).waitFor();
    assert.equal(await reviews.count(), 0);
    state = "empty";
    await field.fill("plain text");
    await field.fill("/absent");
    await page.getByText("No matching commands or skills", { exact: true }).waitFor();
    assert.equal(await options.count(), 0);
  }
  await writeFile(join(output, phase + ".json"), JSON.stringify(report, null, 2) + "\n");
} finally {
  await browser.close();
}
