// @vitest-environment jsdom
// useLarkPeoplePicker: Enter / Tab only pick from the results of the query being typed, never from an older one that
// is still on screen while the new lookup runs.
import { act, useState, type KeyboardEvent } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import type { LarkPerson } from "../lib/lark-people";
import { LarkPeopleMenu, useLarkPeoplePicker, type FindPeople, type PeoplePicker } from "./LarkPeoplePicker";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const alice: LarkPerson = { user_id: "u-alice", name: "Alice" };
const bob: LarkPerson = { user_id: "u-bob", name: "Bob" };

interface Lookup {
  query: string;
  signal: AbortSignal;
  resolve: (people: LarkPerson[]) => void;
  reject: (err: unknown) => void;
}

let host: HTMLDivElement;
let root: Root;
let lookups: Lookup[];
let picker: PeoplePicker;
let setText: (text: string) => void;
let apply: Mock<(text: string, caret: number) => void>;
let onPicked: Mock<(p: LarkPerson) => void>;

const find: FindPeople = (query, signal) =>
  new Promise((resolve, reject) => {
    lookups.push({ query, signal, resolve, reject });
  });

function Harness() {
  const [text, set] = useState("");
  setText = set;
  picker = useLarkPeoplePicker({ text, caret: text.length, open: true, find, apply, onPicked });
  return null;
}

function lookup(query: string): Lookup {
  const l = lookups.findLast((x) => x.query === query);
  if (!l) throw new Error(`no lookup for ${query}`);
  return l;
}

async function type(text: string) {
  await act(async () => setText(text));
}

async function settle(fn: () => void = () => {}) {
  await act(async () => {
    fn();
  });
}

function key(name: string, shiftKey = false) {
  const e = { key: name, shiftKey, keyCode: 0, nativeEvent: { isComposing: false }, preventDefault: vi.fn(), stopPropagation: vi.fn() };
  let took = false;
  act(() => {
    took = picker.onKeyDown(e as unknown as KeyboardEvent);
  });
  return { took, prevented: e.preventDefault.mock.calls.length > 0 };
}

beforeEach(() => {
  vi.useFakeTimers();
  lookups = [];
  apply = vi.fn<(text: string, caret: number) => void>();
  onPicked = vi.fn<(p: LarkPerson) => void>();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Harness />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

/** '@al' looked up and answered with Alice, then the text changed to '@bob' (lookup not answered yet). */
async function aliceShownWhileBobLoads() {
  await type("hi @al");
  await settle(() => vi.advanceTimersByTime(140));
  await settle(() => lookup("al").resolve([alice]));
  expect(picker.rows).toEqual([alice]);
  await type("hi @bob");
  await settle(() => vi.advanceTimersByTime(140));
  expect(lookup("bob").signal.aborted).toBe(false);
  // the old rows stay on screen while 'bob' loads (no visual change)
  expect(picker.rows).toEqual([alice]);
  expect(picker.loading).toBe(true);
}

describe("useLarkPeoplePicker Enter / Tab", () => {
  it("draws member pictures through their owning hub, including legacy paths and missing pictures", () => {
    Element.prototype.scrollIntoView = vi.fn();
    const result = { ...picker, token: { start: 0, end: 1, query: "" }, rows: [
      { ...alice, avatar: "/agent/v1/media/old-alice", avatar_url: "/api/v1/avatars/lark/YWxpY2U?lark_account=4" },
      { ...bob, avatar: "/agent/v1/media/bob?ol_account=4" },
      { user_id: "other", name: "Other", avatar: "https://example.test/external.png" },
    ] };
    act(() => root.render(<LarkPeopleMenu picker={result} manager="docs-owner" />));
    const options = host.querySelectorAll("[role=option]");
    const aliceUrl = new URL(options[0].querySelector("img")!.src);
    expect(aliceUrl.pathname).toBe("/api/v1/avatars/lark/YWxpY2U");
    expect([aliceUrl.searchParams.get("lark_account"), aliceUrl.searchParams.get("size"), aliceUrl.searchParams.get("manager")]).toEqual(["4", "24", "docs-owner"]);
    const bobUrl = new URL(options[1].querySelector("img")!.src);
    expect(bobUrl.pathname).toBe("/api/v1/lark/media/bob");
    expect([bobUrl.searchParams.get("ol_account"), bobUrl.searchParams.get("manager")]).toEqual(["4", "docs-owner"]);
    expect(options[2].querySelector("img")).toBeNull();
    expect(options[2].textContent).toContain("Other");
  });

  it("picks from the current query's results", async () => {
    await type("hi @al");
    await settle(() => vi.advanceTimersByTime(140));
    await settle(() => lookup("al").resolve([alice]));
    const k = key("Enter");
    expect(k).toEqual({ took: true, prevented: true });
    expect(onPicked).toHaveBeenCalledWith(alice);
    expect(apply).toHaveBeenCalledWith("hi @Alice ", "hi @Alice ".length);
  });

  it("swallows Enter and Tab while the new query loads instead of picking the previous query's person", async () => {
    await aliceShownWhileBobLoads();
    expect(key("Enter")).toEqual({ took: true, prevented: true });
    expect(key("Tab")).toEqual({ took: true, prevented: true });
    expect(onPicked).not.toHaveBeenCalled();
    expect(apply).not.toHaveBeenCalled();

    await settle(() => lookup("bob").resolve([bob]));
    expect(picker.loading).toBe(false);
    expect(key("Enter")).toEqual({ took: true, prevented: true });
    expect(onPicked).toHaveBeenCalledTimes(1);
    expect(onPicked).toHaveBeenCalledWith(bob);
    expect(apply).toHaveBeenCalledWith("hi @Bob ", "hi @Bob ".length);
  });

  it("swallows Enter before the debounced lookup for a new query has even started", async () => {
    await type("hi @al");
    await settle(() => vi.advanceTimersByTime(140));
    await settle(() => lookup("al").resolve([alice]));
    await type("hi @bob");
    expect(key("Enter")).toEqual({ took: true, prevented: true });
    expect(onPicked).not.toHaveBeenCalled();
  });

  it("keeps the arrow keys working on the rows shown while loading", async () => {
    await type("hi @a");
    await settle(() => vi.advanceTimersByTime(140));
    await settle(() => lookup("a").resolve([alice, bob]));
    await type("hi @ab");
    expect(key("ArrowDown")).toEqual({ took: true, prevented: true });
    expect(picker.active).toBe(1);
  });

  it("leaves nothing to pick after a failed lookup, so Enter goes to the box", async () => {
    await aliceShownWhileBobLoads();
    await settle(() => lookup("bob").reject(new Error("offline")));
    expect(picker.rows).toEqual([]);
    expect(picker.loading).toBe(false);
    expect(key("Enter")).toEqual({ took: false, prevented: false });
    expect(onPicked).not.toHaveBeenCalled();
  });

  it("ignores an aborted lookup's answer", async () => {
    await type("hi @al");
    await settle(() => vi.advanceTimersByTime(140));
    await type("hi @bob");
    expect(lookup("al").signal.aborted).toBe(true);
    await settle(() => lookup("al").resolve([alice]));
    expect(picker.rows).toEqual([]);
    expect(key("Enter")).toEqual({ took: true, prevented: true });
    expect(onPicked).not.toHaveBeenCalled();
  });

  it("leaves Shift+Enter to the box", async () => {
    await aliceShownWhileBobLoads();
    expect(key("Enter", true)).toEqual({ took: false, prevented: false });
    expect(onPicked).not.toHaveBeenCalled();
  });
});
