// Real Settings › Models with synthetic extension data. Start scripts/mock/mock_hub.py, then Vite with PERSONA_HUB_URL:
// node web/e2e/extension-model-candidates.mjs http://127.0.0.1:5173 [--shots <dir>] [--executable <chromium>]
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => args.includes(name) ? args[args.indexOf(name) + 1] : undefined;
const base = args[0], shots = flag("--shots"), executablePath = flag("--executable");
if (!base || base.startsWith("--")) throw Error("Pass the web URL served against the mock hub.");
if (shots) await mkdir(shots, { recursive: true });
const labels = (lang, ns) => readFile(new URL(`../src/i18n/locales/${lang}/${ns}.json`, import.meta.url), "utf8").then(JSON.parse);
const notes = "extension.research-notes", manager = "m-3fa21c"; // The mock roster’s second manager.
const providers = [
  { id: "launcher.atlas", name: "Atlas", kind: "api", has_key: true, model: "quill-small", models: [{ id: "quill-small", name: "Quill Small" }] },
  { id: "launcher.ink", name: "Inkwell", kind: "api", has_key: true, model: "quill-general" },
  { id: "launcher.local", name: "Local CLI", kind: "launcher", api: "responses", has_key: true },
];
const copy = {
  en: ["Research notes", "Information processing", "No configured provider offers this model."],
  "zh-Hans": ["研究笔记", "信息处理", "已配置的供应商均不提供此模型。"],
  ja: ["研究ノート", "情報処理", "設定したプロバイダーはこのモデルを提供していません。"],
};
const cases = [
  ["desktop-light", "en", 1180, 880, "standard"],
  ["desktop-dark", "ja", 1180, 880, "large"],
  ["phone-light", "zh-Hans", 390, 844, "standard"],
  ["phone-dark", "en", 390, 844, "large"],
  ["desktop-large-light", "en", 1180, 880, "large"],
  ["desktop-standard-dark", "ja", 1180, 880, "standard"],
  ["phone-large-light", "zh-Hans", 390, 844, "large"],
  ["phone-standard-dark", "en", 390, 844, "standard"],
  ["narrow", "ja", 320, 720, "large"],
];
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const browser = await chromium.launch(executablePath ? { executablePath } : { channel: "chrome" });
try {
  for (const [name, lang, width, height, textSize] of cases) {
    const theme = name.endsWith("dark") ? "dark" : "light";
    const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1, colorScheme: theme, reducedMotion: "reduce", hasTouch: width < 640, timezoneId: "UTC" });
    const [message, common, modelId] = await Promise.all([labels(lang, "settings-modelconfiguration"), labels(lang, "common"), labels(lang, "lib-model-id")]);
    const [nameOfUse, nameOfParent, reason] = copy[lang];
    const parent = { id: "kb", name: nameOfParent, group: "", kind: "api", category_id: "kb", fields: ["provider", "model"], provider_ids: providers.map((p) => p.id),
      effective: { provider: providers[0].id, model: providers[0].model }, override: {}, revision: 1 };
    const requested = [
      { last_used: "2026-10-12T09:20:00Z", models: [{ model: "quill-small", provider: providers[0].id, provider_name: "Atlas", available: true }, { model: "quill-archive", available: false, problem: reason }] },
      { last_used: "2026-10-11T17:05:00Z", models: [{ model: "quill-general", provider: providers[1].id, provider_name: "Inkwell", available: true }] },
    ];
    const candidates = { mode: "extension", modes: ["extension", "off", "custom"], most: 2, direct_provider_ids: providers.slice(0, 2).map((p) => p.id), custom: [], requested };
    const purpose = { ...parent, id: notes, name: nameOfUse, parent: "kb", inherited_from: "kb", revision: 7, candidates };
    let conflict = true;
    const writes = [];
    await ctx.route(/\/api\/v1\/model-config(?:\/purposes\/[^/?]+)?(?:\?.*)?$/, async (route) => {
      const req = route.request(), path = new URL(req.url()).pathname;
      if (path.endsWith("/purposes/assistant.supervise")) return route.fulfill({ status: 404, json: { error: "Unknown purpose." } });
      if (path.endsWith(notes)) assert.equal(req.headers()["x-persona-manager"], manager);
      if (req.method() === "PATCH") {
        const patch = req.postDataJSON();
        writes.push(patch);
        await pause(250);
        if (conflict) {
          conflict = false; purpose.revision++; candidates.mode = "off";
          return route.fulfill({ status: 409, json: { error: "The purpose changed." } });
        }
        assert.equal(patch.revision, purpose.revision);
        candidates.mode = patch.candidates.mode;
        purpose.revision++;
      }
      const json = path.endsWith("/model-config") ? { purposes: [parent, purpose], providers } : { purpose: path.endsWith("/kb") ? parent : purpose, providers };
      return route.fulfill({ json });
    });
    const page = await ctx.newPage();
    try {
      await page.goto(`${base}/?token=mock`);
      await page.waitForFunction(() => localStorage.getItem("persona.token"));
      await page.evaluate(([theme, lang, size, selectedManager]) => {
        localStorage.setItem("persona.currentManager", selectedManager);
        localStorage.setItem("persona.theme", theme);
        localStorage.setItem("persona.language", lang);
        localStorage.setItem("persona.text-size", size);
      }, [theme, lang, textSize, manager]);
      await page.goto(`${base}/settings?tab=models#settings-model-config`);
      const open = (id) => page.locator(`[data-model-purpose="${id}"] button`).first().click();
      const dialog = page.getByRole("dialog");
      const mode = dialog.getByLabel(message.candidate_title, { exact: true });
      const button = (label) => dialog.getByRole("button", { name: label, exact: true });
      const row = (label, index) => dialog.getByLabel(label.replace("{index}", String(index)), { exact: true });
      const shot = async (view) => {
        await page.evaluate(() => {
          document.activeElement?.blur();
          document.querySelector('[role="dialog"] .overflow-y-auto').scrollTop = 0;
        });
        await page.evaluate(() => document.fonts.ready);
        const metrics = await dialog.evaluate((panel) => {
          const rect = panel.getBoundingClientRect(), body = panel.querySelector(".overflow-y-auto");
          const controls = [...panel.querySelectorAll("input, select, button, summary, time")].filter((el) => el.getClientRects().length);
          const outside = controls.filter((el) => { const r = el.getBoundingClientRect(); return r.left < rect.left - 1 || r.right > rect.right + 1; });
          const footer = panel.querySelector("[data-dialog-footer]").getBoundingClientRect();
          const group = panel.querySelector('[data-settings-group="extension-candidates"]');
          const smallTouchControls = matchMedia("(pointer: coarse)").matches ? [...group.querySelectorAll("input, select, button, summary")].filter((el) => {
            const r = el.getBoundingClientRect(); return r.height && (r.width < 44 || r.height < 44);
          }).map((el) => el.getAttribute("aria-label") || el.textContent) : [];
          return { overflow: outside.length || body.scrollWidth - body.clientWidth || document.documentElement.scrollWidth - innerWidth,
            smallTouchControls, footerVisible: footer.bottom <= innerHeight + 1 && footer.top >= 0, width: Math.round(rect.width) };
        });
        assert.equal(metrics.overflow, 0, name + " " + view + " horizontal overflow");
        assert.ok(metrics.footerVisible, name + " footer");
        assert.deepEqual(metrics.smallTouchControls, [], name + " 44px touch targets");
        if (shots && name !== "narrow") await page.screenshot({ path: join(shots, `web-${name}-${view}.png`) });
      };

      if (name === "desktop-light") {
        await open("kb"); await dialog.getByText(nameOfParent, { exact: true }).first().waitFor();
        assert.equal(await mode.count(), 0);
        if (shots) await page.screenshot({ path: join(shots, "reference-ordinary-editor.png") });
        await button(common.cancel).click(); await dialog.waitFor({ state: "hidden" });
      }
      await open(notes); await mode.waitFor();
      assert.equal(await mode.inputValue(), "extension");
      const history = dialog.locator("details").filter({ has: page.locator("time") });
      await history.locator("summary").click();
      await history.getByText(reason, { exact: false }).waitFor();
      assert.deepEqual(await history.locator("time").evaluateAll((els) => els.map((el) => el.dateTime)), requested.map((r) => r.last_used));
      await shot("requested");
      await history.locator("summary").click();
      const began = await page.evaluate(() => performance.now());
      await mode.selectOption("custom");
      const first = row(message.candidate_provider, 1);
      await first.waitFor();
      const editMs = Math.round((await page.evaluate(() => performance.now())) - began);
      assert.deepEqual(await first.locator("option").evaluateAll((options) => options.map((o) => o.value)), ["", "launcher.atlas", "launcher.ink"]);
      await first.selectOption("launcher.atlas");
      const firstModel = row(message.candidate_model, 1);
      await firstModel.fill("quill small");
      await dialog.getByText(modelId.space, { exact: true }).waitFor();
      assert.equal(await button(common.save).isDisabled(), true);
      await firstModel.fill("quill-small");
      await button(message.candidate_add).click();
      await row(message.candidate_provider, 2).selectOption("launcher.ink");
      await button(message.candidate_up.replace("{index}", "2")).click();
      assert.equal(await first.inputValue(), "launcher.ink");
      assert.equal(await button(message.candidate_add).isDisabled(), true);
      await shot("custom");
      const submitted = await page.evaluate(() => performance.now());
      await button(common.save).click();
      assert.ok(await mode.isDisabled(), name + " keeps controls disabled while saving");
      await dialog.getByRole("alert").waitFor();
      const conflictMs = Math.round((await page.evaluate(() => performance.now())) - submitted);
      assert.equal(await first.inputValue(), "launcher.ink"); assert.equal(await button(common.save).isDisabled(), true);
      assert.deepEqual(writes, [{ revision: 7, candidates: { mode: "custom", custom: [{ provider: "launcher.ink", model: "" }, { provider: "launcher.atlas", model: "quill-small" }] } }]);
      await button(message.reload_discard).click();
      await page.waitForFunction((label) => document.querySelector(`select[aria-label="${label}"]`)?.value === "off", message.candidate_title);
      await mode.selectOption("extension"); await button(common.save).click();
      await dialog.waitFor({ state: "hidden" });
      assert.deepEqual(writes[1], { revision: 8, candidates: { mode: "extension" } });
      console.log(JSON.stringify({ name, width, height, lang, textSize, editMs, conflictMs, revisionConflict: true, savedRevision: 8 }));
    } finally { await ctx.close(); }
  }
} finally { await browser.close(); }
