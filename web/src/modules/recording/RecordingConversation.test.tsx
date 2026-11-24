// @vitest-environment jsdom
// The meeting conversation on a recording's detail (#3862): only on a hub that lists meeting_conversation, the private
// switch, a room meeting's one thread (read-only while it runs), the meeting_live event's newer parts, and the follow-up's
// first message, which any recording with text or minutes offers once it has ended (phase 6).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { recording } from "./module";
import { setToken } from "../../lib/api";
import type { KBDetailResponse, KBSegment, RecordingAsk, RecordingLive, RecordingMinutes } from "../../lib/kb";
import { setRosterDefault } from "../../lib/manager-choice";
import { getState } from "../../lib/store";
import type { Item } from "../../lib/types";
import { EntryDetail } from "../../views/knowledge/KBDetail";
import "./test-register";

const hub = vi.hoisted(() => ({ listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>() }));
vi.mock("../../lib/store", async (original) => ({
  ...await original<typeof import("../../lib/store")>(),
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => { hub.listeners.get(type)?.delete(l); };
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const room = "dddddddd-0000-4000-8000-000000000001";
const follow = "eeeeeeee-0000-4000-8000-000000000002";
const askID = "11111111-1111-4111-8111-111111111111";
const detailPath = `/api/v1/kb/recordings/${id}`;
const livePath = `/api/v1/recordings/${id}/live`;
const itemsPath = (sid: string) => `/api/v1/sessions/server/${sid}/items?turns=400&fold_work=1&lazy_images=1`;
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const initial = { features: getState().features, sessions: getState().sessions, assistant: getState().assistant };
const recent = () => new Date(Date.now() - 60_000).toISOString();
const ready = { state: "ready", machine_id: "server", session_id: room };
const ended = { state: "ended", machine_id: "server", session_id: room };

const fixture = ({ segments = [{ start: 0, end: 4, text: "Let's keep the cap for this release." }], minutes, ...meta }: Record<string, unknown> = {}): KBDetailResponse => ({ detail: {
  kind: "recordings", id, path: `recordings/${id}`,
  meta: { title: "Design review", started_at: new Date(2026, 10, 10, 14).toISOString(), complete: false, kind: "meeting", revision: 2,
    minutes_state: "pending", attendance: "participant", updated: recent(), room: ready, ...meta },
  segments: segments as KBSegment[] | undefined,
  ...(minutes ? { minutes: minutes as RecordingMinutes } : {}),
} });
const ask = (over: Partial<RecordingAsk> = {}): RecordingAsk => ({
  id: askID, question: "What did we decide about the export cap last time?", question_source: "typed", at: 75, state: "answered",
  answer: "You kept the 4 MB cap.", lang: "en", speak: true, sources: [], asked_at: "2026-11-10T06:01:15Z", via: "room", ...over,
});
const live = (over: Partial<RecordingLive> = {}): RecordingLive => ({
  next: 3, fed: 3, attendance: "participant", asks: [ask()], room: ready, revs: { asks: 1, room: 2 }, ...over,
});
let seq = 0;
const item = (over: Partial<Item>): Item => ({ id: `i${++seq}#0`, seq, role: "user", kind: "text", ts: new Date(2026, 10, 10, 14, 5).toISOString(), ...over });
const source = '<persona-context kind="message-source" title="">\n{"kind":"meeting","title":"Design review"}\n</persona-context>\n\n';
const roomItems = (): Item[] => [
  item({ client_msg_id: `meeting-${id}`, text: `${source}The meeting is starting. Write nothing now.\n\n<persona-context kind="context" title="Design review" url="">\nTitle: Design review\nStarted: 2026-11-10 14:00 CST\nLanguage: en\nRole: participant: answers may be said aloud\n</persona-context>` }),
  item({ source: { kind: "meeting" }, text: `${source}New lines from "Design review".\n\n<persona-context kind="context" title="Design review · live" url="">\n[0:05] Ada: Today we look at the export flow.\n[0:12] Bo: The cap is 4 MB.\n</persona-context>` }),
  item({ client_msg_id: `ask-${askID}-1`, text: "What did we decide about the export cap last time?" }),
  item({ role: "assistant", text: "You kept the 4 MB cap." }),
];
const followItems = (): Item[] => [
  item({ client_msg_id: "followup-x", text: '<persona-context kind="context" title="Meeting: Design review" url="">\nThis is the material of a meeting I recorded.\n</persona-context>\n\nDraft the email to the vendor.' }),
  item({ role: "assistant", text: "Here is a **draft**." }),
];
const render = () => act(() => root.render(<EntryDetail kind="recordings" id={id} reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
const reply = async (request: Request | undefined, body: unknown, status = 200) => act(async () => {
  expect(request, "the request was made").toBeDefined();
  request!.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
});
const pending = (path: string) => requests.filter((r) => r.url === path);
const load = async (detail = fixture()) => { render(); await reply(pending(detailPath).at(-1), detail); };
const section = () => host.querySelector<HTMLElement>("[data-meeting-conversation]");
const privacy = () => host.querySelector<HTMLElement>("[data-meeting-private]");
const kinds = () => [...section()!.querySelectorAll("li[data-thread]")].map((li) => li.getAttribute("data-thread"));
const fire = (msg: Record<string, unknown>) => act(() => { hub.listeners.get(String(msg.type))?.forEach((l) => l(msg)); });
const factRow = (label: string) => [...host.querySelectorAll("div")].find((row) => row.firstElementChild?.textContent === label && row.children.length === 2);
const typeInto = (el: HTMLTextAreaElement, value: string) => act(() => {
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const submit = async () => act(async () => { section()!.querySelector("form")!.requestSubmit(); });
const fresh = () => { act(() => root.render(null)); host.replaceChildren(); requests = []; };

beforeEach(async () => {
  // this module's strings come with its code, not the client catalog (module.ts)
  await initI18n("en", recording.locales!);
  localStorage.clear();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  history.replaceState(null, "", `/kb/recordings/${id}`);
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  Object.assign(getState(), { features: ["meeting_attendance", "meeting_conversation"] });
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  Object.assign(getState(), initial);
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("a recorded meeting's conversation", () => {
  it("leaves the page as before on a hub without meeting_conversation", async () => {
    Object.assign(getState(), { features: ["meeting_attendance"] });
    await load(fixture({ room: undefined, private: false, attending_session: { machine_id: "server", session_id: "conv-2" } }));
    expect(section()).toBeNull();
    expect(privacy()).toBeNull();
    expect(factRow("Attending session")).toBeDefined();
    // Only "Questions in the meeting" reads the live state, whole.
    expect(requests.filter((r) => r.url.startsWith(livePath)).map((r) => r.url)).toEqual([livePath]);
    await reply(pending(livePath)[0], live({ room: undefined, revs: undefined }));
    expect(host.querySelector("[data-recording-asks]")).not.toBeNull();
    expect(requests.some((r) => r.url.includes("/private") || r.url.includes("/sessions/"))).toBe(false);
  });

  it("reads nothing of a room or a follow-up detail.meta names on a hub without meeting_conversation", async () => {
    Object.assign(getState(), { features: ["meeting_attendance"] });
    await load(fixture({ followup: { machine_id: "server", session_id: follow }, private: false }));
    expect(section()).toBeNull();
    expect(privacy()).toBeNull();
    await reply(pending(livePath)[0], live({ room: undefined, revs: undefined }));
    expect(host.querySelector("[data-recording-asks]")).not.toBeNull();
    expect(requests.some((r) => r.url.includes("/sessions/") || r.url.startsWith(`${livePath}?`))).toBe(false);
  });

  it("shows a running room meeting's thread, read-only, with its state and no attending session", async () => {
    await load();
    expect(host.querySelector("[data-recording-asks]")).toBeNull();
    expect(factRow("Attending session")).toBeUndefined();
    expect(factRow("Persona")?.lastElementChild?.textContent).toBe("Participant");
    await reply(pending(livePath)[0], live());
    expect(new Headers(pending(livePath)[0].init.headers).get("X-Persona-Manager")).toBe("assistant");
    await reply(pending(itemsPath(room))[0], { items: roomItems(), has_more: false });
    const s = section()!;
    expect(s.querySelector("h2")!.textContent).toBe("Conversation");
    expect(s.querySelector("[role=status]")!.textContent).toBe("Persona is in the meeting as a participant · answers can be said aloud");
    expect(s.querySelector('a[title="Open the meeting’s conversation"]')!.getAttribute("href")).toBe(`/persona/${room}`);
    // No "Persona joined the meeting" (#9411): the web can't know whether Persona was ever in it.
    expect(kinds()).toEqual(["lines", "question", "answer"]);
    expect(s.textContent).not.toContain("joined the meeting");
    expect(s.textContent).toContain("2 lines from the meeting");
    expect(s.textContent).toContain("You kept the 4 MB cap.");
    expect(s.querySelector('[title="Typed on the phone"]')).not.toBeNull();
    // The batch opens onto its lines.
    const fold = s.querySelector<HTMLButtonElement>('li[data-thread="lines"] button')!;
    await act(async () => fold.click());
    expect(fold.getAttribute("aria-expanded")).toBe("true");
    expect(s.textContent).toContain("Bo: The cap is 4 MB.");
    expect(s.querySelector("textarea")).toBeNull();
    expect(s.textContent).toContain("Questions are asked on the phone while the meeting runs.");
  });

  it("says a notes room (#8851) keeps notes without Persona in the meeting, not that Persona stopped listening", async () => {
    await load(fixture({ attendance: "notes" }));
    expect(factRow("Persona")).toBeUndefined();
    await reply(pending(livePath)[0], live({ attendance: "notes", asks: [] }));
    await reply(pending(itemsPath(room))[0], { items: roomItems(), has_more: false });
    expect(section()!.querySelector("[role=status]")!.textContent).toBe("Persona isn’t in this meeting: it keeps the topics and highlights and answers nothing.");
  });

  it("reads only the newer parts a meeting_live event names, and the thread again when the asks moved", async () => {
    await load();
    await reply(pending(livePath)[0], live());
    await reply(pending(itemsPath(room))[0], { items: roomItems() });
    fire({ type: "meeting_live", recording_id: id, changed: ["room", "asks"], revs: { room: 2, asks: 1 } });
    fire({ type: "meeting_live", recording_id: "bbbbbbbb-0000-4000-8000-000000000000", changed: ["asks"], revs: { asks: 9 } });
    fire({ type: "meeting_live", recording_id: id, manager: "m-3fa21c", changed: ["asks"], revs: { asks: 9 } });
    expect(requests.filter((r) => r.url.startsWith(`${livePath}?`))).toHaveLength(0);
    fire({ type: "meeting_live", recording_id: id.toUpperCase(), changed: ["asks", "room", "lines"], revs: { asks: 2, room: 2, lines: 40 } });
    const parts = requests.filter((r) => r.url.startsWith(`${livePath}?`));
    expect(parts.map((r) => r.url)).toEqual([`${livePath}?parts=asks`]);
    await reply(parts[0], { asks: [ask(), ask({ id: "22222222-2222-4222-8222-222222222222", question: "", question_source: "heard", state: "pending", answer: "" })], revs: { asks: 2 } });
    expect(pending(itemsPath(room))).toHaveLength(2);
    // The room turns ended: its state line says so.
    fire({ type: "meeting_live", recording_id: id, changed: ["room"], revs: { room: 3 } });
    await reply(pending(`${livePath}?parts=room`)[0], { room: ended, revs: { room: 3 } });
    expect(section()!.querySelector("[role=status]")!.textContent).toBe("The meeting is over.");
  });

  it("shows where each question asked in the room stands, and those its thread does not hold", async () => {
    const lost = "33333333-3333-4333-8333-333333333333";
    const waiting = "44444444-4444-4444-8444-444444444444";
    const spokenID = "55555555-5555-4555-8555-555555555555";
    await load();
    await reply(pending(livePath)[0], live({ asks: [
      ask({ state: "failed", answer: "", error: "no_answer" }),
      ask({ id: lost, question: "", question_source: "heard", at: 30, state: "failed", answer: "", error: "nothing_heard" }),
      ask({ id: waiting, question: "And the deadline?", state: "pending", answer: "" }),
      ask({ id: spokenID, question: "Who presents next?", question_source: "spoken", via: "material", state: "failed", answer: "", error: "room_unavailable" }),
    ] }));
    // Until the room's items are in, only the asks answered from the material show beside it.
    expect(section()!.querySelectorAll("li[data-ask]")).toHaveLength(1);
    await reply(pending(itemsPath(room))[0], { items: roomItems().slice(0, 3) });
    const s = section()!;
    // The question in the thread says why it has no answer.
    const asked = s.querySelector('li[data-thread="question"]')!;
    expect(asked.textContent).toContain("Persona didn’t answer this one.");
    // Those that never went into the room, failed or still waiting, show among the asks; the spoken one says so.
    expect([...s.querySelectorAll("li[data-ask]")].map((li) => li.getAttribute("data-ask"))).toEqual([lost, waiting, spokenID]);
    const spokenRow = s.querySelector(`li[data-ask="${spokenID}"]`)!;
    expect(spokenRow.querySelector('[title="Said to Persona on the phone"]')).not.toBeNull();
    expect(spokenRow.textContent).toContain("Persona couldn’t reach the meeting’s conversation.");
    expect(s.querySelector(`li[data-ask="${waiting}"]`)!.textContent).toContain("Answering…");
    // Once its message is in the room, the waiting one is there, being answered.
    fire({ type: "meeting_live", recording_id: id, changed: ["asks"], revs: { asks: 2 } });
    await reply(pending(`${livePath}?parts=asks`)[0], { asks: [ask(), ask({ id: waiting, question: "And the deadline?", state: "pending", answer: "" })], revs: { asks: 2 } });
    await reply(pending(itemsPath(room))[1], { items: [...roomItems(), item({ client_msg_id: `ask-${waiting}-1`, text: "And the deadline?" })] });
    expect(section()!.querySelectorAll("li[data-ask]")).toHaveLength(0);
    expect(section()!.querySelector('li[data-thread="question"]:last-of-type')!.textContent).toContain("Answering…");
  });

  it("leaves a #2981 meeting's polling to its questions", async () => {
    vi.useFakeTimers();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    await load(fixture({ room: undefined, attending_session: { machine_id: "server", session_id: "conv-2" } }));
    // Both read once: the questions (whole) and the meeting's private mark and follow-up.
    expect(pending(livePath)).toHaveLength(2);
    for (const r of pending(livePath)) await reply(r, live({ room: undefined }));
    act(() => vi.advanceTimersByTime(5000));
    expect(pending(livePath)).toHaveLength(3);
  });

  it("polls while the meeting runs and not once the recording is complete", async () => {
    vi.useFakeTimers();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    await load();
    await reply(pending(livePath)[0], live());
    act(() => vi.advanceTimersByTime(5000));
    expect(pending(livePath)).toHaveLength(2);
    await reply(pending(livePath)[1], live());
    fresh();
    await load(fixture({ complete: true, ended_at: recent(), room: ended }));
    await reply(pending(livePath)[0], live({ room: ended }));
    act(() => vi.advanceTimersByTime(20000));
    expect(pending(livePath)).toHaveLength(1);
  });

  it("marks a meeting private or not, counting no mark as private", async () => {
    await load(fixture({ room: undefined, attendance: undefined, complete: true }));
    await reply(pending(livePath).at(-1), { attendance: "off", next: 0, fed: 0, asks: [], revs: { asks: 0 } });
    expect(section()).toBeNull();
    const box = privacy()!;
    const toggle = box.querySelector<HTMLButtonElement>("[role=switch]")!;
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    expect(box.textContent).toContain("Not chosen yet, so it counts as private");
    await act(async () => toggle.click());
    const path = `/api/v1/recordings/${id}/private`;
    expect(pending(path)[0].init.method).toBe("PUT");
    expect(JSON.parse(String(pending(path)[0].init.body))).toEqual({ private: false });
    expect(toggle.getAttribute("aria-checked")).toBe("false");
    await reply(pending(path)[0], { private: false });
    expect(box.textContent).toContain("Persona may use its minutes to answer in later meetings.");
    await act(async () => toggle.click());
    expect(JSON.parse(String(pending(path)[1].init.body))).toEqual({ private: true });
    await reply(pending(path)[1], { error: "read only", code: "kb_read_only" }, 403);
    expect(toggle.getAttribute("aria-checked")).toBe("false");
    expect(box.querySelector("[role=alert]")!.textContent).toContain("Couldn’t change it");
    // Not a meeting: no mark to set; its follow-up is read, and nothing to talk about while it still records.
    fresh();
    await load(fixture({ room: undefined, attendance: undefined, kind: "note" }));
    expect(privacy()).toBeNull();
    expect(pending(livePath).map((r) => r.url)).toEqual([livePath]);
    await reply(pending(livePath)[0], { attendance: "off", next: 0, fed: 0, asks: [], revs: { asks: 0 } });
    expect(section()).toBeNull();
    // Nothing of it to talk about: nothing read.
    fresh();
    await load(fixture({ room: undefined, attendance: undefined, kind: "note", complete: true, ended_at: recent(), segments: [] }));
    expect(section()).toBeNull();
    expect(pending(livePath)).toHaveLength(0);
  });

  it("starts the follow-up with the owner's first words and opens it, retrying a lost answer under the same id", async () => {
    await load(fixture({ complete: true, ended_at: recent(), room: ended }));
    await reply(pending(livePath)[0], live({ room: ended }));
    await reply(pending(itemsPath(room))[0], { items: roomItems() });
    typeInto(section()!.querySelector("textarea")!, "Draft the email to the vendor.");
    await submit();
    const path = `/api/v1/recordings/${id}/followup`;
    const first = JSON.parse(String(pending(path)[0].init.body));
    expect(first).toEqual({ id: expect.stringMatching(/^[0-9a-f-]{36}$/), text: "Draft the email to the vendor." });
    await reply(pending(path)[0], { error: "bad gateway" }, 502);
    expect(section()!.querySelector("[role=alert]")!.textContent).toContain("Couldn’t send");
    await submit();
    expect(JSON.parse(String(pending(path)[1].init.body)).id).toBe(first.id);
    await reply(pending(path)[1], { error: "running", code: "meeting_running" }, 409);
    expect(section()!.querySelector("[role=alert]")!.textContent).toBe("The meeting is still running. Once it ends, you can talk about it here.");
    await submit();
    const third = JSON.parse(String(pending(path)[2].init.body));
    expect(third.id).not.toBe(first.id);
    // The hub's receipt still waiting for the first call: not definite, so the next try is the same call.
    await reply(pending(path)[2], { state: "pending" }, 202);
    expect(section()!.querySelector("[role=alert]")!.textContent).toBe("Not sure this was sent yet. Send again to check: it won’t be sent twice.");
    expect(location.pathname).toBe(`/kb/recordings/${id}`);
    await submit();
    expect(JSON.parse(String(pending(path)[3].init.body)).id).toBe(third.id);
    await reply(pending(path)[3], { followup: { machine_id: "server", session_id: follow }, created: true, client_msg_id: `followup-${third.id}` });
    expect(location.pathname).toBe(`/persona/${follow}`);
  });

  it("shows the follow-up after the divider, its seed folded, and continues there", async () => {
    const followup = { machine_id: "server", session_id: follow };
    await load(fixture({ complete: true, ended_at: recent(), room: ended, followup }));
    await reply(pending(livePath)[0], live({ room: ended, followup, revs: { asks: 1, room: 3, followup: 1 } }));
    await reply(pending(itemsPath(room))[0], { items: roomItems() });
    await reply(pending(itemsPath(follow))[0], { items: followItems() });
    const s = section()!;
    expect(s.querySelector("[role=separator]")!.textContent).toContain("After the meeting");
    expect(kinds()).toEqual(["lines", "question", "answer", "material", "question", "answer"]);
    const seed = s.querySelector<HTMLButtonElement>('li[data-thread="material"] button')!;
    expect(seed.textContent).toBe("The meeting’s material");
    expect(seed.getAttribute("aria-expanded")).toBe("false");
    expect(s.querySelector("li[data-thread]:last-child strong")?.textContent).toBe("draft");
    expect([...s.querySelectorAll("a")].find((a) => a.textContent === "Continue in the conversation")!.getAttribute("href")).toBe(`/persona/${follow}`);
    expect(s.querySelector("textarea")).toBeNull();
  });

  it("offers Talk about this meeting on any recording once it has ended, and a follow-up made elsewhere joins the thread", async () => {
    const note = { room: undefined, attendance: undefined, kind: "note", complete: true, ended_at: recent() };
    await load(fixture(note));
    await reply(pending(livePath)[0], { attendance: "off", next: 0, fed: 0, asks: [], revs: { asks: 0 } });
    expect(privacy()).toBeNull();
    const s = section()!;
    expect(s.querySelector("h2")!.textContent).toBe("Conversation");
    expect(s.querySelector("[role=status]")).toBeNull();
    expect(s.querySelector("[role=separator]")).toBeNull();
    expect(s.querySelector("label")!.textContent).toBe("Talk about this meeting");
    expect(requests.some((r) => r.url.includes("/sessions/"))).toBe(false);
    // The follow-up made on the phone meanwhile: the event names it, and the thread shows it instead of the composer.
    const followup = { machine_id: "server", session_id: follow };
    fire({ type: "meeting_live", recording_id: id, changed: ["followup"], revs: { followup: 1 } });
    await reply(pending(`${livePath}?parts=followup`)[0], { followup, revs: { followup: 1 } });
    await reply(pending(itemsPath(follow))[0], { items: followItems() });
    expect(kinds()).toEqual(["material", "question", "answer"]);
    expect(section()!.textContent).toContain("After the meeting");
    expect(section()!.querySelector("textarea")).toBeNull();
    expect([...section()!.querySelectorAll("a")].find((a) => a.textContent === "Continue in the conversation")!.getAttribute("href")).toBe(`/persona/${follow}`);

    // Its first words start the follow-up through POST …/followup and open it.
    fresh();
    await load(fixture({ ...note, segments: [], chars: 0, minutes: { version: 1, revision: 1, generated_at: recent(), title: "Design review", when: "", markdown: "# Design review" } }));
    await reply(pending(livePath)[0], { attendance: "off", next: 0, fed: 0, asks: [], revs: { asks: 0 } });
    typeInto(section()!.querySelector("textarea")!, "What did we decide?");
    await submit();
    const path = `/api/v1/recordings/${id}/followup`;
    const body = JSON.parse(String(pending(path)[0].init.body));
    expect(body).toEqual({ id: expect.stringMatching(/^[0-9a-f-]{36}$/), text: "What did we decide?" });
    await reply(pending(path)[0], { followup, created: true, client_msg_id: `followup-${body.id}` });
    expect(location.pathname).toBe(`/persona/${follow}`);
  });

  it("offers the talk only once …/live has answered for the recording, and none where it is gone", async () => {
    const note = { room: undefined, attendance: undefined, kind: "note", complete: true, ended_at: recent() };
    await load(fixture(note));
    // Not before that read: hello's meeting_conversation is the default manager's, and a recording of another, older
    // manager's knowledge base has no …/followup.
    expect(pending(livePath)).toHaveLength(1);
    expect(section()).toBeNull();
    await reply(pending(livePath)[0], { error: "404 page not found" }, 404);
    expect(section()).toBeNull();
    expect(requests.some((r) => r.url.endsWith("/followup"))).toBe(false);
    fresh();
    await load(fixture(note));
    expect(section()).toBeNull();
    await reply(pending(livePath)[0], { attendance: "off", next: 0, fed: 0, asks: [], revs: { asks: 0 } });
    expect(section()!.querySelector("textarea")).not.toBeNull();
  });

  it("offers no talk about an all-day recording, whose page is its day recap, and shows a follow-up made for one", async () => {
    const day = { room: undefined, attendance: undefined, minutes_state: undefined, kind: "day", day: "2026-11-10", part: 1, complete: true,
      ended_at: recent() };
    await load(fixture(day));
    expect(pending(livePath)).toHaveLength(0);
    expect(section()).toBeNull();
    // One made before, or by another app: its thread, as any recording's.
    fresh();
    const followup = { machine_id: "server", session_id: follow };
    await load(fixture({ ...day, followup }));
    await reply(pending(livePath)[0], { attendance: "off", next: 0, fed: 0, asks: [], followup, revs: { asks: 0, followup: 1 } });
    await reply(pending(itemsPath(follow))[0], { items: followItems() });
    expect(kinds()).toEqual(["material", "question", "answer"]);
    expect(section()!.querySelector("textarea")).toBeNull();
  });

  it("talks about a #2981 meeting too, its questions staying under Questions in the meeting", async () => {
    const followup = { machine_id: "server", session_id: follow };
    await load(fixture({ room: undefined, attending_session: { machine_id: "server", session_id: "conv-2" }, complete: true, ended_at: recent() }));
    for (const r of pending(livePath)) await reply(r, live({ room: undefined }));
    expect(section()!.querySelector("textarea")).not.toBeNull();
    expect(section()!.querySelectorAll("li[data-ask]")).toHaveLength(0);
    expect(host.querySelectorAll("[data-recording-asks] li[data-ask]")).toHaveLength(1);
    // With its follow-up: the thread, and the questions still once, where they were.
    fresh();
    await load(fixture({ room: undefined, attending_session: { machine_id: "server", session_id: "conv-2" }, complete: true, ended_at: recent(), followup }));
    for (const r of pending(livePath)) await reply(r, live({ room: undefined, followup, revs: { asks: 1, followup: 1 } }));
    await reply(pending(itemsPath(follow))[0], { items: followItems() });
    expect(kinds()).toEqual(["material", "question", "answer"]);
    expect(section()!.querySelectorAll("li[data-ask]")).toHaveLength(0);
    expect(host.querySelectorAll("[data-recording-asks] li[data-ask]")).toHaveLength(1);
  });

  it("shows the asks of a room that never came up, and starts at the follow-up once the room is deleted", async () => {
    const down = { state: "unavailable", problem: "machine_offline" };
    await load(fixture({ room: down }));
    await reply(pending(livePath)[0], live({ room: down, asks: [ask({ via: "material" })] }));
    expect(section()!.querySelector("[role=status]")!.textContent).toContain("The assistant’s computer is offline.");
    expect(section()!.querySelectorAll("li[data-ask]")).toHaveLength(1);
    expect(requests.some((r) => r.url.includes("/sessions/"))).toBe(false);

    fresh();
    const followup = { machine_id: "server", session_id: follow };
    await load(fixture({ complete: true, ended_at: recent(), trimmed_at: recent(), room: { state: "deleted" }, followup }));
    await reply(pending(livePath)[0], live({ room: { state: "deleted" }, asks: [], followup }));
    await reply(pending(itemsPath(follow))[0], { items: followItems() });
    const s = section()!;
    expect(s.querySelector("[role=status]")!.textContent).toBe("The meeting’s conversation was deleted with its transcript.");
    expect(s.querySelector("[role=separator]")).toBeNull();
    expect(s.textContent).toContain("After the meeting");
    expect(kinds()).toEqual(["material", "question", "answer"]);
  });
});

describe("a recorded meeting's live view (phase 2)", () => {
  const withLive = () => Object.assign(getState(), { features: ["meeting_attendance", "meeting_conversation", "meeting_live_view"] });
  const line = (n: number, text = `Line ${n}.`) => ({ n, start: 10 * n, end: 10 * n + 4, text });
  const topics = [
    { title: "Export cap", state: "done", at: 0 },
    { title: "Pricing", state: "current", at: 300, note: "Two options on the table" },
    { title: "Hiring", state: "open" },
  ];
  const topicsBox = () => host.querySelector<HTMLElement>("[data-live-topics]");
  const linesBox = () => host.querySelector<HTMLElement>("[data-live-lines]");
  const shownLines = () => [...host.querySelectorAll("[data-live-line]")].map((li) => li.lastElementChild!.textContent);
  const reads = () => requests.filter((r) => r.url.startsWith(livePath)).map((r) => r.url.slice(livePath.length));

  it("shows nothing of it on a hub without meeting_live_view, and reads no lines", async () => {
    await load();
    await reply(pending(livePath)[0], live({ revs: { asks: 1, room: 2, lines: 3, topics: 1 }, topics }));
    fire({ type: "meeting_live", recording_id: id, changed: ["lines", "topics"], revs: { lines: 5, topics: 2 } });
    expect(topicsBox()).toBeNull();
    expect(linesBox()).toBeNull();
    expect(reads()).toEqual([""]);
  });

  it("shows a running room meeting's topics in order and its newest lines, and reads only what an event says is newer", async () => {
    withLive();
    await load();
    expect(reads()).toEqual([""]);
    await reply(pending(livePath)[0], live({ revs: { asks: 1, room: 2, lines: 3, topics: 1 }, topics, topics_at: "2026-11-10T06:05:00Z" }));
    const chips = [...topicsBox()!.querySelectorAll("li")];
    expect(topicsBox()!.querySelector("h2")!.textContent).toBe("Topics");
    expect(chips.map((li) => li.getAttribute("data-topic"))).toEqual(["done", "current", "open"]);
    // Each state in words; the other topics' start for screen readers, the current one's start and note shown under the chips.
    expect(chips.map((li) => li.textContent)).toEqual([expect.stringMatching(/^DoneExport cap, From \d\d:\d\d:\d\d$/), "NowPricing", "Still to comeHiring"]);
    expect(chips[0].lastElementChild!.className).toContain("sr-only");
    expect(chips[1].getAttribute("title")).toMatch(/^Now · Pricing · From \d\d:\d\d:\d\d · Two options on the table$/);
    expect(topicsBox()!.querySelector("[data-live-topic-now]")!.textContent).toMatch(/^From \d\d:\d\d:\d\d · Two options on the table$/);
    // The lines once the live log's length is known, from the start of this short one.
    expect(reads()).toEqual(["", "?parts=lines&from=0"]);
    await reply(pending(`${livePath}?parts=lines&from=0`)[0], { lines: [line(0), line(1), line(2)], revs: { lines: 3 } });
    expect(linesBox()!.querySelector("h2")!.textContent).toBe("Live");
    expect(shownLines()).toEqual(["Line 0.", "Line 1.", "Line 2."]);
    // The log is the scroller the keyboard reaches, named by its heading, with a plain list inside; nothing was cut.
    const log = linesBox()!.querySelector<HTMLElement>("[role=log]")!;
    expect([log.tagName, log.tabIndex, log.getAttribute("aria-labelledby")]).toEqual(["DIV", 0, linesBox()!.querySelector("h2 span")!.id]);
    expect(log.className).toContain("focus-visible:outline-accent");
    expect(log.querySelector("ol")!.hasAttribute("role")).toBe(false);
    expect(linesBox()!.querySelector("[data-live-line]")!.parentElement!.tagName).toBe("OL");
    expect(linesBox()!.querySelector("[data-live-earlier]")).toBeNull();
    // Nothing newer: nothing read. Newer lines and topics: from this page's own next position.
    fire({ type: "meeting_live", recording_id: id, changed: ["lines", "topics"], revs: { lines: 3, topics: 1 } });
    expect(reads()).toHaveLength(2);
    fire({ type: "meeting_live", recording_id: id, changed: ["lines", "topics"], revs: { lines: 5, topics: 2 } });
    expect(reads().at(-1)).toBe("?parts=lines,topics&from=3");
    await reply(pending(`${livePath}?parts=lines,topics&from=3`)[0], {
      lines: [line(3), line(4, "Ada: we keep the vendor.")], topics: [{ ...topics[0] }, { ...topics[1], state: "done" }, { ...topics[2], state: "current" }], revs: { lines: 5, topics: 2 },
    });
    expect(shownLines()).toEqual(["Line 0.", "Line 1.", "Line 2.", "Line 3.", "Ada: we keep the vendor."]);
    expect([...topicsBox()!.querySelectorAll("li")].map((li) => li.getAttribute("data-topic"))).toEqual(["done", "done", "current"]);
    fire({ type: "meeting_live", recording_id: id, changed: ["lines"], revs: { lines: 5 } });
    expect(reads()).toHaveLength(3);
  });

  it("starts a long meeting near its newest lines and reads on while there are more", async () => {
    withLive();
    await load();
    await reply(pending(livePath)[0], live({ revs: { asks: 1, room: 2, lines: 1000, topics: 0 }, topics: [] }));
    expect(topicsBox()).toBeNull(); // no Topics before the room set any
    expect(linesBox()!.querySelector("[role=log]")).toBeNull();
    expect(reads().at(-1)).toBe("?parts=lines&from=800");
    await reply(pending(`${livePath}?parts=lines&from=800`)[0], { lines: [800, 801, 802].map((n) => line(n)), more: true, revs: { lines: 1000 } });
    expect(reads().at(-1)).toBe("?parts=lines&from=803");
    await reply(pending(`${livePath}?parts=lines&from=803`)[0], { lines: Array.from({ length: 197 }, (_, i) => line(803 + i)), revs: { lines: 1000 } });
    expect(reads()).toHaveLength(3);
    expect(shownLines()).toHaveLength(200);
    // The first read skipped the 800 before: the note says the transcript has them.
    expect(linesBox()!.querySelector("[data-live-earlier]")!.textContent).toBe("Earlier lines are in the transcript.");
  });

  it("says earlier lines are in the transcript only once some were skipped or let go, not for a blank first position", async () => {
    withLive();
    await load();
    await reply(pending(livePath)[0], live({ revs: { asks: 1, room: 2, lines: 150, topics: 0 }, topics: [] }));
    await reply(pending(`${livePath}?parts=lines&from=0`)[0], { lines: [{ n: 0, start: 0, end: 1, text: "" }, ...Array.from({ length: 149 }, (_, i) => line(i + 1))], revs: { lines: 150 } });
    expect(shownLines()[0]).toBe("Line 1.");
    expect(linesBox()!.querySelector("[data-live-earlier]")).toBeNull();
    // More than LIVE_KEEP read: the oldest are let go.
    fire({ type: "meeting_live", recording_id: id, changed: ["lines"], revs: { lines: 250 } });
    await reply(pending(`${livePath}?parts=lines&from=150`)[0], { lines: Array.from({ length: 100 }, (_, i) => line(150 + i)), revs: { lines: 250 } });
    expect(shownLines()).toHaveLength(200);
    expect(shownLines()[0]).toBe("Line 50.");
    expect(linesBox()!.querySelector("[data-live-earlier]")).not.toBeNull();
  });

  it("shows nothing of it for a meeting Persona doesn't attend, and comes up with the room", async () => {
    withLive();
    const none = { state: "none" };
    await load(fixture({ room: none, attendance: "off" }));
    await reply(pending(livePath)[0], live({ room: none, attendance: "off", asks: [], revs: { asks: 0, room: 1, lines: 2, topics: 0 }, topics: [] }));
    fire({ type: "meeting_live", recording_id: id, changed: ["lines"], revs: { lines: 4 } });
    expect(topicsBox()).toBeNull();
    expect(linesBox()).toBeNull();
    expect(reads()).toEqual([""]);
    // Persona turned on: once the room's part says it attends, its topics and lines are read at once.
    fire({ type: "meeting_live", recording_id: id, changed: ["room"], revs: { room: 2 } });
    await reply(pending(`${livePath}?parts=room`)[0], { room: { state: "starting" }, revs: { room: 2 } });
    expect(reads().at(-1)).toBe("?parts=topics,lines&from=0");
    await reply(pending(`${livePath}?parts=topics,lines&from=0`)[0], { topics, lines: [line(0), line(1), line(2), line(3)], revs: { topics: 1, lines: 4 } });
    expect(topicsBox()).not.toBeNull();
    expect(shownLines()).toEqual(["Line 0.", "Line 1.", "Line 2.", "Line 3."]);
  });

  it("is gone once the room says the meeting ended, before the detail is read again", async () => {
    withLive();
    await load();
    await reply(pending(livePath)[0], live({ revs: { asks: 1, room: 2, lines: 1, topics: 1 }, topics }));
    await reply(pending(`${livePath}?parts=lines&from=0`)[0], { lines: [line(0)], revs: { lines: 1 } });
    expect(topicsBox()!.querySelector("[data-topic=current]")).not.toBeNull();
    expect(shownLines()).toEqual(["Line 0."]);
    fire({ type: "meeting_live", recording_id: id, changed: ["room"], revs: { room: 3 } });
    await reply(pending(`${livePath}?parts=room`)[0], { room: ended, revs: { room: 3 } });
    expect(topicsBox()).toBeNull();
    expect(linesBox()).toBeNull();
    fire({ type: "meeting_live", recording_id: id, changed: ["lines", "topics"], revs: { lines: 2, topics: 2 } });
    expect(reads().filter((u) => u.includes("lines") || u.includes("topics"))).toEqual(["?parts=lines&from=0"]);
  });

  it("polls with its own next position while the meeting records, and is gone once the recording is complete", async () => {
    vi.useFakeTimers();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    withLive();
    await load();
    await reply(pending(livePath)[0], live({ revs: { asks: 1, room: 2, lines: 0, topics: 0 }, topics: [] }));
    expect(linesBox()!.textContent).toContain("Lines show here as the meeting is heard.");
    act(() => vi.advanceTimersByTime(5000));
    expect(reads()).toEqual(["", ""]);
    await reply(pending(livePath)[1], live({ revs: { asks: 1, room: 2, lines: 2, topics: 0 }, topics: [] }));
    expect(reads().at(-1)).toBe("?parts=lines&from=0");
    await reply(pending(`${livePath}?parts=lines&from=0`)[0], { lines: [line(0), line(1)], revs: { lines: 2 } });
    act(() => vi.advanceTimersByTime(5000));
    expect(reads().at(-1)).toBe("?from=2");
    await reply(pending(`${livePath}?from=2`)[0], live({ lines: [line(2)], revs: { asks: 1, room: 2, lines: 3, topics: 0 }, topics: [] }));
    expect(shownLines()).toEqual(["Line 0.", "Line 1.", "Line 2."]);
    // Complete, or a #2981 meeting: no live view, no lines read.
    fresh();
    await load(fixture({ complete: true, ended_at: recent(), room: ended }));
    await reply(pending(livePath)[0], live({ room: ended, revs: { asks: 1, room: 3, lines: 3, topics: 0 }, topics: [] }));
    expect(linesBox()).toBeNull();
    expect(topicsBox()).toBeNull();
    fresh();
    await load(fixture({ room: undefined, attending_session: { machine_id: "server", session_id: "conv-2" } }));
    for (const r of pending(livePath)) await reply(r, live({ room: undefined, revs: { asks: 1, lines: 3 } }));
    expect(linesBox()).toBeNull();
    expect(reads().every((u) => !u.includes("from="))).toBe(true);
  });
});
