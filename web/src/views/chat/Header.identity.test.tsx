// @vitest-environment jsdom
// One face beside the expanded sidebar; the same header menu survives folding, hiding and phone layouts.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
const viewport = vi.hoisted(() => ({ narrow: false }));
vi.mock("../../lib/viewport", async (orig) => ({ ...(await orig<typeof import("../../lib/viewport")>()), useMedia: (q: string) => q === "(max-width: 1023px)" && viewport.narrow }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Chosen look", mochi: true }), PersonaDot: () => <span data-test-face /> }));
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: () => Promise.resolve({}) }));
import { Header } from "./ChatView";
import { useAssistantTitle } from "../ManagerParts";
import { setRoster, resetRoster } from "../../lib/managers";
import { resetManagerChoice } from "../../lib/manager-choice";
import { getLayout, setSidebarCollapsed, toggleMaximized } from "../../lib/layout";
import { t } from "../../i18n";
import type { Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const session: Session = { machine_id: "demo", session_id: "main", title: "Persona", cwd: "/", status: "idle", state: "idle", assistant: true };
let root: Root;
let host: HTMLDivElement;
function ChatHeader() {
  return <Header session={session} title={useAssistantTitle()} machineName="Example computer" assistant main siblings={[]} onInterrupt={() => {}} />;
}
const draw = () => act(() => root.render(<ChatHeader />));
beforeEach(() => {
  viewport.narrow = false;
  resetManagerChoice();
  setSidebarCollapsed(false);
  if (getLayout().maximized) toggleMaximized();
  setRoster({ default: "assistant", managers: [{ id: "assistant", name: "Example assistant", online: true }] });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); resetRoster(); resetManagerChoice();
  setSidebarCollapsed(false); if (getLayout().maximized) toggleMaximized();
});
describe("the main conversation header identity", () => {
  it("has a name pill alone beside the expanded assistant head, and opens the same menu", async () => {
    draw();
    expect(host.querySelector("[data-chat-face]")).toBeNull();
    const pill = host.querySelector<HTMLButtonElement>("[data-assistant-name-menu]")!;
    expect(pill.textContent).toBe("Example assistant");
    expect(pill.getAttribute("aria-haspopup")).toBe("menu");
    await act(async () => pill.click());
    expect(document.querySelector('[role="menu"]')?.textContent).toContain(t("assistant-assistantchat.details"));
    expect(document.querySelector('[role="menu"]')?.textContent).toContain(t("assistant-assistantchat.knowledge"));
    expect(pill.getAttribute("aria-expanded")).toBe("true");
  });
  it("restores the face in the rail and full-screen layouts and removes it on expanding", () => {
    draw();
    act(() => setSidebarCollapsed(true));
    expect(host.querySelector("[data-test-face]")).not.toBeNull();
    act(() => setSidebarCollapsed(false));
    expect(host.querySelector("[data-chat-face]")).toBeNull();
    act(() => toggleMaximized());
    expect(host.querySelector("[data-test-face]")).not.toBeNull();
    act(() => toggleMaximized());
    expect(host.querySelector("[data-chat-face]")).toBeNull();
  });
  it("keeps the face on a phone, and on an old hub with no assistant head", () => {
    viewport.narrow = true; draw();
    expect(host.querySelector("[data-test-face]")).not.toBeNull();
    viewport.narrow = false;
    act(() => setRoster({ default: "assistant", managers: [] })); draw();
    expect(host.querySelector("[data-test-face]")).not.toBeNull();
    expect(host.querySelector("[data-assistant-name-menu]")?.textContent).toBe("Chosen look");
  });
});
