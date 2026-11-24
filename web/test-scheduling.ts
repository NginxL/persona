import { availableParallelism } from "node:os";
import type { Plugin } from "vite";

// Every isolated file loads the shared catalog and its own module graph. Spawning one
// process per host CPU makes small runners spend their CPU on initialization and
// scheduling instead of the suite (vitest run --maxWorkers=8 measured in #12458).
export function testWorkers(parallelism = availableParallelism(), configured?: number | string): number | string {
  if (configured !== undefined) return configured;
  return Math.min(8, Math.max(parallelism - 1, 1));
}

export const testWorkerLimit = (parallelism?: number): Plugin => ({
  name: "persona-test-worker-limit",
  config(config) {
    if (config.test?.maxWorkers === undefined) return { test: { maxWorkers: testWorkers(parallelism) } };
  },
});
