// @vitest-environment jsdom
// @ another Agent's session from Persona's composer on a paging hub (#12983). A connection that asked for `sessions_list`
// gets no `sessions` pushes: the store's `sessions` holds only the chats opened in this tab, and every other Agent is only
// in the Agents list's rows (`sessionSummaries`). The + / @ panel offers both, each session once (an opened chat's
// complete row over its list row), never an assistant conversation, and a picked list row is quoted like an opened chat.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { loadPages } from "../../lib/lazy-page";
import { runtimeSessions } from "../../lib/runtime-list";
import { applyCompleteSessions, getState, reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Composer, type Outgoing } from "./Composer";
import type { MentionSource } from "./ComposerPanel";

const row = (id: string, extra: Partial<Session> = {}): Session => ({
  machine_id: "laptop", session_id: id, title: id, cwd: `/home/me/code/${id}`, status: "idle", state: "idle", agent: "claude",
  updated_at: "2026-11-20T10:00:00Z", waiting: false, ...extra,
});

// the Agents list's first page as a paging hub sends it: summary rows (`waiting` present, no detail arrays)
const LISTED: Session[] = [
  row("trip", { title: "Weekend trip", assistant: true, updated_at: "2026-11-20T13:00:00Z" }),
  row("weather", { title: "Weather app redesign", updated_at: "2026-11-20T12:00:00Z", last_text: "The new icons are in." }),
  row("login", { title: "Login page (list row)", updated_at: "2026-11-20T11:00:00Z" }),
];
// the one chat opened in this tab: its complete row
const OPENED = row("login", { title: "Fix the login page", updated_at: "2026-11-20T11:30:00Z", waiting: undefined, queued: [] });

const api = vi.fn(async (path: string, _init?: unknown): Promise<unknown> => {
  if (path.startsWith("/sessions?")) return { sessions: LISTED, remove: [], total: LISTED.length, has_more: false, revision: "r1" };
  if (path === "/workspaces") return { workspaces: [] };
  throw new Error(`unexpected ${path}`);
});
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Persona's composer: the main chat offers workspaces, so this is its panel (AssistantComposerPanel)
const SOURCE: MentionSource = { self: "persona-new", files: () => [], workspaces: true };
let host: HTMLDivElement;
let root: Root;

// the + / @ panel is a lazy chunk: in before the first @
beforeAll(() => loadPages());

beforeEach(async () => {
  Element.prototype.scrollIntoView ??= () => {}; // jsdom has none
  api.mockClear();
  localStorage.clear();
  // a paging hub's list, read as the store reads it on hello (summary rows), and one chat opened here
  runtimeSessions.setSummary(true);
  await act(async () => { await runtimeSessions.load(); });
  act(() => applyCompleteSessions([OPENED]));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

const render = (onSend: (m: Outgoing) => Promise<boolean> = async () => true) =>
  act(() => root.render(<Composer draftKey="persona-new" placeholder="" mention={SOURCE} onSend={onSend} />));

const box = () => host.querySelector("textarea")!;

async function type(text: string) {
  const el = box();
  await act(async () => {
    el.focus();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.setSelectionRange(text.length, text.length);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {}); // the workspaces' read settles
}

const key = (k: string) => act(() => void box().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
// the panel's resource rows (its own "Add images…" row left out)
const options = () => [...host.querySelectorAll('[role="option"]:not([data-panel-action])')].map((o) => o.textContent ?? "");

describe("@ other sessions on a paging hub (#12983)", () => {
  it("starts from the list's summary rows and one opened chat, as a paging hub leaves the store", () => {
    expect(api).toHaveBeenCalledWith(expect.stringMatching(/^\/sessions\?(.+&)?summary=1(&|$)/), undefined);
    expect(getState().sessionSummaries.map((s) => s.session_id)).toEqual(["trip", "weather", "login"]);
    expect(Object.values(getState().sessions).flat().map((s) => s.session_id)).toEqual(["login"]);
  });

  it("lists the Agents nobody opened here, each session once with an opened chat's complete row, no assistant conversation", async () => {
    render();
    await type("@");
    const rows = options();
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain("Weather app redesign");
    expect(rows[0]).toContain("Claude · weather");
    expect(rows[1]).toContain("Fix the login page");
    expect(rows.join("|")).not.toContain("Login page (list row)");
    expect(rows.join("|")).not.toContain("Weekend trip");
  });

  it("finds an Agent nobody opened here by what is typed after @", async () => {
    render();
    await type("@weather");
    expect(options()).toHaveLength(1);
    expect(options()[0]).toContain("Weather app redesign");
  });

  it("quotes a picked list row like an opened chat: its title, folder and last words", async () => {
    const onSend = vi.fn(async (_m: Outgoing) => true);
    render(onSend);
    await type("@weath");
    key("Enter");
    expect(options()).toHaveLength(0); // the panel closed
    expect(box().value).toBe("");
    await type("What changed?");
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!.click());
    expect(onSend).toHaveBeenCalledOnce();
    const text = onSend.mock.calls[0][0].text;
    expect(text).toContain('url="persona://session/laptop/weather"');
    expect(text).toContain("/home/me/code/weather");
    expect(text).toContain("The new icons are in.");
    expect(text.endsWith("</persona-context>\n\nWhat changed?")).toBe(true);
  });
});
