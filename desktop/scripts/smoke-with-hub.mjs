import { spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { createServer } from "node:net";

/** Start isolated local web + mock services, then a real Electron window; every child is owned by this script. */
const root = fileURLToPath(new URL("../..", import.meta.url));
const python = process.platform === "win32" ? "python" : "python3";
const windows = process.platform === "win32";
// npm is a .cmd shim on Windows, which Node refuses to spawn directly; the arguments here are fixed, never input.
const npm = windows ? "cmd.exe" : "npm";
const npmArgs = args => windows ? ["/d", "/s", "/c", "npm", ...args] : args;
const built = spawn(npm, npmArgs(["--prefix", "desktop", "run", "build"]), { cwd: root, stdio: "inherit" });
if (await new Promise(resolve => built.once("close", resolve)) !== 0) throw new Error("Desktop build failed");
const children = [];
let output = "";
function launch(command, args, env) {
  const child = spawn(command, args, { cwd: root, env: { ...process.env, ...env }, stdio: ["ignore", "ignore", "pipe"], detached: !windows });
  child.stderr.on("data", data => { output += data; });
  children.push(child);
  return child;
}
async function port() {
  const probe = createServer();
  await new Promise(resolve => probe.listen(0, "127.0.0.1", resolve));
  const value = probe.address().port;
  await new Promise(resolve => probe.close(resolve));
  return value;
}
async function ready(address) {
  const deadline = Date.now() + 25_000;
  for (;;) {
    if (children.some(child => child.exitCode !== null)) throw new Error("A smoke service exited before readiness");
    try { if ((await fetch(address, { signal: AbortSignal.timeout(800) })).ok) return; } catch { /* still starting */ }
    if (Date.now() > deadline) throw new Error("Smoke service readiness timed out");
    await new Promise(resolve => setTimeout(resolve, 150));
  }
}
const hubPort = await port();
const webPort = await port();
const hub = launch(python, ["-I", "scripts/mock/mock_hub.py"], { PORT: String(hubPort) });
const vite = launch(npm, npmArgs(["--prefix", "web", "run", "dev", "--", "--host", "127.0.0.1", "--port", String(webPort), "--strictPort"]), { PERSONA_HUB_URL: `http://127.0.0.1:${hubPort}/` });
let failed = false;
try {
  await ready(`http://127.0.0.1:${hubPort}/api/v1/health`);
  await ready(`http://127.0.0.1:${webPort}/`);
  const smoke = spawn(process.execPath, [fileURLToPath(new URL("smoke.mjs", import.meta.url))], { cwd: root, env: { ...process.env, PERSONA_HUB_URL: `http://127.0.0.1:${webPort}/` }, stdio: "inherit" });
  const code = await new Promise(resolve => smoke.once("close", resolve));
  if (code !== 0) { console.error(output.slice(-6000)); failed = true; }
} catch (error) {
  console.error(error, output.slice(-6000));
  failed = true;
} finally {
  // Each service is stopped with everything it started: a process group on POSIX, the process tree on Windows.
  for (const child of [hub, vite]) {
    try { if (windows) spawnSync("taskkill", ["/T", "/F", "/PID", String(child.pid)], { stdio: "ignore" }); else process.kill(-child.pid, "SIGTERM"); } catch { /* already exited */ }
  }
  await new Promise(resolve => setTimeout(resolve, 1500));
  for (const child of [hub, vite]) {
    try { if (!windows) process.kill(-child.pid, "SIGKILL"); } catch { /* already exited */ }
  }
  for (const child of children) child.stderr.destroy();
}
console.log(`Desktop smoke with local web/mock: ${failed ? "FAIL" : "PASS"}`);
process.exitCode = failed ? 1 : 0;
