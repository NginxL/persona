// Run with user-history-navigation.mjs <loopback origin> <artifact directory> 75 main 12 standard cancel.
import assert from "node:assert/strict";
import { join } from "node:path";

export const interruptedJumps = [
  { size: "wide", theme: "light", source: "rail", action: "Jump to bottom" },
  { size: "narrow", theme: "light", source: "rail", action: "wheel to bottom" },
  { size: "wide", theme: "dark", source: "link", action: "pick another target" },
  { size: "narrow", theme: "dark", source: "link", action: "wheel into history" },
];

function readerPosition(scroller) {
  const top = scroller.getBoundingClientRect().top;
  const row = [...scroller.querySelectorAll("[data-line]")].find(el => el.firstElementChild?.getBoundingClientRect().bottom > top);
  return {
    gap: scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop,
    line: row?.dataset.line, offset: row?.firstElementChild.getBoundingClientRect().top - top,
  };
}

// Let the wheel or glide finish, including the frames a queued seek gets after the page is rendered.
async function settledPosition(chat) {
  await chat.evaluate(sc => new Promise(resolve => {
    let lastTop, still = 0;
    const frame = () => {
      still = lastTop === sc.scrollTop ? still + 1 : 0;
      lastTop = sc.scrollTop;
      if (still >= 12) resolve();
      else requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
  }));
  return chat.evaluate(readerPosition);
}

// Intercept the same before_seq response for each case. Observe real scroll offsets after the
// prepend, including the frames where a stale seek would scroll or light its row.
export async function checkInterruptedJump({ page, url, run, output, target, second, secondOrdinal, source, action, release, append }) {
  if (source === "link") url.searchParams.set("at", target.id);
  await page.goto(url.href);
  const chat = page.locator("[data-chat-scroll]");
  await page.locator('[data-nav-source="server"]').waitFor();
  run.initialBodies = await chat.locator("[data-user-bubble]").count();
  const box = await chat.boundingBox();
  const wheel = async dy => {
    await page.mouse.move(box.x + 40, box.y + box.height / 2);
    await page.mouse.wheel(0, dy);
  };
  if (source === "rail") {
    await wheel(-1200);
    await page.waitForFunction(() => document.querySelector("[data-jump-pill]"));
    await page.locator('[data-nav-rail] [data-tick="0"]').click();
  }
  await page.locator("[data-loading-earlier]").waitFor();
  assert.equal(run.olderReads, 1);
  if (action === "pick another target") {
    await page.getByRole("button", { name: new RegExp(`^Jump to user input ${secondOrdinal}:`) }).click();
    await page.waitForFunction(id => {
      const row = document.querySelector(`[data-line="seg:${CSS.escape(id)}"]`).firstElementChild;
      return Math.abs(row.getBoundingClientRect().top - document.querySelector("[data-chat-scroll]").getBoundingClientRect().top - 12) <= 1;
    }, second.id);
  } else if (action === "Jump to bottom") {
    await page.locator("[data-jump-pill]").click();
    await page.waitForFunction(() => !document.querySelector("[data-jump-pill]"));
  } else {
    await wheel(action === "wheel into history" ? -1800 : 3600);
    await page.waitForFunction(history => {
      const sc = document.querySelector("[data-chat-scroll]");
      const gap = sc.scrollHeight - sc.clientHeight - sc.scrollTop;
      return history ? gap > 1000 : gap === 0;
    }, action === "wheel into history");
  }
  const end = action === "Jump to bottom" || action === "wheel to bottom";
  if (end) await page.waitForFunction(() => {
    const sc = document.querySelector("[data-chat-scroll]");
    return sc.scrollTop === sc.scrollHeight - sc.clientHeight;
  });
  run.before = await settledPosition(chat);
  assert.ok(run.before.line);
  // Deliver live activity and the backward page together.
  append();
  release();
  await page.locator(`[data-line="seg:${target.id}"]`).waitFor({ state: "attached" });
  await chat.locator("[data-compact-divider]").waitFor({ state: "attached" });
  await chat.locator("[data-supervision]").waitFor({ state: "attached" });
  await page.locator("[data-nav-loading]").waitFor({ state: "hidden" });
  run.after = await settledPosition(chat);
  run.action = `${source} then ${action}`;
  console.log(`${run.action}: ${JSON.stringify(run.before)} -> ${JSON.stringify(run.after)}`);
  assert.equal(await chat.locator("[data-search-hit]").count(), 0, "The cancelled link must not light its row");
  if (end) {
    assert.equal(run.after.gap, 0);
    assert.equal(await page.locator("[data-jump-pill]").count(), 0, "Still following live messages");
  } else {
    assert.equal(run.after.line, run.before.line);
    assert.ok(Math.abs(run.after.offset - run.before.offset) <= 1, "The visible line keeps its pixel offset");
  }
  assert.equal(run.olderReads, 1);
  assert.deepEqual(run.pageErrors, []);
  const name = `history-${run.name}-after-release.png`;
  await page.screenshot({ path: join(output, name) });
  run.screenshots.push(name);
}
