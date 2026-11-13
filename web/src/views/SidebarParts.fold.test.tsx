// @vitest-environment jsdom
// The Agents title row's fold-all button (#6447): every click changes what is on screen, and only the list under the
// controls. It folds the groups to their headings and opens them; with the list folded under the title it opens the
// list and every group; in the chat-list view and with no groups it is not there (a first fix folded the whole block,
// controls and all). Its tooltip names the action, with no second line. The groups here stand in for the sidebar's: headings
// driven by useFolded; SidebarFoldAll.test.tsx clicks the real one.
import { act, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { t } from "../i18n";
import { AGENTS_FOLD } from "../lib/sidebar-api";
import { FoldScope, useFolded } from "../lib/use-folded";
import { Icon, type IconName } from "../ui/icons";
import { GroupFoldButton } from "./SidebarParts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Group({ k, initial }: { k: string; initial: boolean }) {
  const [folded, toggle] = useFolded(k, initial);
  return <div data-sidebar-group-heading><button type="button" aria-expanded={!folded} onClick={toggle}>{k}</button></div>;
}

/** The Agents block in small: its controls, then its groups, inert while the block is folded (as Sidebar.tsx's). */
function Block({ groups, foldable = true, flat = false, blockFolded = false }: { groups: { k: string; folded?: boolean }[]; foldable?: boolean; flat?: boolean; blockFolded?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const [folded, setFolded] = useState(blockFolded);
  return (
    <div ref={ref}>
      <div data-sidebar-agents-controls>
        <span data-block={folded ? "folded" : "open"} />
        <GroupFoldButton groups={ref} onAction={() => {}} block={foldable ? { folded, toggle: () => setFolded((f) => !f) } : undefined} flat={flat} />
      </div>
      <div inert={folded || undefined}>
        <FoldScope.Provider value={AGENTS_FOLD}>{groups.map((g) => <Group key={g.k} k={g.k} initial={g.folded ?? false} />)}</FoldScope.Provider>
      </div>
    </div>
  );
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (el: React.ReactElement) => {
  await act(async () => root.render(el));
  await flush();
};
const button = () => host.querySelector<HTMLButtonElement>("[data-testid=agents-fold-all]");
/** The glyph an icon draws, and the one a button shows: corners in (Collapse all) or out (Expand all). */
const glyph = (name: IconName) => renderToStaticMarkup(<Icon name={name} />).match(/ d="([^"]+)"/)?.[1];
const shows = (b: Element | null | undefined) => b?.querySelector("path")?.getAttribute("d");
/** The tooltip after the button: its name, nothing more (#6447). */
const tip = () => host.querySelector<HTMLElement>("[data-testid=agents-fold-all-tip]")?.textContent;
const block = () => host.querySelector("[data-block]")!.getAttribute("data-block");
const opened = () => [...host.querySelectorAll("[data-sidebar-group-heading] button")].map((b) => b.getAttribute("aria-expanded"));
const press = async () => {
  await act(async () => button()!.click());
  await flush();
};

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("the Agents fold-all button", () => {
  it("folds every group while any is open, and opens them all once none is", async () => {
    await render(<Block groups={[{ k: "machine:a" }, { k: "machine:b", folded: true }]} />);
    expect(button()?.getAttribute("aria-label")).toBe(t("sidebar.fold_all"));
    expect(tip()).toBe(t("sidebar.fold_all"));
    expect(shows(button())).toBe(glyph("foldToggle"));
    await press();
    expect(opened()).toEqual(["false", "false"]);
    expect(button()?.getAttribute("aria-label")).toBe(t("sidebar.open_all"));
    expect(tip()).toBe(t("sidebar.open_all"));
    // folded, its corners point out: the next click opens
    expect(shows(button())).toBe(glyph("unfoldToggle"));
    await press();
    expect(opened()).toEqual(["true", "true"]);
    expect(shows(button())).toBe(glyph("foldToggle"));
    expect(block()).toBe("open");
  });

  it("is not there in the chat-list view, whose only groups are its end sections, so it never folds the block", async () => {
    // before #6447 a click there opened Recent, out of sight at the list's end; the first fix folded the whole block
    await render(<Block flat groups={[{ k: "recent", folded: true }]} />);
    expect(button()).toBeNull();
    expect(block()).toBe("open");
    expect(opened()).toEqual(["false"]);
  });

  it("is not there while the list has no groups, where a click once changed only its own label", async () => {
    await render(<Block groups={[]} />);
    expect(button()).toBeNull();
    expect(block()).toBe("open");
  });

  it("opens the list folded under its title together with every group in it", async () => {
    // before #6447 the click opened the groups inside the hidden list, and the list stayed folded
    await render(<Block blockFolded groups={[{ k: "machine:a", folded: true }, { k: "machine:b", folded: true }]} />);
    expect(button()?.getAttribute("aria-label")).toBe(t("sidebar.open_all"));
    expect(shows(button())).toBe(glyph("unfoldToggle"));
    expect(tip()).toBe(t("sidebar.open_all"));
    await press();
    expect(block()).toBe("open");
    expect(opened()).toEqual(["true", "true"]);
    expect(button()?.getAttribute("aria-label")).toBe(t("sidebar.fold_all"));
  });

  it("acts on the groups alone where the list cannot fold under its title (the phone's Agents page)", async () => {
    await render(<Block foldable={false} groups={[{ k: "machine:a" }]} />);
    await press();
    expect(opened()).toEqual(["false"]);
    expect(button()?.getAttribute("aria-label")).toBe(t("sidebar.open_all"));
    await render(<Block foldable={false} flat groups={[{ k: "recent", folded: true }]} />);
    expect(button()).toBeNull();
  });

  it("opens a list folded under its title in the chat-list view, and then has nothing left to do", async () => {
    await render(<Block blockFolded flat groups={[{ k: "recent", folded: true }]} />);
    expect(button()?.getAttribute("aria-label")).toBe(t("sidebar.open_all"));
    await press();
    expect(block()).toBe("open");
    expect(button()).toBeNull();
  });

  it("shows its name in a tooltip right after it, with no line explaining it", async () => {
    await render(<Block groups={[{ k: "machine:a" }]} />);
    const el = host.querySelector<HTMLElement>("[data-testid=agents-fold-all-tip]")!;
    expect(button()!.nextElementSibling).toBe(el);
    expect(el.textContent).toBe(t("sidebar.fold_all"));
    expect(el.getAttribute("aria-hidden")).toBe("true"); // the button's aria-label already says it
    expect(button()!.hasAttribute("aria-describedby")).toBe(false);
    expect(button()!.hasAttribute("title")).toBe(false);
  });
});
