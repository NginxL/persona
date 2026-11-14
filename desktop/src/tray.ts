import { app, Menu, Tray, nativeImage } from "electron";
import type { BrowserWindow } from "electron";
import { PERSONA_TRAY_ICON } from "./badge-art.js";

/** Closing a window leaves the signed-in renderer alive, so notifications and presence do not depend on a visible tab. */
export function closeToTray(window: BrowserWindow, quitting: () => boolean): void {
  window.on("close", event => {
    if (quitting()) return;
    event.preventDefault();
    window.hide();
  });
  if (process.platform === "darwin") {
    app.on("activate", () => window.show());
  }
}

export function installTray(open: () => void, quit: () => void): Tray {
  // Resolve the current main window at click time: the local setup window may have closed before it existed.
  const icon = nativeImage.createFromDataURL(PERSONA_TRAY_ICON);
  const tray = new Tray(icon.resize({ width: 16, height: 16 }));
  tray.setToolTip("Persona");
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: "Open Persona", click: open },
    { type: "separator" },
    { label: "Quit Persona", click: quit },
  ]));
  tray.on("click", open);
  return tray;
}
