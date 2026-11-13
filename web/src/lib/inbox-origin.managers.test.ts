// Two managers may list the same item id (one account in both, lib/inbox.ts itemKey): a conversation's item is its
// manager's copy, so an item of another manager that happens to share the id must never lend its title or address.
import { describe, expect, it } from "vitest";
import type { InboxItem } from "./inbox";
import { inboxOriginForSession, type InboxOriginInput } from "./inbox-origin";
import type { Session } from "./types";

const MID = "machine-a";
const SID = "conversation";

const session = (extra: Partial<Session> = {}): Session => ({ machine_id: MID, session_id: SID, title: "", cwd: "/work", status: "idle", ...extra });

const item = (id: string, extra: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "connector", kind: "dm", account: "account", account_name: "Account", title: `Title ${id}`, preview: "", count: 1, ...extra,
});

const resolve = (current: Session, extra: Partial<InboxOriginInput> = {}) =>
  inboxOriginForSession(MID, SID, { session: current, sessions: {}, assistant: null, items: [], history: [], ...extra });

describe("an inbox conversation of one manager", () => {
  it("does not borrow another manager's item with the same id", () => {
    // the session row names its manager; no copy of the item under that manager is loaded
    const current = session({ manager: "manager-a", origin: "inbox", origin_ref: "same-id" });
    const theirs = item("same-id", { manager: "manager-b", title: "Another manager's matter" });
    expect(resolve(current, { items: [theirs] })).toEqual({ id: "same-id", manager: "manager-a", title: undefined });
  });

  it("takes the item of its own manager when both managers list the id", () => {
    const current = session({ manager: "manager-a", origin: "inbox", origin_ref: "same-id" });
    const theirs = item("same-id", { manager: "manager-b", title: "Another manager's matter" });
    const mine = item("same-id", { manager: "manager-a", title: "My matter" });
    expect(resolve(current, { items: [theirs, mine] })).toEqual({ id: "same-id", manager: "manager-a", title: "My matter" });
  });

  it("matches a handling conversation by both computer and session id", () => {
    const current = session();
    const wrongMachine = item("other", { handling: { state: "agent", machine_id: "machine-b", session_id: SID } });
    const mine = item("mine", { manager: "manager-a", handling: { state: "agent", machine_id: MID, session_id: SID } });
    expect(resolve(current, { items: [wrongMachine, mine] })?.id).toBe("mine");
  });

  it("follows no session link of another computer, even with the same session id", () => {
    const current = session();
    const theirs: InboxItem = item("theirs", {
      chain: [{ id: "e", kind: "session", text: "", link: { type: "session", machine_id: "machine-b", session_id: SID } }],
    });
    expect(resolve(current, { items: [theirs] })).toBeUndefined();
  });
});
