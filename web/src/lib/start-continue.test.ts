// A start's startup question answered from here (#10365): what the computer lists is read into a question with its
// answers, the answer goes to the session's start/continue, the row shows starting at once, and only the messages that
// start refused (shell_waiting) are sent again: the outbox's and a New Agent launch's first message.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./store", () => {
  const state = {
    outbox: {} as Record<string, import("./outbox").OutboxEntry[]>,
    launches: {} as Record<string, import("./new-agent").AgentLaunch>,
    sessions: {} as Record<string, import("./types").Session[]>,
    assistant: null,
    features: ["start_continue"] as string[],
  };
  return {
    getState: () => state,
    setOutbox: (key: string, list: import("./outbox").OutboxEntry[]) => {
      if (list.length) state.outbox[key] = list;
      else delete state.outbox[key];
    },
    putLaunch: (launch: import("./new-agent").AgentLaunch) => { state.launches[launch.id] = launch; },
    upsertSession: vi.fn(),
    refreshAssistant: vi.fn(async () => {}),
    isOpen: () => true,
    onHubEvent: () => () => {},
  };
});
vi.mock("./router", () => ({ href: (r: { mid: string; sid: string }) => `/s/${r.mid}/${r.sid}`, navigate: vi.fn() }));
vi.mock("../ui/atoms", async (actual) => ({ ...(await actual<typeof import("../ui/atoms")>()), toast: vi.fn() }));

import { ApiError } from "./api";
import { keepFailed, type OutboxEntry } from "./outbox";
import { continueStart, openQuestion, resendRefused, shellQuestion } from "./start-continue";
import { getState, upsertSession } from "./store";
import type { AgentLaunch } from "./new-agent";
import type { Session } from "./types";

const SID = "0e000000-0000-4000-8000-0000000000aa";
const args = {
  line: "[oh-my-zsh] Would you like to update? [Y/n]", screen: "Last login: today\n[oh-my-zsh] Would you like to update? [Y/n]",
  pane: "w1:p2", kept_until: "2026-11-18T10:10:00Z", kind: "update", choices: "y,n,enter", default: "y",
};
function response(body: unknown, status = 200) {
  return { ok: status < 400, status, headers: new Headers(), text: async () => JSON.stringify(body) };
}
const outbox = () => getState().outbox as Record<string, OutboxEntry[]>;
const posts = (fetcher: ReturnType<typeof vi.fn>) => fetcher.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === "POST")
  .map((c) => ({ url: String(c[0]), body: JSON.parse(String((c[1] as RequestInit).body ?? "null")) }));

beforeEach(() => {
  vi.stubGlobal("localStorage", { getItem: () => "test" });
  vi.stubGlobal("location", { pathname: "/other" });
  for (const k of Object.keys(outbox())) delete outbox()[k];
  getState().launches = {};
  getState().sessions = {};
  vi.clearAllMocks();
});
afterEach(() => vi.unstubAllGlobals());

describe("the question a start stopped on", () => {
  it("reads the question and its answers from start_error_args or a refused message's body", () => {
    expect(shellQuestion(args)).toEqual({
      line: args.line, screen: args.screen, kind: "update", choices: ["y", "n", "enter"], lineMode: false, secret: false,
      default: "y", keptUntil: args.kept_until,
    });
    const refused = { error: "…", code: "shell_waiting", delivered: "none", retryable: false, ...args, line_mode: "true" };
    expect(shellQuestion(refused)?.lineMode).toBe(true);
    expect(shellQuestion({ ...args, kind: "secret", secret: "true", choices: "cancel" })).toMatchObject({ secret: true, choices: ["cancel"] });
    // an older node's: the question without answers, nothing to offer
    expect(shellQuestion({ line: args.line, screen: args.screen })).toBeUndefined();
    expect(shellQuestion(undefined)).toBeUndefined();
  });

  it("is open until its tab's kept_until", () => {
    const q = shellQuestion(args)!;
    expect(openQuestion(q, Date.parse(args.kept_until) - 1)).toBe(q);
    expect(openQuestion(q, Date.parse(args.kept_until))).toBeUndefined();
    expect(openQuestion({ ...q, keptUntil: undefined }, Date.now())).toBeDefined();
  });
});

describe("answering it", () => {
  it("posts the answer and shows the session starting at once", async () => {
    getState().sessions = { mac: [{ machine_id: "mac", session_id: SID, status: "offline", start_error: "stopped on a question",
      start_error_code: "shell_waiting", start_error_args: args } as unknown as Session] };
    const fetcher = vi.fn(async () => response({ session: { machine_id: "mac", session_id: SID, status: "idle", starting: true } }));
    vi.stubGlobal("fetch", fetcher);
    await continueStart("mac", SID, "n");
    expect(posts(fetcher)).toEqual([{ url: `/api/v1/sessions/mac/${SID}/start/continue`, body: { choice: "n" } }]);
    const row = vi.mocked(upsertSession).mock.calls[0][0];
    expect(row).toMatchObject({ session_id: SID, starting: true, status: "idle" });
    expect(row.start_error).toBeUndefined();
    expect(row.start_error_code).toBeUndefined();
    expect(row.start_error_args).toBeUndefined();
  });

  it("refuses as the hub does: nothing changes locally", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => response({ error: "the tab is gone", code: "start_tab_gone", delivered: "none" }, 409)));
    await expect(continueStart("mac", SID, "n")).rejects.toMatchObject({ status: 409, code: "start_tab_gone" });
    expect(upsertSession).not.toHaveBeenCalled();
  });

  it("then sends again only what that start refused: outbox messages and a launch's first message", async () => {
    const refused = new ApiError(409, "stopped on a question", { code: "shell_waiting", delivered: "none", ...args });
    keepFailed("mac", SID, { id: "m1", text: "first", images: [], at: 1 }, refused);
    keepFailed("mac", SID, { id: "m2", text: "other", images: [], at: 2 }, new ApiError(409, "a dialog", { code: "dialog_open", delivered: "none" }));
    keepFailed("mac", SID, { id: "m3", text: "unsure", images: [], at: 3 }, new ApiError(504, "no answer", { code: "timeout", delivered: "unknown" }));
    const launch: AgentLaunch = { id: "local-1", machineId: "mac", cwd: "/w", launcher: "", text: "fix it", attachments: [], phase: "failed",
      uploaded: {}, startOpId: "s1", startPromptOpId: "s2", messageId: "l1", session: { machine_id: "mac", session_id: SID } as Session,
      outgoing: { text: "fix it", images: [] }, shellWaiting: { code: "shell_waiting", ...args } };
    getState().launches = { [launch.id]: launch, other: { ...launch, id: "other", shellWaiting: undefined } };
    const fetcher = vi.fn(async () => response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    resendRefused("mac", SID);
    await vi.waitFor(() => expect(posts(fetcher)).toHaveLength(2));
    const sent = posts(fetcher);
    expect(sent.every((p) => p.url === `/api/v1/sessions/mac/${SID}/messages`)).toBe(true);
    expect(sent.map((p) => p.body.text).sort()).toEqual(["first", "fix it"]);
    await vi.waitFor(() => expect(getState().launches["local-1"].phase).toBe("ready"));
    expect(getState().launches.other.phase).toBe("failed");
    const states = Object.fromEntries((outbox()[`mac/${SID}`] ?? []).map((e) => [e.text, e.state]));
    expect(states).toMatchObject({ other: "failed", unsure: "failed" });
    expect(states.first).not.toBe("failed");
  });

  // The computer hands a conversation's messages to the agent in the order they arrive: the launch's first message
  // was written before any the chat's start refused, so it goes first, and those once it went, one after another.
  const refusedLaunch = (): AgentLaunch => ({ id: "local-1", machineId: "mac", cwd: "/w", launcher: "", text: "fix it", attachments: [],
    phase: "failed", uploaded: {}, startOpId: "s1", startPromptOpId: "s2", messageId: "l1", session: { machine_id: "mac", session_id: SID } as Session,
    outgoing: { text: "fix it", images: [] }, shellWaiting: { code: "shell_waiting", ...args } });
  const refusedInChat = () => {
    const refused = new ApiError(409, "stopped on a question", { code: "shell_waiting", delivered: "none", ...args });
    keepFailed("mac", SID, { id: "m2", text: "second", images: [], at: 2 }, refused);
    keepFailed("mac", SID, { id: "m3", text: "third", images: [], at: 3 }, refused);
  };

  it("sends the launch's first message before the chat's refused ones, and those in their order", async () => {
    refusedInChat();
    getState().launches = { "local-1": refusedLaunch() };
    let release = () => {};
    const fetcher = vi.fn(async (_url: string, init?: RequestInit) => {
      if (JSON.parse(String(init?.body ?? "null"))?.client_msg_id === "l1") await new Promise<void>((done) => { release = done; });
      return response({ ok: true });
    });
    vi.stubGlobal("fetch", fetcher);
    const resent = resendRefused("mac", SID);
    await vi.waitFor(() => expect(posts(fetcher)).toHaveLength(1));
    expect(posts(fetcher)[0].body.text).toBe("fix it");
    release();
    await resent;
    await vi.waitFor(() => expect(posts(fetcher)).toHaveLength(3));
    expect(posts(fetcher).map((p) => p.body.text)).toEqual(["fix it", "second", "third"]);
  });

  it("keeps the chat's back when the launch's first message is refused again", async () => {
    refusedInChat();
    getState().launches = { "local-1": refusedLaunch() };
    const fetcher = vi.fn(async () => response({ error: "stopped on a question", code: "shell_waiting", delivered: "none", ...args }, 409));
    vi.stubGlobal("fetch", fetcher);
    await resendRefused("mac", SID);
    expect(posts(fetcher).map((p) => p.body.text)).toEqual(["fix it"]);
    expect(getState().launches["local-1"].phase).toBe("failed");
    expect((outbox()[`mac/${SID}`] ?? []).map((e) => e.state)).toEqual(["failed", "failed"]);
  });
});
