// @vitest-environment jsdom
// The composer's voice entry (docs/web/web.md "Voice chat"): a chat with a running protocol 2 voice extension offers a
// call; a meeting's room conversation (origin `meeting`, #3862) offers none, since the extension takes only the
// iPhone's meeting calls there and refuses an ordinary one (`meeting_room`). Where the button explains instead of
// starting, what its tap opens (VoiceCall.tsx, a chunk of its own) is fetched before the tap.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../../lib/types";
import type { VoiceProbe } from "../support";
import { useVoiceEntry, type VoiceOffer } from "./VoiceParts";

const probe: VoiceProbe = {
  extensions: [{ id: "voice", name: "Voice", kind: "voice", status: "running" }],
  info: { kind: "info", info: { protocol: 2, features: ["target", "runs", "meeting"], setup: [] } },
};
vi.mock("../support", async (orig) => ({ ...(await orig<typeof import("../support")>()), useVoiceProbe: () => probe }));
const loaded = vi.hoisted(() => vi.fn());
vi.mock("./VoiceCall", async (original) => {
  loaded();
  return await original<typeof import("./VoiceCall")>();
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let offered: ReturnType<typeof useVoiceEntry>["offer"] | "unset";

function Entry({ session }: { session: Session }) {
  const entry = useVoiceEntry({ mid: session.machine_id, sid: session.session_id, owner: "test", session, items: () => [], label: "Persona" });
  offered = entry.offer;
  return <>{entry.dialog}</>;
}

const chat = (extra: Partial<Session> = {}) => ({ machine_id: "server", session_id: "8a1d2c3e-0000-4000-8000-000000000002", assistant: true, title: "1112 | 会议 | Design review", ...extra }) as Session;

beforeEach(() => {
  // a secure page in a browser that can record: an ordinary chat offers the call itself
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("AudioWorkletNode", function AudioWorkletNode() {});
  Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { getUserMedia: async () => ({}) } });
  offered = "unset";
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the composer's voice entry", () => {
  it("offers a call in an ordinary conversation, its meeting's follow-up included", () => {
    for (const origin of [undefined, "meeting_followup"]) {
      act(() => root.render(<Entry session={chat({ origin })} />));
      expect(offered).toMatchObject({ mode: "start" });
    }
  });

  it("offers no call in a meeting's room conversation", () => {
    act(() => root.render(<Entry session={chat({ origin: "meeting", origin_ref: "8a1d2c3e-0000-4000-8000-000000000001" })} />));
    expect(offered).toBeUndefined();
  });

  it("drops the offer when the page moves from a chat to a room", () => {
    act(() => root.render(<Entry session={chat({ session_id: "a1f3c2d4-0000-4000-8000-000000000001", assistant: false })} />));
    expect(offered).toMatchObject({ mode: "start" });
    act(() => root.render(<Entry session={chat({ origin: "meeting" })} />));
    expect(offered).toBeUndefined();
  });

  it("fetches what the button opens before the tap where it explains, so the tap draws it at once", async () => {
    // the offers above fetched nothing
    expect(loaded).not.toHaveBeenCalled();
    // a plain-HTTP page: the microphone needs a secure context, so the button says why instead of starting
    vi.stubGlobal("isSecureContext", false);
    act(() => root.render(<Entry session={chat()} />));
    expect(offered).toMatchObject({ mode: "explain" });
    await vi.waitFor(() => expect(loaded).toHaveBeenCalledTimes(1));
    await act(async () => {
      await import("./VoiceCall");
    });
    act(() => (offered as VoiceOffer).onClick());
    expect(document.body.querySelector('[data-testid="voice-unavailable"]')).not.toBeNull();
  });
});
