// @vitest-environment jsdom
// Terminal settings › Renderer › Labs opens Labs over the page, never away from it (#9877 review): the chat's composer
// stays mounted, so an unsent draft keeps its text, its images (held only in the composer, released when it unmounts)
// and anything being edited, while the choice made in Labs is saved for the next terminal and this one keeps its renderer.
// The real composer, Terminal settings menu, Labs dialog and router, wired as TerminalPanel wires them.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { sendDraft } from "../../lib/drafts";
import { useRoute } from "../../lib/router";
import { t } from "../../i18n";
import { Composer } from "./Composer";
import { TerminalOptions } from "../session/terminal/TerminalOptions";
import type { FeedView, TerminalFeed } from "../session/terminal/feed";
import { LabsDialog } from "../settings/Labs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const key = "labs-m/labs-s";
const session = "/s/labs-m/labs-s";
const revoke = vi.fn();
let host: HTMLDivElement;
let root: Root;

// This terminal opened with Standard; the menu's other rows only read these.
const view = { control: "active", wrap: false, fontSize: 13, mouse: "program", cursorStyle: "block", cursorBlink: false, renderer: "standard", rendererChosen: "standard", rendererFellBack: false } as unknown as FeedView;

/** The session page: its chat's composer and the terminal's gear, menu and Labs dialog (TerminalPanel's wiring). */
function SessionPage() {
  const route = useRoute();
  const [gear, setGear] = useState<HTMLElement | null>(null);
  const [options, setOptions] = useState<HTMLElement | null>(null);
  const [labs, setLabs] = useState(false);
  if (route.name === "settings") return <div data-page="settings" />; // what leaving for Settings would show instead
  return (
    <>
      <Composer draftKey={key} placeholder="" onSend={async () => true} />
      <button ref={setGear} type="button" data-gear onClick={() => setOptions(gear)} />
      {options && (
        <TerminalOptions
          anchor={options}
          feed={{} as TerminalFeed}
          v={view}
          keyBar={false}
          onKeyBar={() => {}}
          onEditShortcuts={() => {}}
          onOpenLabs={() => setLabs(true)}
          onClose={() => setOptions(null)}
        />
      )}
      <LabsDialog open={labs} onClose={() => setLabs(false)} />
    </>
  );
}

beforeEach(() => {
  localStorage.clear();
  history.replaceState(null, "", session);
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as unknown as typeof window.matchMedia;
  URL.createObjectURL = vi.fn(() => "blob:labs/unsent-image");
  URL.revokeObjectURL = revoke;
  revoke.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<SessionPage />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  localStorage.clear();
});

const byText = (text: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === text);
const dialog = () => document.querySelector<HTMLElement>('[data-labs-dialog]');
const imageTile = () => host.querySelector(`button[aria-label="${t("ui-attachmenttray.edit", { name: "unsent.png" })}"]`);

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};

/** Gear › Renderer › Labs, choose WebGL, close the dialog. */
async function chooseWebglInLabs() {
  act(() => host.querySelector<HTMLButtonElement>("[data-gear]")!.click());
  expect(document.body.textContent).toContain(t("session-terminal-terminaloptions.renderer_standard"));
  act(() => byText(t("session-terminal-terminaloptions.renderer_open_labs"))!.click());
  expect(document.querySelector("[data-terminal-renderer]")).toBeNull(); // the menu closed, as for Edit shortcuts
  expect(dialog()).not.toBeNull();
  expect(location.pathname + location.search).toBe(session);
  await settle(); // the form reads the module's document
  act(() => byText(t("settings-labs.terminal_renderer_webgl"))!.click());
  await settle();
  expect(localStorage.getItem("persona.terminal.webgl")).toBe("1");
  expect(dialog()!.textContent).toContain(t("settings-configuration.saved"));
  act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
}

it("keeps the unsent text and the same composer through Labs, with the choice saved", async () => {
  act(() => sendDraft(key, { type: "replace", text: "Unsent words" }));
  const box = host.querySelector("textarea")!;
  await chooseWebglInLabs();
  expect(host.querySelector('[data-page="settings"]')).toBeNull();
  expect(host.querySelector("textarea")).toBe(box); // never unmounted: every bit of the composer's state is as it was
  expect(box.value).toBe("Unsent words");
});

it("keeps an unsent image (not released) through Labs", async () => {
  act(() => sendDraft(key, { type: "image", image: { name: "unsent.png", mime: "image/png", data: btoa("unsent-image-bytes") } }));
  expect(imageTile()).not.toBeNull();
  await chooseWebglInLabs();
  expect(revoke).not.toHaveBeenCalledWith("blob:labs/unsent-image");
  expect(imageTile()).not.toBeNull();
});

it("this terminal still says Standard after choosing WebGL: the choice is for terminals opened next", async () => {
  await chooseWebglInLabs();
  act(() => host.querySelector<HTMLButtonElement>("[data-gear]")!.click());
  expect(document.querySelector("[data-terminal-renderer]")?.getAttribute("data-terminal-renderer")).toBe("standard");
});
