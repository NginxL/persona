import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import type { MenuItemConstructorOptions } from "electron";
import { appMenuTemplate } from "../src/app-menu.js";
import { loadStrings } from "../src/strings.js";

const locales = join(import.meta.dirname, "..", "..", "src", "locales");

const LABELS = {
  en: {
    app_menu_about: "About Persona",
    app_menu_hide: "Hide Persona",
    tray_quit: "Quit Persona",
  },
  "zh-Hans": {
    app_menu_about: "关于 Persona",
    app_menu_hide: "隐藏 Persona",
    tray_quit: "退出 Persona",
  },
  ja: {
    app_menu_about: "Personaについて",
    app_menu_hide: "Personaを隠す",
    tray_quit: "Persona を終了",
  },
} as const;

const NEW_STRING_KEYS = ["app_menu_about", "app_menu_hide"] as const;

type Locale = keyof typeof LABELS;
type AppMenuLabels = (typeof LABELS)[Locale];

interface MenuDump {
  label: string;
  role: string | null;
  type: string;
  submenu?: MenuDump[];
}

/** Electron 45.3.2's default macOS menu, dumped as label/role/type. */
const BASELINE: MenuDump[] = [
  {
    label: "persona-desktop", role: "appmenu", type: "submenu", submenu: [
      { label: "About persona-desktop", role: "about", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Services", role: "services", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Hide persona-desktop", role: "hide", type: "normal" },
      { label: "Hide Others", role: "hideothers", type: "normal" },
      { label: "Show All", role: "unhide", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Quit persona-desktop", role: "quit", type: "normal" },
    ],
  },
  {
    label: "File", role: "filemenu", type: "submenu", submenu: [
      { label: "Close Window", role: "close", type: "normal" },
    ],
  },
  {
    label: "Edit", role: "editmenu", type: "submenu", submenu: [
      { label: "Undo", role: "undo", type: "normal" },
      { label: "Redo", role: "redo", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Cut", role: "cut", type: "normal" },
      { label: "Copy", role: "copy", type: "normal" },
      { label: "Paste", role: "paste", type: "normal" },
      { label: "Paste and Match Style", role: "pasteandmatchstyle", type: "normal" },
      { label: "Delete", role: "delete", type: "normal" },
      { label: "Select All", role: "selectall", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      {
        label: "Substitutions", role: null, type: "submenu", submenu: [
          { label: "Show Substitutions", role: "showsubstitutions", type: "normal" },
          { label: "separator", role: null, type: "separator" },
          { label: "Smart Quotes", role: "togglesmartquotes", type: "normal" },
          { label: "Smart Dashes", role: "togglesmartdashes", type: "normal" },
          { label: "Text Replacement", role: "toggletextreplacement", type: "normal" },
        ],
      },
      {
        label: "Speech", role: null, type: "submenu", submenu: [
          { label: "Start Speaking", role: "startspeaking", type: "normal" },
          { label: "Stop Speaking", role: "stopspeaking", type: "normal" },
        ],
      },
    ],
  },
  {
    label: "View", role: "viewmenu", type: "submenu", submenu: [
      { label: "Reload", role: "reload", type: "normal" },
      { label: "Force Reload", role: "forcereload", type: "normal" },
      { label: "Toggle Developer Tools", role: "toggledevtools", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Actual Size", role: "resetzoom", type: "normal" },
      { label: "Zoom In", role: "zoomin", type: "normal" },
      { label: "Zoom Out", role: "zoomout", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Toggle Full Screen", role: "togglefullscreen", type: "normal" },
    ],
  },
  {
    label: "Window", role: "windowmenu", type: "submenu", submenu: [
      { label: "Minimize", role: "minimize", type: "normal" },
      { label: "Zoom", role: "zoom", type: "normal" },
      { label: "separator", role: null, type: "separator" },
      { label: "Bring All to Front", role: "front", type: "normal" },
    ],
  },
];

function defaultRoleLabels(items: MenuDump[], labels = new Map<string, string>()): Map<string, string> {
  for (const item of items) {
    if (item.role) labels.set(item.role, item.label);
    if (item.submenu) defaultRoleLabels(item.submenu, labels);
  }
  return labels;
}

const DEFAULT_ROLE_LABELS = defaultRoleLabels(BASELINE);

/**
 * Project the template onto the runtime baseline without testing Electron itself.
 * An undefined role label means Electron supplies its localized default; the
 * English baseline stands in for that default in this structural comparison.
 */
function dump(template: readonly MenuItemConstructorOptions[]): MenuDump[] {
  return template.map(item => {
    const role = item.role?.toLowerCase() ?? null;
    return {
      label: item.label ?? (role ? DEFAULT_ROLE_LABELS.get(role) : undefined) ?? item.type ?? "",
      role,
      type: item.type ?? (item.submenu ? "submenu" : "normal"),
      ...(Array.isArray(item.submenu) ? { submenu: dump(item.submenu) } : {}),
    };
  });
}

function assertRoleLabels(template: readonly MenuItemConstructorOptions[], labels: AppMenuLabels): void {
  assert.equal(template[0]?.label, "persona-desktop", "the top appMenu preserves appName");
  const localized = new Map([
    ["0.submenu.0", labels.app_menu_about],
    ["0.submenu.4", labels.app_menu_hide],
    ["0.submenu.8", labels.tray_quit],
  ]);
  const visit = (items: readonly MenuItemConstructorOptions[], path = "") => {
    items.forEach((item, index) => {
      const itemPath = `${path}${index}`;
      if (itemPath !== "0" && item.role) {
        const expected = localized.get(itemPath);
        if (expected) {
          assert.equal(item.label, expected, `${itemPath}: localized Persona label`);
        } else {
          assert.equal(item.label, undefined, `${itemPath}: unchanged role-based items must leave label undefined so Electron can localize it`);
        }
      }
      if (Array.isArray(item.submenu)) visit(item.submenu, `${itemPath}.submenu.`);
    });
  };
  visit(template);
}

function expectedMenu(labels: AppMenuLabels): MenuDump[] {
  const expected = structuredClone(BASELINE);
  const applicationItems = expected[0]?.submenu;
  assert.ok(applicationItems);
  applicationItems[0]!.label = labels.app_menu_about;
  applicationItems[4]!.label = labels.app_menu_hide;
  applicationItems[8]!.label = labels.tray_quit;
  return expected;
}

function labelChanges(before: MenuDump[], after: MenuDump[], path = ""): string[] {
  return before.flatMap((item, index) => {
    const other = after[index];
    assert.ok(other);
    const itemPath = `${path}${index}`;
    const changed = item.label === other.label ? [] : [`${itemPath}.label`];
    return [
      ...changed,
      ...labelChanges(item.submenu ?? [], other.submenu ?? [], `${itemPath}.submenu.`),
    ];
  });
}

test("the macOS menu preserves every default item and changes only About, Hide and Quit in every language", async () => {
  for (const locale of ["en", "zh-Hans", "ja"] as const) {
    const strings = await loadStrings(locales, locale);
    const template = appMenuTemplate("darwin", "persona-desktop", strings);
    assert.ok(template, `${locale}: macOS must receive an explicit application menu`);
    assertRoleLabels(template, LABELS[locale]);
    const actual = dump(template);
    assert.deepEqual(actual, expectedMenu(LABELS[locale]), locale);
    if (locale === "en") {
      assert.deepEqual(labelChanges(BASELINE, actual), [
        "0.submenu.0.label",
        "0.submenu.4.label",
        "0.submenu.8.label",
      ]);
    }
  }
});

test("an application menu template exists on macOS only", async () => {
  const strings = await loadStrings(locales, "en");
  assert.equal(appMenuTemplate("win32", "persona-desktop", strings), null);
  assert.equal(appMenuTemplate("linux", "persona-desktop", strings), null);
  assert.ok(appMenuTemplate("darwin", "persona-desktop", strings));
});

test("all locale files have the same keys, translate About and Hide, and reuse the tray Quit label", async () => {
  const read = async (locale: Locale) => JSON.parse(await readFile(join(locales, `${locale}.json`), "utf8")) as Record<string, string>;
  const english = await read("en");
  const englishKeys = Object.keys(english).sort();
  for (const locale of ["en", "zh-Hans", "ja"] as const) {
    const strings = await read(locale);
    assert.deepEqual(Object.keys(strings).sort(), englishKeys, locale);
    for (const key of NEW_STRING_KEYS) assert.equal(strings[key], LABELS[locale][key], `${locale}:${key}`);
    assert.equal(strings.tray_quit, LABELS[locale].tray_quit, `${locale}:tray_quit`);
  }
});
