import { spawn } from "node:child_process";
import { constants } from "node:os";
import { fileURLToPath } from "node:url";

const signals = constants.signals;

const web = fileURLToPath(new URL("..", import.meta.url));
const vitest = fileURLToPath(new URL("../node_modules/vitest/vitest.mjs", import.meta.url));

function run(args) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, args, { cwd: web, stdio: "inherit", windowsHide: true });
    child.once("error", (error) => resolve({ code: 127, error }));
    child.once("close", (code, signal) => resolve({ code: code ?? 128 + (signals[signal] ?? 1) }));
  });
}

// Keep the existing Node regression first, then run the complete Vitest suite.
// Extra npm test arguments belong to Vitest, as they did before this supervisor.
const node = await run(["--test", "e2e/extension-mirrors-cleanup.node-test.mjs", "scripts/process-group.node-test.mjs"]);
const result = node.code === 0 ? await run([vitest, "run", ...process.argv.slice(2)]) : node;
if (result.error) console.error(result.error.message);
process.exitCode = result.code;
