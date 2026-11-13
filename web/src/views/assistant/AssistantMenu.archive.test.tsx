// @vitest-environment jsdom
// An ordinary Persona conversation's title menu reuses the sidebar row's Archive action (#6565): the same hub call,
// feedback and Undo, followed by an assistant refresh that marks it archived. The main chat and background supervisor
// cannot be archived, so their title menus do not offer the action.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MenuItem } from "../../ui/atoms";
import type { Session } from "../../lib/types";

const hub = vi.hoisted(() => ({
  archived: false,
  calls: [] as { path: string; method: string; json?: unknown }[],
}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => {
    hub.calls.push({ path, method: init?.method ?? "GET", json: init?.json });
    if (path === "/assistant/conversations/c1/archive") hub.archived = true;
    if (path === "/assistant/conversations/c1/unarchive") hub.archived = false;
    return Promise.resolve(path === "/assistant"
      ? { machine_id: "hub", main: "main", background_supervisor: "bg", conversations: [{ ...SESSION, archived: hub.archived }] }
      : { ok: true });
  },
}));
const navigate = vi.hoisted(() => vi.fn());
vi.mock("../../lib/router", async (orig) => ({ ...(await orig<typeof import("../../lib/router")>()), navigate }));
const notify = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../../ui/atoms")>()),
  toast: notify,
}));

import { getState, reset } from "../../lib/store";
import { useAssistantMenu } from "./AssistantChat";

const SESSION = { machine_id: "hub", session_id: "c1", title: "周报", cwd: "/", assistant: true, status: "idle" } as Session;
let items: MenuItem[] = [];
function Probe({ session }: { session: Session }) {
  items = useAssistantMenu(session).items;
  return null;
}
const keys = () => items.map((i) => i.key);
function render(session: Session) {
  const host = document.createElement("div");
  const root = createRoot(host);
  act(() => root.render(<Probe session={session} />));
  act(() => root.unmount());
}
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeEach(() => {
  reset();
  hub.archived = false;
  hub.calls.length = 0;
  navigate.mockClear();
  notify.mockClear();
  Object.assign(getState(), {
    features: [],
    assistant: { machine_id: "hub", main: "main", background_supervisor: "bg", conversations: [SESSION] },
  });
});

afterEach(() => reset());

describe("Archive in a Persona conversation's title menu (#6565)", () => {
  it("uses the sidebar action, refreshes to the archived result, and offers Undo without leaving the chat", async () => {
    render(SESSION);
    expect(keys()).toEqual(["profile", "kb", "main", "archive", "copyinfo"]);
    const archive = items.find((i) => i.key === "archive")!;
    expect(archive).toMatchObject({ label: "归档", icon: "archive" });

    await archive.onSelect();
    expect(hub.calls).toContainEqual({ path: "/assistant/conversations/c1/archive", method: "POST", json: {} });
    expect(notify).toHaveBeenCalledWith("已归档「周报」", "ok", expect.objectContaining({ label: "撤销" }));
    await vi.waitFor(() => expect(getState().assistant?.conversations[0]?.archived).toBe(true));
    expect(navigate).not.toHaveBeenCalled();

    const undo = notify.mock.calls[0][2]!;
    undo.run();
    await vi.waitFor(() => expect(getState().assistant?.conversations[0]?.archived).toBe(false));
    expect(hub.calls).toContainEqual({ path: "/assistant/conversations/c1/unarchive", method: "POST", json: {} });
  });

  it("is not offered for the main conversation", () => {
    render({ ...SESSION, session_id: "main" });
    expect(keys()).not.toContain("archive");
  });

  it("is not offered for the background supervisor", () => {
    render({ ...SESSION, session_id: "bg" });
    expect(keys()).not.toContain("archive");
  });
});
