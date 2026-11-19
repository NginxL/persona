// @vitest-environment jsdom
// The question a New Agent start's terminal tab stopped on (#10365): the card quotes it, offers its answers as buttons,
// says until when the tab waits and folds its last lines; a tap sends the answer, after which the refused messages go
// again; it hides once the tab is no longer kept, and a password prompt gets Cancel alone. The launch page shows it for
// a first message refused with shell_waiting. A planned task's failed start offers its plan task's question where the
// hub takes the answer (todo_start_answer), and posts it to the to-do's start/answer.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

const state = vi.hoisted(() => ({
  features: ["start_continue"] as string[],
  machines: [{ id: "mac", name: "Mac" }],
  sessions: {} as Record<string, unknown[]>,
  launches: {} as Record<string, unknown>,
  outbox: {} as Record<string, unknown[]>,
}));
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (s: typeof state) => unknown) => select(state),
  getState: () => state,
  upsertSession: vi.fn(),
}));
const resend = vi.hoisted(() => vi.fn());
vi.mock("../lib/start-continue", async (original) => ({ ...(await original<typeof import("../lib/start-continue")>()), resendRefused: resend }));

import { setToken } from "../lib/api";
import type { PlanTask } from "../lib/feed";
import type { Todo } from "../lib/todos";
import { clock } from "../lib/format";
import type { AgentLaunch } from "../lib/new-agent";
import { shellQuestion } from "../lib/start-continue";
import ShellQuestion, { choiceLabel, SessionShellQuestion, ShellQuestionCard } from "./ShellQuestionCard";
import type { Session } from "../lib/types";
import { StartingAgent } from "./StartingAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SID = "0e000000-0000-4000-8000-0000000000aa";
const LINE = "[oh-my-zsh] Would you like to update? [Y/n]";
const args = (o: Record<string, string> = {}) => ({
  line: LINE, screen: "Last login: today\n" + LINE, pane: "w1:p2", kept_until: new Date(Date.now() + 5 * 60_000).toISOString(),
  kind: "update", choices: "y,n,enter", default: "y", ...o,
});
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const card = () => host.querySelector<HTMLElement>("[data-testid=shell-question]");
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("[data-testid^=shell-choice-]")].map((b) => b.textContent);

beforeEach(() => {
  setToken("synthetic-shell-question");
  state.features = ["start_continue"];
  state.sessions = {};
  state.launches = {};
  resend.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("the card", () => {
  it("quotes the question, offers its answers, says until when the tab waits and folds its last lines", async () => {
    const a = args();
    act(() => root.render(<ShellQuestionCard question={shellQuestion(a)!} answer={async () => {}} />));
    expect(card()!.textContent).toContain(t("shellquestioncard.asking", { line: LINE }));
    expect(buttons()).toEqual([t("shellquestioncard.update_yes", { key: "y" }), t("shellquestioncard.update_no", { key: "n" }), t("shellquestioncard.enter_yes")]);
    expect(card()!.textContent).toContain(t("shellquestioncard.kept_until", { time: clock(a.kept_until) }));
    expect(card()!.textContent).not.toContain("Last login");
    const details = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(t("ui-errornotice.details")))!;
    act(() => details.click());
    expect(card()!.textContent).toContain("Last login");
  });

  it("sends the answer tapped, then says it is taken; a refusal stays on the card", async () => {
    const answer = vi.fn(async (choice: string) => { if (choice === "y") throw new Error("the question changed"); });
    const answered = vi.fn();
    act(() => root.render(<ShellQuestionCard question={shellQuestion(args())!} answer={answer} onAnswered={answered} />));
    act(() => host.querySelector<HTMLButtonElement>("[data-testid=shell-choice-y]")!.click());
    await flush();
    expect(answer).toHaveBeenCalledWith("y");
    expect(answered).not.toHaveBeenCalled();
    expect(host.querySelector("[role=alert]")!.textContent).toContain("the question changed");
    act(() => host.querySelector<HTMLButtonElement>("[data-testid=shell-choice-n]")!.click());
    await flush();
    expect(answered).toHaveBeenCalledWith("n");
  });

  it("hides once the tab is no longer kept", async () => {
    vi.useFakeTimers();
    act(() => root.render(<ShellQuestionCard question={shellQuestion(args({ kept_until: new Date(Date.now() + 20_000).toISOString() }))!} answer={async () => {}} />));
    expect(card()).not.toBeNull();
    act(() => { vi.advanceTimersByTime(31_000); });
    expect(card()).toBeNull();
  });

  it("offers a password prompt Cancel alone, and an ssh host key no trust", () => {
    act(() => root.render(<ShellQuestionCard question={shellQuestion(args({ line: "[sudo] password for me:", kind: "secret", secret: "true", choices: "cancel", default: "" }))!} answer={async () => {}} />));
    expect(buttons()).toEqual([t("shellquestioncard.cancel")]);
    expect(card()!.textContent).toContain(t("shellquestioncard.hint_secret"));
    expect(choiceLabel({ kind: "ssh_host_key" }, "no")).toBe(t("shellquestioncard.no"));
    expect(choiceLabel({ kind: "yes_no" }, "q")).toBe(t("shellquestioncard.key", { key: "q" }));
  });
});

describe("a session's start", () => {
  it("posts the answer to the session's start/continue and sends the refused messages again", async () => {
    const fetcher = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify({ session: { machine_id: "mac", session_id: SID, starting: true } }), { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    act(() => root.render(<SessionShellQuestion mid="mac" sid={SID} args={args()} />));
    act(() => host.querySelector<HTMLButtonElement>("[data-testid=shell-choice-n]")!.click());
    await flush();
    const post = fetcher.mock.calls.find((c) => c[1]?.method === "POST")!;
    expect(post[0]).toBe(`/api/v1/sessions/mac/${SID}/start/continue`);
    expect(JSON.parse(String(post[1]?.body))).toEqual({ choice: "n" });
    expect(resend).toHaveBeenCalledWith("mac", SID);
  });

  it("is drawn from a chat's own start error", () => {
    const session = { machine_id: "mac", session_id: SID, status: "offline", start_error: "stopped", start_error_code: "shell_waiting",
      start_error_args: args({ line: "Press Enter to continue", kind: "press_key", choices: "enter", default: "" }) } as unknown as Session;
    act(() => root.render(<ShellQuestion session={session} />));
    expect(buttons()).toEqual([t("shellquestioncard.enter")]);
  });

  it("is not offered by a hub that cannot take the answer", () => {
    state.features = ["sessions_list"];
    act(() => root.render(<SessionShellQuestion mid="mac" sid={SID} args={args()} />));
    expect(card()).toBeNull();
  });

  it("shows on the launch page of a first message refused with shell_waiting", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ providers: [] }), { status: 200 })));
    const launch: AgentLaunch = { id: "l1", machineId: "mac", cwd: "/code/app", launcher: "", text: "fix it", attachments: [], phase: "failed",
      error: "stopped on a question", uploaded: {}, startOpId: "o1", startPromptOpId: "o2", messageId: "m1",
      session: { machine_id: "mac", session_id: SID } as AgentLaunch["session"], shellWaiting: { code: "shell_waiting", ...args() } };
    await act(async () => {
      root.render(<StartingAgent launch={launch} />);
      // The launch page resolves this already-imported card through React.lazy.
      await import("./ShellQuestionCard");
    });
    await vi.waitFor(() => expect(card()).not.toBeNull());
    expect(buttons()).toHaveLength(3);
    // the row's own question wins: renewed after a retry in the kept tab
    state.sessions = { mac: [{ machine_id: "mac", session_id: SID, start_error_code: "shell_waiting",
      start_error_args: args({ line: "Ignore insecure directories and continue [y] or abort compinit [n]?", kind: "compinit", choices: "y,n", default: "" }) }] };
    act(() => root.render(<StartingAgent launch={{ ...launch }} />));
    await flush();
    expect(buttons()).toEqual([t("shellquestioncard.compinit_yes", { key: "y" }), t("shellquestioncard.compinit_no", { key: "n" })]);
    state.sessions = {};
    act(() => root.render(<StartingAgent launch={{ ...launch, shellWaiting: undefined }} />));
    await flush();
    expect(card()).toBeNull();
  });
});

describe("a planned task's failed start", () => {
  const todo = { id: "todo_1", title: "Inspect the logs" } as Todo;
  const task = (o: Partial<PlanTask> = {}): PlanTask => ({ title: "Inspect the logs", prompt: "", machine: "mac", cwd: "/code/app", todo_id: "todo_1",
    error: "stopped on a question", error_code: "shell_waiting", error_args: args(), ...o }) as PlanTask;
  beforeEach(() => { state.features = ["todo_start_answer"]; });

  it("offers the plan task's question and posts the answer to the to-do's start/answer", async () => {
    const fetcher = vi.fn(async (url: string, _init?: RequestInit) => new Response(JSON.stringify(
      String(url).endsWith("/start/answer") ? { todo: { id: "todo_1", title: "Inspect the logs", done_at: null } } : { todos: [] }), { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    act(() => root.render(<ShellQuestion todo={todo} task={task()} />));
    expect(card()!.textContent).toContain(t("shellquestioncard.asking", { line: LINE }));
    expect(buttons()).toEqual([t("shellquestioncard.update_yes", { key: "y" }), t("shellquestioncard.update_no", { key: "n" }), t("shellquestioncard.enter_yes")]);
    act(() => host.querySelector<HTMLButtonElement>("[data-testid=shell-choice-n]")!.click());
    await flush();
    const posts = fetcher.mock.calls.filter((c) => c[1]?.method === "POST");
    expect(posts.map((c) => c[0])).toEqual(["/api/v1/todos/todo_1/start/answer"]);
    expect(JSON.parse(String(posts[0][1]?.body))).toEqual({ choice: "n" });
    expect(resend).not.toHaveBeenCalled();
    expect(host.querySelector("[role=alert]")).toBeNull();
  });

  it("keeps the hub's refusal on the card", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "start_tab_gone", error: "That terminal tab isn't waiting any more." }), { status: 409 })));
    act(() => root.render(<ShellQuestion todo={todo} task={task()} />));
    act(() => host.querySelector<HTMLButtonElement>("[data-testid=shell-choice-y]")!.click());
    await flush();
    expect(host.querySelector("[role=alert]")!.textContent).toContain("That terminal tab isn't waiting any more.");
  });

  it("is absent on a hub that cannot take it, for another failure, and once the tab is no longer kept", () => {
    state.features = ["start_continue"]; // a session's own start only: the task's Retry stays its way on
    act(() => root.render(<ShellQuestion todo={todo} task={task()} />));
    expect(card()).toBeNull();
    state.features = ["todo_start_answer"];
    act(() => root.render(<ShellQuestion todo={todo} task={task({ error_code: "launcher_missing", error_args: { where: "terminal" } })} />));
    expect(card()).toBeNull();
    act(() => root.render(<ShellQuestion todo={todo} task={task({ error_args: args({ kept_until: new Date(Date.now() - 1_000).toISOString() }) })} />));
    expect(card()).toBeNull();
  });

  it("hides when the tab's time runs out", () => {
    vi.useFakeTimers();
    act(() => root.render(<ShellQuestion todo={todo} task={task({ error_args: args({ kept_until: new Date(Date.now() + 20_000).toISOString() }) })} />));
    expect(card()).not.toBeNull();
    act(() => { vi.advanceTimersByTime(31_000); });
    expect(card()).toBeNull();
  });
});
