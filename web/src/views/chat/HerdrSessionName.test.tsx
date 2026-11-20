// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { refreshAgentsDisplay, resetAgentsDisplay } from "../../lib/agents-display";
import { sessionTitle } from "../../lib/format";
import { reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import { SessionRow } from "../SidebarRows";
import { Header } from "./ChatView";
import { RenameDialog } from "./SessionMenuDialogs";

vi.hoisted(() => {
  globalThis.matchMedia = (() => ({ matches: false, addEventListener() {}, removeEventListener() {} })) as unknown as typeof matchMedia;
});
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session: Session = {
  machine_id: "demo", session_id: "export", title: "CLI session name", agent: "claude", cwd: "/work/export", status: "idle",
  space_kind: "herdr", slot_id: "w1:t1", slot_name: "Export work",
};
let root: Root, host: HTMLDivElement;
let preferTab: boolean;
let renameRequests: { tab_name?: string; title?: string }[];

beforeEach(() => {
  preferTab = false;
  renameRequests = [];
  localStorage.clear();
  resetAgentsDisplay();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/agents/display-settings")) return Response.json({ configured: false, show_botmux: true, use_herdr_tab_name: preferTab });
    if (url.endsWith("/rename")) renameRequests.push(JSON.parse(String(init?.body)));
    return Response.json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("sidebar and chat header naming", () => {
  const draw = (s: Session) => act(() => root.render(<>
    <SessionRow s={s} k="s:demo/export" reg={() => () => {}} hover={() => {}} active={false}
      to={{ name: "session", mid: s.machine_id, sid: s.session_id }} go={() => () => {}} onMenu={() => {}} />
    <Header session={s} title={sessionTitle(s)} machineName="Example computer" siblings={[]} onInterrupt={() => {}} />
  </>));
  const sidebar = () => host.querySelector("a")!;
  const header = () => host.querySelector("[data-chat-title]")!;

  it("follows the hub setting and the next observed inventory name", async () => {
    draw(session);
    expect(sidebar().textContent).toContain(session.title);
    expect(header().textContent).toBe(session.title);
    preferTab = true;
    await act(refreshAgentsDisplay);
    expect(sidebar().textContent).toContain(session.slot_name);
    expect(sidebar().textContent).not.toContain(session.title);
    expect(sidebar().getAttribute("title")).toContain(`\n${session.title}`);
    expect(header().textContent).toBe(session.slot_name);
    expect(header().getAttribute("title")).toContain(`\n${session.title}`);
    draw({ ...session, slot_name: "Owner's new name" });
    expect(sidebar().textContent).toContain("Owner's new name");
    expect(header().textContent).toBe("Owner's new name");
    draw({ ...session, slot_id: undefined, slot_name: undefined });
    expect(header().textContent).toBe(session.title);
  });

  it.each(["user@host:~/work/export", ""])("falls back to the tab with the preference off for title %j", (title) => {
    draw({ ...session, title });
    expect(sidebar().textContent).toContain(session.slot_name);
    expect(header().textContent).toBe(session.slot_name);
  });
});

describe("Rename dialog", () => {
  const fields = () => [...document.querySelectorAll<HTMLInputElement>('[role="dialog"] input')];
  const save = () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((button) => button.textContent === t("common.save"))!;
  const type = (input: HTMLInputElement, value: string) => act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });

  it("shows one Name field and renames both names when enabled", async () => {
    preferTab = true;
    await refreshAgentsDisplay();
    act(() => root.render(<RenameDialog session={session} open onClose={() => {}} />));
    expect(fields().map((field) => [field.closest("label")?.textContent, field.value])).toEqual([[t("chat-sessiondialogs.name"), session.slot_name]]);
    type(fields()[0], "Updated export");
    await act(async () => save().click());
    expect(renameRequests).toEqual([{ tab_name: "Updated export", title: "Updated export" }]);
  });

  it("keeps separate fields and sends only the edited name when off", async () => {
    act(() => root.render(<RenameDialog session={session} open onClose={() => {}} />));
    expect(fields().map((field) => field.value)).toEqual([session.slot_name, session.title]);
    type(fields()[1], "CLI only");
    await act(async () => save().click());
    expect(renameRequests).toEqual([{ title: "CLI only" }]);
  });

  it("renames just the tab if the CLI has no native rename here", async () => {
    preferTab = true;
    await refreshAgentsDisplay();
    act(() => root.render(<RenameDialog session={{ ...session, agent: "opencode" }} open onClose={() => {}} />));
    expect(fields()).toHaveLength(1);
    type(fields()[0], "Tab only");
    await act(async () => save().click());
    expect(renameRequests).toEqual([{ tab_name: "Tab only" }]);
  });
});
