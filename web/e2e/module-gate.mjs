// The browser half of the module e2e gate (scripts/ci/module-e2e.py, docs/operations/testing.md "Module e2e gate"):
// one phase of the real web app against a hub, a module's entry present and usable or absent, the core's pages, the
// hello the browser got and which of a module's chunks it requested. The gate starts the hubs and writes the spec;
// this only drives Chrome and writes what it saw.
//
//   node web/e2e/module-gate.mjs <spec.json>
//
// spec: { base, token, phase, hub: "test" | "mock", expect: { <id>: true | false }, modules: { <id>: { entries, chunks } },
//         shots, out, schemes: ["light", "dark"] }
// An entry (scripts/ci/module-e2e.py MODULES): { name, hubs, page, ready, selector | text, opens?: { selector | text | url },
// off_page?: { path, text } }. Writes { checks: [{ module, check, scheme, ok, detail }], hello, requested } to `out`.
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright-core";

const spec = JSON.parse(readFileSync(process.argv[2], "utf8"));
mkdirSync(spec.shots, { recursive: true });
const checks = [];
// warn: seen and recorded, not a failure (what the web does before a page's hello, see below)
const record = (module, check, scheme, ok, detail = "", warn = false) => checks.push({ module, check, scheme, ok: Boolean(ok), warn, detail: String(detail).slice(0, 400) });
const hellos = {};
const requestedByModule = {};

const locate = (page, e) => (e.selector ? page.locator(e.selector) : page.getByText(new RegExp(e.text, "i")));

async function visible(page, e, ms) {
  try {
    await locate(page, e).first().waitFor({ state: "visible", timeout: ms });
    return true;
  } catch {
    return false;
  }
}

const browser = await chromium.launch();
try {
  for (const scheme of spec.schemes) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 820 }, colorScheme: scheme, reducedMotion: "reduce", locale: "en-US" });
    await context.addInitScript(({ token }) => {
      localStorage.setItem("persona.token", token);
      localStorage.setItem("persona.language", "en");
    }, { token: spec.token });
    const page = await context.newPage();
    page.setDefaultTimeout(20000);
    // Every request, and whether this page load's hello had come: before it the web draws every module as on (the hub
    // flag rule, core/module-host.ts hubServes), so what an off module fetches then is told apart from after it.
    const requests = [];
    let navHello = null;
    let steady = false;
    page.on("request", (r) => requests.push({ url: r.url(), afterHello: Boolean(navHello), steady }));
    const errors = [];
    page.on("pageerror", (e) => errors.push(`pageerror: ${e}`));
    page.on("console", (m) => m.type() === "error" && errors.push(`console: ${m.text()} @ ${m.location()?.url ?? ""}`));
    let hello;
    page.on("websocket", (ws) => ws.on("framereceived", (f) => {
      if (navHello || typeof f.payload !== "string" || !f.payload.includes('"hello"')) return;
      try {
        const m = JSON.parse(f.payload);
        if (m.type === "hello") hello = navHello = m;
      } catch {}
    }));
    // A page load, then its own hello, then a moment for what the hello switches.
    const go = async (path) => {
      navHello = null;
      await page.goto(`${spec.base}${path}`);
      const deadline = Date.now() + 20000;
      while (!navHello && Date.now() < deadline) await page.waitForTimeout(100);
      await page.waitForTimeout(1500);
      return Boolean(navHello);
    };
    // The manager names itself after this machine (os.Hostname): masked, so no screenshot carries it.
    const shot = (name) => page.screenshot({ path: join(spec.shots, `${spec.phase}-${scheme}-${name}.png`), mask: spec.mask_text ? [page.getByText(spec.mask_text)] : [] });

    // The core: the app loads, the main chat's composer and the Agents page render.
    record("core", "the main chat's page gets its hello", scheme, await go("/"));
    const composer = await visible(page, { selector: "textarea" }, 30000);
    record("core", "assistant page renders its composer", scheme, composer, page.url());
    await page.waitForTimeout(1500);
    await shot("assistant");
    await go("/agents");
    const agents = await visible(page, { selector: "main" }, 20000) && (await page.locator("main").first().innerText()).includes("Agents");
    record("core", "agents page renders", scheme, agents, page.url());
    await shot("agents");
    const features = hello?.features ?? [];
    hellos[scheme] = features.filter((f) => f === "modules" || f.startsWith("module."));
    record("core", "the browser got a hello", scheme, Boolean(hello), hello ? `${features.length} features` : "no hello frame");

    for (const [id, m] of Object.entries(spec.modules)) {
      const on = spec.expect[id];
      const flag = features.includes(`module.${id}`);
      const marker = features.includes("modules");
      if (spec.hub === "mock" && on && !marker) record(id, "older hub: no modules marker, so on", scheme, !marker && !flag, `marker ${marker}, flag ${flag}`);
      else record(id, `hello ${on ? "has" : "lacks"} module.${id}, marker ${marker ? "there" : "missing"}`, scheme, flag === on && (marker || spec.hub === "mock"), `marker ${marker}, flag ${flag}`);
      for (const e of m.entries.filter((e) => e.hubs.includes(spec.hub))) {
        await go(e.page);
        const ready = await visible(page, e.ready, 30000);
        record(id, `${e.name}: its page renders`, scheme, ready, e.page);
        const there = await visible(page, e, on ? 15000 : 4000);
        record(id, `${e.name} ${on ? "present" : "absent"}`, scheme, there === on, `${e.selector ?? e.text} on ${e.page}`);
        await shot(`${id}-${e.name.replace(/\W+/g, "-")}`);
        if (on && there && e.opens) {
          await locate(page, e).first().click();
          const o = e.opens;
          let opened;
          if (o.url) opened = await page.waitForURL(new RegExp(o.url), { timeout: 15000 }).then(() => true, () => false);
          else opened = await visible(page, o, 15000);
          await page.waitForTimeout(1200);
          record(id, `${e.name} opens`, scheme, opened, page.url());
          await shot(`${id}-${e.name.replace(/\W+/g, "-")}-opened`);
        }
        if (!on && e.off_page) {
          await go(e.off_page.path);
          const gone = await visible(page, { text: e.off_page.text }, 15000);
          record(id, `${e.off_page.path} answers like a removed page`, scheme, gone, page.url());
          await shot(`${id}-${e.off_page.path.replace(/\W+/g, "-")}-off`);
        }
      }
    }
    // The steady state: one load and its hello, then every entry's page and an off module's own address reached inside
    // the app (history, no reload), as a person clicking about does. Nothing of an off module may be fetched here.
    await go("/");
    steady = true;
    for (const m of Object.values(spec.modules)) {
      for (const e of m.entries.filter((e) => e.hubs.includes(spec.hub))) {
        for (const path of [e.page, e.off_page?.path].filter(Boolean)) {
          await page.evaluate((p) => {
            history.pushState(null, "", p);
            dispatchEvent(new PopStateEvent("popstate"));
          }, path);
          await page.waitForTimeout(2000);
        }
      }
    }
    steady = false;
    for (const [id, m] of Object.entries(spec.modules)) {
      const all = requests.filter((r) => m.chunks.some((c) => new RegExp(c).test(r.url)));
      const hits = all.map((r) => r.url);
      (requestedByModule[id] ??= []).push(...all.map((r) => `${r.steady ? "in the app" : r.afterHello ? "on a reload, hello frame in" : "on a reload, before the hello"}: ${r.url}`));
      if (!spec.expect[id]) {
        const after = all.filter((r) => r.steady).map((r) => r.url);
        const before = all.filter((r) => !r.steady).map((r) => r.url);
        record(id, "none of its chunks requested in the app once it has the hello", scheme, after.length === 0, after.join(" "));
        record(id, "none of its chunks requested while a reload waits for the hello", scheme, before.length === 0, before.join(" "), !spec.strict_before_hello);
      } else if (m.chunk_when_on && m.chunk_when_on.includes(spec.hub)) record(id, "its chunk loads", scheme, hits.length > 0, hits.join(" ") || `none of ${m.chunks.join(", ")}`);
    }
    const ignored = (spec.ignore_console ?? []).map((r) => new RegExp(r));
    const bad = errors.filter((e) => !ignored.some((r) => r.test(e)));
    record("core", "no page or console errors", scheme, bad.length === 0, bad.join(" | "));
    await context.close();
  }
} finally {
  await browser.close();
  writeFileSync(spec.out, JSON.stringify({ checks, hello: hellos, requested: requestedByModule }, null, 2));
}
