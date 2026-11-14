import { app, ipcMain, Notification } from "electron";
import { setBadge } from "./badge.js";
import type { BrowserWindow } from "electron";
import { inHub } from "./urls.js";
import { notificationRoute } from "./contract.cjs";
import type { HubAddress } from "./urls.js";

function text(value: unknown, maximum: number): string {
  return typeof value === "string" ? value.trim().slice(0, maximum) : "";
}

export class DesktopNotifications {
  private readonly notifications = new Map<string, Notification>();
  constructor(private readonly open: (route: string) => void) {}

  show(value: unknown, hub: HubAddress | undefined): boolean {
    if (!value || typeof value !== "object") return false;
    const v = value as Record<string, unknown>;
    const title = text(v.title, 200), body = text(v.body, 500), tag = text(v.tag, 300);
    const route = typeof v.route === "string" ? notificationRoute(v.route) : undefined;
    if (!title || !tag || !route || !hub) return false;
    const target = new URL(route, hub.url);
    if (!inHub(hub, target)) return false;
    this.close(tag);
    const notification = new Notification({ title, body, silent: v.silent === true });
    this.notifications.set(tag, notification);
    notification.on("click", () => this.open(route));
    notification.on("close", () => {
      if (this.notifications.get(tag) === notification) this.notifications.delete(tag);
    });
    try {
      notification.show();
      return true;
    } catch {
      this.close(tag);
      return false;
    }
  }

  close(tag: string): void {
    this.notifications.get(tag)?.close();
    this.notifications.delete(tag);
  }

  closeAll(): void {
    for (const tag of [...this.notifications.keys()]) this.close(tag);
  }
}

export function installDesktopIPC(window: BrowserWindow, deps: {
  readonly hub: () => HubAddress | undefined;
  readonly notifications: DesktopNotifications;
  readonly presence: () => { active: boolean; notifies: boolean };
}): () => void {
  const authorized = (event: Electron.IpcMainEvent | Electron.IpcMainInvokeEvent): boolean => {
    const hub = deps.hub();
    return event.sender === window.webContents && !window.isDestroyed() && event.senderFrame === event.sender.mainFrame && !!hub && inHub(hub, event.sender.getURL());
  };
  const names = ["desktop:notification-permission", "desktop:badge", "desktop:notify", "desktop:notifications-clear", "desktop:notification-close", "desktop:presence"];
  for (const name of names) ipcMain.removeAllListeners(name);
  for (const name of ["desktop:login-get", "desktop:login-set"]) ipcMain.removeHandler(name);
  ipcMain.on("desktop:notification-permission", event => {
    event.returnValue = authorized(event) && Notification.isSupported() ? "granted" : "denied";
  });
  ipcMain.on("desktop:badge", (event, value: unknown) => {
    if (!authorized(event)) return;
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0 || value > 1_000_000) return;
    setBadge(window, value);
  });
  ipcMain.on("desktop:notify", (event, value: unknown) => {
    event.returnValue = authorized(event) ? deps.notifications.show(value, deps.hub()) : false;
  });
  ipcMain.on("desktop:notifications-clear", event => {
    if (authorized(event)) deps.notifications.closeAll();
  });
  ipcMain.on("desktop:notification-close", (event, value: unknown) => {
    if (!authorized(event) || typeof value !== "string") return;
    deps.notifications.close(value);
  });
  ipcMain.on("desktop:presence", event => {
    event.returnValue = authorized(event) ? deps.presence() : { active: false, notifies: false };
  });
  ipcMain.handle("desktop:login-get", event => {
    if (!authorized(event)) return { enabled: false };
    const settings = app.getLoginItemSettings();
    return { enabled: settings.openAtLogin, requiresApproval: process.platform === "darwin" && settings.status === "requires-approval" };
  });
  ipcMain.handle("desktop:login-set", (event, enabled: unknown) => {
    if (!authorized(event)) return { enabled: false };
    if (typeof enabled !== "boolean") throw new TypeError("Launch at login expects a boolean");
    app.setLoginItemSettings({ openAtLogin: enabled });
    const settings = app.getLoginItemSettings();
    return { enabled: settings.openAtLogin, requiresApproval: process.platform === "darwin" && settings.status === "requires-approval" };
  });
  return () => {
    for (const name of names) ipcMain.removeAllListeners(name);
    for (const name of ["desktop:login-get", "desktop:login-set"]) ipcMain.removeHandler(name);
    deps.notifications.closeAll();
  };
}
