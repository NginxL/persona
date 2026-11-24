// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useRevisionReread } from "./session-models";
import type { Session } from "./types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const chat = (x: Partial<Session> = {}): Session => ({
  machine_id: "hub", session_id: "c1", title: "", cwd: "/", assistant: true, status: "idle", state: "idle", ...x,
}) as Session;
const working = { status: "working", state: "working" } as const;

let host: HTMLDivElement;
let root: Root;
let log: string[];

function Probe({ s, enabled }: { s: Session | undefined; enabled: boolean }) {
  useRevisionReread(s, enabled, () => {
    const id = `${s?.session_id}@${s?.rev}`;
    log.push(`read ${id}`);
    return () => void log.push(`cleanup ${id}`);
  });
  return null;
}
const show = (s: Session | undefined, enabled = true) => act(async () => root.render(<Probe s={s} enabled={enabled} />));

beforeEach(() => {
  log = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("useRevisionReread", () => {
  it("keeps a read's cleanup when the conversation starts working, until the next read or unmount", async () => {
    await show(chat({ rev: 1 }));
    await show(chat({ rev: 2 }));
    expect(log).toEqual(["read c1@2"]);
    // a new turn marks the conversation working before its first write moves the revision
    await show(chat({ rev: 2, ...working }));
    await show(chat({ rev: 3, ...working }));
    expect(log).toEqual(["read c1@2"]);
    await show(chat({ rev: 3 }));
    expect(log).toEqual(["read c1@2", "cleanup c1@2", "read c1@3"]);
    act(() => root.unmount());
    root = createRoot(host);
    expect(log).toEqual(["read c1@2", "cleanup c1@2", "read c1@3", "cleanup c1@3"]);
  });

  it("keeps a move that arrives before it can read, and reads it once it can", async () => {
    await show(chat({ rev: 1 }), false);
    await show(chat({ rev: 2 }), false);
    expect(log).toEqual([]);
    await show(chat({ rev: 2 }), true);
    expect(log).toEqual(["read c1@2"]);
  });

  it("starts afresh for another conversation: no read for its revision, nothing owed carried over", async () => {
    await show(chat({ rev: 1, ...working }));
    await show(chat({ rev: 2, ...working }));
    // another conversation, idle, at another revision: not a move of this one's
    await show(chat({ session_id: "c2", rev: 9 }));
    expect(log).toEqual([]);
    await show(chat({ session_id: "c2", rev: 10 }));
    expect(log).toEqual(["read c2@10"]);
    // switching away runs the last read's cleanup
    await show(undefined);
    expect(log).toEqual(["read c2@10", "cleanup c2@10"]);
  });
});
