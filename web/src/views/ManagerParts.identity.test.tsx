// @vitest-environment jsdom
// The header shares the sidebar identity; offline restrictions keep their multi-assistant gate.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { chooseManager, noteShownIdentity, resetManagerChoice } from "../lib/manager-choice";
import { resetRoster, setRoster, type ManagerList } from "../lib/managers";
import { useAssistantTitle, useManagerOffline, useShownOffline } from "./ManagerParts";

vi.mock("./assistant/Persona", () => ({ usePersona: () => ({ name: "Chosen look" }) }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const DEFAULT = { id: "assistant", name: "Only assistant", online: true, default: true, builtin: true };
const OTHER = { id: "m-other", name: "Second assistant", online: false };
let root: Root;
let host: HTMLDivElement;
function Chrome() {
  const title = useAssistantTitle();
  const offline = useShownOffline();
  const composer = useManagerOffline("assistant");
  return <><h1>{title}</h1><span>{offline?.title}</span><span>{composer}</span></>;
}
function render(roster: ManagerList) {
  act(() => { setRoster(roster); root.render(<Chrome />); });
}
beforeEach(() => {
  resetManagerChoice();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetRoster();
  resetManagerChoice();
});
describe("assistant conversation identity", () => {
  it("uses the remembered head during a reload, then the chosen look on an old hub", () => {
    act(() => { noteShownIdentity({ id: "assistant", name: "Remembered assistant" }); root.render(<Chrome />); });
    expect(host.querySelector("h1")?.textContent).toBe("Remembered assistant");
    render({ default: "assistant", managers: [] });
    expect(host.querySelector("h1")?.textContent).toBe("Chosen look");
  });
  it("names the single assistant, including offline, without adding offline restrictions", () => {
    render({ default: "assistant", managers: [DEFAULT] });
    expect(host.textContent).toBe(DEFAULT.name);
    render({ default: "assistant", managers: [{ ...DEFAULT, online: false }] });
    expect(host.textContent).toBe(DEFAULT.name);
  });
  it("names the current assistant with multiple assistants and updates on a switch", () => {
    render({ default: "assistant", managers: [DEFAULT, OTHER] });
    expect(host.querySelector("h1")?.textContent).toBe(DEFAULT.name);
    act(() => chooseManager(OTHER.id));
    expect(host.querySelector("h1")?.textContent).toBe(OTHER.name);
    expect(host.textContent).toContain(OTHER.name);
    act(() => setRoster({ default: "assistant", managers: [DEFAULT] }));
    expect(host.textContent).toBe(DEFAULT.name);
  });
  it("names the current assistant while another is still installing", () => {
    render({ default: "assistant", managers: [DEFAULT, { ...OTHER, installing: new Date(Date.now() + 60_000).toISOString() }] });
    expect(host.textContent).toBe(DEFAULT.name);
  });
});
