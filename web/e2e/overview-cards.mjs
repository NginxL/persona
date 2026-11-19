// Overview's extension cards arranged by the user (#9745) in a real browser against the mock hub, in Simplified Chinese:
// with nothing arranged the made-up cards come in the hub's order, two a row; Arrange cards drags Agenda to the front,
// makes it a full row, moves Needs me earlier with the keyboard and hides Quota left; after Done, Agenda spans the row
// over two half cards, the hidden card is not drawn and, after a reload, its render is never asked for; the hub keeps
// the arrangement and a second browser (another device) draws the same. `--old-hub`: a hub without the route, and
// `--old-manager`: a default manager from before the field, keep the arrangement in the browser and say so. Light and
// dark screenshots at 1440 × 900 of Overview as it was, the edit state, the arranged result and the edit state with the
// hidden card, and the arranged result at 390 × 844.
//
// Start the mock with MOCK_EXTENSIONS=1 MOCK_OVERVIEW_CARDS=1 (and MOCK_NAVIGATION=off for --old-hub,
// MOCK_NAVIGATION=no-overview for --old-manager), and Vite (or `vite preview`) pointing at it (PERSONA_HUB_URL). The
// run sets the mock's arrangement.
// node web/e2e/overview-cards.mjs <loopback web URL> <artifact directory> [--old-hub | --old-manager] [Chromium executable]
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const mode = args.includes("--old-hub") ? "old-hub" : args.includes("--old-manager") ? "old-manager" : "";
const [base, shots, executable] = args.filter((a) => !a.startsWith("--old-"));
if (!base || !shots || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?\/?$/.test(base)) {
  console.error("usage: node web/e2e/overview-cards.mjs <loopback web URL> <artifact directory> [--old-hub | --old-manager] [Chromium executable]");
  process.exit(2);
}
mkdirSync(shots, { recursive: true });
const watchdog = setTimeout(() => {
  console.error("RESULT: overview-cards timed out (300s)");
  process.exit(124);
}, 300_000);
const root = base.replace(/\/$/, "");
const results = [];
const check = (name, ok, detail) => {
  results.push({ name, ok: Boolean(ok), detail });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${detail === undefined ? "" : ` ${JSON.stringify(detail)}`}`);
};

const HUB_ORDER = ["example-gateway/today", "example-quota/left", "example-today/needs", "example-today/agenda"];
const ARRANGED = [
  { extension: "example-today", id: "agenda", span: "full" },
  { extension: "example-gateway", id: "today" },
  { extension: "example-today", id: "needs" },
  { extension: "example-quota", id: "left", hidden: true },
];
const LOCAL = "只存在这个浏览器里";

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });

async function device({ width, height, theme = "light" }) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: theme, deviceScaleFactor: 2, serviceWorkers: "block", hasTouch: width < 640 });
  await context.addInitScript((theme) => {
    localStorage.setItem("persona.theme", theme);
    localStorage.setItem("persona.language", "zh-Hans");
  }, theme);
  await context.route("**/*", (route) => (new URL(route.request().url()).origin === new URL(root).origin ? route.continue() : route.abort()));
  const p = await context.newPage();
  // each card's render request, as the browser sends it
  const renders = [];
  p.on("request", (r) => {
    const m = new URL(r.url()).pathname.match(/^\/api\/v1\/extensions\/([^/]+)\/-\/ui\/([^/]+)$/);
    if (m && r.method() === "POST") renders.push(`${m[1]}/${m[2]}`);
  });
  await p.goto(`${root}/?token=mock`);
  await p.waitForFunction(() => localStorage.getItem("persona.token"));
  await overview(p);
  return { context, p, renders };
}

async function overview(p) {
  await p.goto(`${root}/overview`);
  await p.waitForLoadState("networkidle");
  await p.locator("[data-extension-card]").first().waitFor({ timeout: 20_000 });
}

/** This browser's copy of the arrangement, kept per hub and sign-in (`<scope>:overview.cards`, lib/overview-cards.ts). */
const COPY = () => {
  const key = Object.keys(localStorage).find((k) => k.endsWith(":overview.cards"));
  return key ? JSON.parse(localStorage.getItem(key)) : [];
};

const navigation = (p, method, body) =>
  p.evaluate(async ([method, body]) => {
    const r = await fetch("/api/v1/navigation", { method, headers: { Authorization: `Bearer ${localStorage.getItem("persona.token")}`, "Content-Type": "application/json" }, ...(body ? { body: JSON.stringify(body) } : {}) });
    return r.status === 200 ? r.json() : { status: r.status };
  }, [method, body]);

/** The cards as drawn, once their own entrance is over: key, box, and whether it is hidden (drawn only while arranging,
 *  without its content). */
const cards = async (p) => {
  await p.waitForFunction(() => [...document.querySelectorAll("[data-extension-card]")].every((c) => c.getAnimations().every((a) => a.playState !== "running")));
  return p.evaluate(() =>
    [...document.querySelectorAll("[data-extension-card]")].map((c) => {
      const b = c.getBoundingClientRect();
      return { key: c.getAttribute("data-extension-card"), x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), hidden: !c.classList.contains("bg-surface") };
    }),
  );
};
const rowWidth = (p) => p.evaluate(() => Math.round(document.querySelector("[data-extension-card]").parentElement.getBoundingClientRect().width));
const arrangeButton = (p) => p.getByRole("button", { name: "整理卡片" });
const head = (p) => p.locator("[data-overview-arrange]");
async function startArranging(p) {
  await arrangeButton(p).click();
  await head(p).waitFor({ timeout: 20_000 });
  await p.waitForFunction(() => document.querySelectorAll("[data-extension-card] select").length > 0);
}
const card = (p, key) => p.locator(`[data-extension-card="${key}"]`);
const done = (p) => head(p).getByRole("button", { name: "完成" }).click();

/** Lays the cards out as ARRANGED by hand: drag, width, keyboard, switch. */
async function arrangeByHand(p) {
  await startArranging(p);
  await card(p, "example-today/agenda").dragTo(card(p, "example-gateway/today"), { sourcePosition: { x: 16, y: 18 } });
  await p.waitForFunction(() => document.querySelector("[data-extension-card]")?.getAttribute("data-extension-card") === "example-today/agenda");
  check("a card dragged onto the first takes its place", (await cards(p))[0].key === "example-today/agenda", (await cards(p)).map((c) => c.key));
  await p.getByLabel("「Agenda」的宽度").selectOption({ label: "整行" });
  // the keyboard's way: Needs me one place earlier, past Quota left
  await p.getByRole("button", { name: "把「Needs me」往前移" }).focus();
  await p.keyboard.press("Enter");
  await p.getByRole("switch", { name: "在总览上显示「Quota left」" }).click();
  await p.waitForFunction(() => !document.querySelector('[data-extension-card="example-quota/left"]')?.classList.contains("bg-surface"));
}

let failed = false;
try {
  if (mode) {
    const { context, p, renders } = await device({ width: 1440, height: 900 });
    if (mode === "old-hub") check("the hub has no navigation route", (await navigation(p, "GET")).status === 404);
    else check("the manager answers a change of only the cards 400", (await navigation(p, "PUT", { overview: [] })).status === 400);
    await arrangeByHand(p);
    await p.waitForFunction(`(${COPY})().some((e) => e.hidden)`);
    check("the arrangement stays in this browser", (await p.evaluate(COPY))[0]?.id === "agenda");
    await p.waitForFunction((local) => document.querySelector("[data-overview-arrange]")?.textContent.includes(local), LOCAL, { timeout: 10_000 });
    check("the edit state says so", true);
    check("the hub keeps none", !("overview" in (await navigation(p, "GET"))));
    await done(p);
    renders.length = 0;
    await overview(p);
    await p.waitForTimeout(500);
    check("after a reload the cards are as arranged", JSON.stringify((await cards(p)).map((c) => c.key)) === JSON.stringify(["example-today/agenda", "example-gateway/today", "example-today/needs"]), await cards(p));
    check("the hidden card is not asked for", !renders.includes("example-quota/left") && renders.includes("example-today/agenda"), renders);
    await context.close();
  } else {
    const laptop = await device({ width: 1440, height: 900 });
    const { p } = laptop;
    await navigation(p, "PUT", { overview: [] });
    await overview(p);
    let drawn = await cards(p);
    check("nothing arranged: the hub's cards in its order", JSON.stringify(drawn.map((c) => c.key)) === JSON.stringify(HUB_ORDER), drawn.map((c) => c.key));
    const width = await rowWidth(p);
    check("nothing arranged: two a row, each half the row", drawn[0].y === drawn[1].y && drawn[2].y === drawn[3].y && drawn.every((c) => c.w < width * 0.55), { drawn, width });

    await arrangeByHand(p);
    const outline = await card(p, "example-quota/left").textContent();
    check("arranging: the hidden card is drawn as its outline, with nothing of it", outline.includes("已隐藏") && !outline.includes("4.69"), outline);
    check("arranging: the heading says every device follows", (await head(p).textContent()).includes("你的所有设备都会跟着变"));
    await done(p);
    await p.waitForFunction(() => document.querySelectorAll("[data-extension-card]").length === 3);
    drawn = await cards(p);
    check("done: Agenda over Usage today and Needs me", JSON.stringify(drawn.map((c) => c.key)) === JSON.stringify(["example-today/agenda", "example-gateway/today", "example-today/needs"]), drawn.map((c) => c.key));
    check("done: Agenda spans the row, the two under it side by side", drawn[0].w >= width - 2 && drawn[1].y === drawn[2].y && drawn[1].y > drawn[0].y && drawn[1].w < width * 0.55, { drawn, width });
    const hub = await navigation(p, "GET");
    check("the hub keeps the arrangement", JSON.stringify(hub.overview) === JSON.stringify(ARRANGED), hub.overview);
    check("this browser's copy is kept per hub and sign-in", (await p.evaluate(COPY)).length === 4 && (await p.evaluate(() => localStorage.getItem("overview.cards"))) === null);

    // the hidden card's render is never asked for
    laptop.renders.length = 0;
    await overview(p);
    await p.waitForTimeout(500);
    check("after a reload the hidden card is not asked for", !laptop.renders.includes("example-quota/left") && laptop.renders.includes("example-today/agenda"), laptop.renders);

    // another device draws it from its hello
    const other = await device({ width: 1440, height: 900 });
    await other.p.waitForFunction(`(${COPY})().length === 4`, null, { timeout: 20_000 });
    await other.p.waitForFunction(() => document.querySelectorAll("[data-extension-card]").length === 3);
    const there = await cards(other.p);
    check("another device draws the same", JSON.stringify(there.map((c) => [c.key, c.w >= width - 2])) === JSON.stringify(drawn.map((c) => [c.key, c.w >= width - 2])), there);
    check("another device does not ask for the hidden card", !other.renders.includes("example-quota/left"), other.renders);
    await other.context.close();

    // Reset puts the cards back, with Undo
    await startArranging(p);
    await head(p).getByRole("button", { name: "恢复默认" }).click();
    await p.waitForFunction(() => document.querySelector('[data-extension-card="example-quota/left"]')?.classList.contains("bg-surface"));
    check("Reset: the hub's order again", JSON.stringify((await cards(p)).map((c) => c.key)) === JSON.stringify(HUB_ORDER));
    await p.getByRole("button", { name: "撤销" }).click();
    await p.waitForFunction(() => document.querySelector("[data-extension-card]")?.getAttribute("data-extension-card") === "example-today/agenda");
    check("Undo: the arrangement back", JSON.stringify((await navigation(p, "GET")).overview) === JSON.stringify(ARRANGED));
    await done(p);
    await laptop.context.close();

    // screenshots
    for (const theme of ["light", "dark"]) {
      const d = await device({ width: 1440, height: 900, theme });
      await navigation(d.p, "PUT", { overview: [] });
      await overview(d.p);
      await d.p.waitForTimeout(800); // the cards fade in
      await d.p.screenshot({ path: join(shots, `overview-default-${theme}-1440.png`) });
      await startArranging(d.p);
      await d.p.waitForTimeout(400);
      await d.p.screenshot({ path: join(shots, `overview-editing-${theme}-1440.png`) });
      await done(d.p);
      await navigation(d.p, "PUT", { overview: ARRANGED });
      await overview(d.p);
      await d.p.waitForFunction(() => document.querySelectorAll("[data-extension-card]").length === 3);
      await d.p.waitForTimeout(800);
      await d.p.screenshot({ path: join(shots, `overview-arranged-${theme}-1440.png`) });
      await startArranging(d.p);
      await d.p.waitForTimeout(400);
      await d.p.screenshot({ path: join(shots, `overview-hidden-${theme}-1440.png`) });
      await d.context.close();
      const phone = await device({ width: 390, height: 844, theme });
      await phone.p.waitForFunction(() => document.querySelectorAll("[data-extension-card]").length === 3);
      await phone.p.waitForTimeout(800);
      const narrow = await cards(phone.p);
      check(`${theme} 390: one column, Agenda first`, narrow[0].key === "example-today/agenda" && narrow[1].y > narrow[0].y && narrow[2].y > narrow[1].y, narrow);
      await phone.p.screenshot({ path: join(shots, `overview-arranged-${theme}-390.png`) });
      await startArranging(phone.p);
      await phone.p.waitForTimeout(400);
      await phone.p.screenshot({ path: join(shots, `overview-editing-${theme}-390.png`) });
      await phone.context.close();
    }
  }
} catch (e) {
  failed = true;
  console.error(e);
}
clearTimeout(watchdog);
await browser.close();
writeFileSync(join(shots, mode ? `results-${mode}.json` : "results.json"), JSON.stringify(results, null, 2));
const ok = !failed && results.length > 0 && results.every((r) => r.ok);
console.log(`RESULT: overview-cards ${ok ? "passed" : "failed"} (${results.filter((r) => r.ok).length}/${results.length})`);
process.exit(ok ? 0 : 1);
