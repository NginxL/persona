import type { MenuItemConstructorOptions } from "electron";
import type { DesktopStrings } from "./strings.js";

/** Electron 45.3.2's default macOS menu; unchanged role labels stay with Electron, and only the three app-named labels are localized. */
export function appMenuTemplate(platform: NodeJS.Platform, appName: string, strings: DesktopStrings): MenuItemConstructorOptions[] | null {
  if (platform !== "darwin") return null;
  return [
    {
      label: appName, role: "appMenu", type: "submenu", submenu: [
        { label: strings.app_menu_about ?? "About Persona", role: "about", type: "normal" },
        { type: "separator" },
        { role: "services", type: "normal" },
        { type: "separator" },
        { label: strings.app_menu_hide ?? "Hide Persona", role: "hide", type: "normal" },
        { role: "hideOthers", type: "normal" },
        { role: "unhide", type: "normal" },
        { type: "separator" },
        { label: strings.tray_quit ?? "Quit Persona", role: "quit", type: "normal" },
      ],
    },
    {
      role: "fileMenu", type: "submenu", submenu: [
        { role: "close", type: "normal" },
      ],
    },
    {
      role: "editMenu", type: "submenu", submenu: [
        { role: "undo", type: "normal" },
        { role: "redo", type: "normal" },
        { type: "separator" },
        { role: "cut", type: "normal" },
        { role: "copy", type: "normal" },
        { role: "paste", type: "normal" },
        { role: "pasteAndMatchStyle", type: "normal" },
        { role: "delete", type: "normal" },
        { role: "selectAll", type: "normal" },
        { type: "separator" },
        {
          label: "Substitutions", type: "submenu", submenu: [
            { role: "showSubstitutions", type: "normal" },
            { type: "separator" },
            { role: "toggleSmartQuotes", type: "normal" },
            { role: "toggleSmartDashes", type: "normal" },
            { role: "toggleTextReplacement", type: "normal" },
          ],
        },
        {
          label: "Speech", type: "submenu", submenu: [
            { role: "startSpeaking", type: "normal" },
            { role: "stopSpeaking", type: "normal" },
          ],
        },
      ],
    },
    {
      role: "viewMenu", type: "submenu", submenu: [
        { role: "reload", type: "normal" },
        { role: "forceReload", type: "normal" },
        { role: "toggleDevTools", type: "normal" },
        { type: "separator" },
        { role: "resetZoom", type: "normal" },
        { role: "zoomIn", type: "normal" },
        { role: "zoomOut", type: "normal" },
        { type: "separator" },
        { role: "togglefullscreen", type: "normal" },
      ],
    },
    {
      role: "windowMenu", type: "submenu", submenu: [
        { role: "minimize", type: "normal" },
        { role: "zoom", type: "normal" },
        { type: "separator" },
        { role: "front", type: "normal" },
      ],
    },
  ];
}
