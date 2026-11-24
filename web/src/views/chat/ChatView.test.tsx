import { renderToReadableStream, renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { Outgoing } from "./Composer";
import type { MentionSource } from "./ComposerPanel";
import type { Item, Session } from "../../lib/types";
import { getFeedState, setRoute } from "../../lib/feed";
import { loadPages } from "../../lib/lazy-page";
import { outboxKey, type OutboxEntry } from "../../lib/outbox";
import { toast } from "../../ui/atoms";
import { t } from "../../i18n";
import { ChatView, ComposerSource, footerRows } from "./ChatView";

const captured = vi.hoisted(() => ({
  onSend: undefined as ((out: Outgoing) => Promise<boolean>) | undefined,
  mention: undefined as MentionSource | undefined,
  session: { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle" } as Session,
  items: [] as Item[],
  /** The chat's outbox (lib/outbox.ts), by outboxKey. */
  outbox: {} as Record<string, OutboxEntry[]>,
  error: "",
  loading: false,
  /** Set: the hub could not say what this chat is (useRuntimeSession's notice, no row). */
  noDetails: "",
  /** The manager a link tapped in the chat opens what it opens as (useLinkManager under ChatView). */
  linkManager: undefined as string | undefined,
}));

vi.mock("./Composer", async (importOriginal) => {
  const { useLinkManager } = await import("../../lib/manager-choice");
  return {
    ...await importOriginal<typeof import("./Composer")>(),
    Composer: ({ onSend, mention }: { onSend: (out: Outgoing) => Promise<boolean>; mention?: MentionSource }) => {
      captured.onSend = onSend;
      captured.mention = mention;
      captured.linkManager = useLinkManager();
      return <div data-testid="composer" />;
    },
  };
});
vi.mock("./useTranscript", () => ({
  useTranscript: () => ({ items: captured.items, loading: captured.loading, error: captured.error, hasMore: false, streaming: "" }),
}));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) => select({ ...store.getState(), sessions: { hub: [captured.session] }, outbox: captured.outbox }),
  };
});
vi.mock("../../lib/runtime-session", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/runtime-session")>();
  return {
    ...real,
    useRuntimeSession: (...args: Parameters<typeof real.useRuntimeSession>) => {
      const found = real.useRuntimeSession(...args);
      return captured.noDetails ? { session: undefined, detail: { loading: false, error: captured.noDetails, missing: false, legacy: true }, retry: () => {} } : found;
    },
  };
});
vi.mock("../../lib/panel", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/panel")>(),
  usePanel: () => null,
}));
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../ui/atoms")>(),
  toast: vi.fn(),
}));
vi.mock("../../lib/router", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/router")>(),
  useRoute: () => ({ name: "session", mid: "hub", id: "c1" }),
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
// a reply's Forward and Use as context drawn in the page: they are in its menus (the bar's ⋯ since #5314), which open
// on a click, so the chat's actions are read here as those menus read them
vi.mock("./MessageActions", async (importOriginal) => {
  const real = await importOriginal<typeof import("./MessageActions")>();
  const { useChatActions } = await import("./chat-actions");
  function MessageActions(p: Parameters<typeof real.MessageActions>[0]) {
    const chat = useChatActions();
    const pick = p.itemId ? { itemId: p.itemId, text: p.text, isUser: false } : undefined;
    return <>{p.children}{real.messageContextActions(p.text, chat, pick).map((a) => <button key={a.key} type="button" aria-label={a.label} />)}{p.bar && <div data-reply-bar="" />}</>;
  }
  return { ...real, MessageActions };
});
vi.mock("../assistant/AssistantChat", () => ({
  AssistantFace: () => null,
  CompactionStrip: () => null,
  useAssistantMenu: () => ({ items: [], dialogs: null }),
}));
vi.mock("../assistant/Tasks", () => ({
  DayLine: () => null,
  FeedLineView: () => null,
  QuickResultCard: () => null,
  RouteChip: () => null,
  AssistantActionBar: () => null,
  TaskDock: () => null,
  TaskQuestions: () => null,
}));

function sender(assistant = true) {
  renderToStaticMarkup(<ChatView mid="hub" sid="c1" assistant={assistant} />);
  expect(captured.onSend).toBeTypeOf("function");
  return captured.onSend!;
}

const image = { mime: "image/png", data: "aW1hZ2U=" };
let fetcher: ReturnType<typeof vi.fn>;
beforeEach(() => {
  setRoute("c1", null);
  captured.onSend = undefined;
  captured.mention = undefined;
  delete captured.session.manager;
  delete captured.session.agent;
  captured.items = [];
  captured.outbox = {};
  captured.error = "";
  captured.loading = false;
  delete captured.session.source_kind;
  delete captured.session.capabilities;
  vi.mocked(toast).mockClear();
  vi.stubGlobal("localStorage", { getItem: () => "", setItem: vi.fn(), removeItem: vi.fn() });
  fetcher = vi.fn().mockResolvedValue({ ok: true, status: 200, text: async () => "{}" });
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => {
  setRoute("c1", null);
  vi.unstubAllGlobals();
});

describe("ChatView composer task routing", () => {
  it("wires ordinary Agent catalogs to the session's manager and preserves the selected ref in its send", async () => {
    captured.session.manager = "helper-agent";
    captured.session.agent = "codex";
    const text = '<persona-context kind="skill" ref="opaque-session-skill">review</persona-context>\nReview this';
    expect(await sender(false)({ text, images: [] })).toBe(true);
    expect(captured.mention?.skills).toMatchObject({ agent: true, manager: "helper-agent", session: "c1" });
    expect(JSON.parse(fetcher.mock.calls[0][1].body).text).toContain('ref="opaque-session-skill"');
    expect(JSON.parse(fetcher.mock.calls[0][1].body).text).toContain("Review this");
  });
  it.each(["Look at this", ""])("rejects a routed image with text %j without delivering to Persona or dropping the route", async (text) => {
    setRoute("c1", "task1");
    const out = { text, images: [image] };
    expect(await sender()(out)).toBe(false);
    expect(fetcher).not.toHaveBeenCalled();
    expect(getFeedState().routes.c1).toBe("task1");
    expect(out).toEqual({ text, images: [image] });
    expect(toast).toHaveBeenCalledWith(t("chat-chatview.route_no_images"), "error");
  });

  it("sends a text-only message to the selected task", async () => {
    setRoute("c1", "task1");
    expect(await sender()({ text: "Continue", images: [] })).toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(1);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/api/v1/assistant/conversations/c1/route");
    expect(JSON.parse(init.body)).toEqual({ todo_id: "task1", text: "Continue" });
  });

  it.each([true, false])("still delivers an unrouted image to its conversation (assistant=%j)", async (assistant) => {
    expect(await sender(assistant)({ text: "Look at this", images: [image] })).toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(1);
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe("/api/v1/sessions/hub/c1/messages");
    expect(JSON.parse(init.body)).toMatchObject({ text: "Look at this", images: [image] });
  });

  // a terminal Claude held on a question at the computer reads it only once that is answered (#5890)
  it.each([[true, 1], [false, 0]])("says when the message waits on the computer (waits_on_computer=%j)", async (waits, toasts) => {
    fetcher.mockResolvedValueOnce({ ok: true, status: 200, text: async () => JSON.stringify(waits ? { ok: true, waits_on_computer: true } : { ok: true }) });
    expect(await sender(false)({ text: "One more thing", images: [] })).toBe(true);
    expect(vi.mocked(toast).mock.calls.filter(([text]) => text === t("lib-outbox.waits_on_computer"))).toEqual(toasts ? [[t("lib-outbox.waits_on_computer"), "info"]] : []);
  });

  // your own message to a Claude with Persona's plugin went through its inbox instead of as your input
  it.each([
    [{ delivered_as: "relayed", relay_reason: "busy" }, "lib-outbox.relayed_busy"],
    [{ delivered_as: "relayed", relay_reason: "plugin_unavailable" }, "lib-outbox.relayed_plugin"],
    [{ delivered_as: "user_input" }, undefined],
    [{ delivered_as: "relayed", relay_reason: "no_plugin" }, undefined],
  ])("says when the message may not count as your approval (%j)", async (reply, key) => {
    fetcher.mockResolvedValueOnce({ ok: true, status: 200, text: async () => JSON.stringify({ ok: true, ...reply }) });
    expect(await sender(false)({ text: "Go ahead and merge", images: [] })).toBe(true);
    const relayed = [t("lib-outbox.relayed_busy"), t("lib-outbox.relayed_plugin")];
    expect(vi.mocked(toast).mock.calls.filter(([text]) => relayed.includes(String(text)))).toEqual(key ? [[t(key), "info"]] : []);
  });
});

describe("ChatView message context availability", () => {
  // Forward (#3899) goes from any chat, Persona's included, even one that can't send here
  it.each([
    [false, true, true, true],
    [true, true, true, true],
    [false, false, true, false],
  ])("uses the chat's actual send capability (assistant=%s, send=%s)", (assistant, send, forward, context) => {
    captured.items = [{ id: "reply#0", seq: 1, kind: "text", role: "assistant", text: "A reply" }];
    if (!send) {
      captured.session.source_kind = "codex_history";
      captured.session.capabilities = { read: true };
    }
    const html = renderToStaticMarkup(<ChatView mid="hub" sid="c1" assistant={assistant} />);
    expect(html.includes('aria-label="转发"')).toBe(forward);
    expect(html.includes('aria-label="交给 Persona"')).toBe(false);
    expect(html.includes('aria-label="作为上下文"')).toBe(context);
  });
});

// #5314: a message of yours on its way draws the turn it answers finished for a moment (the live line steps aside):
// its last words get no reply bar until the chat's work is over, so the bar does not come and go.
describe("the reply bar while a message is sent", () => {
  it("is held back on the last exchange while the chat is at work, and there once it is idle", () => {
    captured.items = [
      { id: "u1#0", seq: 1, kind: "text", role: "user", text: "Look at it" },
      { id: "a2#0", seq: 2, kind: "text", role: "assistant", text: "It is fine." },
    ];
    expect(renderToStaticMarkup(<ChatView mid="hub" sid="c1" />)).toContain("data-reply-bar");
    captured.outbox = { [outboxKey("hub", "c1")]: [{ id: "m1", text: "And the tests?", images: [], at: Date.now(), state: "sending" }] };
    const html = renderToStaticMarkup(<ChatView mid="hub" sid="c1" />);
    expect(html).toContain("It is fine.");
    expect(html).not.toContain("data-reply-bar");
  });
});

describe("Agent context usage", () => {
  it("leaves the header and appears in the row below the composer, at the right of the folder; no empty row when there is neither", async () => {
    async function render() {
      const stream = await renderToReadableStream(<ChatView mid="hub" sid="c1" />);
      await stream.allReady;
      return new Response(stream).text();
    }
    const was = captured.session;
    try {
      captured.session = { ...was, agent: "claude", context_tokens: 78000, context_window: 200000 };
      const html = (await render());
      const header = html.slice(html.indexOf("<header"), html.indexOf("</header>"));
      expect(header).not.toContain("上下文 39%");
      expect(html).toContain('data-composer-usage');
      expect(html).toContain('aria-valuenow="39"');
      expect(html.indexOf('data-testid="composer"')).toBeLessThan(html.indexOf("data-composer-usage"));
      expect(html.indexOf("data-composer-source")).toBeLessThan(html.indexOf('aria-valuenow="39"'));
      captured.session = { ...was, agent: "claude", context_tokens: undefined, context_window: undefined };
      const unknown = (await render());
      expect(unknown).toContain("data-composer-source");
      expect(unknown).not.toContain("aria-valuenow");
      captured.session = { ...was, agent: "claude", cwd: "", context_tokens: undefined, context_window: undefined };
      expect((await render())).not.toContain('data-composer-usage');
    } finally {
      captured.session = was;
    }
  });

  it("shows ContextMeter in Assistant chats whenever context or token usage is available (#12541)", async () => {
    async function renderAssistant() {
      const stream = await renderToReadableStream(<ChatView mid="hub" sid="c1" assistant />);
      await stream.allReady;
      return new Response(stream).text();
    }
    const wasSession = captured.session;
    const wasItems = captured.items;
    try {
      captured.session = { ...wasSession, assistant: true, context_tokens: 50_000, context_window: 200_000 };
      captured.items = [];
      const withContext = await renderAssistant();
      expect(withContext).toContain("data-composer-usage");
      expect(withContext).toContain('aria-valuenow="25"');
      expect(withContext).not.toContain("data-composer-source");

      captured.session = { ...wasSession, assistant: true, context_tokens: undefined, context_window: undefined };
      captured.items = [{ id: "a1", seq: 1, role: "assistant", kind: "text", usage: [{ in: 2000, out: 1000 }] }];
      const withUsage = await renderAssistant();
      expect(withUsage).toContain("data-composer-usage");
      expect(withUsage).toContain("3.0k tok");

      captured.items = [];
      expect(await renderAssistant()).not.toContain("data-composer-usage");
    } finally {
      captured.session = wasSession;
      captured.items = wasItems;
    }
  });
});

describe("links in the chat", () => {
  it("open what they open as the chat's manager (docs/assistant/managers.md \"Grouping\")", () => {
    const was = captured.session;
    try {
      captured.session = { ...was, manager: "m-2" };
      renderToStaticMarkup(<ChatView mid="hub" sid="c1" />);
      expect(captured.linkManager).toBe("m-2");
      captured.session = { ...was, manager: undefined };
      renderToStaticMarkup(<ChatView mid="hub" sid="c1" assistant />);
      expect(captured.linkManager).toBeUndefined();
    } finally {
      captured.session = was;
    }
  });
});

describe("cached main conversation refresh notice", () => {
  it.each(["assistant", "transcript"])("keeps the transcript and offers Reload when the %s refresh fails", (source) => {
    captured.items = [{ id: "cached", seq: 1, role: "assistant", kind: "text", text: "Cached reply" }];
    if (source === "transcript") captured.error = "Unavailable";
    const html = renderToStaticMarkup(<ChatView mid="hub" sid="c1" assistant mainCache={{ scope: "test-scope" }} refreshError={source === "assistant" ? "Unavailable" : undefined} />);
    expect(html).toContain("Cached reply");
    expect(html).toContain(t("chat-chatview.refresh_failed"));
    expect(html).toContain(t("chat-chatview.not_live_reload"));
    expect(html).toContain('title="Unavailable"');
  });
});

it("keeps a remembered Agent chat on screen when its refresh fails", () => {
  captured.items = [{ id: "kept", seq: 1, role: "assistant", kind: "text", text: "Kept reply" }];
  captured.error = "Unavailable";
  const html = renderToStaticMarkup(<ChatView mid="hub" sid="c1" />);
  expect(html).toContain("Kept reply");
  expect(html).toContain(t("chat-chatview.refresh_failed"));
  expect(html).not.toContain(t("chat-chatview.retry"));
});

describe("the line under the composer (#3533, #5324)", () => {
  it("lists the folder in full, labelled; the launcher and model are the model pill's (#5032)", () => {
    const s = { machine_id: "m", session_id: "s", launcher: "claude", model: "anthropic/claude-opus-5-5", cwd: "/Users/me/code/weather" } as Session;
    expect(footerRows(s).map((r) => [r.key, r.label, r.hint])).toEqual([["folder", "~/code/weather", t("chat-sessiondialogs.folder")]]);
  });
  it("the line under the box keeps the folder and where it comes from, without the launcher or the model (#5032, #5324)", () => {
    const s = { machine_id: "m", session_id: "s", status: "idle", agent: "claude", launcher: "gateway-cc", model: "claude-opus-5-5", effort: "high", permission_mode: "plan", cwd: "/Users/me/code/weather" } as Session;
    const html = renderToStaticMarkup(<ComposerSource session={s} />);
    expect(html).toContain("data-composer-source");
    expect(html).toContain("~/code/weather");
    expect(html).toContain('title="/Users/me/code/weather"');
    expect(html).not.toContain("gateway-cc");
    expect(html).not.toContain("opus");
    // the permission picker is in the composer's toolbar now, not on this line
    expect(html).not.toContain(t("lib-session-actions.mode_plan"));
    // a Persona conversation shows no folder
    expect(renderToStaticMarkup(<ComposerSource session={{ ...s, assistant: true }} assistant />)).not.toContain("weather");
  });
  it("leaves out what is missing, and in a Persona conversation the folder too", () => {
    const s = { machine_id: "m", session_id: "s", launcher: "claude", model: "opus", cwd: "/code" } as Session;
    expect(footerRows(s, true)).toEqual([]);
    expect(footerRows(s).every((r) => r.wrap)).toBe(true);
    expect(footerRows({ machine_id: "m", session_id: "s" } as Session)).toEqual([]);
  });
});

describe("ChatView opening line over an empty chat (#2450)", () => {
  const opening = t("chat-chatview.started_in", { path: "/code" });
  const markup = () => renderToStaticMarkup(<ChatView mid="hub" sid="c1" />);

  it("shows where the chat started once an empty first page has loaded", () => {
    expect(markup()).toContain(opening);
  });

  it("stays away while the first page is loading or failed to load", () => {
    captured.loading = true;
    expect(markup()).not.toContain(opening);
    captured.loading = false;
    captured.error = "boom";
    expect(markup()).not.toContain(opening);
  });

  it("stays away when the chat could not start", () => {
    captured.session.start_error = "no such folder";
    try {
      expect(markup()).not.toContain(opening);
    } finally {
      delete captured.session.start_error;
    }
  });

  it("stays away under the notice that the hub has no details for the chat", () => {
    captured.noDetails = t("runtime-session.metadata_unavailable");
    try {
      const html = markup();
      expect(html).toContain("role=\"alert\"");
      expect(html).not.toContain(t("chat-chatview.chat_start"));
      expect(html).not.toContain(opening);
    } finally {
      captured.noDetails = "";
    }
  });
});

describe("closeout command evidence wiring", () => {
  // the evidence row is a chunk of its own (CloseoutEvidence, #8118); a static render draws only a loaded one
  beforeAll(() => loadPages(), 60_000);

  it("uses only the closeout's own last exchange, never an earlier run", () => {
    const was = captured.session;
    captured.session = {
      ...was, status: "done", state: "idle",
      work: { phase: "done", closeout: { status: "done", verified: "`npm test`" } },
    };
    const call = (seq: number, id: string, command: string): Item[] => [
      { id: `u${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: id, tool_name: "Bash", tool_input: { command } },
      { id: `r${seq}#0`, seq: seq + 1, role: "user", kind: "tool_result", tool_use_id: id, text: "ok", is_error: false, outcome: { status: "ok", exit_code: 0 } },
    ];
    captured.items = [
      { id: "q1#0", seq: 1, role: "user", kind: "text", text: "First" },
      ...call(2, "earlier", "npm test"),
      { id: "q2#0", seq: 4, role: "user", kind: "text", text: "Second" },
      ...call(5, "later", "npm run build"),
      { id: "a7#0", seq: 7, role: "assistant", kind: "text", text: '<closeout status="done">\nverified: `npm test`\n</closeout>' },
    ];
    try {
      expect(renderToStaticMarkup(<ChatView mid="hub" sid="c1" />)).toContain('data-closeout-command-evidence="not_found"');
      captured.items = captured.items.map((entry) => entry.tool_use_id === "later" && entry.kind === "tool_use" ? { ...entry, tool_input: { command: "npm test" } } : entry);
      expect(renderToStaticMarkup(<ChatView mid="hub" sid="c1" />)).toContain('data-closeout-command-evidence="exit"');
    } finally {
      captured.session = was;
    }
  });
});
