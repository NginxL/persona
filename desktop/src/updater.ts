import { app } from "electron";
import electronUpdater from "electron-updater";
const { autoUpdater } = electronUpdater;

/** Public GitHub releases only; no repository token is embedded in the client. */
export function installAutoUpdate(): void {
  if (!app.isPackaged || process.platform === "linux" || process.env.PERSONA_DESKTOP_DISABLE_UPDATE === "1") return;
  autoUpdater.autoDownload = true;
  autoUpdater.autoInstallOnAppQuit = true;
  autoUpdater.on("error", error => {
    // The web UI remains available; the next normal scheduled check may retry without overwriting user work.
    console.warn("Desktop update check failed:", error.message);
  });
  void autoUpdater.checkForUpdates();
}
