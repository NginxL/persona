// UI version 10's graph (#9727) in a real browser against the mock hub: the made-up "Work" page of 200 nodes, a canvas
// jsdom cannot lay out. Synthetic data only; it writes nothing to the hub.
//
// Start the mock with MOCK_EXTENSIONS=1 MOCK_EXTENSION_GRAPH=1 and Vite (or `vite preview`) pointing at it
// (PERSONA_HUB_URL).
// node web/e2e/extension-graph.mjs <loopback web URL> <artifact directory> [Chromium executable]
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const [base, shots, executable] = process.argv.slice(2);
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/extension-graph.mjs <loopback web URL> <artifact directory> [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.error("RESULT: extension-graph timed out (180s)");
  process.exit(124);
}, 180_000);
const root = base.replace(/\/$/, "");
const PAGE = "/extension/example-work/work";
const results = [];
const check = (name, ok, detail) => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
async function page({ width, height, theme, at = PAGE }) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, deviceScaleFactor: 2, serviceWorkers: "block" });
  await context.addInitScript(([theme]) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "en");
  }, [theme]);
  // nothing leaves the loopback origin
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort()));
  const p = await context.newPage();
  await p.goto(`${root}/?token=mock`);
  await p.waitForFunction(() => localStorage.getItem("persona.token"));
  await p.goto(root + at);
  if (at === PAGE) await p.locator("[data-graph-stage]").waitFor({ timeout: 30_000 });
  // the nodes' fade-in comes to rest (the app's busy loops go on, and are not waited for)
  await p.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
  return p;
}

/** The canvas as the browser laid it out: its stage, layer transform, and the visible nodes with their boxes. */
const measure = (p) =>
  p.evaluate(() => {
    const box = (e) => {
      const r = e.getBoundingClientRect();
      return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
    };
    const stage = document.querySelector("[data-graph-stage]");
    const layer = document.querySelector("[data-graph-layer]");
    return {
      stage: box(stage),
      stageClass: stage.className,
      nodes: [...document.querySelectorAll("[data-graph-node]")].map((n) => ({ id: n.dataset.graphNode, box: box(n), label: n.getAttribute("aria-label") })),
      folds: [...document.querySelectorAll("[data-graph-fold]")].map((f) => ({ id: f.dataset.graphFold, label: f.getAttribute("aria-label"), text: f.textContent })),
      edges: document.querySelectorAll("svg path[data-graph-edge]").length,
      frames: [...document.querySelectorAll("[data-graph-frame]")].map((f) => f.dataset.graphFrame),
      selected: !!document.querySelector("[data-graph-selected]"),
      transform: layer.style.transform,
      lw: layer.offsetWidth,
      lh: layer.offsetHeight,
      zoomButtons: [...document.querySelectorAll("button[aria-label]")].slice(-4).map((b) => b.getAttribute("aria-label")),
    };
  });

const transform = (m) => {
  const v = m.transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/);
  return v ? v.slice(1).map(Number) : null;
};

const ZOOM = ["Zoom out", "Zoom in", "Fit", "Full screen"];

let failed = false;
try {
  for (const theme of ["light", "dark"]) {
    const p = await page({ width: 1440, height: 900, theme });
    const m = await measure(p);
    // part 6 is folded by the view: its 12 hidden nodes show as +N, the rest of the 8 parts is there
    const part6 = m.folds.find((f) => f.id === "p6");
    check(`1440 ${theme}: part 6 folded with its hidden count`, part6 && /^Show \d+ hidden/.test(part6.label) && /^\+\d+$/.test(part6.text), part6);
    const ids = m.nodes.map((n) => n.id);
    check(`1440 ${theme}: the visible nodes in reading order, the root first`, ids[0] === "root" && ids.includes("p2.s3") && !ids.includes("p6.s0"), { first: ids[0], count: ids.length, folded: ids.includes("p6.s0") });
    check(`1440 ${theme}: the selected node has the accent ring`, m.selected);
    check(`1440 ${theme}: the group frame with its title`, m.frames.includes("Two ways"), m.frames);
    check(`1440 ${theme}: the four zoom buttons`, JSON.stringify(m.zoomButtons) === JSON.stringify(ZOOM), m.zoomButtons);
    const tr = transform(m);
    check(`1440 ${theme}: opens fitted, held to 50-100%`, tr && tr[2] >= 0.5 && tr[2] <= 1, tr);
    // the opening view is centred on the selected node p2.s3
    const sel = m.nodes.find((n) => n.id === "p2.s3");
    const cx = m.stage.x + m.stage.w / 2;
    check(`1440 ${theme}: the selected node near the centre`, sel && Math.abs(sel.box.x + sel.box.w / 2 - cx) < 200, sel && sel.box);
    await p.screenshot({ path: join(shots, `work-1440-${theme}.png`) });
    // press a node while it is centred in the opening view: the pushed page shows, and Back returns to the same view
    await p.locator('[data-graph-node="p2.s3"]').click();
    const pushed = await p.waitForFunction(() => document.body.textContent.includes("A made-up step"), null, { timeout: 15_000 }).then(() => true, () => false);
    check(`1440 ${theme}: pressing a node pushes its page`, pushed);
    await p.goBack();
    await p.locator("[data-graph-stage]").waitFor({ timeout: 15_000 });
    await p.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
    const back = transform(await measure(p));
    check(`1440 ${theme}: the canvas reopens with its position kept`, back && tr && Math.abs(back[2] - tr[2]) < 0.01, { tr, back });
    // fold part 2 (the person's own choice, beside the centred node): its steps fold away with the count
    await p.locator('[data-graph-fold="p2"]').click();
    const folded2 = await measure(p);
    const hidden = folded2.nodes.filter((n) => n.id.startsWith("p2."));
    check(`1440 ${theme}: folding part 2 hides its subtree`, hidden.length === 0 && folded2.folds.some((f) => f.id === "p2" && /^\+\d+$/.test(f.text)), { hidden: hidden.slice(0, 3), p2: folded2.folds.find((f) => f.id === "p2") });
    // pan: drag the canvas
    const before = back;
    await p.mouse.move(700, 450);
    await p.mouse.down();
    await p.mouse.move(600, 400, { steps: 4 });
    await p.mouse.up();
    const afterPan = transform(await measure(p));
    check(`1440 ${theme}: a drag pans`, afterPan && before && (afterPan[0] !== before[0] || afterPan[1] !== before[1]), { before, afterPan });
    // a redraw that changes only states (the mock advances one step on each draw): the view's own page change — the
    // pushed node's page and back — is the app's own redraw, with the same key, so the person's fold, pan and zoom
    // survive it (the canvas's store keeps them). Press whichever node is inside the stage now.
    const panM = await measure(p);
    const inView = panM.nodes.find((n) =>
      n.box.x >= m.stage.x && n.box.y >= m.stage.y && n.box.x + n.box.w <= m.stage.x + m.stage.w && n.box.y + n.box.h <= m.stage.y + m.stage.h);
    const pressed = inView
      ? (await p.locator(`[data-graph-node="${inView.id}"]`).click({ timeout: 10_000 }).then(() => true, () => false))
      : false;
    const pushed2 = pressed && (await p.waitForFunction(() => document.body.textContent.includes("stands for a node"), null, { timeout: 15_000 }).then(() => true, () => false));
    check(`1440 ${theme}: a node in reach presses after the pan`, pushed2, inView);
    await p.goBack();
    await p.locator("[data-graph-stage]").waitFor({ timeout: 15_000 });
    await p.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
    const redrawn = await measure(p);
    const redrew = transform(redrawn);
    check(`1440 ${theme}: the person's fold, pan and zoom kept across a redraw`,
      redrawn.nodes.filter((n) => n.id.startsWith("p2.")).length === 0 && redrew && afterPan && Math.abs(redrew[2] - afterPan[2]) < 0.01 && (redrew[0] !== back[0] || redrew[1] !== back[1]),
      { redrew, afterPan, back, p2Nodes: redrawn.nodes.filter((n) => n.id.startsWith("p2.")).length });
    // zoom: the Zoom in button (25% - 200%)
    await p.getByRole("button", { name: "Zoom in" }).click();
    const afterZoom = transform(await measure(p));
    check(`1440 ${theme}: the button zooms in`, afterZoom && redrew && afterZoom[2] > redrew[2], { redrew, afterZoom });
    // Fit: everything inside the stage, centred (it may go below the opening view's 50%, down to 25%)
    await p.getByRole("button", { name: "Fit" }).click();
    const fittedM = await measure(p);
    const fitted = transform(fittedM);
    const wantZ = Math.max(0.25, Math.min(1, Math.min((fittedM.stage.w - 80) / fittedM.lw, (fittedM.stage.h - 80) / fittedM.lh)));
    check(`1440 ${theme}: Fit fits everything, centred`, fitted && Math.abs(fitted[2] - wantZ) < 0.01 && Math.abs(fitted[0] - (fittedM.stage.w - fittedM.lw * wantZ) / 2) < 2 && Math.abs(fitted[1] - (fittedM.stage.h - fittedM.lh * wantZ) / 2) < 2, { fitted, wantZ, stage: fittedM.stage, lw: fittedM.lw, lh: fittedM.lh });
    // Fit counts as the person's move: a redraw of the same key (a pushed page and back) keeps it, where it used to
    // survive only one poll of a live graph before the opening view took over again
    await p.locator('[data-graph-node="root"]').click();
    const pushedFit = await p.waitForFunction(() => document.body.textContent.includes("A made-up step"), null, { timeout: 15_000 }).then(() => true, () => false);
    check(`1440 ${theme}: the step page shows after Fit`, pushedFit);
    await p.goBack();
    await p.locator("[data-graph-stage]").waitFor({ timeout: 15_000 });
    await p.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running" || a.effect?.getTiming().iterations === Infinity));
    const keptFit = transform(await measure(p));
    check(`1440 ${theme}: the fitted view kept across a redraw`, keptFit && fitted && Math.abs(keptFit[2] - fitted[2]) < 0.01 && Math.abs(keptFit[0] - fitted[0]) < 2, { fitted, keptFit });
    // Full screen
    await p.getByRole("button", { name: "Full screen" }).click();
    const overlay = await p.waitForSelector('[role="dialog"]', { timeout: 10_000 });
    check(`1440 ${theme}: Full screen covers the window`, overlay && (await overlay.boundingBox()).width === 1440);
    await p.screenshot({ path: join(shots, `work-1440-${theme}-full.png`) });
    await p.keyboard.press("Escape");
    check(`1440 ${theme}: Esc closes Full screen`, !(await p.$('[role="dialog"]')));
    // the focus comes back to the Full screen button (the overlay replaces it while open, so the focus used to fall
    // to the body and a keyboard user had to Tab from the top of the document)
    const active = await p.evaluate(() => document.activeElement?.getAttribute("aria-label") ?? document.activeElement?.tagName ?? "");
    check(`1440 ${theme}: the focus returns to the Full screen button`, active === "Full screen", active);
    await p.context().close();
  }
  // a phone width: the canvas is 70% of the visible height, clamped to 320-900
  for (const theme of ["light", "dark"]) {
    const p = await page({ width: 390, height: 844, theme });
    const m = await measure(p);
    check(`390 ${theme}: the canvas between 320 and 900 tall`, m.stage.h >= 320 && m.stage.h <= 900, m.stage);
    // a node is whole, and the selected node (p2.s3) is inside the stage
    const sel = m.nodes.find((n) => n.id === "p2.s3");
    check(
      `390 ${theme}: the selected node's centre inside the canvas`,
      sel && sel.box.x < m.stage.x + m.stage.w && sel.box.x + sel.box.w > m.stage.x && sel.box.y < m.stage.y + m.stage.h && sel.box.y + sel.box.h > m.stage.y,
      { sel: sel && sel.box, stage: m.stage },
    );
    // the fold control counters the zoom: at the opening scale (about 0.5) it is still at least 24 px on screen
    const foldBox = await p.locator('[data-graph-fold]').first().boundingBox();
    check(`390 ${theme}: the fold control at least 24 px on screen at any zoom`, foldBox && foldBox.width >= 24 && foldBox.height >= 24, foldBox);
    await p.screenshot({ path: join(shots, `work-390-${theme}.png`) });
    // the keyboard: Tab reaches the stage's own nodes, arrows pan, + zooms, 0 fits (the stage, not a node, focused)
    await p.locator("[data-graph-stage]").focus();
    const beforeKeys = transform(await measure(p));
    await p.keyboard.press("ArrowRight");
    await p.keyboard.press("ArrowDown");
    const afterArrows = transform(await measure(p));
    check(`390 ${theme}: the arrows pan`, beforeKeys && afterArrows && (afterArrows[0] !== beforeKeys[0] || afterArrows[1] !== beforeKeys[1]), { beforeKeys, afterArrows });
    await p.keyboard.press("+");
    const afterPlus = transform(await measure(p));
    check(`390 ${theme}: + zooms in`, afterPlus && afterArrows && afterPlus[2] > afterArrows[2], { afterArrows, afterPlus });
    await p.keyboard.press("0");
    const afterZero = transform(await measure(p));
    check(`390 ${theme}: 0 fits`, afterZero && afterPlus && afterZero[2] < afterPlus[2], { afterPlus, afterZero });
    await p.context().close();
  }
  // the overview card's compact preview: 200 tall, still, opens Full screen
  {
    const p = await page({ width: 1440, height: 900, theme: "light", at: "/overview" });
    const card = await p.locator(".graph-preview").first().waitFor({ timeout: 15_000 }).then(() => true, () => false);
    check("compact: the overview card draws the still preview", card);
    if (card) {
      const h = await p.locator(".graph-preview").first().evaluate((e) => e.getBoundingClientRect().height);
      check("compact: the preview 200 tall", Math.abs(h - 200) < 4, h);
      // the preview fits the whole layer: its scaled box sits inside the .graph-preview box
      const inside = await p.locator(".graph-preview").first().evaluate((e) => {
        const layer = e.querySelector("[data-graph-layer]");
        if (!layer) return null;
        const b = e.getBoundingClientRect();
        const l = layer.getBoundingClientRect();
        return { preview: { x: b.x, y: b.y, w: b.width, h: b.height }, layer: { x: l.x, y: l.y, w: l.width, h: l.height } };
      });
      check(
        "compact: the fitted layer inside the preview box",
        inside && inside.layer.x >= inside.preview.x - 1 && inside.layer.y >= inside.preview.y - 1 &&
          inside.layer.x + inside.layer.w <= inside.preview.x + inside.preview.w + 1 &&
          inside.layer.y + inside.layer.h <= inside.preview.y + inside.preview.h + 1,
        inside,
      );
      // no node presses and no zoom buttons in the preview
      const buttons = await p.locator(".graph-preview button[aria-label]").count();
      check("compact: no buttons in the preview", buttons === 0, buttons);
      await p.locator(".graph-preview").first().click();
      const overlay = await p.waitForSelector('[role="dialog"]', { timeout: 10_000 }).then(() => true, () => false);
      check("compact: pressing the preview opens Full screen", overlay);
      if (overlay) {
        // Full screen from the card is the same interactive canvas: node buttons, fold controls, a drag that pans
        const nodes = await p.locator('[role="dialog"] [data-graph-node]').count();
        const folds = await p.locator('[role="dialog"] [data-graph-fold]').count();
        check("compact: Full screen has node buttons and fold controls", nodes > 0 && folds > 0, { nodes, folds });
        const beforeDrag = transform(await measure(p));
        await p.mouse.move(700, 450);
        await p.mouse.down();
        await p.mouse.move(600, 400, { steps: 4 });
        await p.mouse.up();
        const afterDrag = transform(await measure(p));
        check("compact: a drag pans Full screen", afterDrag && beforeDrag && (afterDrag[0] !== beforeDrag[0] || afterDrag[1] !== beforeDrag[1]), { beforeDrag, afterDrag });
        await p.screenshot({ path: join(shots, "card-1440-light-full.png") });
        await p.keyboard.press("Escape");
        // from a card too: the focus returns to the preview button, the control that reopens Full screen
        const fromCard = await p.evaluate(() => document.activeElement?.classList.contains("graph-preview") ?? false);
        check("compact: the focus returns to the preview button", fromCard);
      }
    }
    await p.screenshot({ path: join(shots, "card-1440-light.png") });
    await p.context().close();
  }
} catch (e) {
  failed = true;
  console.error(e);
} finally {
  await browser.close();
  clearTimeout(watchdog);
}
writeFileSync(join(shots, "results.json"), JSON.stringify(results, null, 2));
const bad = results.filter((r) => !r.ok);
console.log(`RESULT: ${failed || bad.length ? "FAIL" : "PASS"} (${results.length - bad.length}/${results.length} checks)`);
process.exit(failed || bad.length ? 1 : 0);
