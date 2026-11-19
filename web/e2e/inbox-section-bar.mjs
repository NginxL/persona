// The Inbox's section bar in a real browser (#9053, docs/assistant/inbox.md "Section bar"): it opens on Needs you, stays in
// sight over the scrolling list, keeps to one row (its words never wrap, in every language) with All and the grouping
// always in full and the choice made in sight, its sections scrolling sideways (faded, a mouse's wheel too) where they
// don't fit, shows a choice's section alone, keeps the grouping in its trailing menu, lands on a linked item's choice
// with its row unfolded and stays there, moves only a list beside an item opened from inside and goes back with it, and
// screenshots each at 1280 and 390, light and dark.
// Usage: node web/e2e/inbox-section-bar.mjs <loopback-web-origin> <artifact-directory>
// The origin is an exclusively owned web server whose hub is scripts/mock/mock_hub.py started with
// MOCK_INBOX_MANY=action:30,working:6,fyi:40,noise:20,done:25 MOCK_MANAGERS=0 (only the mock's invented data).
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import assert from "node:assert/strict";
import { chromium } from "playwright-core";

const [origin, output] = process.argv.slice(2);
if (!origin || !output || !/^http:\/\/(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$/.test(origin)) {
  console.error("usage: node web/e2e/inbox-section-bar.mjs <loopback-web-origin> <artifact-directory>");
  process.exit(2);
}
await mkdir(output, { recursive: true });
let browser;
const deadline = setTimeout(() => {
  console.error("INBOX_SECTION_BAR TIMEOUT");
  void browser?.close();
  process.exit(1);
}, 300_000);

const results = {};
const errors = [];
const LOCALE = { en: "en-US", "zh-Hans": "zh-CN", ja: "ja-JP" };

async function page(theme, lang, { width = 1280, height = 820, touch = false } = {}) {
  const context = await browser.newContext({ viewport: { width, height }, hasTouch: touch, isMobile: touch, locale: LOCALE[lang], colorScheme: theme, deviceScaleFactor: 2 });
  await context.addInitScript(([th, lg]) => {
    localStorage.setItem("persona.token", "mock");
    localStorage.setItem("persona.language", lg);
    localStorage.setItem("persona.theme", th);
    for (const k of ["inbox.source", "inbox.kind", "inbox.folder", "inbox.grouping", "inbox.folds"]) localStorage.removeItem(k);
  }, [theme, lang]);
  const p = await context.newPage();
  p.on("pageerror", (e) => errors.push(e.message));
  return { context, p };
}
const shot = (p, name) => p.screenshot({ path: join(output, `${name}.png`) });
const chosen = (p) => p.locator('[data-bar-choice][aria-checked="true"]').getAttribute("data-bar-choice");
const sections = (p) => p.locator("main [data-section]").evaluateAll((els) => els.map((el) => el.getAttribute("data-section")));
// the bar's choices: one row, none taller than a pill (a wrapped word would be), and the page itself never scrolls sideways;
// All and the grouping wholly in the bar, the choice made wholly in the sections' row, and that row faded while it
// scrolls sideways (`cut`: the choices only partly in sight then)
const barShape = (p) => p.evaluate(() => {
  const bar = document.querySelector("[data-inbox-bar]");
  const group = bar.querySelector('[role="radiogroup"]');
  const strip = bar.querySelector("[data-bar-strip]");
  const within = (el, box) => {
    const r = el.getBoundingClientRect();
    const b = box.getBoundingClientRect();
    return r.left >= b.left - 0.5 && r.right <= b.right + 0.5;
  };
  const pills = [...group.querySelectorAll("[data-bar-choice]")];
  const rects = pills.map((el) => el.getBoundingClientRect());
  const on = group.querySelector('[data-bar-choice][aria-checked="true"]');
  return {
    rows: new Set(rects.map((r) => Math.round(r.top))).size,
    tallest: Math.max(...rects.map((r) => r.height)),
    pageWidth: document.documentElement.scrollWidth,
    width: innerWidth,
    allIn: within(group.querySelector('[data-bar-choice="all"]'), bar) && within(bar.querySelector('button[aria-haspopup="menu"]'), bar),
    chosenIn: strip.contains(on) ? within(on, strip) : within(on, bar),
    sideways: strip.scrollWidth > strip.clientWidth,
    faded: Boolean(strip.style.maskImage || strip.style.webkitMaskImage),
    cut: pills.filter((el) => strip.contains(el) && !within(el, strip)).map((el) => el.dataset.barChoice),
  };
});
const fits = (tag, shape) => {
  assert.ok(shape.rows === 1 && shape.tallest <= 24 && shape.pageWidth <= shape.width, `${tag}: one row, no choice wraps, the page doesn't scroll sideways ${JSON.stringify(shape)}`);
  assert.ok(shape.allIn && shape.chosenIn, `${tag}: All, the grouping and the choice made in full ${JSON.stringify(shape)}`);
  assert.ok(!shape.sideways || shape.faded, `${tag}: a row that scrolls sideways is faded where it goes on ${JSON.stringify(shape)}`);
};

try {
  browser = await chromium.launch({ channel: "chrome", headless: true });
  for (const theme of ["light", "dark"]) {
    for (const [width, height, touch] of [[1280, 820, false], [390, 844, true]]) {
      const tag = `${width}-${theme}`;
      const { context, p } = await page(theme, "en", { width, height, touch });
      await p.goto(`${origin}/inbox`);
      await p.locator("main [data-item]").first().waitFor({ timeout: 60_000 });
      await p.waitForTimeout(600);
      // opens on Needs you, alone, with the bar over the list's scroller
      assert.equal(await chosen(p), "action", `${tag}: opens on Needs you`);
      assert.deepEqual(await sections(p), ["action"], `${tag}: Needs you alone`);
      assert.equal(await p.locator("[data-inbox-list-scroll] [data-inbox-bar]").count(), 0, `${tag}: the bar is outside the scroller`);
      const shape = await barShape(p);
      fits(tag, shape);
      results[`${tag}-cut`] = shape.cut;
      const names = await p.locator("[data-bar-choice]").evaluateAll((els) => els.map((el) => el.getAttribute("aria-label") ?? el.textContent));
      results[`${tag}-names`] = names;
      assert.match(names[0], /^Needs you, \d+$/, `${tag}: the count is in Needs you's spoken name`);
      await shot(p, `bar-needs-you-${tag}`);
      // it stays in sight while the list scrolls
      const top = await p.locator("[data-inbox-bar]").evaluate((el) => el.getBoundingClientRect().top);
      await p.locator("[data-inbox-list-scroll]").evaluate((el) => { el.scrollTop = 1500; });
      await p.waitForTimeout(200);
      assert.equal(await p.locator("[data-inbox-bar]").evaluate((el) => el.getBoundingClientRect().top), top, `${tag}: the bar stays in sight`);
      await shot(p, `bar-needs-you-scrolled-${tag}`);
      // where All and the grouping's place are: the chosen pill's heavier words may move All by a fraction of a px
      const allAt = () => p.locator('[data-bar-choice="all"]').evaluate((el) => el.getBoundingClientRect().left);
      const groupingAt = () => p.locator("[data-bar-grouping]").evaluate((el) => el.getBoundingClientRect().left);
      const allX = await allAt();
      const groupingX = await groupingAt();
      for (const id of ["working", "fyi", "done"]) {
        await p.locator(`[data-bar-choice="${id}"]`).click();
        await p.waitForTimeout(400);
        assert.deepEqual(await sections(p), [id], `${tag}: ${id} alone`);
        assert.equal(await p.locator(`main [data-section="${id}"] > div > button[aria-expanded]:not([aria-haspopup])`).count(), 0, `${tag}: ${id}'s title doesn't fold`);
        assert.equal(await p.locator("[data-inbox-list-scroll]").evaluate((el) => el.scrollTop), 0, `${tag}: ${id} from its top`);
        // the grouping is hidden where the choice isn't grouped, its place kept, so All doesn't move between choices
        fits(`${tag} ${id}`, await barShape(p));
        assert.equal(await p.locator('[data-inbox-bar] button[aria-label="Group by"]').isVisible(), id === "fyi", `${tag}: ${id}'s grouping`);
        assert.equal(await groupingAt(), groupingX, `${tag}: ${id} keeps the grouping's place`);
        assert.ok(Math.abs((await allAt()) - allX) < 1.5, `${tag}: ${id} leaves All in place`);
        await shot(p, `bar-${id}-${tag}`);
      }
      await p.locator('[data-bar-choice="all"]').click();
      await p.waitForTimeout(400);
      assert.deepEqual(await sections(p), ["action", "working", "fyi", "noise", "done"], `${tag}: All stacks every section`);
      await shot(p, `bar-all-${tag}`);
      // the grouping is the bar's trailing menu
      await p.locator('[data-inbox-bar] button[aria-label="Group by"]').click();
      await p.getByRole("menuitemradio", { name: "By time" }).waitFor();
      await p.waitForTimeout(300);
      await shot(p, `bar-grouping-menu-${tag}`);
      await p.getByRole("menuitemradio", { name: "By time" }).click();
      assert.equal(await p.evaluate(() => localStorage.getItem("inbox.grouping")), "time");
      // a search shows All, clearing it goes back to the choice before
      await p.locator('[data-bar-choice="fyi"]').click();
      await p.getByRole("textbox", { name: "Search Inbox" }).fill("a");
      assert.equal(await chosen(p), "all", `${tag}: a search shows All`);
      await p.getByRole("textbox", { name: "Search Inbox" }).fill("");
      assert.equal(await chosen(p), "fyi", `${tag}: clearing it goes back`);
      // the keyboard: one stop, the arrows move and choose, the focus ring in full
      await p.locator('[data-bar-choice="fyi"]').focus();
      await p.keyboard.press("ArrowLeft");
      assert.equal(await chosen(p), "working", `${tag}: ArrowLeft chooses the one before`);
      assert.equal(await p.evaluate(() => document.activeElement?.getAttribute("data-bar-choice")), "working");
      await p.keyboard.press("Home");
      assert.equal(await chosen(p), "action", `${tag}: Home chooses the first`);
      await shot(p, `bar-keyboard-${tag}`);
      await context.close();
    }
  }
  // the words of every language keep to one row, on a phone and in the wide page's 380px list column; where a section
  // is cut, choosing it brings it into sight, and a mouse's wheel scrolls the row
  for (const lang of ["zh-Hans", "ja"]) {
    for (const theme of ["light", "dark"]) {
      for (const [width, height, touch] of [[1280, 820, false], [390, 844, true]]) {
        const tag = `${width}-${theme}-${lang}`;
        const { context, p } = await page(theme, lang, { width, height, touch });
        await p.goto(`${origin}/inbox`);
        await p.locator("main [data-item]").first().waitFor({ timeout: 60_000 });
        await p.waitForTimeout(600);
        const shape = await barShape(p);
        fits(tag, shape);
        results[`${tag}-cut`] = shape.cut;
        results[`${tag}-names`] = await p.locator("[data-bar-choice]").evaluateAll((els) => els.map((el) => el.getAttribute("aria-label") ?? el.textContent));
        await shot(p, `bar-needs-you-${tag}`);
        if (shape.cut.length && !touch) {
          await p.locator("[data-bar-strip]").hover();
          await p.mouse.wheel(0, 120);
          await p.waitForTimeout(200);
          assert.ok(await p.locator("[data-bar-strip]").evaluate((el) => el.scrollLeft > 0), `${tag}: the wheel scrolls the row sideways`);
          await p.locator("[data-bar-strip]").evaluate((el) => { el.scrollLeft = 0; });
        }
        for (const id of shape.cut) {
          await p.locator(`[data-bar-choice="${id}"]`).click();
          await p.waitForTimeout(300);
          fits(`${tag} ${id}`, await barShape(p));
          await shot(p, `bar-${id}-${tag}`);
        }
        await context.close();
      }
    }
  }
  // the landing (docs/assistant/inbox.md "Section bar"): a link to a done item, from outside the Inbox, lands the list on
  // Done with the item's row marked, and the list stays there when the item closes; one opened from inside (another
  // item's Related line, the mock's first Needs-you item names the first done one) moves only a list beside the item,
  // and Back returns that list to the choice it had
  const done = "many:done:0";
  const opener = "many:action:0";
  // the mock's done and collapsed items share their made-up titles: the Related line is picked by its place, the done
  // one first (scripts/mock/test_inbox_many_mock.py)
  const related = (p) => p.locator("[data-event-related] button").first();
  {
    const { context, p } = await page("light", "en");
    await p.goto(`${origin}/inbox`);
    await p.locator("main [data-item]").first().waitFor({ timeout: 60_000 });
    const relates = await p.evaluate(async (id) => {
      const r = await fetch("/api/v1/inbox?sources=all", { headers: { Authorization: "Bearer mock" } });
      return (await r.json()).items.find((it) => it.id === id)?.related?.[0]?.id ?? "";
    }, opener);
    assert.equal(relates, done, "the mock's first Needs-you item names its first done item first");
    // ⌘K over the list: a new entry with no list under it
    await p.evaluate((id) => { history.pushState(null, "", `/inbox/${encodeURIComponent(id)}`); dispatchEvent(new PopStateEvent("popstate")); }, done);
    await p.waitForTimeout(800);
    assert.equal(await chosen(p), "done", "a done item's link lands on Done");
    assert.equal(await p.locator(`main [data-section="done"] [data-item="${done}"] a[aria-current="page"]`).count(), 1, "its row is marked");
    assert.equal(await p.locator(`main [data-item="${done}"]`).evaluate((el) => el.closest("[inert]") === null), true, "its row is not in a folded group");
    await shot(p, "bar-landed-done-1280-light");
    await p.goBack();
    await p.waitForTimeout(600);
    assert.equal(await chosen(p), "done", "from outside, Back leaves the list where the item lives");
    // from inside, beside the list: Needs you, its first item, then that item's Related line to the done one
    await p.locator('[data-bar-choice="action"]').click();
    await p.locator(`main [data-item="${opener}"] > a`).click();
    await related(p).waitFor({ timeout: 10_000 });
    assert.equal(await chosen(p), "action", "a row opened from the list doesn't move it");
    await related(p).click();
    await p.waitForTimeout(800);
    assert.equal(await chosen(p), "done", "from inside, a list beside the item moves to its choice");
    assert.equal(await p.locator(`main [data-section="done"] [data-item="${done}"] a[aria-current="page"]`).count(), 1, "its row is marked");
    await shot(p, "bar-inside-done-1280-light");
    await p.goBack();
    await p.waitForTimeout(600);
    assert.equal(await chosen(p), "action", "Back returns the list beside the item to the choice it had");
    await context.close();
  }
  // a phone: the item takes the list's place. From a link, ‹ shows the list where the item lives; from inside, the list
  // under the item is left alone, and ‹ shows it on its own choice
  {
    const { context, p } = await page("light", "en", { width: 390, height: 844, touch: true });
    await p.goto(`${origin}/inbox/${encodeURIComponent(done)}`);
    const back = p.getByRole("button", { name: "Back to Inbox" });
    await back.waitFor({ timeout: 60_000 });
    await p.waitForTimeout(600);
    await back.click();
    await p.waitForTimeout(600);
    assert.equal(await chosen(p), "done", "a phone: closing a linked item shows the list where it lives");
    assert.equal(await p.locator(`main [data-section="done"] [data-item="${done}"]`).isVisible(), true, "its row is in sight");
    await shot(p, "bar-landed-done-390-light");
    await p.locator('[data-bar-choice="action"]').click();
    await p.locator(`main [data-item="${opener}"] > a`).click();
    await related(p).click();
    await p.waitForTimeout(800);
    assert.match(new URL(p.url()).pathname, /^\/inbox\/many/, "the related item is open");
    await back.click();
    await p.waitForTimeout(600);
    assert.equal(new URL(p.url()).pathname, "/inbox");
    assert.equal(await chosen(p), "action", "a phone: the list under an item opened from inside keeps its choice");
    await shot(p, "bar-inside-back-390-light");
    await context.close();
  }
  assert.deepEqual(errors, []);
  await writeFile(join(output, "results.json"), JSON.stringify(results, null, 2));
  console.log(JSON.stringify(results, null, 2));
  console.log("INBOX_SECTION_BAR PASS");
} catch (error) {
  await writeFile(join(output, "results.json"), JSON.stringify({ results, errors, failure: String(error?.stack ?? error) }, null, 2)).catch(() => {});
  console.error(error);
  console.error("INBOX_SECTION_BAR FAIL");
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser?.close();
}
