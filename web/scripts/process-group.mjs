import { execFile, spawn } from "node:child_process";
import { constants } from "node:os";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { setTimeout as delay } from "node:timers/promises";

const signals = constants.signals;

const execFileAsync = promisify(execFile);
const entry = fileURLToPath(import.meta.url);
const defaultTimeout = 600_000;
const maxTimeout = 2_147_483_647;

function timeoutValue(value) {
  if (typeof value === "string" && value.trim() === "") throw new Error("test timeout must not be empty");
  const timeout = Number(value);
  if (!Number.isSafeInteger(timeout) || timeout < 0 || timeout > maxTimeout) {
    throw new Error(`test timeout must be an integer between 0 and ${maxTimeout} milliseconds`);
  }
  return timeout;
}

function outcome(code, signal, extra = {}) {
  return { code: code ?? 128 + (signals[signal] ?? 1), signal: signal ?? null, ...extra };
}

function argumentsFor(argv) {
  let timeout = timeoutValue(process.env.PERSONA_WEB_TEST_TIMEOUT_MS ?? defaultTimeout);
  const separator = argv.indexOf("--");
  const options = separator === -1 ? argv : argv.slice(0, separator);
  if (options.length === 2 && options[0] === "--timeout-ms") timeout = timeoutValue(options[1]);
  else if (options.length !== 0) throw new Error("usage: process-group.mjs [--timeout-ms N] -- command [args...]");
  const command = separator === -1 ? [] : argv.slice(separator + 1);
  if (!command.length) throw new Error("a test command is required after --");
  return { timeout, command };
}

async function parent() {
  const { timeout, command } = argumentsFor(process.argv.slice(2));
  const watchdog = spawn(process.execPath, [entry, "--watchdog"], {
    detached: true, windowsHide: true, stdio: ["ignore", "inherit", "inherit", "ipc"],
  });
  const result = new Promise((resolve) => {
    let settled = false;
    const finish = (value) => { if (!settled) { settled = true; resolve(value); } };
    watchdog.once("error", (error) => finish(outcome(1, null, { error: error.message })));
    watchdog.on("message", (message) => {
      if (message?.type === "result") finish(message.outcome);
      else if (message?.type === "deadline-ignored" && process.connected) process.send(message);
    });
    watchdog.on("disconnect", () => finish(outcome(1, null, { error: "test watchdog disconnected before reporting a result" })));
  });
  watchdog.send({ type: "start", timeout, command });
  // A parent embedding this runner can arm the same watchdog deadline after its
  // own fixture is ready. Losing that parent still means immediate supervised exit.
  process.on("message", (message) => {
    if (message?.type === "deadline" && watchdog.connected) watchdog.send(message);
  });
  let answering = false;
  process.on("disconnect", () => { if (!answering) process.exit(1); });
  const answer = await result;
  if (answer.timedOut) console.error(`Web tests exceeded ${answer.timeout ?? timeout} ms; their owned processes were stopped`);
  if (answer.error) console.error(`Web test supervision failed: ${answer.error}`);
  if (answer.cleanup === false) console.error("Web test supervision could not confirm that its owned processes stopped");
  if (watchdog.connected) watchdog.disconnect();
  answering = true;
  if (process.connected) process.disconnect();
  process.exitCode = answer.cleanup === false ? 1 : answer.code;
}

async function groupAlive(pgid) {
  try {
    const { stdout } = await execFileAsync("ps", ["-eo", "pgid=,stat="], { timeout: 1_000, maxBuffer: 4 * 1024 * 1024 });
    return stdout.split("\n").some((line) => {
      const [id, state] = line.trim().split(/\s+/);
      return Number(id) === pgid && state && !state.startsWith("Z");
    });
  } catch {
    // A missing ps cannot turn an unknown group into a claim that cleanup succeeded.
    try { process.kill(-pgid, 0); return true; } catch (error) { return error.code !== "ESRCH"; }
  }
}

async function stopPosixGroup(pgid) {
  if (!Number.isSafeInteger(pgid) || pgid <= 1 || pgid === process.pid) return false;
  const signal = (name) => {
    try { process.kill(-pgid, name); return true; } catch (error) { return error.code === "ESRCH"; }
  };
  if (!signal("SIGTERM")) return false;
  const termDeadline = Date.now() + 250;
  while (await groupAlive(pgid) && Date.now() < termDeadline) await delay(20);
  if (!await groupAlive(pgid)) return true;
  if (!signal("SIGKILL")) return false;
  const killDeadline = Date.now() + 2_000;
  while (await groupAlive(pgid) && Date.now() < killDeadline) await delay(20);
  return !await groupAlive(pgid);
}

async function stopWindowsTree(pid) {
  try {
    await execFileAsync("taskkill", ["/PID", String(pid), "/T", "/F"], { timeout: 5_000, windowsHide: true });
    return true;
  } catch (error) {
    // The process may already have exited; any other failure is not proof of cleanup.
    try { process.kill(pid, 0); return false; } catch { return true; }
  }
}

async function watchdog() {
  let session;
  let timer;
  let finishing = false;
  let finished = false;

  const finish = async (answer, notify = true) => {
    if (finishing || finished) return;
    finishing = true;
    clearTimeout(timer);
    let cleanup = true;
    if (session?.pid) cleanup = process.platform === "win32" ? await stopWindowsTree(session.pid) : await stopPosixGroup(session.pid);
    const result = { ...answer, cleanup };
    finished = true;
    if (notify && process.connected) process.send({ type: "result", outcome: result });
    if (process.connected) process.disconnect();
  };

  process.on("disconnect", () => { void finish(outcome(1, null, { parentLost: true }), false); });
  process.on("message", (message) => {
    if (message?.type === "deadline" && session && (finishing || finished)) {
      if (process.connected) process.send({ type: "deadline-ignored", phase: finishing ? "cleanup" : "finished" });
      return;
    }
    if (message?.type === "deadline" && session && !finishing && !finished) {
      clearTimeout(timer);
      let timeout;
      try { timeout = timeoutValue(message.timeout); }
      catch (error) { void finish(outcome(1, null, { error: error.message })); return; }
      if (timeout > 0) timer = setTimeout(() => { void finish(outcome(124, null, { timedOut: true, timeout })); }, timeout);
      return;
    }
    if (message?.type !== "start" || session || finished) return;
    const { timeout, command } = message;
    session = spawn(process.execPath, [entry, "--session", "--", ...command], {
      detached: true, windowsHide: true, stdio: ["ignore", "inherit", "inherit", "ipc"],
    });
    session.once("error", (error) => { void finish(outcome(127, null, { error: error.message })); });
    session.on("message", (reply) => {
      if (reply?.type === "result") void finish(reply.outcome);
    });
    session.once("close", (code, signal) => {
      if (!finishing) void finish(outcome(code ?? 1, signal, { error: "test session exited before reporting a result" }));
    });
    if (timeout > 0) timer = setTimeout(() => { void finish(outcome(124, null, { timedOut: true, timeout })); }, timeout);
  });
}

async function session() {
  const separator = process.argv.indexOf("--", 2);
  const command = separator === -1 ? [] : process.argv.slice(separator + 1);
  if (!command.length) {
    if (process.connected) process.send({ type: "result", outcome: outcome(2, null, { error: "empty test command" }) });
    return;
  }
  const child = spawn(command[0], command.slice(1), { stdio: "inherit", windowsHide: true });
  child.once("error", (error) => {
    if (process.connected) process.send({ type: "result", outcome: outcome(127, null, { error: error.message }) });
  });
  child.once("close", (code, signal) => {
    if (process.connected) process.send({ type: "result", outcome: outcome(code, signal) });
  });
  // Keep the group leader alive until the watchdog has reaped every group member.
  // If the watchdog itself disappears, closing this IPC channel is the session's
  // last chance to kill only its own process group rather than leave workers behind.
  process.on("disconnect", () => {
    if (process.platform === "win32") { void stopWindowsTree(process.pid); return; }
    try { process.kill(-process.pid, "SIGKILL"); } catch { process.exit(1); }
  });
}

if (process.argv[2] === "--watchdog") await watchdog();
else if (process.argv[2] === "--session") await session();
else await parent();
