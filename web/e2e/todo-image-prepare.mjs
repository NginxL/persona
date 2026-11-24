// Browser acceptance for large to-do images (#13797): a synthetic 2880 × 1800 screenshot over 1 MiB is prepared in the
// page and attached, Create waiting while it is; a file over the 64 MiB source cap and a decompression-bomb header fail
// by name while the small picture beside them stays; the created to-do's pictures are PNG or JPEG of at most 1 MiB and
// 4096 px, as sent and again after a reload. Desktop and phone width, in the theme and text size given (light and the
// standard size unless --theme dark / --text-size large). Synthetic pictures only.
//
//   PORT=18984 python3 scripts/mock/mock_hub.py &
//   (cd web && PERSONA_HUB_URL=http://127.0.0.1:18984 npx vite --host 127.0.0.1 --port 5984 --strictPort &)
//   node web/e2e/todo-image-prepare.mjs http://127.0.0.1:5984 [--shots <dir>] [--executable <chromium>] [--theme dark] [--text-size large]
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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
const theme = flag("--theme") ?? "light";
const textSize = flag("--text-size") ?? "standard";
const suffix = `${theme === "light" ? "" : `-${theme}`}${textSize === "standard" ? "" : `-${textSize}`}`;
if (!base) {
  console.error("usage: node web/e2e/todo-image-prepare.mjs <web URL> [--shots <dir>] [--executable <chromium>] [--theme dark] [--text-size large]");
  process.exit(2);
}
if (shots) mkdirSync(shots, { recursive: true });
setTimeout(() => {
  console.log("FAIL timed out after 5 minutes");
  process.exit(1);
}, 300_000).unref();

const MiB = 1024 * 1024;
// a 96 × 64 PNG (web/e2e/todo-images.mjs)
const small = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAIAAABqVuVZAAABqUlEQVR4nO3brU4DURDF8QvZ1zgW9Co0lqeBIKsqkA3vgqgEXXU0mpdAIjbB7d3u/ZqZO/eolZtf/u0kTXpz9/wZNO3j9xg07Vb6BVQPp/Mk/Q5Kh9N5eRgFxXRCCKOgVZplo6CYzigoRrNsFBQiOrGCvt8fQ+Xdv3wFxTTeC8IVOk6/g3AdjdOCsEfHV0HYSeOrICTpuCgIqTQuCkKeTs8FIZum54JQSOfn9am3glCIZtHp7SOGcuH8P3cChAo0/XwHoZqO+YJQk8Y8EEroRGgMA6F+ONtA4r9myYZjsiC0pTF2xSChY6MgCNHYAEK2TjKNdiCIhqMdCNLh6AWCGhqNV4xvD6p0FBVEfTSKgJinU4lGBRC1hqMCiIrDEQaiBRqxK0Y7Oq0Loima1kDM0BGhaQdEg+G0A6LNcFoA0ThN3SvGLnSqFMQMmvlw0fZvn0mJzny4BJWblIQTtK4MELsLpxgQ+6UpcMXYu056QXRAkw7EJB1zNClAdBNOChA9hbMPiC5prr1idKyzURB902wAJejMfdGsAo1wYkAjnFWgQRO7YkMnrOwPBY2wDBygVIEAAAAASUVORK5CYII=",
  "base64",
);
// a PNG header claiming 100,000 × 100,000 pixels: refused from the header, never decoded
const bomb = Buffer.alloc(64);
bomb.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52]);
bomb.writeUInt32BE(100_000, 16);
bomb.writeUInt32BE(100_000, 20);
bomb.set([8, 6], 24);
// one over the 64 MiB source cap, picked with the bomb and a small one: as paths, as a file this size is not handed to
// the browser as a buffer
const scratch = mkdtempSync(join(tmpdir(), "todo-image-prepare-"));
const picks = ["huge.png", "bomb.png", "note.png"].map((name) => join(scratch, name));
writeFileSync(picks[0], Buffer.concat([bomb.subarray(0, 33), Buffer.alloc(64 * MiB + 1 - 33)]));
writeFileSync(picks[1], bomb);
writeFileSync(picks[2], small);

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
const ok = (message) => console.log(`ok  ${message}`);
const fail = (message) => {
  console.log(`FAIL ${message}`);
  process.exitCode = 1;
  throw new Error(message);
};

/** Draws a synthetic screenshot in the page (a window with lines of text and a grainy picture) and returns its PNG. */
async function screenshotPng(page) {
  const b64 = await page.evaluate(async () => {
    const c = document.createElement("canvas");
    c.width = 2880;
    c.height = 1800;
    const g = c.getContext("2d");
    g.fillStyle = "#f4f4f2";
    g.fillRect(0, 0, c.width, c.height);
    g.fillStyle = "#e3e3df";
    g.fillRect(0, 0, 520, c.height);
    g.fillStyle = "#1d1d1f";
    g.font = "28px -apple-system, Helvetica, Arial, sans-serif";
    for (let i = 0; i < 40; i++) g.fillText(`Sidebar item ${i + 1}`, 40, 70 + i * 42);
    g.font = "30px -apple-system, Helvetica, Arial, sans-serif";
    for (let i = 0; i < 40; i++) g.fillText(`Line ${i + 1}: synthetic screenshot text that should stay readable after preparation.`, 580, 70 + i * 42);
    const img = g.createImageData(1100, 760);
    let seed = 13797;
    const rand = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
    for (let y = 0; y < img.height; y++) {
      for (let x = 0; x < img.width; x++) {
        const i = (y * img.width + x) * 4;
        const n = (rand() - 0.5) * 40;
        img.data[i] = 90 + (x / img.width) * 120 + n;
        img.data[i + 1] = 120 + (y / img.height) * 90 + n;
        img.data[i + 2] = 170 - (x / img.width) * 60 + n;
        img.data[i + 3] = 255;
      }
    }
    g.putImageData(img, 1700, 960);
    const blob = await new Promise((ok) => c.toBlob(ok, "image/png"));
    const bytes = new Uint8Array(await blob.arrayBuffer());
    let bin = "";
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    return btoa(bin);
  });
  return Buffer.from(b64, "base64");
}

/** The type and pixel size of `bytes` as the browser decodes them, or the reason it cannot. */
const decoded = (page, b64, mime) => page.evaluate(async ({ b64, mime }) => {
  const bin = atob(b64);
  const bytes = Uint8Array.from(bin, (ch) => ch.charCodeAt(0));
  const magic = bytes[0] === 0x89 && bytes[1] === 0x50 ? "image/png" : bytes[0] === 0xff && bytes[1] === 0xd8 ? "image/jpeg" : "other";
  try {
    const bitmap = await createImageBitmap(new Blob([bytes], { type: mime }));
    return { magic, size: bytes.length, width: bitmap.width, height: bitmap.height };
  } catch (error) {
    return { magic, size: bytes.length, error: String(error) };
  }
}, { b64, mime });

const withinLimits = (tag, what, d, mime) => {
  if (d.error) fail(`${tag}: ${what} does not decode (${d.error})`);
  if (d.magic !== mime) fail(`${tag}: ${what} is ${d.magic}, labelled ${mime}`);
  if (d.size > MiB || Math.max(d.width, d.height) > 4096) fail(`${tag}: ${what} is ${d.size} B, ${d.width} × ${d.height}`);
};

try {
  for (const width of [1280, 390]) {
    const tag = `${width}px ${theme} ${textSize}`;
    const phone = width <= 600;
    const context = await browser.newContext({
      viewport: { width, height: phone ? 844 : 900 },
      deviceScaleFactor: phone ? 2 : 1,
      colorScheme: theme,
      locale: "en-US",
      hasTouch: phone,
    });
    await context.addInitScript(({ theme, textSize }) => {
      localStorage.setItem("persona.theme", theme);
      localStorage.setItem("persona.language", "en");
      localStorage.setItem("persona.text-size", textSize);
    }, { theme, textSize });
    const page = await context.newPage();
    page.setDefaultTimeout(30_000);
    await page.goto(`${base}/?token=mock`);
    await page.waitForFunction(() => localStorage.getItem("persona.token"));
    await page.goto(`${base}/todos`);
    const shot = await screenshotPng(page);
    if (shot.length <= MiB) fail(`${tag}: the synthetic screenshot is only ${shot.length} B`);
    await page.getByRole("button", { name: "New to-do", exact: true }).first().click();
    const dialog = page.getByRole("dialog").last();
    await dialog.locator('input[type="text"]').first().fill("Check the large screenshot");
    await dialog.locator("textarea").first().fill("Prepared in the page instead of refused over 1 MiB.");
    const input = dialog.locator('input[type="file"][accept*="image"]');
    const status = dialog.locator('[data-testid="todo-images-preparing"]');
    const create = dialog.getByRole("button", { name: "Create", exact: true });

    // a throttled CPU keeps the preparing state on screen long enough to see it
    const cdp = await context.newCDPSession(page);
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 20 });
    await input.setInputFiles([{ name: "screenshot.png", mimeType: "image/png", buffer: shot }]);
    if (await status.isVisible()) {
      if (!(await create.isDisabled())) fail(`${tag}: Create is enabled while a picture is prepared`);
      if (shots) await page.screenshot({ path: join(shots, `todo-image-prepare-${width}-preparing${suffix}.png`) });
      ok(`${tag}: "${await status.textContent()}" shown, Create disabled`);
    }
    await cdp.send("Emulation.setCPUThrottlingRate", { rate: 1 });
    await status.waitFor({ state: "detached" });
    await dialog.locator('img[alt="screenshot.jpg"]').waitFor();
    if (await dialog.getByRole("alert").count()) fail(`${tag}: ${await dialog.getByRole("alert").textContent()}`);
    if (shots) await page.screenshot({ path: join(shots, `todo-image-prepare-${width}-attached${suffix}.png`) });
    ok(`${tag}: a ${(shot.length / MiB).toFixed(2)} MiB screenshot was prepared and attached`);

    await input.setInputFiles(picks);
    await dialog.locator('img[alt="note.png"]').waitFor();
    await status.waitFor({ state: "detached" });
    const alert = (await dialog.getByRole("alert").textContent()) ?? "";
    for (const name of ["huge.png", "bomb.png"]) if (!alert.includes(`${name} is too large to prepare`)) fail(`${tag}: no message for ${name}: ${alert}`);
    const kept = await dialog.locator("img").evaluateAll((imgs) => imgs.map((img) => img.alt));
    if (JSON.stringify(kept) !== JSON.stringify(["screenshot.jpg", "note.png"])) fail(`${tag}: attached ${JSON.stringify(kept)}`);
    if ((await dialog.locator('input[type="text"]').first().inputValue()) !== "Check the large screenshot") fail(`${tag}: the title was lost`);
    if (shots) await page.screenshot({ path: join(shots, `todo-image-prepare-${width}-refused${suffix}.png`) });
    ok(`${tag}: the over-cap file and the bomb were refused by name; the draft and both pictures stayed`);

    const posted = page.waitForRequest((r) => r.method() === "POST" && new URL(r.url()).pathname === "/api/v1/todos");
    await create.click();
    const body = (await posted).postDataJSON();
    if (body.images?.length !== 2) fail(`${tag}: ${body.images?.length} images sent`);
    for (const image of body.images) withinLimits(tag, `sent ${image.name}`, await decoded(page, image.data, image.mime), image.mime);
    ok(`${tag}: sent ${body.images.map((i) => `${i.name} ${i.mime} ${atob(i.data).length} B`).join(", ")}`);

    const persisted = page.locator('[data-testid="todos-detail-column"] img[alt="screenshot.jpg"]');
    await persisted.waitFor();
    await page.reload();
    await persisted.waitFor();
    const stored = await persisted.evaluate(async (img) => {
      const bytes = new Uint8Array(await (await fetch(img.src)).arrayBuffer());
      let bin = "";
      for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
      return btoa(bin);
    });
    const d = await decoded(page, stored, "image/jpeg");
    withinLimits(tag, "the stored screenshot", d, "image/jpeg");
    ok(`${tag}: after a reload the to-do shows the screenshot, ${d.width} × ${d.height}, ${d.size} B`);
    if (shots && !phone) {
      await persisted.locator("xpath=ancestor::button[1]").click();
      const viewer = page.getByRole("dialog").last();
      await viewer.getByRole("button", { name: "Download", exact: true }).waitFor();
      await viewer.locator("img").first().waitFor({ state: "visible" });
      await page.waitForTimeout(800); // the viewer's opening animation
      await page.screenshot({ path: join(shots, `todo-image-prepare-${width}-viewer${suffix}.png`) });
    }
    await context.close();
  }
  console.log("PASS");
} catch (error) {
  if (!process.exitCode) {
    console.log(`FAIL ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  }
} finally {
  await browser.close();
  rmSync(scratch, { recursive: true, force: true });
}
