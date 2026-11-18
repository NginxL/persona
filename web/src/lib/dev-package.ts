// Developing Persona's export for the web and the desktop app (#9617, docs/operations/developing-persona.md "The
// package"): one zip an Agent can read, `README.md` first, in the format the iPhone writes (apple/Persona/DevMode/
// DevPackage.swift). Built from what the recorder kept, filtered by the categories the owner left ticked; the browser,
// system and versions always go in. Its own chunk with jszip, loaded when an export is asked for.
import JSZip from "jszip";
import type { DevLine, DevStore } from "./dev-mode";

export type DevCategory = "logs" | "errors" | "energy";
export const DEV_CATEGORIES: DevCategory[] = ["logs", "errors", "energy"];

/** Event kinds that are performance numbers; any other kind is a log line. */
const ENERGY_KINDS = new Set(["minute", "foreground", "network"]);
/** The desktop shell's kinds (#9675): its minutes, power and thermal are numbers; processes gone and windows that hung
 *  are errors; the rest are log lines. */
const SHELL_ENERGY = new Set(["shell_minute", "power", "thermal"]);
const SHELL_ERRORS = new Set(["process_gone", "window"]);

/** What the desktop shell kept (`personaDesktop.readDevDiagnostics`), null in a browser or an older shell. */
export interface ShellRecord {
  lines: string[];
  crashDumps: number;
}

/** The shell's record, if this page runs in a desktop shell that keeps one. */
export async function readShell(): Promise<ShellRecord | null> {
  try {
    return (await window.personaDesktop?.readDevDiagnostics?.()) ?? null;
  } catch {
    return null;
  }
}

export interface DevDevice {
  platform: "web" | "desktop";
  os?: string;
  os_version?: string;
  browser: string;
  build: string;
  hub_version: string;
  utc_offset_minutes: number;
}

export interface DevSummary {
  events: number;
  errors: number;
  logLines: number;
}

/** This browser, as the manifest names it: the platform (`desktop` inside the desktop app), the system, the browser's
 *  name and major version, the web build, the hub's version. */
export function thisDevice(build: string, hubVersion: string): DevDevice {
  const ua = navigator.userAgent;
  const brand = /Edg\/(\d+)/.exec(ua) ? `Edge ${/Edg\/(\d+)/.exec(ua)![1]}`
    : /Firefox\/(\d+)/.exec(ua) ? `Firefox ${/Firefox\/(\d+)/.exec(ua)![1]}`
    : /Chrome\/(\d+)/.exec(ua) ? `Chrome ${/Chrome\/(\d+)/.exec(ua)![1]}`
    : /Version\/(\d+).*Safari/.exec(ua) ? `Safari ${/Version\/(\d+).*Safari/.exec(ua)![1]}`
    : "other";
  return {
    platform: typeof window !== "undefined" && window.personaDesktop ? "desktop" : "web",
    ...systemOf(ua),
    browser: brand,
    build,
    hub_version: hubVersion,
    utc_offset_minutes: -new Date().getTimezoneOffset(),
  };
}

/** The system from the user agent, as the error reports read it (lib/error-reports.ts osOf, not imported: that
 *  would add an export to the first screen). */
export function systemOf(ua: string): { os?: string; os_version?: string } {
  let m: RegExpExecArray | null;
  if ((m = /Android (\d+)/.exec(ua))) return { os: "android", os_version: m[1] };
  if ((m = /(?:iPhone|iPad|iPod).*? OS (\d+)/.exec(ua))) return { os: "ios", os_version: m[1] };
  if ((m = /Windows NT (\d+)/.exec(ua))) return { os: "windows", os_version: m[1] };
  if ((m = /Mac OS X (\d+)/.exec(ua))) return { os: "macos", os_version: m[1] };
  if (/Linux|X11|CrOS/.test(ua)) return { os: "linux" };
  return {};
}

/** The file name: `persona-diagnostics-web-20261117-1032.zip`, in local time. */
export function packageName(device: DevDevice, now: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `persona-diagnostics-${device.platform}-${now.getFullYear()}${p(now.getMonth() + 1)}${p(now.getDate())}-${p(now.getHours())}${p(now.getMinutes())}.zip`;
}

const kindOf = (text: string) => /"kind":"([^"]*)"/.exec(text)?.[1] ?? "";

/** The package's files, by name, and what went in. */
export async function packageFiles(store: DevStore, categories: ReadonlySet<DevCategory>, since: string | null, device: DevDevice, now: number, shell: ShellRecord | null = null):
  Promise<{ files: Record<string, string>; summary: DevSummary }> {
  await store.trim(now);
  const lines: DevLine[] = await store.read(0);
  const events = lines.filter((l) => l.cat === "event" && categories.has(ENERGY_KINDS.has(kindOf(l.text)) ? "energy" : "logs")).map((l) => l.text);
  const errors = categories.has("errors") ? lines.filter((l) => l.cat === "error").map((l) => l.text) : [];
  const logs = categories.has("logs") ? lines.filter((l) => l.cat === "log").map((l) => l.text) : [];
  const from = lines[0]?.at;
  const files: Record<string, string> = {
    "README.md": readme(categories),
    "events.jsonl": events.map((e) => e + "\n").join(""),
  };
  if (categories.has("errors")) files["errors.jsonl"] = errors.map((e) => e + "\n").join("");
  if (categories.has("logs")) files["log.txt"] = logs.map((e) => e + "\n").join("");
  const shellLines = (shell?.lines ?? []).filter((l) => {
    const kind = kindOf(l);
    return categories.has(SHELL_ENERGY.has(kind) ? "energy" : SHELL_ERRORS.has(kind) ? "errors" : "logs");
  });
  if (shell) files["desktop.jsonl"] = shellLines.map((e) => e + "\n").join("");
  const summary = { events: events.length, errors: errors.length, logLines: logs.length };
  const manifest = {
    format: "persona-diagnostics",
    version: 1,
    ...device,
    recording_since: since,
    exported_at: new Date(now).toISOString(),
    window: { from: from !== undefined ? new Date(from).toISOString() : since, to: new Date(now).toISOString() },
    categories: ["device", ...DEV_CATEGORIES.filter((c) => categories.has(c))],
    counts: {
      events: summary.events,
      errors: summary.errors,
      log_lines: summary.logLines,
      ...(shell ? { desktop_lines: shellLines.length, ...(categories.has("errors") ? { crash_dumps: shell.crashDumps } : {}) } : {}),
    },
  };
  const sorted = Object.fromEntries(Object.entries(manifest).sort(([a], [b]) => a.localeCompare(b)));
  files["manifest.json"] = JSON.stringify(sorted, null, 2) + "\n";
  return { files, summary };
}

/** The package as a zip `File`, its files in a folder of the zip's name. */
export async function buildPackage(store: DevStore, categories: ReadonlySet<DevCategory>, since: string | null, device: DevDevice, now = Date.now()): Promise<File> {
  const { files } = await packageFiles(store, categories, since, device, now, device.platform === "desktop" ? await readShell() : null);
  const name = packageName(device, new Date(now));
  const zip = new JSZip();
  const folder = zip.folder(name.replace(/\.zip$/, ""))!;
  for (const [file, text] of Object.entries(files)) folder.file(file, text);
  const blob = await zip.generateAsync({ type: "blob", compression: "DEFLATE" });
  return new File([blob], name, { type: "application/zip" });
}

/** The package's README, for the Agent that reads it (not shown in the page). */
export function readme(categories: ReadonlySet<DevCategory>): string {
  const left = DEV_CATEGORIES.filter((c) => !categories.has(c));
  return `# Persona diagnostics

Recorded in one browser (or the desktop app) by Persona's "Developing Persona" switch (Settings › About Persona) and
exported by its owner, to find out why the page fails, freezes or does more work than it should. Only numbers and words
chosen by the page's code are recorded: no message text, knowledge base content, keys, tokens, passwords, cookies,
pairing links, hub address, machine names or accounts. Free text (error messages, console lines) was scrubbed: URLs are
cut to their path, secrets, e-mail and IP addresses, host names and long keys are replaced with \`<redacted>\`,
\`<email>\`, \`<ip>\`, \`<host>\`.

Times are UTC (ISO 8601); \`manifest.json\` has the device's offset (\`utc_offset_minutes\`).

## Files

- \`manifest.json\`: platform (\`web\` or \`desktop\`), system, browser, the web build (its entry script's name), the hub's
  version, the time window, the categories included and counts.
- \`events.jsonl\`: one JSON object a line, oldest first, by \`kind\`:
  - \`minute\`: one a minute while the page is visible, one for each stretch hidden (\`background: true\`). \`http\` lists
    requests to the hub by route pattern (\`:id\` stands for ids) with count, bytes in (as declared), median and longest
    time to the answer and status codes (\`failed\`: no answer); \`ws\` counts the hub WebSocket's frames by message type,
    its bytes, connects and drops; \`pages\` the seconds on each page; \`online\`; \`memory_mb\` (JS heap) where the browser
    tells it.
  - \`foreground\`: one per visible period: its length, requests, bytes, frames, connects, drops, pages and their seconds.
  - \`app\` (recording, visible, hidden), \`page\` (the page in front changed), \`network\` (online / offline), \`socket\`
    (the hub WebSocket connected or dropped), \`request_error\` (the first of each failing route and status in a minute).
  - \`todo_view\` and \`todo_view_mismatch\`: the hub's view of the to-dos against the web's own sections. The summary
    counts the rows with a view (\`compared\`): read the same (\`same\`), apart by a decided difference (\`by_design\`, by
    why), a section or reason of a newer hub's (\`unknown\`), a run whose session the page lacks (\`client_gap\`), a view
    that time alone changes within 2 minutes (\`boundary\`), a run that moved since the list was read (\`stale_view\`),
    apart until a run 10 s later still sees it (\`unsettled\`), apart with nothing to explain it (\`mismatch\`), and the
    runs that failed (\`errors\`). Each mismatch line names the to-do, its manager and both sides' section and reason.
- \`errors.jsonl\`: the page's error reports (uncaught errors, unhandled rejections, what its boundaries caught, freezes of
  5 s or more), in the hub's error report format, kept here whether or not the hub takes error reports.
- \`log.txt\`: the page's \`console.error\` and \`console.warn\` lines.
- \`desktop.jsonl\` (the desktop app only): its main process's own lines: \`shell_minute\` (each process type's count, CPU
  percent, idle wakeups a second and memory in MB, and whether on battery), \`power\` (suspend, resume, on battery / AC,
  lock), \`thermal\` (macOS thermal state), \`process_gone\` (a renderer or child process gone: its type, reason and exit
  code), \`window\` (unresponsive / responsive), \`shell\` (recording started, app and Electron versions). Crash dumps
  stay on the computer; \`counts.crash_dumps\` in the manifest says how many were written since recording began.
${left.length ? `\nLeft out by the owner: ${left.join(", ")}.\n` : ""}`;
}
