// Developing Persona's record of the desktop shell's main process (#9675, docs/operations/developing-persona.md "The
// desktop app"): what the page cannot see (its processes gone, windows unresponsive, power and thermal changes, each
// process type's CPU and memory a minute). Kept only while the page's switch is on, in a rolling pair of files under the
// app's user data; read back as lines of code-chosen words and numbers. No Electron here: dev-diagnostics.ts wires it.

/** The most one file holds; with the previous file kept beside it, about twice this in all. */
export const MAX_FILE_BYTES = 2 * 1024 * 1024;
export const CURRENT = "main.jsonl";
export const PREVIOUS = "main.1.jsonl";

export interface DiagnosticsFiles {
  append(name: string, text: string): Promise<void>;
  size(name: string): Promise<number>;
  read(name: string): Promise<string>;
  rename(from: string, to: string): Promise<void>;
  removeAll(): Promise<void>;
}

/** One JSON line: `t` (ISO, UTC), `kind` and the fields, keys sorted, as the page's lines are written. */
export function line(at: number, kind: string, fields: Record<string, unknown>): string {
  const all: Record<string, unknown> = { ...fields, t: new Date(at).toISOString(), kind };
  return JSON.stringify(Object.fromEntries(Object.keys(all).sort().map((k) => [k, all[k]])));
}

/** A code-chosen word as it may be recorded (a process type, a reason): letters, digits and `_ - . :` and spaces, at
 *  most 48; anything else is `other`. */
export function word(raw: unknown): string {
  return typeof raw === "string" && raw.length > 0 && raw.length <= 48 && /^[A-Za-z0-9_.: -]+$/.test(raw) ? raw : "other";
}

export interface ProcessMetric {
  type: string;
  cpu: { percentCPUUsage: number; idleWakeupsPerSecond?: number };
  memory: { workingSetSize: number };
}

/** A minute's line: each process type's count, CPU percent, idle wakeups a second and memory (MB), summed. */
export function metricsFields(metrics: readonly ProcessMetric[], onBattery: boolean): Record<string, unknown> {
  const by: Record<string, { n: number; cpu: number; wakeups: number; memory_mb: number }> = {};
  for (const m of metrics) {
    const t = (by[word(m.type)] ??= { n: 0, cpu: 0, wakeups: 0, memory_mb: 0 });
    t.n += 1;
    t.cpu += m.cpu.percentCPUUsage || 0;
    t.wakeups += m.cpu.idleWakeupsPerSecond || 0;
    t.memory_mb += (m.memory.workingSetSize || 0) / 1024;
  }
  for (const t of Object.values(by)) {
    t.cpu = Math.round(t.cpu * 10) / 10;
    t.wakeups = Math.round(t.wakeups);
    t.memory_mb = Math.round(t.memory_mb);
  }
  return { processes: by, on_battery: onBattery };
}

/** Only the hub's page may turn it on or read it: its window's main frame, on an http(s) address (the local setup and
 *  the island use the shell's own schemes). */
export function readerAllowed(frameURL: string | undefined, isMainFrame: boolean): boolean {
  if (!isMainFrame || !frameURL) return false;
  try {
    const p = new URL(frameURL).protocol;
    return p === "https:" || p === "http:";
  } catch {
    return false;
  }
}

/** The record: lines appended in order, the current file moved aside when it is full. */
export class ShellDiagnostics {
  private writing: Promise<void> = Promise.resolve();

  constructor(
    private readonly files: DiagnosticsFiles,
    private readonly now: () => number = () => Date.now(),
    private readonly maxBytes = MAX_FILE_BYTES,
  ) {}

  /** One line of `kind`. Never throws: a failed write is dropped. */
  note(kind: string, fields: Record<string, unknown> = {}): Promise<void> {
    const text = line(this.now(), kind, fields) + "\n";
    this.writing = this.writing.then(async () => {
      if ((await this.files.size(CURRENT)) + text.length > this.maxBytes) await this.files.rename(CURRENT, PREVIOUS);
      await this.files.append(CURRENT, text);
    }).catch(() => {});
    return this.writing;
  }

  /** Every kept line, oldest first. */
  async read(): Promise<string[]> {
    await this.writing;
    const text = (await this.files.read(PREVIOUS).catch(() => "")) + (await this.files.read(CURRENT).catch(() => ""));
    return text.split("\n").filter(Boolean);
  }

  /** Everything kept goes (the switch went off). */
  async clear(): Promise<void> {
    await this.writing;
    await this.files.removeAll();
  }
}
