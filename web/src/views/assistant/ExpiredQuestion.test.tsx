// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { assistantEntries } from "../../lib/assistant-entries";
import { getState, reset } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import * as atoms from "../../ui/atoms";
import { QuestionCard } from "../chat/Interactions";
import { TaskDecision } from "./TaskDecision";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = "2026-11-17T06:00:00Z";
let todo: Todo;
let root: Root;
let host: HTMLDivElement;
let posts: { path: string; body: Record<string, unknown> }[];
let status: number;
let gate: Promise<void> | undefined;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const choice = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")]
  .find((b) => b.textContent?.includes(label))!;
const mount = () => act(() => root.render(<TaskDecision todo={todo} rowKey={`task:${todo.id}`} />));

beforeEach(() => {
  posts = [];
  status = 200;
  gate = undefined;
  todo = {
    id: "todo_ask_example", title: "Which follow-up should run next?", created_at: at, updated_at: at, agents: [],
    ask: {
      outcome: "decide", verdict: "Which follow-up should run next?", question: "Which follow-up should run next?",
      asked_at: at, options: [{ label: "Docs", leads_to: "Write the guide" }, { label: "Tests", leads_to: "Check the behavior" }],
      questions: [{ question: "Which follow-up should run next?", header: "Next", multi_select: false, recommend: "Tests",
        options: [{ label: "Docs", description: "Write the guide" }, { label: "Tests", description: "Check the behavior" }] }],
    },
  };
  Object.assign(getState(), { todos: [todo], sessions: {}, interactions: [], answered: {} });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (init?.method === "POST") {
      posts.push({ path, body: JSON.parse(String(init.body)) });
      await gate;
      return new Response(JSON.stringify(status === 200 ? { ok: true } : { error: "Synthetic write failure" }),
        { status, headers: { "Content-Type": "application/json" } });
    }
    return new Response(JSON.stringify(path.includes("/todos") ? { todos: [todo] } : {}),
      { headers: { "Content-Type": "application/json" } });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("expired ask_user questions", () => {
  it("puts an origin-less question in Needs you once, not In progress", () => {
    const entries = assistantEntries({
      conversation: "main", main: "main", todos: [todo], sessions: [], interactions: [], plans: {}, feed: [],
      now: Date.parse(at) + 1000,
    });
    expect(entries.needsYou.map((item) => item.key)).toEqual([`task:${todo.id}`]);
    expect(entries.running).toEqual([]);
    expect(entries.needsYou[0].kind).toBe("task");
  });

  it("shows original choices and sends one tap through the to-do answer path, not the expired interaction", async () => {
    mount();
    expect(host.querySelector("[data-expired-question]")).not.toBeNull();
    expect(host.textContent).toContain("Write the guide");
    act(() => choice("Tests").click());
    await flush();
    expect(posts).toHaveLength(1);
    expect(posts[0].path).toContain(`/todos/${todo.id}/accept`);
    expect(posts[0].body).toEqual({ asked_at: at, answers: { "Which follow-up should run next?": "Tests" } });
    expect(posts.some((p) => p.path.includes("/interactions/"))).toBe(false);
    expect(host.textContent).toContain(t("assistant-decisionrow.answer_recorded"));
  });

  it("admits only one answer while the first write is in flight", async () => {
    let release!: () => void;
    gate = new Promise<void>((resolve) => { release = resolve; });
    mount();
    act(() => {
      choice("Tests").click();
      choice("Docs").click();
    });
    await flush();
    expect(posts).toHaveLength(1);
    release();
    await flush();
  });

  it("keeps the original choices available after a failed write", async () => {
    status = 503;
    mount();
    act(() => choice("Tests").click());
    await flush();
    expect(host.querySelector("[data-expired-question]")).not.toBeNull();
    expect(choice("Docs")).toBeDefined();
    expect(host.textContent).not.toContain(t("assistant-decisionrow.answer_recorded"));
    status = 200;
    act(() => choice("Docs").click());
    await flush();
    expect(posts).toHaveLength(2);
    expect(posts[1].body).toEqual({ asked_at: at, answers: { "Which follow-up should run next?": "Docs" } });
  });

  it("does not record a 409 answer and keeps the refreshed open form retryable", async () => {
    const feedback = vi.spyOn(atoms, "toast");
    status = 409;
    mount();
    act(() => choice("Tests").click());
    await flush();
    expect(host.textContent).not.toContain(t("assistant-decisionrow.answer_recorded"));
    expect(feedback).toHaveBeenCalledWith(t("assistant-brief.handled"));
    expect(vi.mocked(fetch).mock.calls.some(([input, init]) => String(input).includes("/todos") && !init?.method)).toBe(true);
    expect(choice("Docs").disabled).toBe(false);
    status = 200;
    act(() => choice("Docs").click());
    await flush();
    expect(posts).toHaveLength(2);
    expect(posts[1].body).toEqual({ asked_at: at, answers: { "Which follow-up should run next?": "Docs" } });
    expect(host.textContent).toContain(t("assistant-decisionrow.answer_recorded"));
  });

  it("leaves completion to the caller and unlocks the same choice when onAnswer returns false", async () => {
    const onAnswer = vi.fn<(answers: Record<string, string>) => Promise<boolean>>()
      .mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    act(() => root.render(<QuestionCard ix={{
      id: "expired-callback", machine_id: "", session_id: "", kind: "question", tool_name: "ask_user",
      questions: todo.ask!.questions, created_at: at,
    }} canApprove onAnswer={onAnswer} />));
    act(() => choice("Tests").click());
    await flush();
    expect(onAnswer).toHaveBeenCalledExactlyOnceWith({ "Which follow-up should run next?": "Tests" });
    expect(choice("Tests").getAttribute("aria-pressed")).toBe("true");
    expect(choice("Tests").disabled).toBe(false);
    expect(host.textContent).not.toContain(t("assistant-decisionrow.answer_recorded"));
    act(() => choice("Tests").click());
    await flush();
    expect(onAnswer).toHaveBeenCalledTimes(2);
    expect(onAnswer.mock.calls[1]).toEqual(onAnswer.mock.calls[0]);
    expect(posts).toEqual([]); // A callback rejection must not fall through to resolving the expired interaction.
  });

  it.each([409, 503])("retains multiple selections after HTTP %s returns false and retries the identical answer", async (failure) => {
    todo.ask!.questions![0].multi_select = true;
    status = failure;
    mount();
    act(() => {
      choice("Docs").click();
      choice("Tests").click();
    });
    const submit = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-interactions.send_answer")}"]`)!;
    act(() => submit().click());
    await flush();
    expect(posts).toHaveLength(1);
    expect(host.textContent).not.toContain(t("assistant-decisionrow.answer_recorded"));
    expect(choice("Docs").getAttribute("aria-pressed")).toBe("true");
    expect(choice("Tests").getAttribute("aria-pressed")).toBe("true");
    expect(submit().disabled).toBe(false);
    status = 200;
    act(() => submit().click());
    await flush();
    expect(posts).toHaveLength(2);
    expect(posts[1].body).toEqual(posts[0].body);
    expect(host.textContent).toContain(t("assistant-decisionrow.answer_recorded"));
  });

  it("retains pagination and multiple-selection until the complete answer is submitted", async () => {
    vi.useFakeTimers();
    todo.ask!.questions!.push({ question: "Which clients?", multi_select: true, options: [{ label: "Web" }, { label: "Phone" }] });
    mount();
    act(() => choice("Docs").click());
    act(() => vi.advanceTimersByTime(400));
    expect(posts).toEqual([]);
    act(() => {
      choice("Web").click();
      choice("Phone").click();
    });
    expect(posts).toEqual([]);
    const submit = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-interactions.send_answer")}"]`)!;
    act(() => submit.click());
    await flush();
    expect(posts[0].body).toEqual({
      asked_at: at, answers: { "Which follow-up should run next?": "Docs", "Which clients?": "Web, Phone" },
    });
  });
});
