// @vitest-environment jsdom
// The side panel's Files / Git / file viewer / link panels load when first opened (#2673): the panel shows the loading
// look while the chunk comes, then the panel; a chunk that will not load shows a way to retry, never a blank panel.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const gate = vi.hoisted(() => {
  let open!: () => void;
  const ready = new Promise<void>((r) => (open = r));
  return { ready, open: () => open() };
});
vi.mock("./session/FilesPanel", async () => {
  await gate.ready;
  return { FilesPanel: ({ cwd }: { cwd: string }) => <p data-testid="files">文件 {cwd}</p> };
});
vi.mock("./session/GitPanel", () => {
  throw new TypeError("Failed to fetch dynamically imported module: /assets/GitPanel-abc.js");
});
vi.mock("./session/ChangesPanel", () => ({
  ChangesPanel: ({ opened }: { opened: { at?: { path: string } } }) => <p data-testid="changes">{opened.at?.path}</p>,
}));
const bg = vi.hoisted(() => ({ mounts: 0 }));
vi.mock("./chat/BackgroundPanel", async () => {
  const { useEffect } = await import("react");
  return {
    BackgroundPanel: ({ opened }: { opened: { run?: string } }) => {
      useEffect(() => void bg.mounts++, []);
      return <p data-testid="background">{opened.run ?? "list"}</p>;
    },
  };
});
import { registerComposer } from "../lib/links";
import { closePanel, openPanel } from "../lib/panel";
import { CHAT_MIN, panelWidth, SidePanel } from "./SidePanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
// a lazy chunk resolves after an unknown number of ticks (the first test also pays for the module's transform)
const until = async (ok: () => boolean) => {
  for (let i = 0; i < 200 && !ok(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 10)); });
};

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => closePanel());
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("侧边面板按需加载", () => {
  it("shows the loading look until the panel's chunk is there, then the panel", async () => {
    act(() => openPanel({ kind: "files", mid: "m1", sid: "s1", cwd: "/work/demo" }));
    act(() => root.render(<SidePanel />));
    await act(async () => { await Promise.resolve(); });
    expect(host.querySelector('[data-testid="files"]')).toBeNull();
    expect(host.textContent).toContain("加载");
    act(() => gate.open());
    await until(() => Boolean(host.querySelector('[data-testid="files"]')));
    expect(host.querySelector('[data-testid="files"]')?.textContent).toBe("文件 /work/demo");
  });

  // vitest wraps the factory's error, so this is the boundary's general message; which errors reload is PageBoundary.test
  it("shows a way to retry, inside the panel, when a panel's chunk does not load", async () => {
    sessionStorage.setItem("persona.chunkReload", String(Date.now()));
    vi.spyOn(console, "error").mockImplementation(() => {});
    act(() => openPanel({ kind: "git", mid: "m1", sid: "s1", cwd: "/work/demo" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[role="alert"]')));
    const alert = host.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("重新加载");
    // the panel's frame (its resize handle) is still there: the error stays inside the panel
    expect(host.querySelector('aside [role="separator"]')).not.toBeNull();
  });
});

// #5421: an Agent's Background tasks, one panel per session: asking for another run keeps it and goes to that run
describe("the Background tasks panel", () => {
  it("is labelled, and asking for another run of the session does not draw it anew", async () => {
    bg.mounts = 0;
    act(() => openPanel({ kind: "background", mid: "m1", sid: "s1", run: "w1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="background"]')));
    expect(host.querySelector("aside")?.getAttribute("aria-label")).toBe("后台任务");
    expect(host.querySelector('[data-testid="background"]')?.textContent).toBe("w1");
    act(() => openPanel({ kind: "background", mid: "m1", sid: "s1", run: "w2" }));
    expect(host.querySelector('[data-testid="background"]')?.textContent).toBe("w2");
    expect(bg.mounts).toBe(1);
    act(() => openPanel({ kind: "background", mid: "m1", sid: "s2" }));
    expect(host.querySelector('[data-testid="background"]')?.textContent).toBe("list");
    expect(bg.mounts).toBe(2);
  });
});

describe("the Changes panel", () => {
  it("loads on demand at the selected file", async () => {
    act(() => openPanel({ kind: "changes", mid: "m1", sid: "s1", at: { path: "/work/a.ts", call: "c1" } } as Parameters<typeof openPanel>[0]));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="changes"]')));
    expect(host.querySelector("aside")?.getAttribute("aria-label")).toBe("变更");
    expect(host.querySelector('[data-testid="changes"]')?.textContent).toBe("/work/a.ts");
  });
});

// #5148: the panel never takes the conversation beside it under CHAT_MIN, whatever was dragged or remembered
describe("侧边面板的宽度", () => {
  it("keeps the width asked for when there is room", () => {
    expect(panelWidth(600, 1440 - 284)).toBe(600);
  });
  it("leaves the conversation CHAT_MIN, at 1180 wide with the sidebar", () => {
    const room = 1180 - 284;
    expect(panelWidth(600, room)).toBe(room - CHAT_MIN);
    expect(panelWidth(5000, room)).toBe(room - CHAT_MIN);
  });
  it("gives more once the sidebar folds", () => {
    expect(panelWidth(600, 1180 - 56)).toBe(600);
  });
  it("is never under its own minimum", () => {
    expect(panelWidth(100, 2000)).toBe(360);
    expect(panelWidth(600, 700)).toBe(360);
  });
});

// #5149: the resize handle works from the keyboard, and Esc in the panel closes it and gives focus back
describe("侧边面板的键盘操作", () => {
  const separator = () => host.querySelector<HTMLElement>('aside [role="separator"]')!;
  const press = (el: Element, key: string) => {
    const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
    act(() => { el.dispatchEvent(e); });
    return e;
  };
  let opener: HTMLButtonElement;

  beforeEach(() => {
    localStorage.removeItem("persona.panelWidth");
    opener = document.createElement("button");
    opener.textContent = "文件";
    document.body.append(opener);
    opener.focus();
    act(() => openPanel({ kind: "files", mid: "m1", sid: "s1", cwd: "/work/demo" }));
    act(() => root.render(<SidePanel />));
  });
  afterEach(() => {
    opener.remove();
    document.querySelectorAll("[data-test-menu]").forEach((m) => m.remove());
  });

  it("puts the resize handle in the tab order with its width", () => {
    const sep = separator();
    expect(sep.tabIndex).toBe(0);
    expect(sep.getAttribute("aria-valuenow")).toBe("600");
    expect(sep.getAttribute("aria-valuemin")).toBe("360");
    expect(sep.getAttribute("aria-valuemax")).toBe(String(Math.round(innerWidth * 0.72)));
    expect(sep.getAttribute("aria-controls")).toBe(host.querySelector("aside")!.id);
  });

  it("widens with ArrowLeft, narrows with ArrowRight, and remembers it", () => {
    expect(press(separator(), "ArrowLeft").defaultPrevented).toBe(true);
    expect(separator().getAttribute("aria-valuenow")).toBe("616");
    expect(localStorage.getItem("persona.panelWidth")).toBe("616");
    press(separator(), "ArrowRight");
    press(separator(), "ArrowRight");
    expect(separator().getAttribute("aria-valuenow")).toBe("584");
    expect(localStorage.getItem("persona.panelWidth")).toBe("584");
  });

  it("goes to the ends with Home / End and back to the default with Enter", () => {
    press(separator(), "End");
    expect(separator().getAttribute("aria-valuenow")).toBe(separator().getAttribute("aria-valuemax"));
    press(separator(), "Home");
    expect(separator().getAttribute("aria-valuenow")).toBe("360");
    press(separator(), "Enter");
    expect(separator().getAttribute("aria-valuenow")).toBe("600");
    expect(localStorage.getItem("persona.panelWidth")).toBeNull();
  });

  it("closes on Esc from inside the panel and gives focus back to what opened it", () => {
    separator().focus();
    const e = press(separator(), "Escape");
    expect(e.defaultPrevented).toBe(true);
    expect(host.querySelector("aside")).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it("returns to an overview row when its background detail replaces an open panel", async () => {
    const row = document.createElement("button");
    row.textContent = "Overview run";
    document.body.append(row);
    try {
      row.focus();
      act(() => openPanel({ kind: "background", mid: "m1", sid: "s1", run: "w1", from: "overview" }));
      await until(() => Boolean(host.querySelector('[data-testid="background"]')));
      separator().focus();
      press(separator(), "Escape");
      expect(host.querySelector("aside")).toBeNull();
      expect(document.activeElement).toBe(row);
    } finally {
      row.remove();
    }
  });

  it("keeps its Esc from the window, where the conversation's Esc stops a running agent", () => {
    const onWindow = vi.fn();
    addEventListener("keydown", onWindow);
    try {
      separator().focus();
      press(separator(), "Escape");
      expect(host.querySelector("aside")).toBeNull();
      expect(onWindow).not.toHaveBeenCalled();
    } finally {
      removeEventListener("keydown", onWindow);
    }
  });

  it("does not reuse an old opener: opened again with nothing focused, Esc gives focus to the message box", () => {
    act(() => closePanel());
    const input = document.createElement("textarea");
    document.body.append(input);
    const unregister = registerComposer({ key: "m1/s1", send: async () => true, focus: () => input.focus() });
    try {
      (document.activeElement as HTMLElement | null)?.blur();
      expect(document.activeElement).toBe(document.body);
      act(() => openPanel({ kind: "files", mid: "m1", sid: "s1", cwd: "/work/demo" }));
      separator().focus();
      press(separator(), "Escape");
      expect(host.querySelector("aside")).toBeNull();
      expect(document.activeElement).toBe(input);
    } finally {
      unregister();
      input.remove();
    }
  });

  it("leaves Esc to an open menu", () => {
    const menu = document.createElement("div");
    menu.setAttribute("role", "menu");
    menu.dataset.testMenu = "";
    document.body.append(menu);
    separator().focus();
    expect(press(separator(), "Escape").defaultPrevented).toBe(false);
    expect(host.querySelector("aside")).not.toBeNull();
  });

  it("leaves an Esc something in the panel already took", () => {
    separator().focus();
    const e = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    e.preventDefault();
    act(() => { separator().dispatchEvent(e); });
    expect(host.querySelector("aside")).not.toBeNull();
  });
});
