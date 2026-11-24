// @vitest-environment jsdom
// The voice module (#13431, docs/web/web.md "Voice chat" and "Modules"): it reaches the chat only through the four chat
// slots. On (the default, and with any hub that publishes no module flags), the composer offers the call through
// `composer.trailingAction`; off (Settings › General › Optional features, or a hub that publishes module flags without module.voice),
// there is no button, no mark, and none of its code or strings is requested.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatTail, InteractionMarks, useChatBar, useComposerAction } from "../../core/ChatSlots";
import { CHAT_SLOTS, chatSlotFillers, connectModuleHub, loadModule, MODULES_FEATURE, registerModule, resetModulesForTest, type ChatPageProps, type ComposerAction } from "../../core/module-host";
import { forgetModuleSwitches, setModuleSwitchedOff } from "../../core/module-switch";
import { getState, reset, subscribe } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";
import type { VoiceProbe } from "./support";
import { voice } from "./module";

const probe: VoiceProbe = {
  extensions: [{ id: "voice", name: "Voice", kind: "voice", status: "running" }],
  info: { kind: "info", info: { protocol: 2, features: ["target", "runs", "meeting"], setup: [] } },
};
vi.mock("./support", async (orig) => ({ ...(await orig<typeof import("./support")>()), useVoiceProbe: () => probe }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const session = { machine_id: "macbook", session_id: "a1f3c2d4-0000-4000-8000-000000000006", agent: "claude", title: "Weather" } as Session;
const page: ChatPageProps = { mid: session.machine_id, sid: session.session_id, owner: "page", session, assistant: false, label: "Claude", persona: "Persona", canSend: true, items: () => [], cards: [], onStopAgent: () => {} };
const card = { id: "c1", machine_id: session.machine_id, session_id: session.session_id, kind: "question" } as Interaction;

let host: HTMLDivElement;
let root: Root;
let seen: { action: ComposerAction | undefined; taken: boolean };
const load = vi.fn(voice.load);

function Chat() {
  const { action, slot } = useComposerAction(page);
  const bar = useChatBar(page);
  seen = { action, taken: bar.taken };
  return (
    <>
      {slot}
      <ChatTail {...page} />
      {bar.bar("dock")}
      {bar.bar("composer")}
      <InteractionMarks interaction={card} />
    </>
  );
}

const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => await new Promise((r) => setTimeout(r, 0)));
};

beforeEach(() => {
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("AudioWorkletNode", function AudioWorkletNode() {});
  Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { getUserMedia: async () => ({}) } });
  connectModuleHub(getState, subscribe);
  localStorage.clear();
  forgetModuleSwitches();
  reset();
  resetModulesForTest();
  load.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
});

describe("the voice module", () => {
  it("names its hub flag, its strings and the four chat slots, and its code fills each", async () => {
    expect(voice.feature).toBe("module.voice");
    expect(voice.i18nNamespaces).toEqual(["voice"]);
    expect([...(voice.chatSlots ?? [])].sort()).toEqual([...CHAT_SLOTS].sort());
    registerModule(voice);
    const code = await loadModule("voice");
    for (const slot of CHAT_SLOTS) expect(code.chat?.[slot], slot).toBeTypeOf("function");
  });

  it("on: the composer offers the call, and nothing else shows while no call is on", async () => {
    registerModule({ ...voice, load });
    act(() => root.render(<Chat />));
    await vi.waitFor(async () => {
      await flush();
      expect(seen.action).toBeDefined();
    }, { timeout: 5000 });
    expect(seen.action).toMatchObject({ icon: "voice", primary: true, attributes: { "data-testid": "composer-voice", "data-voice-mode": "start" } });
    expect(seen.taken).toBe(false);
    expect(host.querySelector('[data-testid="voice-asking"]')).toBeNull();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("switched off in this browser: no button, and its code is never requested", async () => {
    setModuleSwitchedOff("voice", true);
    registerModule({ ...voice, load });
    act(() => root.render(<Chat />));
    await flush();
    expect(seen).toEqual({ action: undefined, taken: false });
    expect(host.innerHTML).toBe("");
    expect(load).not.toHaveBeenCalled();
  });

  it("follows the hub: off on a hub that publishes module flags without it, on with its flag or with an older hub", () => {
    registerModule(voice);
    Object.assign(getState(), { loaded: true, features: [MODULES_FEATURE] });
    expect(chatSlotFillers("composer.trailingAction")).toEqual([]);
    Object.assign(getState(), { features: [MODULES_FEATURE, "module.voice"] });
    expect(chatSlotFillers("composer.trailingAction").map((f) => f.id)).toEqual(["voice"]);
    Object.assign(getState(), { features: ["terminal_stream"] });
    expect(chatSlotFillers("composer.trailingAction").map((f) => f.id)).toEqual(["voice"]);
  });

  it("turned off while a page is open: the button goes", async () => {
    registerModule(voice);
    act(() => root.render(<Chat />));
    await vi.waitFor(async () => {
      await flush();
      expect(seen.action).toBeDefined();
    }, { timeout: 5000 });
    act(() => void setModuleSwitchedOff("voice", true));
    await flush();
    expect(seen.action).toBeUndefined();
  });
});
