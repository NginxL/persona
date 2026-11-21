// @vitest-environment jsdom
// Settings › Model providers wires provider import links (#5420): a page opened with one opens Add on it (the link
// handed to the wizard, forgotten here once taken and again when the wizard closes); a Persona provider that can be
// shared has ⋯ › "Copy import link" in its detail dialog, which gives way to the copy dialog; and that dialog copies
// a link with no key unless one is typed behind its warning, and forgets the key when it closes.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
});
vi.mock("../lib/store", async (original) => ({
  ...(await original<typeof import("../lib/store")>()),
  useStore: (select: (state: { features: string[]; machines: unknown[]; conn: string; link: { networkUp: boolean; restarting: boolean }; hubName: string; mesh: unknown[] }) => unknown) => select({ features: [], machines: [], conn: "online", link: { networkUp: true, restarting: false }, hubName: "", mesh: [] }),
}));
type WizardProps = { existing: unknown; link?: string | null; onClose: () => void; onImportAlias?: (a: FoundAlias) => void };
type ImportProps = { open: boolean; alias?: string; onClose: () => void };
const seen = vi.hoisted(() => ({ wizard: null as unknown, import: null as unknown }));
vi.mock("./settings/ModelConfiguration", () => ({ ModelConfiguration: () => null }));
vi.mock("./settings/ProviderWizard", () => ({ ProviderWizard: (p: unknown) => ((seen.wizard = p), null) }));
vi.mock("./settings/ProviderImport", () => ({ ProviderImport: (p: unknown) => ((seen.import = p), null) }));
const toasts = vi.hoisted(() => [] as string[]);
vi.mock("../ui/atoms", async (original) => ({ ...(await original<typeof import("../ui/atoms")>()), toast: (text: string) => void toasts.push(text) }));
vi.mock("./settings/LauncherQuota", () => ({ LauncherQuota: () => null }));
vi.mock("../lib/pwa", async (original) => ({ ...(await original<typeof import("../lib/pwa")>()), useInstall: () => "installed" }));
const GW = {
  name: "example-gw", display_name: "Example Gateway", source: "persona", kind: "launcher", agent: "claude", api: "messages", base_url: "https://gateway.example.invalid",
  model: "example-sonnet", models: [{ id: "example-sonnet" }], has_key: true,
};
const CODEX = { name: "example-codex", source: "persona", kind: "launcher", agent: "codex", base_url: "https://api.example.invalid/v1" };
const ALIAS = { name: "cc_alias", source: "zshrc", base_url: "https://gateway.example.invalid", machines: ["laptop"] };
const LIST = vi.hoisted(() => ({ launchers: [] as { name: string }[] }));
vi.mock("../lib/api", async (original) => ({
  ...(await original<typeof import("../lib/api")>()),
  api: (path: string) => {
    if (path === "/launchers") return Promise.resolve({ launchers: LIST.launchers });
    const one = LIST.launchers.find((l) => path === `/launchers/${l.name}`);
    return Promise.resolve(one ? { launcher: one } : {});
  },
}));

import { t } from "../i18n";
import { dropImportLink, holdImportLink, importLinkHeld } from "../lib/import-link";
import type { FoundAlias } from "../lib/provider-paste";
import { loadPages } from "../lib/lazy-page";
import { Settings } from "./Settings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const LINK = "https://persona.example.invalid/settings?tab=models#import=eyJr";
let host: HTMLDivElement;
let root: Root;
let copied: string[];
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const wizard = () => seen.wizard as WizardProps;
// the wizard mounts on its first open (Settings.tsx, lazyOverlay): one never opened is closed, with no link
const wizardOrShut = () => (seen.wizard ?? { existing: null, link: null }) as WizardProps;
const importer = () => seen.import as ImportProps;
const text = () => document.body.textContent ?? "";
const buttons = () => [...document.body.querySelectorAll("button")];
const byLabel = (label: string) => buttons().find((b) => b.getAttribute("aria-label") === label);
const button = (label: string) => {
  const found = buttons().find((b) => b.textContent?.trim() === label);
  expect(found, label).toBeDefined();
  return found!;
};
const click = async (el: Element) => { act(() => (el as HTMLElement).click()); await flush(); };
const copyButton = () => document.body.querySelector<HTMLButtonElement>('[data-testid="provider-link-copy"]')!;
const keySwitch = () => document.body.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-providerlink.key_switch")}"]`)!;
const keyBox = () => document.body.querySelector<HTMLInputElement>('[role="dialog"] input[type="password"], [role="dialog"] input[type="text"]');
function type(el: HTMLInputElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
/** The payload in a copied link, decoded as the hub does it. */
function payloadOf(href: string): unknown {
  const data = new URLSearchParams(new URL(href).hash.slice(1)).get("import")!;
  const bin = atob(data.replace(/-/g, "+").replace(/_/g, "/"));
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0))));
}

async function mount() {
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
  await flush();
}
/** Opens a provider's detail dialog from its row. */
async function detail(title: string) {
  await click(buttons().find((b) => b.textContent?.includes(title))!);
}

beforeAll(() => loadPages());
beforeEach(() => {
  seen.wizard = seen.import = null;
  LIST.launchers = [GW, CODEX, ALIAS];
  copied = [];
  toasts.length = 0;
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: (s: string) => (copied.push(s), Promise.resolve()) } });
  history.replaceState(null, "", "/settings?tab=models");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  dropImportLink();
});

it("filters Model providers by the TRAE CLI command and display name", async () => {
  const legacy = { name: "traex", display_name: "traecli", source: "persona" };
  const named = { name: "trae-cli", display_name: "Team relay", source: "persona" };
  LIST.launchers = [GW, legacy, named];
  await mount();
  const search = host.querySelector<HTMLInputElement>(`input[aria-label="${t("settings.search_providers")}"]`)!;
  expect(search).not.toBeNull();
  type(search, "traecli");
  expect(host.textContent).toContain("TRAE CLI");
  expect(host.textContent).toContain("Team relay");
  expect(host.textContent).not.toContain("Example Gateway");
  type(search, "Team relay");
  expect(host.textContent).toContain("Team relay");
  expect(host.textContent).not.toContain("TRAE CLI");
});

describe("a page opened with an import link", () => {
  it("opens Add on the link, takes it once, and forgets it when the wizard closes", async () => {
    holdImportLink(LINK);
    await mount();
    expect(wizard()).toMatchObject({ existing: "new", link: LINK });
    expect(importLinkHeld()).toBeNull();
    act(() => wizard().onClose());
    await flush();
    expect(wizard()).toMatchObject({ existing: null, link: null });
    // Add by hand afterwards starts as usual
    // the Add button has a short label for phones beside its own
    await click(buttons().find((b) => b.textContent?.startsWith(t("settings.add_provider")))!);
    expect(wizard()).toMatchObject({ existing: "new", link: null });
  });

  it("opens Add on a link opened while the page is up, in place of what is open", async () => {
    await mount();
    await detail("Example Gateway");
    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull();
    await act(async () => holdImportLink(LINK));
    await flush();
    expect(wizard()).toMatchObject({ existing: "new", link: LINK });
    expect(importLinkHeld()).toBeNull();
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
  });

  it("waits for the wizard to close instead of throwing away what is in it, then opens", async () => {
    await mount();
    await click(buttons().find((b) => b.textContent?.startsWith(t("settings.add_provider")))!);
    await act(async () => holdImportLink(LINK));
    await flush();
    expect(wizard()).toMatchObject({ existing: "new", link: null });
    expect(toasts).toEqual([t("settings-providerlink.link_waits")]);
    expect(importLinkHeld()).toBe(LINK);
    act(() => wizard().onClose());
    await flush();
    expect(wizard()).toMatchObject({ existing: "new", link: LINK });
    expect(importLinkHeld()).toBeNull();
  });

  it("waits for the copy dialog to close, keeping the key typed there until then", async () => {
    await mount();
    await detail("Example Gateway");
    await click(byLabel(t("settings-providerlink.more"))!);
    await click(buttons().find((b) => b.textContent?.includes(t("settings-providerlink.copy_link")))!);
    await click(keySwitch());
    type(keyBox()!, "FAKE-EXAMPLE-KEY");
    await act(async () => holdImportLink(LINK));
    await flush();
    expect(keyBox()!.value).toBe("FAKE-EXAMPLE-KEY");
    expect(wizardOrShut()).toMatchObject({ existing: null });
    expect(toasts).toEqual([t("settings-providerlink.link_waits")]);
    await click(button(t("common.cancel")));
    expect(wizard()).toMatchObject({ existing: "new", link: LINK });
  });

  it("waits for the alias import to close, then opens with that import's alias forgotten", async () => {
    await mount();
    await click(buttons().find((b) => b.textContent?.startsWith(t("settings.add_provider")))!);
    act(() => wizard().onImportAlias!({ name: "cc_alias", base_url: "https://gateway.example.invalid", machine: "laptop", computer: "Laptop", online: true }));
    await flush();
    expect(importer()).toMatchObject({ open: true, alias: "cc_alias" });
    await act(async () => holdImportLink(LINK));
    await flush();
    expect(importer()).toMatchObject({ open: true, alias: "cc_alias" });
    expect(wizard()).toMatchObject({ existing: null });
    act(() => importer().onClose());
    await flush();
    expect(wizard()).toMatchObject({ existing: "new", link: LINK });
    expect(importer()).toMatchObject({ open: false, alias: undefined });
  });

  it("opens nothing without one", async () => {
    await mount();
    expect(wizardOrShut()).toMatchObject({ existing: null, link: null });
  });
});

describe("copying a provider's import link", () => {
  it("offers ⋯ › Copy import link only on a provider that can be shared", async () => {
    await mount();
    await detail("Example Gateway");
    expect(byLabel(t("settings-providerlink.more"))).toBeDefined();
    await click(byLabel(t("ui-atoms.close"))!);
    for (const name of ["example-codex", "cc_alias"]) {
      await detail(name);
      expect(text()).toContain(name);
      expect(byLabel(t("settings-providerlink.more")), name).toBeUndefined();
      await click(byLabel(t("ui-atoms.close"))!);
    }
  });

  async function openCopy() {
    await mount();
    await detail("Example Gateway");
    await click(byLabel(t("settings-providerlink.more"))!);
    await click(buttons().find((b) => b.textContent?.includes(t("settings-providerlink.copy_link")))!);
  }

  it("closes the detail dialog and copies a link with no key by default", async () => {
    await openCopy();
    const dialogs = document.body.querySelectorAll('[role="dialog"]');
    expect(dialogs).toHaveLength(1);
    expect(dialogs[0]!.textContent).toContain(t("settings-providerlink.title"));
    expect(text()).toContain(t("settings-providerlink.intro", { name: "Example Gateway" }));
    expect(keySwitch().getAttribute("aria-checked")).toBe("false");
    expect(text()).not.toContain(t("settings-providerlink.key_warning"));
    await act(async () => copyButton().click());
    await flush();
    expect(copied).toHaveLength(1);
    expect(copied[0]!.startsWith(`${location.origin}/settings?tab=models#import=`)).toBe(true);
    expect(payloadOf(copied[0]!)).toEqual({
      kind: "custom", name: "Example Gateway",
      endpoints: [{ protocol: "anthropic-messages", baseUrl: "https://gateway.example.invalid", models: ["example-sonnet"] }],
    });
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
  });

  it("puts a key in only behind the warning, and only a valid one", async () => {
    await openCopy();
    await click(keySwitch());
    expect(text()).toContain(t("settings-providerlink.key_warning"));
    expect(copyButton().disabled).toBe(true);
    type(keyBox()!, "FAKE KEY");
    await flush();
    expect(text()).toContain(t("settings-providerlink.key_invalid"));
    expect(copyButton().disabled).toBe(true);
    type(keyBox()!, " FAKE-EXAMPLE-KEY ");
    await flush();
    expect(text()).not.toContain(t("settings-providerlink.key_invalid"));
    await act(async () => copyButton().click());
    await flush();
    expect(payloadOf(copied[0]!)).toMatchObject({ auth: { method: "apiKey", apiKey: "FAKE-EXAMPLE-KEY" } });
  });

  it("forgets the key when the switch goes off or the dialog closes", async () => {
    await openCopy();
    await click(keySwitch());
    type(keyBox()!, "FAKE-EXAMPLE-KEY");
    await click(keySwitch());
    expect(keyBox()).toBeNull();
    await click(keySwitch());
    expect(keyBox()!.value).toBe("");
    type(keyBox()!, "FAKE-EXAMPLE-KEY");
    await click(button(t("common.cancel")));
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();
    await detail("Example Gateway");
    await click(byLabel(t("settings-providerlink.more"))!);
    await click(buttons().find((b) => b.textContent?.includes(t("settings-providerlink.copy_link")))!);
    expect(keySwitch().getAttribute("aria-checked")).toBe("false");
    expect(keyBox()).toBeNull();
    expect(document.body.innerHTML).not.toContain("FAKE-EXAMPLE-KEY");
  });
});
