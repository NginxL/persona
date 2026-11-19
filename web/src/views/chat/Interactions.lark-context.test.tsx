// @vitest-environment jsdom
// Lark approval cards (#5776): the server-provided readable summary is shown as the card body; internal JSON fields
// remain available only behind "Show original".
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../../lib/ops", () => ({ apiOp: vi.fn(async () => ({})) }));
import { InteractionCard } from "./Interactions";
import { reset, upsertSession } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  upsertSession({ machine_id: "m", session_id: "s", title: "Demo", state: "needs_you" } as Session);
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

describe("a Lark reply approval card", () => {
  it("shows the conversation and original message, with raw internal fields only on demand", () => {
    const ix = {
      id: "ix_lark", machine_id: "m", session_id: "s", kind: "permission", tool_name: "ex",
      summary: "以你的身份在飞书上回复「发布群」里 Example User 的消息（话题回复）\n原消息：Can we ship on Friday?\n正文：\nI will check the build",
      tool_input: { text: "I will check the build", clientId: "internal-client", expectedSenderId: "internal-sender" },
    } as Interaction;
    act(() => root.render(<InteractionCard ix={ix} />));
    const text = host.textContent ?? "";
    expect(text).toContain("发布群");
    expect(text).toContain("Example User");
    expect(text).toContain("Can we ship on Friday?");
    expect(text).toContain("I will check the build");
    expect(text).not.toContain("internal-client");
    expect(text).not.toContain("expectedSenderId");
    expect(text).not.toContain("clientId");
    const show = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === t("chat-interactions.show_input"));
    expect(show).toBeDefined();
    act(() => show!.click());
    expect(host.textContent).toContain("internal-client");
    expect(host.textContent).toContain("expectedSenderId");
  });
});
