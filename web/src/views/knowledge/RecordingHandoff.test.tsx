// @vitest-environment jsdom
// Handing a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting to an Agent
// (#8852)"): which Agents are offered, what the call carries, what a failed try says, and the page's row.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const world = vi.hoisted(() => ({ sessions: {} as Record<string, unknown[]>, machines: [] as unknown[] }));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(sel: (s: ReturnType<typeof real.getState>) => T) =>
      real.useStore((s) => sel({ ...s, sessions: world.sessions, machines: world.machines } as ReturnType<typeof real.getState>)),
  };
});
vi.mock("../../lib/router", async (orig) => ({ ...(await orig<typeof import("../../lib/router")>()), navigate: vi.fn() }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import { navigate } from "../../lib/router";
import type { Machine, Session } from "../../lib/types";
import { toast } from "../../ui/atoms";
import { handoffProblem, lastHandoff, meetingAgents, RecordingHandoffDialog, RecordingHandoffRow, type MeetingHandoff } from "./RecordingHandoff";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const closed = vi.fn();
const done = vi.fn();
const ID = "0f8fad5b-d9cb-469f-a165-70867728950e";

const machines = [{ id: "mac", name: "Work Mac", online: true }, { id: "off", name: "Old Mac", online: false }] as Machine[];
const agent = (id: string, extra: Partial<Session> = {}) =>
  ({ machine_id: "mac", session_id: id, title: `Agent ${id}`, cwd: "/code", status: "idle", state: "idle", updated_at: "2026-11-15T10:00:00Z", ...extra }) as Session;
const site = agent("site", { title: "Website redesign", updated_at: "2026-11-15T11:00:00Z" });
const board = agent("board", { title: "Data board" });

const render = (props: { hasTranscript?: boolean; again?: MeetingHandoff } = {}) => act(() => root.render(
  <RecordingHandoffDialog id={ID} hasTranscript={props.hasTranscript ?? true} auth={{ headers: {} }} again={props.again} onClose={closed} onDone={done} />,
));
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label)!;
const radios = () => [...document.querySelectorAll<HTMLButtonElement>("[role=radio]")];
const reply = async (value: unknown, status = 200) => act(async () => {
  requests.at(-1)!.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
});
const handoff = (extra: Partial<MeetingHandoff> = {}): MeetingHandoff =>
  ({ id: "h1", at: "2026-11-15T11:00:00Z", machine_id: "mac", session_id: "site", title: "Website redesign", state: "sent", ...extra });

beforeEach(async () => {
  await initI18n("en");
  setToken("synthetic-owner-a");
  closed.mockClear();
  done.mockClear();
  vi.mocked(toast).mockClear();
  vi.mocked(navigate).mockClear();
  world.sessions = { mac: [site, board] };
  world.machines = machines;
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the Agents a meeting is offered to (#8852)", () => {
  it("offers Hand to AI's Agents less those the hub refuses outright", () => {
    const sessions = {
      mac: [
        site,
        agent("persona", { assistant: true }),
        agent("closed", { status: "offline", state: "offline" }),
        agent("interrupted", { status: "interrupted" }),
        agent("sub", { is_subagent: true }),
        agent("bypass", { permission_mode: "bypassPermissions" }),
        agent("readonly", { source_kind: "codex_app", capabilities: { send: false } as Session["capabilities"] }),
        agent("sends", { source_kind: "codex_app", capabilities: { send: true } as Session["capabilities"] }),
      ],
      off: [agent("away", { machine_id: "off" })],
    };
    expect(meetingAgents(sessions, machines).map((s) => s.session_id)).toEqual(["site", "sends"]);
  });

  it("reads the latest hand-off from the detail, skipping what it can't read", () => {
    expect(lastHandoff(null)).toBeNull();
    expect(lastHandoff({ handoffs: "no" })).toBeNull();
    const last = handoff({ id: "h2", state: "failed", problem: "unavailable" });
    expect(lastHandoff({ handoffs: [handoff(), last, { id: "h3" }] })).toEqual(last);
  });

  it("says each problem in words, an unknown one as a failed send", () => {
    expect(handoffProblem("not_agent")).toBe("That's a Persona chat, not an Agent.");
    expect(handoffProblem("not_allowed")).toBe("This Agent can't take meetings.");
    expect(handoffProblem("unavailable")).toBe("Its computer isn't reachable, or the chat is closed.");
    expect(handoffProblem("send_failed")).toBe("The message didn't go through.");
    expect(handoffProblem("new_problem")).toBe("The message didn't go through.");
  });
});

describe("handing a meeting to an Agent on the web (#8852)", () => {
  it("picks the latest Agent and sends the minutes without the transcript unless asked", async () => {
    await render();
    expect(radios().map((r) => r.textContent)).toEqual([expect.stringContaining("Website redesign"), expect.stringContaining("Data board")]);
    expect(radios()[0].getAttribute("aria-checked")).toBe("true");
    const box = document.querySelector<HTMLInputElement>("[data-testid=handoff-transcript]")!;
    expect(box.checked).toBe(false);
    await act(async () => radios()[1].click());
    await act(async () => box.click());
    await act(async () => button("Send").click());
    expect(requests).toHaveLength(1);
    expect(requests[0].url).toContain(`/recordings/${ID}/handoff`);
    expect(requests[0].init.method).toBe("POST");
    expect(JSON.parse(String(requests[0].init.body))).toEqual({ machine_id: "mac", session_id: "board", title: "Data board", text: "", transcript: true });
    const kept = handoff({ session_id: "board", title: "Data board", transcript: true });
    await reply({ handoff: kept });
    expect(done).toHaveBeenCalledWith(kept);
    expect(toast).not.toHaveBeenCalled(); // the page's row says it, not a toast as well
    expect(closed).toHaveBeenCalledTimes(1);
  });

  it("offers no transcript box for a recording without one", async () => {
    await render({ hasTranscript: false });
    expect(document.querySelector("[data-testid=handoff-transcript]")).toBeNull();
    await act(async () => button("Send").click());
    expect(JSON.parse(String(requests[0].init.body)).transcript).toBe(false);
  });

  it("keeps a failed try open with its reason, and the page gets it too", async () => {
    await render();
    await act(async () => button("Send").click());
    const failed = handoff({ state: "failed", problem: "unavailable" });
    await reply({ handoff: failed });
    expect(done).toHaveBeenCalledWith(failed);
    expect(closed).not.toHaveBeenCalled();
    expect(document.querySelector("[role=alert]")?.textContent).toBe("Its computer isn't reachable, or the chat is closed.");
  });

  it("says why the hub took nothing: the meeting still going", async () => {
    await render();
    await act(async () => button("Send").click());
    await reply({ error: "running", code: "meeting_running" }, 409);
    expect(done).not.toHaveBeenCalled();
    expect(document.querySelector("[role=alert]")?.textContent).toBe("The meeting is still going. Hand it over once it ends.");
  });

  it("tries a failed hand-off again with its Agent and transcript choice", async () => {
    await render({ again: handoff({ session_id: "board", title: "Data board", transcript: true, state: "failed", problem: "send_failed" }) });
    expect(radios()[1].getAttribute("aria-checked")).toBe("true");
    expect(document.querySelector<HTMLInputElement>("[data-testid=handoff-transcript]")!.checked).toBe(true);
  });
});

describe("the page's hand-off row (#8852)", () => {
  it("opens the Agent's conversation after a hand-off went", async () => {
    await act(() => root.render(<RecordingHandoffRow handoff={handoff()} />));
    expect(host.textContent).toContain('Handed to "Website redesign"');
    await act(async () => button("Open").click());
    expect(navigate).toHaveBeenCalledWith({ name: "session", mid: "mac", sid: "site" });
  });

  it("says why one didn't go, with Retry only while another can be made", async () => {
    const retry = vi.fn();
    const failed = handoff({ state: "failed", problem: "not_allowed", title: "" });
    await act(() => root.render(<RecordingHandoffRow handoff={failed} onRetry={retry} />));
    expect(host.textContent).toContain('Couldn\'t hand to "Agent"');
    expect(host.textContent).toContain("This Agent can't take meetings.");
    await act(async () => button("Retry").click());
    expect(retry).toHaveBeenCalledTimes(1);
    await act(() => root.render(<RecordingHandoffRow handoff={failed} />));
    expect(button("Retry")).toBeUndefined();
  });
});
