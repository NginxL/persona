// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

vi.mock("./HiddenAgentSessions", () => ({ HiddenAgentSessions: () => null }));
vi.mock("./Sidebar", () => ({ AgentsList: () => null }));
vi.mock("./agents/AgentsAdd", () => ({ AgentsAdd: () => null }));
vi.mock("./ShellSwitch", () => ({ ShellHeaderSlots: ({ end }: { end?: React.ReactNode }) => <>{end}</> }));
vi.mock("../lib/palette", () => ({ openPalette: () => {} }));

import { AgentsPage } from "./AgentsPage";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  history.replaceState(null, "", "/agents");
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  history.replaceState(null, "", "/");
});

describe("Agents to Bots navigation", () => {
  it("opens the existing personal-Agent directory at /a with provider-neutral text", async () => {
    await act(async () => root.render(<AgentsPage route={{ name: "agent-list" }} onToggleSidebar={() => {}} onNewAgent={() => {}} />));
    const bots = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === t("sidebar.personal_agents") || b.textContent === t("sidebar.personal_agents"));
    expect(bots, "Bots navigation is present").not.toBeNull();
    expect(bots!.textContent).not.toMatch(/Orca|Lark|Feishu|Grok/);
    await act(async () => bots!.click());
    expect(location.pathname).toBe("/a");
  });
});
