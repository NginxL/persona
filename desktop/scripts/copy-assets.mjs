import { copyFile, mkdir, rename, cp } from "node:fs/promises";
import { join } from "node:path";

// The setup page comes from its own scheme (src/setup-page.ts), which serves only these three files from here.
const setup = join("dist", "setup");
await mkdir(setup, { recursive: true });
await copyFile(join("src", "setup.html"), join(setup, "setup.html"));
await copyFile(join("src", "setup.css"), join(setup, "setup.css"));
await copyFile(join("src", "setup.js"), join(setup, "setup.cjs"));
// The island's page comes from its own scheme (src/island-page.ts), which serves only these three files from here.
const island = join("dist", "island");
await mkdir(island, { recursive: true });
await copyFile(join("src", "island.html"), join(island, "island.html"));
await copyFile(join("src", "island.css"), join(island, "island.css"));
await copyFile(join("src", "island.js"), join(island, "island.cjs"));
await cp(join("src", "locales"), join("dist", "locales"), { recursive: true });
await cp(join("src", "assets"), join("dist", "assets"), { recursive: true });
for (const name of ["preload", "setup-preload", "island-preload", "contract", "link-contract", "notification-permission"]) {
  await rename(join("dist-preload", "desktop", "src", `${name}.cjs`), join("dist", `${name}.cjs`));
}
