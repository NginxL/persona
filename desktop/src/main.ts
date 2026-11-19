import { join } from "node:path";
import { BrowserWindow, Menu, app, dialog, protocol, screen, shell, powerMonitor, Notification } from "electron";
import { parseDeepLink, parseHubAddress, externalBrowserURL, inHub } from "./urls.js";
import type { HubAddress } from "./urls.js";
import { appPairingURL } from "./pairing.js";
import { installDesktopIPC, DesktopNotifications } from "./ipc.js";
import { PageRoutes } from "./page-routes.js";
import { installSetupIPC } from "./setup-ipc.js";
import { JSONStore } from "./store.js";
import { restoreWindow, DEFAULT_WINDOW } from "./window-state.js";
import { closeToTray, installTray } from "./tray.js";
import type { DesktopTray } from "./tray.js";
import { desktopLocale, loadStrings } from "./strings.js";
import { appMenuTemplate } from "./app-menu.js";
import { updateMenu } from "./update-state.js";
import type { UpdateMenuItem } from "./update-state.js";
import { hubPartition } from "./sessions.js";
import { installSmokeCapture } from "./smoke.js";
import { installAutoUpdate } from "./updater.js";
import { HubWindowLifecycle } from "./hub-window-lifecycle.js";
import { desktopPresence } from "./presence.js";
import { NotchIsland, installIslandProtocol } from "./island.js";
import { ISLAND_SCHEME } from "./island-page.js";

installSmokeCapture();
// Electron takes this registration once, before the app is ready: every scheme of the app's own pages belongs here.
protocol.registerSchemesAsPrivileged([{ scheme: ISLAND_SCHEME, privileges: { standard: true, secure: true } }]);

let mainWindow: BrowserWindow | undefined;
let hub: HubAddress | undefined;
let quitting = false;
let pendingDeepLink: string | undefined;
let setupWindow: BrowserWindow | undefined;
let screenLocked = false;
let presenceTimer: ReturnType<typeof setInterval> | undefined;
const hubWindow = new HubWindowLifecycle<BrowserWindow | undefined>();
const store = () => JSONStore.inUserData(app.getPath("userData"));
// Each hub window's routes wait until its page listens (page-routes.ts, #10282): no route is sent around them.
const pageRoutes = new WeakMap<BrowserWindow, PageRoutes>();
function openRoute(window: BrowserWindow | undefined, route: string): void {
  if (window && !window.isDestroyed()) pageRoutes.get(window)?.open(route);
}
// The island at the MacBook notch (#7669): off until the person turns it on; a clicked row opens as a notification does.
const island = new NotchIsland(route => {
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.show();
    mainWindow.focus();
    openRoute(mainWindow, route);
  } else void showMain();
});

function navigateFromDeepLink(raw: string): void {
  const link = parseDeepLink(raw);
  if (!link) return;
  if (!hub) { pendingDeepLink = raw; void showSetup(); return; }
  if (link.kind === "open" && link.route) openRoute(mainWindow, link.route);
  if (link.kind === "pair-app") {
    const pairing = appPairingURL(hub, raw);
    if (pairing) {
      void mainWindow?.loadURL(pairing);
    }
  }
  if (link.kind === "pair-link") {
    // Link pairing belongs to the separate local app, not to the hub-loaded page. No automatic pairing here.
    void showSetup();
  }
  mainWindow?.show();
}

function secureHubWindow(window: BrowserWindow): void {
  const contents = window.webContents;
  contents.setWindowOpenHandler(({ url }) => {
    const external = externalBrowserURL(url);
    if (external && (!hub || !inHub(hub, url))) void shell.openExternal(external);
    else if (hub && inHub(hub, url)) void window.loadURL(url);
    return { action: "deny" };
  });
  contents.on("will-navigate", (event, url) => {
    if (hub && inHub(hub, url)) return;
    event.preventDefault();
    const external = externalBrowserURL(url);
    if (external) void shell.openExternal(external);
  });
  contents.on("will-redirect", (event, url) => {
    if (hub && inHub(hub, url)) return;
    event.preventDefault();
    const external = externalBrowserURL(url);
    if (external) void shell.openExternal(external);
  });
  contents.on("will-attach-webview", event => event.preventDefault());
  contents.session.setPermissionRequestHandler((_contents, permission, callback) => {
    // The web notification preference still resolves; the OS notification itself comes from Electron.
    callback(permission === "notifications");
  });
  contents.session.setPermissionCheckHandler((_contents, permission) => permission === "notifications");
}

// A different hub needs a new window and its own persistent session, not navigation of the old window.
async function replaceMainWindow(next: HubAddress, close: (window: BrowserWindow) => void): Promise<void> {
  const previous = mainWindow;
  if (!previous || previous.isDestroyed()) { hubWindow.replace(); return; }
  // Sub-path containment is not equality: /old/ is inside a new / hub, but still needs its own origin-isolated session.
  if (inHub(next, previous.webContents.getURL()) && (() => { try { return parseHubAddress(previous.webContents.getURL()).url.href === next.url.href; } catch { return false; } })()) return;
  // Setting hub first lets closeToTray destroy this window without putting the old hub back on screen.
  hub = next;
  hubWindow.replace();
  const closed = new Promise<void>(done => previous.once("closed", done));
  close(previous);
  await closed;
  mainWindow = undefined;
}

async function showMain(): Promise<BrowserWindow | undefined> {
  return hubWindow.show(async (): Promise<BrowserWindow | undefined> => {
  const generation = hubWindow.generation;
  const owner = hub;
  if (!owner) {
    if (!setupWindow || setupWindow.isDestroyed()) await showSetup();
    return undefined;
  }
  if (mainWindow && !mainWindow.isDestroyed()) { mainWindow.show(); return mainWindow; }
  const state = restoreWindow((await store().read<{ window?: unknown }>())?.window, screen.getAllDisplays().map(d => d.workArea));
  if (owner !== hub || generation !== hubWindow.generation) return undefined;
  const current = new BrowserWindow({
    ...DEFAULT_WINDOW,
    ...state,
    show: false,
    title: "Persona",
    autoHideMenuBar: true,
    backgroundColor: "#111111",
    webPreferences: {
      preload: join(import.meta.dirname, "preload.cjs"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      allowRunningInsecureContent: false,
      devTools: !app.isPackaged,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      webviewTag: false,
      backgroundThrottling: false,
      spellcheck: true,
      ...(owner ? { partition: hubPartition(owner) } : {}),
    },
  });
  mainWindow = current;
  const routes = new PageRoutes(route => {
    if (!current.isDestroyed()) current.webContents.send("desktop:notification-open", route);
  });
  pageRoutes.set(current, routes);
  const page = current.webContents.id; // whose island state is whose (NotchIsland.forget)
  if (!app.isPackaged && process.env.PERSONA_DESKTOP_DEBUG === "1") {
    const log = (value: unknown) => {
      const line = `${typeof value === "string" ? value : JSON.stringify(value)}\n`;
      void import("node:fs/promises").then(({ appendFile }) => appendFile(process.env.PERSONA_DESKTOP_DEBUG_LOG ?? "/tmp/persona-desktop-debug.log", line, "utf8"));
    };
    current.webContents.openDevTools({ mode: "detach" });
    current.webContents.on("console-message", event => log(event.message));
    current.webContents.on("preload-error", (_event, _path, error) => log(error));
    current.webContents.on("render-process-gone", (_event, details) => log(details));
    current.webContents.on("did-finish-load", () => {
      void current.webContents.executeJavaScript("({title: document.title, text: document.body.innerText.slice(0, 500), html: document.body.innerHTML.slice(0, 500)})").then(value => log(value));
    });
  }
  secureHubWindow(current);
  closeToTray(current, () => quitting);
  const removeIPC = installDesktopIPC(current, {
    // The partition is immutable, so derive authorization from this window's actual origin, never a later global hub.
    hub: () => {
      const url = current.webContents.getURL();
      if (owner && inHub(owner, url)) return owner;
      try { return parseHubAddress(url); } catch { return undefined; }
    },
    presence: () => desktopPresence(powerMonitor.getSystemIdleTime(), screenLocked, Notification.isSupported()),
    openLocalSettings: () => { void showSetup(); },
    routes,
    island: {
      status: () => island.status(),
      set: async enabled => {
        // Saved first: a choice that can't be kept changes nothing, so the island and the switch saying why agree.
        await store().update({ island: enabled && island.supported });
        return island.setEnabled(enabled);
      },
      state: value => island.setState(value, page),
    },
    notifications: new DesktopNotifications(route => {
      current.show();
      current.focus();
      routes.open(route);
    }),
  });
  const save = () => {
    if (current.isDestroyed() || current.isMinimized()) return;
    const maximized = current.isMaximized();
    const bounds = maximized ? current.getNormalBounds() : current.getBounds();
    void store().saveWindow({ ...bounds, maximized });
  };
  current.on("resize", save);
  current.on("move", save);
  current.on("maximize", save);
  current.on("unmaximize", save);
  current.on("close", save);
  current.once("ready-to-show", () => {
    if (owner !== hub || generation !== hubWindow.generation || current.isDestroyed()) return;
    if (state.maximized) current.maximize();
    current.show();
    if (pendingDeepLink) navigateFromDeepLink(pendingDeepLink);
    pendingDeepLink = undefined;
  });
  // The island's state is this page's (#7669): once the page is gone (a hub change destroys this window, its renderer
  // crashed), the island forgets it, so nothing of this hub stays at the notch; a newer page's state stays.
  current.webContents.on("render-process-gone", () => island.forget(page));
  current.once("closed", () => { removeIPC(); island.forget(page); if (mainWindow === current) mainWindow = undefined; });
  if (owner) {
    try {
      await current.loadURL(owner.url.href);
    } catch (error) {
      removeIPC();
      if (mainWindow === current) mainWindow = undefined;
      if (!current.isDestroyed()) current.destroy();
      if (owner === hub && generation === hubWindow.generation) throw error;
      return undefined;
    }
    if (owner !== hub || generation !== hubWindow.generation || current.isDestroyed()) {
      removeIPC();
      if (mainWindow === current) mainWindow = undefined;
      if (!current.isDestroyed()) current.destroy();
      return undefined;
    }
    return current;
  }
  return undefined;
  });
}

async function showSetup(): Promise<void> {
  if (setupWindow && !setupWindow.isDestroyed()) { setupWindow.show(); setupWindow.focus(); return; }
  const setup = new BrowserWindow({
    width: 720,
    height: 640,
    title: "Persona setup",
    autoHideMenuBar: true,
    webPreferences: {
      preload: join(import.meta.dirname, "setup-preload.cjs"),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      devTools: !app.isPackaged,
      webviewTag: false,
    },
  });
  setupWindow = setup;
  let connectQueue: Promise<unknown> = Promise.resolve();
  setup.setMenu(null);
  setup.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  setup.webContents.on("will-navigate", event => event.preventDefault());
  setup.once("closed", () => {
    removeIPC();
    if (setupWindow === setup) setupWindow = undefined;
    // Quitting closes setup too; opening another window here would cancel the quit (#9458).
    if (quitting) return;
    // Do not re-enter a connection whose setup was closed while its creation was still pending.
    if (!mainWindow && !hubWindow.pending) void showMain().catch(() => { if (!setupWindow || setupWindow.isDestroyed()) void showSetup().catch(() => {}); });
  });
  const removeIPC = installSetupIPC(setup, {
    hub: () => hub,
    connect: async (raw: string) => {
      const next = parseHubAddress(raw);
      const previous = connectQueue;
      let generation: number | undefined;
      const run = async (): Promise<{ ok: boolean; error?: string }> => {
      try {
        await replaceMainWindow(next, window => window.destroy());
        const generationValue = hubWindow.generation;
        generation = generationValue;
        hub = next;
        const loadedWindow = await showMain();
        // An obsolete or destroyed load cannot persist or close setup.
        if (generation !== hubWindow.generation || hub?.url.href !== next.url.href ||
            !loadedWindow || loadedWindow !== mainWindow || loadedWindow.isDestroyed()) {
          return { ok: false, error: "The hub connection was replaced before it finished." };
        }
        try {
          await store().update({ hub: next.url.href });
        } catch (error) {
          // Do not leave a live window using a hub that the setup state no longer owns.
          if (generation === hubWindow.generation && hub?.url.href === next.url.href) {
            hub = undefined;
            hubWindow.replace();
            const failedWindow = mainWindow;
            mainWindow = undefined;
            if (failedWindow && !failedWindow.isDestroyed()) failedWindow.destroy();
          }
          if (!setupWindow || setupWindow.isDestroyed()) void showSetup().catch(() => {});
          return { ok: false, error: error instanceof Error ? error.message : "Could not save the hub connection." };
        }
        if (generation !== hubWindow.generation || hub?.url.href !== next.url.href ||
            loadedWindow !== mainWindow || loadedWindow.isDestroyed()) {
          return { ok: false, error: "The hub connection was replaced before it finished." };
        }
        if (!setup.isDestroyed()) setup.close();
        return { ok: true };
      } catch (error) {
        if (generation !== hubWindow.generation || hub?.url.href !== next.url.href) {
          return { ok: false, error: "The hub connection was replaced before it finished." };
        }
        const failedWindow = mainWindow;
        if (failedWindow && !failedWindow.isDestroyed()) {
          // A failed connection must not leave a live window after its global owner is cleared.
          mainWindow = undefined;
          hubWindow.replace();
          failedWindow.destroy();
        }
        hub = undefined;
        hubWindow.replace();
        if (!setupWindow || setupWindow.isDestroyed()) void showSetup().catch(() => {});
        return { ok: false, error: error instanceof Error ? error.message : "Could not connect to the hub." };
      }
      };
      const result = previous.catch(() => {}).then(run);
      connectQueue = result.catch(() => {});
      return result;
    },
  });
  await setup.loadFile(join(import.meta.dirname, "setup.html"));
}

const locked = app.requestSingleInstanceLock();
if (!locked) app.quit();
else {
  // Windows and Linux start the app with the link as an argument when it is not running (macOS: open-url below); it
  // waits like any link that comes before the window, and its route then waits for the page (#10282).
  pendingDeepLink = process.argv.find(value => /^(?:persona|jarvis):\/\//i.test(value));
  app.on("second-instance", (_event, argv) => {
    const raw = argv.find(value => /^(?:persona|jarvis):\/\//i.test(value));
    if (raw) navigateFromDeepLink(raw);
    mainWindow?.show();
    mainWindow?.focus();
  });
  app.on("open-url", (event, raw) => {
    event.preventDefault();
    if (mainWindow) navigateFromDeepLink(raw);
    else pendingDeepLink = raw;
  });
  app.whenReady().then(async () => {
    installIslandProtocol();
    const publishPresence = () => {
      if (!mainWindow || mainWindow.isDestroyed()) return;
      const presence = desktopPresence(powerMonitor.getSystemIdleTime(), screenLocked, Notification.isSupported());
      mainWindow.webContents.send("desktop:presence-changed", presence);
    };
    powerMonitor.on("lock-screen", () => { screenLocked = true; publishPresence(); });
    powerMonitor.on("unlock-screen", () => { screenLocked = false; publishPresence(); });
    powerMonitor.on("suspend", () => { screenLocked = true; publishPresence(); });
    // Resume by itself is not proof that the person unlocked the computer; only unlock-screen restores presence.
    powerMonitor.on("resume", publishPresence);
    presenceTimer = setInterval(publishPresence, 30_000);
    const strings = await loadStrings(join(import.meta.dirname, "locales"), desktopLocale(app.getLocale()));
    const applicationMenu = appMenuTemplate(process.platform, app.name, strings);
    if (applicationMenu) {
      Menu.setApplicationMenu(Menu.buildFromTemplate(applicationMenu));
      app.setAboutPanelOptions({ applicationName: "Persona" });
    }
    // The first check starts here, before any window loads; its state is kept until the tray exists to show it.
    let tray: DesktopTray | undefined;
    let updateItems: readonly UpdateMenuItem[] = [];
    const updates = installAutoUpdate({
      changed: state => { updateItems = updateMenu(state, strings); tray?.setUpdate(updateItems); },
      prepareQuit: () => { quitting = true; },
    });
    const saved = await store().read<{ hub?: string; island?: boolean }>();
    try { hub = parseHubAddress(process.env.PERSONA_HUB_URL ?? saved?.hub ?? ""); } catch { hub = undefined; }
    if (saved?.island === true) island.setEnabled(true);
    for (const scheme of ["persona", "jarvis"]) {
      if (!app.isDefaultProtocolClient(scheme)) app.setAsDefaultProtocolClient(scheme);
    }
    if (hub) await showMain().catch(() => { hub = undefined; hubWindow.replace(); });
    if (!hub) await showSetup();
    tray = installTray({ open: strings.tray_open ?? "Open Persona", quit: strings.tray_quit ?? "Quit Persona" }, () => {
      if (mainWindow && !mainWindow.isDestroyed()) { mainWindow.show(); mainWindow.focus(); }
      else void showMain();
    }, () => { quitting = true; app.quit(); }, action => {
      if (action === "restart") updates?.restart();
      else updates?.check();
    });
    tray.setUpdate(updateItems);
  }).catch(error => {
    dialog.showErrorBox("Persona could not start", error instanceof Error ? error.message : String(error));
    app.quit();
  });
}
app.on("before-quit", () => { quitting = true; if (presenceTimer) clearInterval(presenceTimer); });
app.on("window-all-closed", () => {
  // Closing to the tray deliberately keeps the app alive; an explicit Quit exits on every platform.
});

// Developing Persona (#9675): the shell's own record while the page's switch is on (dev-diagnostics.ts).
import { installDevDiagnostics } from "./dev-diagnostics.js";
installDevDiagnostics();
