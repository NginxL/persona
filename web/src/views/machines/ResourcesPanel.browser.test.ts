// @vitest-environment node
import { access, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";
import { createServer } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { expect, it } from "vitest";
import { i18nCatalogs } from "../../../i18n-catalogs";
import { pageTokensPlugin } from "../../../page-tokens";
import { textScale } from "../../lib/text-scale";

// This uses the real component and stylesheet against synthetic data. It runs on the Linux CI runner only.
it.skipIf(process.platform !== "linux")("shows the versioned queue in three languages and both appearances", async () => {
  const root = fileURLToPath(new URL("../../../", import.meta.url));
  const dir = await mkdtemp(join(root, ".resource-qa-"));
  const name = basename(dir);
  const queue = JSON.parse(await readFile(join(root, "../server/internal/resourcequeue/testdata/queue-v1.json"), "utf8"));
  queue.waiting[0].duplicate_of = "t1";
  const resources = [{ id: "ci.example", capacity: 3, mirror: { machine: "computer-a", target: "runner.example.com" }, mirror_state: { unknown: true, observed_at: new Date(Date.now() - 60000).toISOString(), queue }, leases: [] }];
  await writeFile(join(dir, "index.html"), `<!doctype html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"></head><body class="bg-inset text-ink"><main id="root" class="p-6"></main><script type="module" src="./main.tsx"></script></body></html>`);
  await writeFile(join(dir, "main.tsx"), `import React from 'react';import{createRoot}from'react-dom/client';import{initI18n}from'/src/i18n';import'/src/styles.css';await initI18n(new URLSearchParams(location.search).get('lang')||'en');const{ResourcesPanel}=await import('/src/views/machines/ResourcesPanel');document.documentElement.classList.toggle('dark',new URLSearchParams(location.search).get('scheme')==='dark');createRoot(document.getElementById('root')).render(<ResourcesPanel/>);`);
  // One generated page, served once: no file watcher (watching web/ takes thousands of inotify watches, and where the user's
  // limit is low every one of them fails with ENOSPC) and no HMR.
  const server = await createServer({ root, configFile: false, plugins: [i18nCatalogs, pageTokensPlugin, react(), tailwindcss()], css: { postcss: { plugins: [textScale()] } }, server: { host: "127.0.0.1", port: 0, watch: null, hmr: false }, logLevel: "error" });
  let browser: Awaited<ReturnType<typeof chromium.launch>> | undefined;
  try {
    await server.listen();
    const address = server.httpServer?.address();
    if (!address || typeof address === "string") throw new Error("missing isolated browser fixture port");
    const candidates = [process.env.CHROME_BIN, "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/chromium", chromium.executablePath()].filter((path): path is string => !!path);
    let executablePath: string | undefined;
    for (const path of candidates) { try { await access(path); executablePath = path; break; } catch { /* try the next installed browser */ } }
    if (!executablePath) throw new Error("remote runner requires its installed Chrome for resource UI verification");
    browser = await chromium.launch({ executablePath, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await page.route("**/api/v1/resources", (route) => route.fulfill({ json: { resources } }));
    for (const lang of ["en", "zh-Hans", "ja"]) for (const scheme of ["light", "dark"]) {
      await page.goto(`http://127.0.0.1:${address.port}/${name}/index.html?lang=${lang}&scheme=${scheme}`);
      try { await page.locator("tbody tr").nth(7).waitFor(); }
      catch (error) { throw new Error(`Resource browser fixture failed: ${pageErrors.join("; ").slice(-1500) || String(error)}`); }
      expect(await page.locator("tbody tr").count()).toBe(8);
      expect(await page.locator("[role=status]").count()).toBeGreaterThan(0);
      expect(await page.locator("body").innerText()).not.toContain("resources.");
      // The CI log holds ephemeral review images; images are never added to the repository.
      const png = await page.screenshot({ fullPage: true });
      console.log(`RESOURCE_SCREENSHOT=${lang}-${scheme}|${png.toString("base64")}`);
    }
  } finally { await browser?.close(); await server.close(); await rm(dir, { recursive: true, force: true }); }
}, 90_000);
