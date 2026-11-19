// The meeting uses' editors in Settings › Models on a phone (#7980, docs/web/web.md "Meeting uses in Model settings
// (#7980)"): their reset, "Follow Information processing" / "Follow Main chat", is longer than "Inherit", so where the
// three actions do not fit on one line the reset takes a line of its own above Cancel and Save, its words whole. jsdom
// lays nothing out, so this measures the sheet in a browser: at 320 px, 320 px with Large text and 360 px with Large text,
// in English, 简体中文 and 日本語, every footer button is inside the sheet with its label uncut, Cancel and Save share the
// bottom line at its right edge, and a reset that leaves that line sits above it at the left. Wide (1280), all three keep
// one line where they were. The background supervisor's editor ("Inherit") is measured too: one line, as before.
//
// Needs the web page against the fake hub with the two meeting uses, and Chrome (or any Chromium through --executable):
//
//   MOCK_MEETING_MODELS=1 MOCK_SUPERVISE_LISTED=1 MOCK_CHAT_EFFORT=1 PORT=18797 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18797 npx vite --port 5797 --strictPort &)
//   node web/e2e/meeting-models.mjs http://127.0.0.1:5797 [--shots <dir>] [--executable <chromium>] [--chat-effort]
//
// --shots saves the sheet at each narrow size (light and dark in English, light in the other two) and the wide editor.
// Prints one line per check and a verdict; exits 1 on a failure and after three minutes.
//
// The resets name the category by its name in the catalog. The mock hub names Main chat and Processing in Chinese in
// every language, so the page's catalog gets them renamed as a hub sends them in the page's language (assistant-models
// name_assistant / name_kb, the mock's own MEETING_MODEL_TEXT): the words, and so the footers, are as long as there.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const base = (args[0] ?? "").replace(/\/$/, "");
const shots = flag("--shots");
const executable = flag("--executable");
const chatEffort = args.includes("--chat-effort");
if (!base || base.startsWith("--")) {
  console.error("usage: node web/e2e/meeting-models.mjs <web URL> [--shots <dir>] [--executable <chromium>] [--chat-effort]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 3 minutes");
  process.exit(1);
}, 180_000).unref();

let failed = 0;
const check = (cond, msg) => {
  console.log(`${cond ? "ok  " : "FAIL"} ${msg}`);
  if (!cond) failed++;
};

/** Main chat's and Processing's names as a hub sends them, by language. */
const NAMES = {
  en: { assistant: "Main chat", kb: "Processing" },
  "zh-Hans": { assistant: "主对话", kb: "信息处理" },
  ja: { assistant: "メインチャット", kb: "情報処理" },
};
/** The category each meeting use follows. */
const PARENT = { "meeting.room": "assistant", "meeting.minutes": "kb" };
/** The reset each editor shows, by language: Inherit for the background supervisor, Follow <the category> for the others. */
const FOLLOW = { en: (name) => `Follow ${name}`, "zh-Hans": (name) => `跟随 ${name}`, ja: (name) => `${name}に従う` };
const INHERIT = { en: "Inherit", "zh-Hans": "恢复继承", ja: "継承に戻す" };
const resetLabel = (lang, id) => PARENT[id] ? FOLLOW[lang](NAMES[lang][PARENT[id]]) : INHERIT[lang];
const IDS = ["assistant.supervise", "meeting.room", "meeting.minutes"]; // the supervisor first: the wide reference

/** The catalog as a hub in `lang` sends it: the categories, and what follows one, by their names in that language. */
async function localizeCatalog(ctx, lang) {
  const chinese = Object.fromEntries(Object.entries(NAMES["zh-Hans"]).map(([id, name]) => [name, id]));
  await ctx.route(/\/api\/v1\/model-config(\/purposes\/[^/?]+)?(\?.*)?$/, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    for (const p of [...(body.purposes ?? []), ...(body.purpose ? [body.purpose] : [])]) {
      if (NAMES[lang][p.id]) p.name = NAMES[lang][p.id];
      if (chinese[p.inherited_from]) p.inherited_from = NAMES[lang][chinese[p.inherited_from]];
    }
    await route.fulfill({ response, json: body });
  });
}
const SIZES = [
  { width: 320, text: "standard" },
  { width: 320, text: "large" },
  { width: 360, text: "large" },
  { width: 1280, text: "standard" },
];

/** The sheet's footer, measured: the panel's edges, the footer's padding box, each button's box and whether its label is cut. */
const measure = (dialog) => dialog.evaluate((d) => {
  const box = (e) => { const r = e.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom }; };
  const footer = d.lastElementChild;
  const pad = parseFloat(getComputedStyle(footer).paddingLeft);
  const f = box(footer);
  return {
    panel: box(d),
    inner: { left: f.left + pad, right: f.right - parseFloat(getComputedStyle(footer).paddingRight) },
    buttons: [...footer.querySelectorAll("button")].map((b) => ({ text: b.textContent.trim(), ...box(b), cut: b.scrollWidth > b.clientWidth + 0.5 })),
  };
});

const near = (a, b) => Math.abs(a - b) < 1;
const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
/** Where Cancel and Save stood in the wide editor of the background supervisor, the reference the meeting editors keep. */
const wide = {};
try {
  for (const [theme, lang] of [["light", "en"], ["dark", "en"], ["light", "zh-Hans"], ["light", "ja"]]) {
    for (const { width, text } of SIZES) {
      if (width === 1280 && theme === "dark") continue;
      const where = `${theme} ${lang} ${width}${text === "large" ? " Large text" : ""}`;
      const ctx = await browser.newContext({ viewport: { width, height: 760 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: "reduce", hasTouch: width < 640 });
      await localizeCatalog(ctx, lang);
      const page = await ctx.newPage();
      await page.goto(`${base}/?token=mock`);
      await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
      await page.evaluate(([th, l, size]) => {
        localStorage.setItem("persona.theme", th);
        localStorage.setItem("persona.language", l);
        if (size === "standard") localStorage.removeItem("persona.text-size");
        else localStorage.setItem("persona.text-size", size);
      }, [theme, lang, text]);
      for (const id of IDS) {
        await page.goto(`${base}/settings?tab=models#settings-model-config`);
        const row = page.locator(`[data-model-purpose="${id}"]`);
        if (!(await row.waitFor({ timeout: 15000 }).then(() => true, () => false))) {
          console.log(`FAIL ${where}: no ${id} row (start the mock hub with MOCK_MEETING_MODELS=1 MOCK_SUPERVISE_LISTED=1)`);
          process.exit(1);
        }
        await row.locator("button").first().click();
        const dialog = page.locator('[role="dialog"]');
        await dialog.locator("select").first().waitFor({ timeout: 10000 });
        await page.waitForTimeout(400); // the sheet settles
        const m = await measure(dialog);
        const [reset, cancel, save] = m.buttons;
        const label = resetLabel(lang, id);
        check(m.buttons.length === 3 && reset.text === label, `${where} ${id}: footer is ${m.buttons.map((b) => b.text).join(" | ")}, the reset ${label}`);
        const outside = m.buttons.filter((b) => b.left < m.panel.left - 0.5 || b.right > m.panel.right + 0.5 || b.cut);
        check(!outside.length, `${where} ${id}: every button inside the sheet, its label whole${outside.length ? `; not: ${outside.map((b) => `${b.text} ${Math.round(b.left)}..${Math.round(b.right)}${b.cut ? " cut" : ""}`).join(", ")}` : ""}`);
        check(near(cancel.top, save.top) && near(save.right, m.inner.right) && cancel.right < save.left, `${where} ${id}: Cancel and Save on one line at the right edge`);
        const own = !near(reset.top, save.top);
        check(near(reset.left, m.inner.left) && (!own || reset.bottom <= cancel.top), `${where} ${id}: the reset at the left${own ? ", on its own line above Cancel and Save" : ", on their line"}`);
        // where the three fit, one line as before: always wide, always for "Inherit"
        if (width === 1280 || id === "assistant.supervise") check(!own, `${where} ${id}: the three on one line`);
        if (width === 1280) {
          if (id === "assistant.supervise") Object.assign(wide, { [lang]: { cancel: cancel.left, save: save.left } });
          else if (wide[lang]) check(near(cancel.left, wide[lang].cancel) && near(save.left, wide[lang].save), `${where} ${id}: Cancel and Save where the other editors have them`);
        }
        if (shots && id !== "assistant.supervise" && (width < 1280 || theme === "light")) {
          await page.screenshot({ path: join(shots, `editor-${id.replace(".", "-")}-${lang}-${theme}-${width}${text === "large" ? "-large" : ""}.png`) });
        }
        await page.keyboard.press("Escape");
        await dialog.waitFor({ state: "detached" });
      }
      await ctx.close();
    }
  }
  if (chatEffort) {
    for (const theme of ["light", "dark"]) {
      for (const text of ["standard", "large"]) {
        const where = `${theme} ${text === "large" ? "Large text" : "default text"}`;
        const ctx = await browser.newContext({ viewport: { width: 480, height: 900 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: "reduce" });
        await localizeCatalog(ctx, "en");
        const page = await ctx.newPage();
        await page.goto(`${base}/?token=mock`);
        await page.waitForFunction(() => localStorage.getItem("persona.token"), null, { timeout: 15000 });
        await page.evaluate(([th, size]) => {
          localStorage.setItem("persona.theme", th);
          localStorage.setItem("persona.language", "en");
          if (size === "standard") localStorage.removeItem("persona.text-size");
          else localStorage.setItem("persona.text-size", size);
        }, [theme, text]);
        await page.goto(`${base}/settings?tab=models#settings-model-config`);
        await page.locator('[data-model-purpose="meeting.minutes"] button').first().click();
        const dialog = page.locator('[role="dialog"]');
        await dialog.locator("select").first().selectOption("api.chat");
        const effort = dialog.getByText("Reasoning effort", { exact: true }).locator("..").locator("select");
        await effort.waitFor({ timeout: 10000 });
        const options = await effort.locator("option").evaluateAll((nodes) => nodes.map((node) => node.value));
        check(JSON.stringify(options) === JSON.stringify(["", "off", "low", "medium", "high"]), `${where} Chat effort: reported options ${options.join(" | ")}`);
        await effort.selectOption("high");
        const [control, panel] = await Promise.all([effort.boundingBox(), dialog.boundingBox()]);
        check(!!control && !!panel && control.x >= panel.x && control.x + control.width <= panel.x + panel.width + 0.5,
          `${where} Chat effort: the selector stays inside the sheet`);
        check(await dialog.getByText("These reasoning options are reported for this model. A current value not in the list is kept.", { exact: true }).isVisible(),
          `${where} Chat effort: reported-capability hint is visible`);
        if (shots) {
          await page.waitForTimeout(400);
          await page.screenshot({ path: join(shots, `chat-effort-${theme}${text === "large" ? "-large" : ""}.png`) });
        }
        await ctx.close();
      }
    }
  }
} finally {
  await browser.close();
}
console.log(failed ? `FAIL ${failed} check(s) failed` : "PASS");
process.exit(failed ? 1 : 0);
