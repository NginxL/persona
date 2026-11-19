// @vitest-environment jsdom
// useChannels after forgetChannels(): a list read already out is not cached, and mounted hooks read again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Channel } from "./channels";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Each GET /channels waits until the test resolves it.
const reads: { path: string; resolve: (v: { channels: Channel[] }) => void }[] = [];

vi.mock("./api", () => ({
  api: vi.fn((path: string) => new Promise((resolve) => reads.push({ path, resolve }))),
  enc: encodeURIComponent, onCredentialChange: () => () => {}, credentialOwner: () => "test-owner",
}));

const ch = (id: string, name: string): Channel => ({
  id, name, kind: "im", source: "plugin", installed: true, enabled: true, state: "connected", capabilities: {}, bind: "qr",
});

let mod: typeof import("./channels");
let host: HTMLDivElement;
let root: Root | null;

function Names() {
  return <span data-names>{mod.useChannels().map((c) => c.name).join(",")}</span>;
}

const names = () => host.querySelector("[data-names]")?.textContent;
const channelReads = () => reads.filter((r) => r.path === "/channels");

function mount() {
  root = createRoot(host);
  act(() => root!.render(<Names />));
}

function unmount() {
  act(() => root?.unmount());
  root = null;
}

async function settle() {
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(async () => {
  reads.length = 0;
  vi.resetModules();
  mod = await import("./channels");
  const store = await import("./store");
  Object.assign(store.getState(), { features: ["channels"] });
  host = document.createElement("div");
  document.body.append(host);
});

afterEach(() => {
  unmount();
  host.remove();
});

describe("useChannels and forgetChannels", () => {
  it("does not cache a read that was out when the list was forgotten", async () => {
    mount();
    expect(channelReads()).toHaveLength(1);
    act(() => mod.forgetChannels());
    // The hook is still mounted: it reads again under the new generation.
    expect(channelReads()).toHaveLength(2);
    channelReads()[0].resolve({ channels: [ch("wechat", "Old")] });
    await settle();
    unmount();

    // A fresh mount must not take the stale list from the cache: it shares the read still out.
    mount();
    expect(channelReads()).toHaveLength(2);
    channelReads()[1].resolve({ channels: [ch("wechat", "New")] });
    await settle();
    expect(names()).toBe("New");

    // And now the new list is cached: another mount reads nothing.
    unmount();
    mount();
    expect(channelReads()).toHaveLength(2);
    expect(names()).toBe("New");
  });

  it("reads again on the next mount when the read out at forget time had no hook left", async () => {
    mount();
    unmount();
    act(() => mod.forgetChannels());
    channelReads()[0].resolve({ channels: [ch("wechat", "Old")] });
    await settle();

    mount();
    expect(channelReads()).toHaveLength(2);
    channelReads()[1].resolve({ channels: [ch("wechat", "New")] });
    await settle();
    expect(names()).toBe("New");
  });

  it("re-reads for a hook already mounted and shows the updated list", async () => {
    mount();
    channelReads()[0].resolve({ channels: [ch("wechat", "Old")] });
    await settle();
    expect(names()).toBe("Old");

    act(() => mod.forgetChannels());
    expect(channelReads()).toHaveLength(2);
    channelReads()[1].resolve({ channels: [ch("wechat", "Renamed")] });
    await settle();
    expect(names()).toBe("Renamed");
  });

  it("shares one read among the hooks mounted when the list is forgotten", async () => {
    root = createRoot(host);
    act(() => root!.render(<><Names /><Names /></>));
    expect(channelReads()).toHaveLength(1);
    act(() => mod.forgetChannels());
    expect(channelReads()).toHaveLength(2);
  });
});
