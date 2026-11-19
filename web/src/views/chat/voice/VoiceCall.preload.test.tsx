// @vitest-environment jsdom
// A call's own parts (VoiceCall.tsx: the bar, the thread's captions, the ended row, why a call cannot start) are a chunk of
// their own, off the first screen: a conversation that offers a call does not fetch it, and the call's start loads it with
// the call's code, so the bar draws in the composer's place at once (the button that explains: VoiceParts.test.tsx).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Session } from "../../../lib/types";
import type { VoiceConversation } from "../../../lib/voice/conversation";
import type { VoiceProbe } from "../../../lib/voice/support";

const loaded = vi.hoisted(() => vi.fn());
vi.mock("./VoiceCall", async (original) => {
  loaded();
  // the bar as a mark: what matters here is whether it draws in the render that asks for it
  return { ...(await original<typeof import("./VoiceCall")>()), VoiceBar: () => <div data-testid="voice-bar" /> };
});
const probe: VoiceProbe = {
  extensions: [{ id: "voice", name: "Voice", kind: "voice", status: "running" }],
  info: { kind: "info", info: { protocol: 2, features: ["target", "runs", "meeting"], setup: [] } },
};
vi.mock("../../../lib/voice/support", async (orig) => ({ ...(await orig<typeof import("../../../lib/voice/support")>()), useVoiceProbe: () => probe }));
const { voiceCalls } = await import("../../../lib/voice/calls");
const { useVoiceEntry, VoiceBar } = await import("./VoiceParts");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session = { machine_id: "server", session_id: "8a1d2c3e-0000-4000-8000-000000000003", assistant: true, title: "Synthetic chat" } as Session;
let offered: ReturnType<typeof useVoiceEntry>["offer"];

// stands in for ChatView: the composer's entry and the dialog it opens
function Entry() {
  const entry = useVoiceEntry({ mid: session.machine_id, sid: session.session_id, owner: "test", session, items: () => [], label: "Persona" });
  offered = entry.offer;
  return <>{entry.dialog}</>;
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("AudioWorkletNode", function AudioWorkletNode() {});
  Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { getUserMedia: async () => ({}) } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("leaves the call's parts off a conversation that offers a call, and loads them with the call so its bar draws at once", async () => {
  vi.stubGlobal("isSecureContext", true);
  await act(async () => root.render(<Entry />));
  expect(offered).toMatchObject({ mode: "start" });
  expect(loaded).not.toHaveBeenCalled();

  const start = vi.spyOn(voiceCalls, "start").mockResolvedValue(null);
  act(() => offered!.onClick());
  const ui = start.mock.calls[0]?.[0].ui;
  expect(ui).toBeTypeOf("function");
  await act(async () => void (await ui!()));
  expect(loaded).toHaveBeenCalledTimes(1);

  // the call is on once that is in: the bar draws in the very render that asks for it
  act(() => root.render(<VoiceBar call={{} as VoiceConversation} session={session} cards={[]} label="Persona" persona="Persona" onStopAgent={() => {}} />));
  expect(host.querySelector('[data-testid="voice-bar"]')).not.toBeNull();
});
