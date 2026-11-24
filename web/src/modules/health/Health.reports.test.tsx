// @vitest-environment jsdom
// The Health home's reports card (docs/assistant/health.md "Checkup reports") with several assistants: the health space
// stays the default manager's (docs/assistant/managers.md "Clients"), so its reports are read on the machine its own list
// names, where uploads go, never on the machine of the assistant the Persona page shows.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ paths: [] as string[], healthDown: false, shown: "mac-b" as string | null }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => {
    hub.paths.push(path);
    if (path.startsWith("/assistant?space=health")) {
      return hub.healthDown ? Promise.reject(new Error("health list down")) : Promise.resolve({ machine_id: "mac-a", conversations: [] });
    }
    if (path.startsWith("/assistant/memory?space=health")) return Promise.resolve({ files: [] });
    if (path.endsWith("/health/reports")) return Promise.resolve({ reports: [] });
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    // the assistant the Persona page shows: another manager's, on another machine
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T =>
      select({ ...real.getState(), assistant: hub.shown ? { machine_id: hub.shown } : null } as ReturnType<typeof real.getState>),
    onHubEvent: () => () => {},
  };
});
vi.mock("../../views/chat/ChatView", () => ({ ChatView: () => null }));

import { Health } from "./Health";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const card = () => host.querySelector('[data-testid="health-reports"]');
const reportReads = () => hub.paths.filter((p) => p.endsWith("/health/reports"));

beforeEach(() => {
  hub.paths.length = 0;
  hub.healthDown = false;
  hub.shown = "mac-b";
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the Health home's reports card", () => {
  it("reads the reports on the health space's machine, not on the shown assistant's", async () => {
    act(() => root.render(<Health fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(reportReads()).toEqual(["/machines/mac-a/health/reports"]);
    expect(card()?.textContent).toContain(t("health.reports_empty"));
  });

  it("says it could not read them, not loading forever, when the health space's list failed", async () => {
    hub.healthDown = true;
    hub.shown = null;
    act(() => root.render(<Health fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(reportReads()).toEqual([]);
    expect(card()?.querySelector("[role=status]")).toBeNull();
    expect(card()?.textContent).toContain(t("health.reports_load_failed", { error: "health list down" }));
  });
});
