import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { stateColor } from "../lib/format";
import type { AgentState } from "../lib/types";
import { Button, Collapse, copyText, Dialog, IconButton, Loading, Menu, StatePill, useMenu } from "./atoms";
import { markOf } from "./StatusMark";
import { STATE_LABEL } from "../lib/state-labels";

vi.mock("react", async (importOriginal) => ({
  ...await importOriginal<typeof import("react")>(),
  useState: vi.fn((initial) => [initial, () => {}]),
}));

vi.mock("react-dom", async (importOriginal) => ({
  ...await importOriginal<typeof import("react-dom")>(),
  createPortal: (children: React.ReactNode) => children,
}));

afterEach(() => vi.unstubAllGlobals());

function renderMenu(state?: AgentState) {
  vi.stubGlobal("document", { body: {} });
  return renderToStaticMarkup(
    <Menu
      anchor={null}
      onClose={() => {}}
      items={[{ key: "agent", label: "#1 · 示例任务", state, checked: true, onSelect: () => {} }]}
    />,
  );
}

describe("state pill semantic fills", () => {
  it("gives completed unread work a green success tint", () => {
    expect(renderToStaticMarkup(<StatePill state="unread" />)).toContain("bg-green-tint text-green");
  });

  it("keeps pending replies and Needs you neutral", () => {
    const pending = renderToStaticMarkup(<StatePill state="idle" awaiting />);
    expect(pending).toContain("bg-field text-green");
    expect(pending).not.toContain("bg-green-tint");
    const needs = renderToStaticMarkup(<StatePill state="needs_you" />);
    expect(needs).toContain("bg-field text-attention");
    expect(needs).not.toMatch(/bg-(red|orange|green)-tint/);
  });
});

describe("menu anchor", () => {
  it("captures the button before a deferred state updater loses currentTarget", () => {
    const setAnchor = vi.fn();
    vi.mocked(useState).mockReturnValueOnce([undefined, setAnchor]);
    const menu = useMenu();
    const target = {} as HTMLElement;
    const event = { currentTarget: target as HTMLElement | null };
    menu.open(event as React.MouseEvent<HTMLElement>);
    event.currentTarget = null;
    const update = setAnchor.mock.calls[0][0] as (previous: HTMLElement | null) => HTMLElement | null;
    expect(update(null)).toBe(target);
    expect(update(target)).toBeNull();
  });
});

describe("agent status in menus", () => {
  it.each(Object.keys(STATE_LABEL) as AgentState[])("shows the %s dot and readable status", (state) => {
    const html = renderMenu(state);
    expect(html).toContain(`aria-label="${STATE_LABEL[state]}"`);
    expect(html).toContain(`--mark-color:${stateColor(state)}`);
    expect(html).toContain(`data-shape="${markOf(state).shape}"`);
    expect(html).toContain(`>${STATE_LABEL[state]}</span>`);
    expect(html).toContain(`title="#1 · 示例任务 · ${STATE_LABEL[state]}"`);
    expect(html).toContain("#1 · 示例任务</span>");
    expect(html).toContain("whitespace-nowrap");
  });

  it("does not add status UI to ordinary menu items", () => {
    const html = renderMenu();
    expect(html).not.toContain('role="img"');
    expect(html).not.toContain("已看");
    expect(html).not.toContain("title=");
    expect(html).toContain("#1 · 示例任务</span>");
  });

  it("gives a finger rows of at least 44pt", () => {
    expect(renderMenu()).toContain("pointer-coarse:min-h-11");
  });
});

describe("dialog", () => {
  function renderDialog(footer?: React.ReactNode) {
    vi.stubGlobal("document", { body: {} });
    return renderToStaticMarkup(
      <Dialog open onClose={() => {}} title="新建 Agent" width={520} footer={footer}>
        <p>正文</p>
      </Dialog>,
    );
  }

  it("is labelled by its title", () => {
    const html = renderDialog();
    const id = /<h2 id="([^"]+)"/.exec(html)?.[1];
    expect(id).toBeTruthy();
    expect(html).toContain(`aria-labelledby="${id}"`);
    expect(html).toContain('aria-modal="true"');
  });

  it("keeps the wide card's width, through a variable the phone's sheet can lift", () => {
    const html = renderDialog();
    expect(html).toContain("--dialog-w:520px");
    expect(html).toContain("max-w-(--dialog-w)");
    expect(html).toContain("max-sm:max-w-none");
    expect(html).not.toContain("max-width:520px");
  });

  it("is a sheet on a phone: on the keyboard's frame, at most 90% of it, sliding up unless motion is reduced", () => {
    const html = renderDialog();
    expect(html).toContain("max-sm:top-(--kb-top)");
    expect(html).toContain("max-sm:h-(--kb-h)");
    expect(html).toContain("max-sm:items-end");
    expect(html).toContain("max-sm:max-h-[calc(var(--kb-h)*0.9)]");
    // a transition (cut to 1ms by the reduced-motion rule in styles.css), not the wide card's keyframes
    expect(html).toContain("max-sm:starting:translate-y-full");
    expect(html).toContain("max-sm:animate-none");
  });

  it("scrolls its body and pins the footer above the home indicator", () => {
    const html = renderDialog(<button type="button">启动</button>);
    expect(html).toContain('class="min-h-0 flex-1 overflow-y-auto overscroll-contain');
    expect(html).toMatch(/flex shrink-0 [^"]* max-sm:pb-\[calc\(10px\+env\(safe-area-inset-bottom\)\)\]">.*启动/);
    // marked for the toasts on a phone, which keep clear of it
    expect(html).toMatch(/<div data-dialog-footer="true" class="flex shrink-0 [^"]*">.*启动/);
    // without a footer the body keeps the safe area
    expect(renderDialog()).toContain("max-sm:pb-[calc(16px+env(safe-area-inset-bottom))]");
  });

  it("is a column on a wide screen too, no taller than the room under its 12vh and above the pb-8", () => {
    const html = renderDialog(<button type="button">启动</button>);
    expect(html).toMatch(/role="dialog"[^>]*class="flex max-h-\[calc\(88vh-\(--spacing\(8\)\)\)\] [^"]*flex-col/);
    expect(html).toContain("pt-[12vh] pb-8");
  });
});

describe("busy states for screen readers", () => {
  it("announces a loading label as a status", () => {
    const html = renderToStaticMarkup(<Loading label="正在读取…" />);
    expect(html).toMatch(/^<div role="status"[^>]*>.*正在读取….*<\/div>$/);
  });

  it("marks a busy button aria-busy, and leaves an idle one alone", () => {
    expect(renderToStaticMarkup(<Button busy>保存</Button>)).toContain('aria-busy="true"');
    expect(renderToStaticMarkup(<Button>保存</Button>)).not.toContain("aria-busy");
  });
});

describe("icon button", () => {
  it("has a 44pt hit area on touch screens (touch-hit), whatever its size", () => {
    const html = renderToStaticMarkup(<IconButton icon="x" label="关闭" size={20} />);
    expect(html).toContain("touch-hit");
    expect(html).toContain("width:20px");
  });
});

describe("menu with the new row kinds", () => {
  function render(items: React.ComponentProps<typeof Menu>["items"], extra: Partial<React.ComponentProps<typeof Menu>> = {}) {
    vi.stubGlobal("document", { body: {} });
    return renderToStaticMarkup(<Menu anchor={null} onClose={() => {}} items={items} {...extra} />);
  }

  it("shows a row's letter, and ⌫ on the close action", () => {
    const html = render([
      { key: "copy", label: "复制路径", accel: "c", onSelect: () => {} },
      { key: "close", label: "关闭标签页", danger: true, closes: true, confirm: "确认关闭", onSelect: () => {} },
    ]);
    expect(html).toContain('aria-keyshortcuts="c"');
    expect(html).toMatch(/>C<\/span>/);
    expect(html).toContain("⌫");
    // the question is not asked until the row is chosen
    expect(html).toContain("关闭标签页");
    expect(html).not.toContain("确认关闭");
  });

  it("marks a row with a submenu", () => {
    const html = render([{ key: "git", label: "Git", children: [], onSelect: () => {} }]);
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).toContain("-rotate-90");
  });

  it("is a popover to read when every row is a note (#4054)", () => {
    const html = render([
      { key: "share", label: "还剩 61% 上下文", hint: "78K / 200K tokens", note: true, onSelect: () => {} },
      { key: "hint", label: "用完会自动压缩", note: true, onSelect: () => {} },
    ]);
    expect(html).toContain('role="dialog"');
    expect(html).toContain('aria-label="还剩 61% 上下文"');
    expect(html).not.toContain("menuitem");
    expect(html.match(/data-menu-note/g)).toHaveLength(2);
    expect(html).toContain("78K / 200K tokens");
    expect(render([{ key: "a", label: "复制目录", onSelect: () => {} }])).toContain('role="menu"');
  });

  it("is the same markup as before for a plain row", () => {
    const html = render([{ key: "a", label: "复制目录", icon: "folder", onSelect: () => {} }]);
    expect(html).not.toContain("aria-haspopup");
    expect(html).not.toContain("aria-keyshortcuts");
    expect(html).not.toContain("bg-red-tint");
    expect(html).toContain('data-menu="');
  });

  it("puts a row's detail under its label, wrapping, as the row's description (#5584)", () => {
    const html = render([{ key: "plan", label: "Plan only", detail: "Reads code and writes a plan; acts only once you approve", checked: true, onSelect: () => {} }]);
    const id = html.match(/aria-describedby="([^"]+)"/)?.[1];
    expect(id).toBeTruthy();
    expect(html).toMatch(new RegExp(`id="${id}" class="[^"]*break-words[^"]*">Reads code and writes a plan; acts only once you approve<`));
    expect(html).toMatch(/class="block break-words">Plan only</);
    expect(html).not.toContain("truncate");
    // a row without a detail keeps its one truncating line and no description
    const plain = render([{ key: "a", label: "复制目录", hint: "⌘C", checked: false, onSelect: () => {} }]);
    expect(plain).toContain("truncate");
    expect(plain).not.toContain("aria-describedby");
  });

  it("says which choice of a set is selected, and leaves a toggle's check as it was", () => {
    const html = render([
      { key: "a", label: "Ask", radio: true, checked: false, onSelect: () => {} },
      { key: "b", label: "Auto", radio: true, checked: true, onSelect: () => {} },
    ]);
    expect(html.match(/role="menuitemradio"/g)).toHaveLength(2);
    expect(html.match(/aria-checked="true"/g)).toHaveLength(1);
    expect(html.match(/aria-checked="false"/g)).toHaveLength(1);
    expect(html).not.toContain("aria-current");
    // an on/off toggle or a filter (several may be on) is not called "current" or one of a set
    const toggles = render([
      { key: "hidden", label: "Show hidden", checked: true, onSelect: () => {} },
      { key: "select", label: "Select", checked: true, onSelect: () => {} },
    ]);
    expect(toggles.match(/role="menuitem"/g)).toHaveLength(2);
    expect(toggles).not.toMatch(/aria-current|aria-checked|menuitemradio/);
  });

  it("opens at a pointer with no anchor", () => {
    expect(render([{ key: "a", label: "复制", onSelect: () => {} }], { point: { x: 10, y: 20 } })).toContain("复制");
  });
});

describe("Collapse", () => {
  // A grid item's automatic minimum size is its content's min-content: without min-w-0 one child that cannot break
  // (a long URL) widens the grid's whole column past its container (#3262, the Inbox list scrolling sideways).
  it("its content can never widen the grid: the item carries min-w-0 whether open, unclipped or closed", () => {
    for (const html of [
      renderToStaticMarkup(<Collapse open><div>内容</div></Collapse>),
      renderToStaticMarkup(<Collapse open unclipped><div>内容</div></Collapse>),
      renderToStaticMarkup(<Collapse open={false}><div>内容</div></Collapse>),
    ]) {
      expect(html).toContain("min-w-0");
    }
  });

  it("an open unclipped one drops its clip, a closed one keeps it", () => {
    expect(renderToStaticMarkup(<Collapse open unclipped><div>x</div></Collapse>)).not.toContain("overflow-hidden");
    expect(renderToStaticMarkup(<Collapse open={false}><div>x</div></Collapse>)).toContain("overflow-hidden");
  });
});

describe("copyText", () => {
  /** A page without the async clipboard (plain http): the textarea way, whose execCommand says whether it worked. */
  function insecurePage(copies: boolean) {
    const ta = { value: "", style: {}, select: vi.fn(), remove: vi.fn() };
    vi.stubGlobal("navigator", {});
    vi.stubGlobal("document", { createElement: () => ta, body: { appendChild: vi.fn() }, execCommand: vi.fn(() => copies) });
    return ta;
  }

  it("the async clipboard when there is one", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    expect(await copyText("ABCD-1234")).toBe(true);
    expect(writeText).toHaveBeenCalledWith("ABCD-1234");
  });

  it("over plain http it falls back to the textarea, and removes it", async () => {
    const ta = insecurePage(true);
    expect(await copyText("ABCD-1234", { done: "验证码已复制" })).toBe(true);
    expect(ta.value).toBe("ABCD-1234");
    expect(ta.remove).toHaveBeenCalled();
  });

  it("says it did not copy when the fallback fails too", async () => {
    const ta = insecurePage(false);
    expect(await copyText("ABCD-1234", { failed: "复制失败" })).toBe(false);
    expect(ta.remove).toHaveBeenCalled();
  });
});
