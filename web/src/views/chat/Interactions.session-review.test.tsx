// @vitest-environment jsdom
// #1808 AC5: the source session's completed review is a manager-owned question card with a report attached. Its two
// choices use the ordinary interaction answer path; the hub, not the browser, owns forwarding the stored report.
// The additive fields below are the expected minimal Interaction extension until lib/types.ts declares them.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const hub = vi.hoisted(() => ({
  calls: [] as { path: string; body: Record<string, unknown> }[],
  fail: false,
}));
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    hub.calls.push({ path, body: init.json });
    if (hub.fail) throw new Error("source Agent refused delivery");
    return { ok: true };
  }),
}));

import { PendingCards } from "./Interactions";
import { getState, reset, upsertSession, useStore } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type SessionReviewInteraction = Interaction & {
  review_count?: number;
  review_report?: string;
  reviewer_session_id?: string;
};

const reviewQuestion = "Independent review";
const sendLabel = "Send to agent";
const dismissLabel = "Dismiss";
const report = "src/cache.ts:42 can drop the latest update. Add a regression test for concurrent writes.";
const review = {
  id: "review-card-1",
  machine_id: "m",
  session_id: "source-agent",
  kind: "question",
  tool_name: "persona_session_review",
  questions: [{
    question: reviewQuestion,
    header: "Review complete",
    multi_select: false,
    options: [
      { label: sendLabel },
      { label: dismissLabel, action: "defer" },
    ],
  }],
  review_count: 2,
  review_report: report,
  reviewer_session_id: "reviewer-1",
} as SessionReviewInteraction;

/** The same dock ChatView feeds, so a successful resolution can remove the card through the normal store path. */
function Dock() {
  const interactions = useStore((state) => state.interactions);
  const answered = useStore((state) => Object.values(state.answered));
  return <PendingCards interactions={interactions} answered={answered} narrow={false} keyboard={false} onDismissKeyboard={() => {}} />;
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const buttons = () => [...host.querySelectorAll<HTMLButtonElement>("[data-card] button")];
const button = (label: string) => buttons().find((candidate) => candidate.textContent?.trim() === label);
const click = async (label: string) => {
  const target = button(label);
  expect(target, label).toBeDefined();
  act(() => target!.click());
  await flush();
};
const noAgentMessage = () => {
  expect(hub.calls.some((call) => call.path.includes("/messages"))).toBe(false);
  expect(hub.calls.some((call) => call.path.includes("reviewer-1"))).toBe(false);
};

beforeEach(() => {
  hub.calls.length = 0;
  hub.fail = false;
  upsertSession({ machine_id: "m", session_id: "source-agent", title: "Feature work", agent: "claude", status: "idle", state: "idle" } as Session);
  Object.assign(getState(), { interactions: [review], answered: {} });
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

describe("a finished independent session review", () => {
  it("shows the findings count and full report with Send to agent and Dismiss", () => {
    act(() => root.render(<Dock />));
    expect(host.textContent).toContain(t("lib-turns.findings", { count: 2 }));
    expect(host.textContent).toContain(report);
    expect(button(sendLabel)).toBeDefined();
    expect(button(dismissLabel)).toBeDefined();
    expect(hub.calls).toEqual([]);
  });

  it("Send resolves the source card once through the normal answer path, with no browser-sent Agent message", async () => {
    act(() => root.render(<Dock />));
    await click(sendLabel);
    expect(hub.calls).toEqual([{
      path: "/interactions/review-card-1/resolve",
      body: { answers: { [reviewQuestion]: sendLabel } },
    }]);
    expect(host.querySelector("[data-card]")).toBeNull();
    expect(JSON.stringify(hub.calls[0].body)).not.toContain(report);
    noAgentMessage();
  });

  it("a failed Send leaves the report card answerable and does not resolve it", async () => {
    hub.fail = true;
    act(() => root.render(<Dock />));
    await click(sendLabel);
    expect(hub.calls).toHaveLength(1);
    expect(host.querySelector("[data-card]")).not.toBeNull();
    expect(button(sendLabel)).toBeDefined();
    expect(button(sendLabel)!.disabled).toBe(false);
    expect(button(dismissLabel)).toBeDefined();
    noAgentMessage();

    // A later explicit retry is a new choice, not a duplicate delivery from the first click.
    await click(sendLabel);
    expect(hub.calls).toHaveLength(2);
    expect(host.querySelector("[data-card]")).not.toBeNull();
    noAgentMessage();
  });

  it("Dismiss resolves the card through its defer answer without sending the report to either Agent", async () => {
    act(() => root.render(<Dock />));
    await click(dismissLabel);
    expect(hub.calls).toEqual([{
      path: "/interactions/review-card-1/resolve",
      body: { answers: { [reviewQuestion]: dismissLabel } },
    }]);
    expect(host.querySelector("[data-card]")).toBeNull();
    expect(JSON.stringify(hub.calls)).not.toContain(report);
    noAgentMessage();
  });
});
