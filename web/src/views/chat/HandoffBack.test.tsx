// @vitest-environment jsdom
// The way back from an Agent's page to the conversation that handed it work (#5096, cindy's HandoffSourcePill), or to
// the page it was opened from inside (#12055).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { navigate } from "../../lib/router";
import { getState, reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import { fromTitle, HandoffBack } from "./HandoffBack";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.restoreAllMocks();
});

const conv = (session_id: string, title: string) => ({ machine_id: "m1", session_id, title, cwd: "", status: "idle" }) as Session;

describe("the way back", () => {
  it("names the conversation: Persona for the main one, its title for another", () => {
    const s = { assistant: { ...getState().assistant!, main: "main-1", conversations: [conv("main-1", "whatever"), conv("c2", "Trip to Kyoto")] } };
    expect(fromTitle(s, "/persona")).toBe("Persona");
    expect(fromTitle(s, "/persona/main-1")).toBe("Persona");
    expect(fromTitle(s, "/persona/c2?machine=m1")).toBe("Trip to Kyoto");
  });

  it("names a page an Agent was opened from inside: a to-do or an Agent by its title, else the page's name (#12055)", () => {
    const todo = { id: "t1", title: "Tidy the release checklist", created_at: "", updated_at: "" };
    const s = { assistant: getState().assistant, todos: [todo, { ...todo, id: "blank", title: "  " }], sessions: { m1: [conv("a1", "Fix the login page")] } };
    expect(fromTitle(s, "/todos?id=t1")).toBe("Tidy the release checklist");
    expect(fromTitle(s, "/todos?view=calendar&id=t1")).toBe("Tidy the release checklist");
    expect(fromTitle(s, "/todos?id=blank")).toBe(t("sidebar.nav_todos"));
    expect(fromTitle(s, "/todos?id=gone")).toBe(t("sidebar.nav_todos"));
    expect(fromTitle(s, "/s/m1/a1")).toBe("Fix the login page");
    expect(fromTitle(s, "/kb/projects/p1")).toBe(t("sidebar.nav_kb"));
    expect(fromTitle(s, "/kb/recordings/r1")).toBe(t("sidebar.nav_kb"));
    expect(fromTitle(s, "/workspace-board")).toBe(t("sidebar.nav_board"));
    expect(fromTitle(s, "/machines/m1")).toBe(t("sidebar.nav_machines"));
    // nothing to call it: the strip says where Back goes instead
    expect(fromTitle(s, "/s/m1/gone")).toBeUndefined();
    expect(fromTitle(s, "/compare/r1")).toBeUndefined();
  });

  it("says Back to the previous page for a page it has no name for", () => {
    history.replaceState({ handoff: { path: "/compare/r1" } }, "", "/s/m1/s1");
    act(() => root.render(<HandoffBack onDismiss={() => {}} />));
    expect(host.querySelector("button")!.textContent).toBe(t("chat-backstrip.handoff_previous"));
    history.replaceState({ handoff: { path: "/kb/projects/p1" } }, "", "/s/m1/s1");
    act(() => root.render(<HandoffBack onDismiss={() => {}} />));
    expect(host.querySelector("button")!.textContent).toBe(t("chat-backstrip.handoff_from", { title: t("sidebar.nav_kb") }));
  });

  it("disappears when reselecting the Agent makes its page a root, without adding history (#12055)", () => {
    history.pushState({ handoff: { path: "/kb/projects/p1" } }, "", "/s/m1/s1");
    const entries = history.length;
    act(() => root.render(<HandoffBack onDismiss={() => {}} />));
    expect(host.querySelector("[data-handoff-back]")).not.toBeNull();

    act(() => navigate("/s/m1/s1", true, {}));

    expect(history.state).toEqual({});
    expect(history.length).toBe(entries);
    expect(host.querySelector("[data-handoff-back]")).toBeNull();
  });

  it("goes back in the history, and is put away by its ✕", () => {
    history.pushState({ handoff: { path: "/persona" } }, "", "/s/m1/s1"); // the card pushed the Agent's page
    const back = vi.spyOn(history, "back").mockImplementation(() => {});
    const dismiss = vi.fn();
    act(() => root.render(<HandoffBack onDismiss={dismiss} />));
    const [go, close] = [...host.querySelectorAll("button")];
    expect(go!.textContent).toBe(t("chat-backstrip.handoff_from", { title: "Persona" }));
    act(() => go!.click());
    expect(back).toHaveBeenCalledOnce();
    expect(close!.getAttribute("aria-label")).toBe(t("chat-backstrip.handoff_dismiss"));
    act(() => close!.click());
    expect(dismiss).toHaveBeenCalledOnce();
  });
});
