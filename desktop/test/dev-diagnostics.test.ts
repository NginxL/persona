import assert from "node:assert/strict";
import test from "node:test";
import { CURRENT, line, metricsFields, PREVIOUS, readerAllowed, ShellDiagnostics, word, type DiagnosticsFiles } from "../src/dev-diagnostics-core.js";

function memoryFiles(): DiagnosticsFiles & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    async append(name, text) { data.set(name, (data.get(name) ?? "") + text); },
    async size(name) { return (data.get(name) ?? "").length; },
    async read(name) {
      const v = data.get(name);
      if (v === undefined) throw new Error("missing");
      return v;
    },
    async rename(from, to) {
      data.set(to, data.get(from) ?? "");
      data.delete(from);
    },
    async removeAll() { data.clear(); },
  };
}

const T0 = Date.parse("2027-01-15T08:00:00Z");

test("lines are JSON with t, kind and the fields, keys sorted, as the page writes them", () => {
  assert.equal(line(T0, "power", { on_battery: true, event: "suspend" }), '{"event":"suspend","kind":"power","on_battery":true,"t":"2027-01-15T08:00:00.000Z"}');
});

test("only code-chosen words are kept", () => {
  assert.equal(word("crashed"), "crashed");
  assert.equal(word("Utility: Network Service"), "Utility: Network Service");
  assert.equal(word("/Users/someone/x"), "other");
  assert.equal(word(undefined), "other");
  assert.equal(word("x".repeat(49)), "other");
});

test("a minute sums each process type's CPU, wakeups and memory", () => {
  const fields = metricsFields([
    { type: "Browser", cpu: { percentCPUUsage: 1.24, idleWakeupsPerSecond: 10 }, memory: { workingSetSize: 102400 } },
    { type: "Tab", cpu: { percentCPUUsage: 3.0, idleWakeupsPerSecond: 40.4 }, memory: { workingSetSize: 204800 } },
    { type: "Tab", cpu: { percentCPUUsage: 0.5 }, memory: { workingSetSize: 51200 } },
  ], true);
  assert.deepEqual(fields, {
    processes: { Browser: { n: 1, cpu: 1.2, wakeups: 10, memory_mb: 100 }, Tab: { n: 2, cpu: 3.5, wakeups: 40, memory_mb: 250 } },
    on_battery: true,
  });
});

test("only the hub page's main frame may turn it on or read it", () => {
  assert.equal(readerAllowed("https://hub.example/settings", true), true);
  assert.equal(readerAllowed("http://127.0.0.1:8080/", true), true);
  assert.equal(readerAllowed("https://hub.example/", false), false);
  assert.equal(readerAllowed("file:///app/setup.html", true), false);
  assert.equal(readerAllowed("persona-setup://app/setup.html", true), false);
  assert.equal(readerAllowed("persona-island://app/island.html", true), false);
  assert.equal(readerAllowed(undefined, true), false);
  assert.equal(readerAllowed("not a url", true), false);
});

test("lines are kept in order, the full file moved aside, and all of it read back", async () => {
  const files = memoryFiles();
  let now = T0;
  const record = new ShellDiagnostics(files, () => now, 200);
  for (let i = 0; i < 6; i++) {
    now += 1000;
    await record.note("window", { event: "unresponsive", i });
  }
  assert.ok(files.data.has(PREVIOUS), "the first file was moved aside");
  assert.ok((files.data.get(CURRENT) ?? "").length <= 200);
  const lines = await record.read();
  const order = lines.map((l) => (JSON.parse(l) as { i: number }).i);
  assert.deepEqual(order, [...order].sort((a, b) => a - b), "oldest first");
  assert.equal(order[order.length - 1], 5);
});

test("clearing deletes everything kept, and reading nothing is empty", async () => {
  const files = memoryFiles();
  const record = new ShellDiagnostics(files, () => T0);
  await record.note("shell", { event: "recording" });
  await record.clear();
  assert.equal(files.data.size, 0);
  assert.deepEqual(await record.read(), []);
});
