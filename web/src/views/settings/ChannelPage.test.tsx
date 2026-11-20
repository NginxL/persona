// @vitest-environment jsdom
// A channel page's changes go one at a time: while one is with the hub the page's other switches, its target row, unbind
// and the test message wait, and an answer older than a later request (a refresh, another change) is not put back over
// it; the page reads the detail again instead, since that later read may predate the change. The hub is faked: every
// read and change waits until the test answers it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import type { ChannelDetail } from "../../lib/channels";

type Asked = { kind: "get" | "put" | "unbind" | "test"; body?: unknown; answer: (d: ChannelDetail) => void };
const hub = vi.hoisted(() => ({ asked: [] as Asked[] }));

vi.mock("../../lib/channels", async (original) => ({
  ...(await original<typeof import("../../lib/channels")>()),
  channelDetail: () => new Promise((resolve) => hub.asked.push({ kind: "get", answer: resolve })),
  updateChannel: (_id: string, body: unknown) => new Promise((resolve) => hub.asked.push({ kind: "put", body, answer: resolve })),
  unbindChannel: () => new Promise((resolve) => hub.asked.push({ kind: "unbind", answer: resolve })),
  testChannel: () => new Promise((resolve) => hub.asked.push({ kind: "test", answer: resolve })),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { ChannelPage } from "./Channels";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

/** The hub's detail with features a and b on or off as given. */
const detail = (a: boolean, b: boolean): ChannelDetail => ({
  id: "demo", name: "Demo", kind: "im", source: "builtin", installed: true, enabled: true, state: "connected",
  account: { name: "owner" }, capabilities: { push: true }, bind: "none", target: { kind: "main" },
  features: [{ id: "a", title: "Feature A", on: a }, { id: "b", title: "Feature B", on: b }],
  waiting: { answers: 0, asks: 0 },
});

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const answer = async (a: Asked | undefined, d: ChannelDetail) => {
  expect(a).toBeTruthy();
  await act(async () => a!.answer(d));
  await settle();
};
const sw = (label: string) => host.querySelector<HTMLButtonElement>(`button[role="switch"][aria-label="${label}"]`)!;
const flip = async (label: string) => {
  await act(async () => sw(label).click());
  await settle();
};
const byText = (text: string) =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(text))!;
const puts = () => hub.asked.filter((a) => a.kind === "put");

beforeEach(async () => {
  hub.asked = [];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root.render(<ChannelPage id="demo" onBack={() => {}} onSettings={() => {}} />));
  await answer(hub.asked.shift(), detail(false, false));
  hub.asked = [];
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

it("offers a test message only when the connected channel can send without a reply", async () => {
  expect(byText(t("settings-channels.send_test"))).toBeDefined();

  const refresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-channels.refresh")}"]`)!;
  await act(async () => refresh.click());
  await answer(hub.asked.shift(), { ...detail(false, false), source: "plugin", capabilities: { format: "plain" } });

  expect(byText(t("settings-channels.send_test"))).toBeUndefined();
});

describe("ChannelPage saves", () => {
  it("keeps the other switches off while a change is with the hub", async () => {
    await flip("Feature A");
    expect(puts()).toHaveLength(1);
    expect(sw("Feature A").disabled).toBe(true);
    expect(sw("Feature B").disabled).toBe(true);
    // a click on the waiting switch sends nothing more
    await flip("Feature B");
    expect(puts()).toHaveLength(1);

    await answer(hub.asked.shift(), detail(true, false));
    expect(sw("Feature B").disabled).toBe(false);
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
  });

  it("reads again when a refresh was sent while a change was out", async () => {
    await flip("Feature A");
    const change = hub.asked.shift();
    // a refresh while the change is out: the hub serves its read before the change lands
    await act(async () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-channels.refresh")}"]`)!.click());
    await settle();
    const read = hub.asked.shift();
    expect(read?.kind).toBe("get");
    await answer(read, detail(false, true));
    await answer(change, detail(true, false));
    // the change's answer is older than the read, so it is not shown; the page asks again
    expect(sw("Feature B").getAttribute("aria-checked")).toBe("true");
    const again = hub.asked.shift();
    expect(again?.kind).toBe("get");
    await answer(again, detail(true, true));
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
    expect(sw("Feature A").disabled).toBe(false);
    expect(hub.asked).toHaveLength(0);
  });

  it("does not read again when the change's answer is the newest", async () => {
    await flip("Feature A");
    await answer(hub.asked.shift(), detail(true, false));
    expect(hub.asked).toHaveLength(0);
    expect(sw("Feature A").getAttribute("aria-checked")).toBe("true");
  });

  it("keeps unbind and the test message waiting while a change is with the hub", async () => {
    await flip("Feature A");
    const unbind = byText(t("settings-channels.unbind"));
    const test = byText(t("settings-channels.send_test"));
    expect(unbind.disabled).toBe(true);
    expect(test.disabled).toBe(true);
    await act(async () => test.click());
    await settle();
    expect(hub.asked.map((a) => a.kind)).toEqual(["put"]);
    await answer(hub.asked.shift(), detail(true, false));
    expect(byText(t("settings-channels.unbind")).disabled).toBe(false);
    expect(byText(t("settings-channels.send_test")).disabled).toBe(false);
  });

  it("does not unbind while a change is with the hub, even from an open dialog", async () => {
    await act(async () => byText(t("settings-channels.unbind")).click());
    await settle();
    const confirm = () => [...document.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent?.includes(t("settings-channels.unbind"))).pop()!;
    await flip("Feature A");
    expect(confirm().disabled).toBe(true);
    await act(async () => confirm().click());
    await settle();
    expect(hub.asked.map((a) => a.kind)).toEqual(["put"]);
    await answer(hub.asked.shift(), detail(true, false));

    // once the change is done the unbind goes out, and the switches wait for it
    await act(async () => confirm().click());
    await settle();
    expect(hub.asked.map((a) => a.kind)).toEqual(["unbind"]);
    expect(sw("Feature A").disabled).toBe(true);
    await flip("Feature B");
    expect(hub.asked.map((a) => a.kind)).toEqual(["unbind"]);
    await answer(hub.asked.shift(), { ...detail(true, false), state: "not_connected", account: undefined });
    expect(sw("Feature A").disabled).toBe(false);
  });
});
