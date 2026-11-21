// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));
vi.mock("./router", async (orig) => ({ ...(await orig<typeof import("./router")>()), navigate: navigateMock }));

import { useAttentionSignals } from "./attention-signals";
import type { DesktopBridge, DesktopPresence } from "./desktop-types";
import type { Interaction } from "./types";

const { sendMock, connectMock, disconnectMock } = vi.hoisted(() => ({ sendMock: vi.fn(), connectMock: vi.fn(), disconnectMock: vi.fn() }));
let features: string[] = ["desktop_unregister"];
let notifyOn = true;
let socketOpen = true;
let badgeCount = 0;
let sessions: { machine_id: string; session_id: string; title: string; cwd: string; status: "working" | "needs_you" | "unread"; state: "working" | "needs_you" | "unread"; last_text?: string }[] = [];
let interactions: Interaction[] = [];
let pushGrading = false;
vi.mock("./store", () => ({
  useStore: (select: (s: { loaded: boolean; interactions: Interaction[]; pushGrading: boolean }) => unknown) =>
    select({ loaded: true, interactions, pushGrading }),
  getState: () => ({ mutes: { spaces: [], sessions: [], allowed: [] }, interactions, pushGrading, features }),
  onHello: (fn: () => void) => { hello = fn; return () => { hello = undefined; }; },
  send: sendMock, appSocketReady: () => socketOpen,
  connect: () => { socketOpen = true; connectMock(); }, disconnect: () => { socketOpen = false; disconnectMock(); },
  hubHas: (list: string[], feature: string) => list.includes(feature),
  DESKTOP_UNREGISTER_FEATURE: "desktop_unregister",
  onHubEvent: () => () => {}, setHelloExtras: () => {}, sayHello: () => {},
}));
vi.mock("./sessions", () => ({ useCounts: () => ({ badge: badgeCount }), useAgentSessions: () => sessions }));
vi.mock("./notify", async (original) => ({
  ...(await original<typeof import("./notify")>()),
  systemNotifyActive: () => notifyOn,
  onSystemNotifyChange: (callback: () => void) => { notificationListeners.add(callback); return () => void notificationListeners.delete(callback); },
}));

let hello: (() => void) | undefined;
let active: boolean | undefined;
let changePresence: ((value: boolean) => void) | undefined;
// every listener of the notification settings (the desktop lifecycle's, and what the page asks the hub for, #8145)
const notificationListeners = new Set<() => void>();
const changeNotifications = () => notificationListeners.forEach((l) => l());

function desktopMock(overrides: Partial<DesktopBridge> = {}): DesktopBridge {
  return {
    version: 1, platform: "linux", deviceName: "Desktop",
    notificationPermission: () => "granted",
    setBadge: vi.fn(), notify: () => true, closeNotification: vi.fn(), clearNotifications: vi.fn(),
    desktopPresence: () => ({ active: active ?? false, notifies: active ?? false }),
    onOpenNotification: () => () => {},
    onPresenceChange: (callback: (presence: DesktopPresence) => void) => {
      changePresence = (value) => { active = value; callback({ active: value, notifies: value }); };
      return () => { changePresence = undefined; };
    },
    setLaunchAtLogin: async () => ({ enabled: false }),
    getLaunchAtLogin: async () => ({ enabled: false }),
    ...overrides,
  };
}

function Probe() {
  useAttentionSignals("Persona", undefined);
  return null;
}

describe("desktop attention lifecycle", () => {
  let root: Root;
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    features = ["desktop_unregister"];
    notifyOn = true;
    socketOpen = true;
    active = true;
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: desktopMock() }));
    hello = undefined;
    changePresence = undefined;
    badgeCount = 0;
    sessions = [];
    interactions = [];
    pushGrading = false;
    notificationListeners.clear();
    root = createRoot(document.createElement("div"));
  });
  afterEach(() => {
    act(() => root.unmount());
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });
  it("registers on authenticated hello, withdraws and restores on unlock on the same socket, and follows notification settings", async () => {
    await act(async () => root.render(<Probe />));
    await act(async () => hello?.());
    expect(sendMock).toHaveBeenNthCalledWith(1, { type: "register_desktop", device_name: "Desktop" });
    // the shell's 30 s repeat of an unchanged presence: the open socket stays and the hub is told nothing again
    await act(async () => changePresence?.(true));
    expect(sendMock).toHaveBeenCalledTimes(1);
    await act(async () => changePresence?.(false));
    expect(sendMock).toHaveBeenNthCalledWith(2, { type: "unregister_desktop" });
    await act(async () => changePresence?.(true));
    expect(connectMock).not.toHaveBeenCalled();
    expect(sendMock).toHaveBeenNthCalledWith(3, { type: "register_desktop", device_name: "Desktop" });
    await act(async () => { notifyOn = false; changeNotifications(); });
    expect(sendMock).toHaveBeenNthCalledWith(4, { type: "unregister_desktop" });
    await act(async () => { notifyOn = true; changeNotifications(); });
    expect(sendMock).toHaveBeenNthCalledWith(5, { type: "register_desktop", device_name: "Desktop" });
    await act(async () => root.unmount());
    expect(hello).toBeUndefined();
    expect(changePresence).toBeUndefined();
    expect(notificationListeners.size).toBe(0);
  });
  it("reconnects an old hub when notifications are restored and routes native clicks through the real listener", async () => {
    let click: ((route: string) => void) | undefined;
    let unsubscribe = 0;
    features = [];
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: Object.assign({}, window.personaDesktop!, {
      onOpenNotification: (callback: (route: string) => void) => {
        click = callback;
        return () => { unsubscribe++; click = undefined; };
      },
    }) }));
    await act(async () => root.render(<Probe />));
    await act(async () => hello?.());
    await act(async () => { notifyOn = false; changeNotifications(); });
    expect(disconnectMock).toHaveBeenCalledOnce();
    await act(async () => { notifyOn = true; changeNotifications(); });
    expect(connectMock).toHaveBeenCalledOnce();
    expect(sendMock).toHaveBeenLastCalledWith({ type: "register_desktop", device_name: "Desktop" });
    click?.("/persona/s/m/a?view=chat");
    click?.("https://example.invalid/not-the-hub");
    click?.("//example.invalid/not-the-hub");
    click?.("/persona\\example.invalid");
    expect(navigateMock).toHaveBeenCalledExactlyOnceWith("/persona/s/m/a?view=chat");
    await act(async () => root.unmount());
    expect(unsubscribe).toBe(1);
    expect(click).toBeUndefined();
  });
  it("keeps the browser lifecycle when an older desktop lacks additive capabilities", async () => {
    const legacy: Partial<DesktopBridge> = desktopMock();
    delete legacy.desktopPresence;
    delete legacy.onPresenceChange;
    delete legacy.onOpenNotification;
    vi.stubGlobal("window", Object.assign(window, { personaDesktop: legacy }));
    await act(async () => root.render(<Probe />));
    await act(async () => hello?.());
    expect(sendMock).toHaveBeenCalledWith({ type: "register_desktop", device_name: "Desktop" });
    await act(async () => root.unmount());
    expect(changePresence).toBeUndefined();
  });
  it("applies push grading to Electron Dock setBadge and desktop notifications (#11771)", async () => {
    localStorage.setItem("persona.systemNotify", "1");
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    const desktopNotify = vi.fn(() => true);
    const bridge = desktopMock({ notify: desktopNotify });
    Object.defineProperty(window, "personaDesktop", { value: bridge, configurable: true });
    pushGrading = true;
    badgeCount = 0;
    sessions = [
      { machine_id: "m", session_id: "a", title: "Agent A", cwd: "/p", status: "working", state: "working" },
      { machine_id: "m", session_id: "b", title: "Agent B", cwd: "/p", status: "working", state: "working" },
    ];
    await act(async () => root.render(<Probe />));
    expect(bridge.setBadge).toHaveBeenLastCalledWith(0);

    // P1 card: no native popup, Mac Dock badge stays 0.
    sessions = [
      { machine_id: "m", session_id: "a", title: "Agent A", cwd: "/p", status: "needs_you", state: "needs_you" },
      { machine_id: "m", session_id: "b", title: "Agent B", cwd: "/p", status: "working", state: "working" },
    ];
    interactions = [{ id: "ix_p1", machine_id: "m", session_id: "a", kind: "permission", summary: "grep logs", push_level: "P1" }];
    await act(async () => root.render(<Probe />));
    expect(desktopNotify).not.toHaveBeenCalled();
    expect(bridge.setBadge).toHaveBeenLastCalledWith(0);

    // P0 card: native popup fires and Mac Dock badge updates.
    badgeCount = 1;
    sessions = [
      { machine_id: "m", session_id: "a", title: "Agent A", cwd: "/p", status: "needs_you", state: "needs_you" },
      { machine_id: "m", session_id: "b", title: "Agent B", cwd: "/p", status: "needs_you", state: "needs_you" },
    ];
    interactions = [
      ...interactions,
      { id: "ix_p0", machine_id: "m", session_id: "b", kind: "question", summary: "Ship release?", push_level: "P0" },
    ];
    await act(async () => root.render(<Probe />));
    expect(desktopNotify).toHaveBeenCalledOnce();
    expect(bridge.setBadge).toHaveBeenLastCalledWith(1);
    localStorage.removeItem("persona.systemNotify");
    vi.restoreAllMocks();
  });
});
