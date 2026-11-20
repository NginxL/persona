import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

describe("QueueStrip's loading boundary", () => {
  it("keeps draft restoration with the intent-loaded consumers and out of the startup catalog", () => {
    const eager = readFileSync(join(__dirname, "QueueStrip.tsx"), "utf8");
    expect(eager).toContain('import type { QueuePanelProps } from "./QueuePanel"');
    expect(eager).toContain('lazyPage(() => import("./QueuePanel").then((m) => m.QueuePanel))');
    expect(eager).not.toContain("restoreQueued");
    expect(eager).not.toContain("readPhoto");
    expect(eager).not.toContain("chat-queuestrip");
    expect(eager).not.toContain('from "./queue-restore"');
    for (const name of ["QueuePanel.tsx", "ForwardDialog.tsx"]) {
      expect(readFileSync(join(__dirname, name), "utf8")).toContain('from "./queue-restore"');
    }
    const forwardHost = readFileSync(join(__dirname, "ForwardHost.tsx"), "utf8");
    expect(forwardHost).toContain('lazyPage(() => import("./ForwardDialog").then((m) => m.ForwardDialog))');
    const restore = readFileSync(join(__dirname, "queue-restore.ts"), "utf8");
    expect(restore).toContain('t("chat-queuestrip.photos_as_paths"');
    const startup = JSON.parse(readFileSync(join(__dirname, "../../i18n/startup-namespaces.json"), "utf8")) as string[];
    expect(startup).not.toContain("chat-queuestrip");
  });
});
