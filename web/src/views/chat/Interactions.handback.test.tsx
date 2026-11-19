// @vitest-environment jsdom
// Handing a card back to the computer (#2450): a labelled button on its own row under the card's body, not a × in the
// card's head (where the receipt's × means "collapse"). One click sends one `dismiss`, with no confirmation, and the
// card turns into a "Handed back" receipt naming the request, as an answer leaves one.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
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

const permission = { id: "p_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "Bash", tool_input: { command: "npm test" }, summary: "Run the tests" } as Interaction;

/** The chat's dock as ChatView feeds it: this session's pending cards and the ones just answered here. */
function Dock() {
  const interactions = useStore((s) => s.interactions);
  const answered = useStore((s) => Object.values(s.answered));
  return <PendingCards interactions={interactions} answered={answered} narrow={false} keyboard={false} onDismissKeyboard={() => {}} />;
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  calls.length = 0;
  upsertSession({ machine_id: "m", session_id: "s", title: "Demo", state: "needs_you" } as Session);
  Object.assign(getState(), { interactions: [permission], answered: {} });
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

describe("handing a card back", () => {
  it("is a labelled button under the card's body, with no × in its head", () => {
    act(() => root.render(<Dock />));
    const card = host.querySelector<HTMLElement>("[data-card]")!;
    const button = card.querySelector<HTMLButtonElement>("button[data-card-answer]")!;
    expect(button.textContent).toBe(t("chat-interactions.hand_back"));
    expect(button.getAttribute("aria-label")).toBe(t("chat-interactions.hand_back"));
    // below the body, outside the head row with the kicker and title
    const head = card.querySelector("[data-card-keys] > div > div")!;
    expect(head.contains(button)).toBe(false);
    expect(head.querySelector("button")).toBeNull();
  });

  it("sends one dismiss without asking and leaves a receipt naming the request", async () => {
    const ask = vi.spyOn(window, "confirm");
    act(() => root.render(<Dock />));
    act(() => host.querySelector<HTMLButtonElement>("button[data-card-answer]")!.click());
    await flush();
    expect(ask).not.toHaveBeenCalled();
    expect(calls).toEqual([{ path: "/interactions/p_1/resolve", body: { dismiss: true } }]);
    expect(host.querySelector("[data-card]")).toBeNull();
    const receipt = host.querySelector<HTMLElement>('[data-answered="p_1"]')!;
    // its head says it once, and the line under it is which request went back (not those words again)
    expect(receipt.textContent).toBe(t("chat-interactions.handed_back") + "Run the tests");
    expect(receipt.querySelector("[data-answered-about]")?.textContent).toBe("Run the tests");
    expect(getState().answered.p_1).toMatchObject({ handedBack: true });
  });
});
