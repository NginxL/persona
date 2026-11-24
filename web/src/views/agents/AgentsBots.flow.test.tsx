// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentMessage, AgentSession } from "../../lib/agents";
import { t } from "../../i18n";

const state = vi.hoisted(() => ({
  sends: [] as { id: string; clientId: string; text: string }[],
  replies: [] as boolean[],
  listeners: new Set<(ids: string[]) => void>(),
}));
const session: AgentSession = {
  id: "bot_research:endpoint", source: "bot_research", sessionKey: "endpoint", accountId: "research", provider: "bot",
  title: "Research bot", kind: "bot", canSend: true, unreadCount: 0, rankTimeMs: 1,
};

function history(): AgentMessage[] {
  const rows: AgentMessage[] = [];
  state.sends.forEach((send, i) => {
    rows.push({ externalId: `user-${i + 1}`, sessionKey: "endpoint", role: "user", createTimeMs: Date.now(), position: i * 2 + 1,
      blocks: [{ type: "text", text: send.text }] });
    if (state.replies[i]) rows.push({ externalId: `assistant-${i + 1}`, sessionKey: "endpoint", role: "assistant", createTimeMs: Date.now() + 1, position: i * 2 + 2,
      blocks: [{ type: "text", text: i === 0 ? "First bot reply" : "Follow-up bot reply" }] });
  });
  // A poll and an invalidation can return the same stable row. AgentsView.mergeMessages must draw it once.
  return rows.flatMap((row) => row.externalId === "assistant-1" ? [row, row] : [row]);
}

vi.mock("../../lib/agents", async (original) => ({
  ...(await original<typeof import("../../lib/agents")>()),
  useAgentList: () => ({ sessions: [session], loaded: true, error: "", hints: [], sources: [{ source: "bot_research", enabled: true, accounts: [{}] }] }),
  fetchMessages: async () => ({ session, messages: history(), hasMore: false }),
  fetchHiddenSessions: async () => [],
  markRead: async () => {},
  sendMessage: async (id: string, clientId: string, text: string) => {
    state.sends.push({ id, clientId, text });
    state.replies.push(false); // accepted is not bot completion
    return { duplicate: false, outbox: { clientId, status: "sent" } };
  },
  onAgentsChanged: (listener: (ids: string[]) => void) => {
    state.listeners.add(listener);
    return () => state.listeners.delete(listener);
  },
}));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  useStore: <T,>(select: (s: { features: string[] }) => T): T => select({ features: [] }),
}));
vi.mock("./PagedSessionList", () => ({ PagedSessionList: () => null }));

import { AgentsView } from "./AgentsView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  state.sends = [];
  state.replies = [];
  state.listeners.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

async function typeAndSend(text: string) {
  const box = host.querySelector("textarea")!;
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const send = [...host.querySelectorAll("button")].find((b) => b.getAttribute("aria-label") === t("agents-agentsview.send"))!;
  await act(async () => send.click());
}

async function invalidate() {
  await act(async () => state.listeners.forEach((listener) => listener([session.id])));
}

describe("registered bot first assignment, reply and follow-up", () => {
  it("uses the existing history, composer and invalidation flow without synthesizing a reply", async () => {
    await act(async () => root.render(<AgentsView id={session.id} onToggleSidebar={() => {}} />));
    expect(host.querySelector("h1")?.textContent).toBe("Research bot");
    await typeAndSend("First assignment");
    expect(state.sends.map((s) => [s.id, s.text])).toEqual([[session.id, "First assignment"]]);
    expect(host.textContent).toContain("First assignment");
    expect(host.textContent).not.toContain("First bot reply");

    state.replies[0] = true;
    await invalidate();
    expect(host.textContent?.match(/First bot reply/g)).toHaveLength(1);
    expect(host.textContent?.match(/First assignment/g)).toHaveLength(1);

    await typeAndSend("Follow-up assignment");
    expect(state.sends).toHaveLength(2);
    expect(state.sends[1].text).toBe("Follow-up assignment");
    expect(state.sends[1].clientId).not.toBe(state.sends[0].clientId);
    expect(host.textContent).not.toContain("Follow-up bot reply");

    state.replies[1] = true;
    await invalidate();
    expect(host.textContent?.match(/First bot reply/g)).toHaveLength(1);
    expect(host.textContent?.match(/Follow-up bot reply/g)).toHaveLength(1);
    expect(host.textContent?.match(/Follow-up assignment/g)).toHaveLength(1);
  });
});
