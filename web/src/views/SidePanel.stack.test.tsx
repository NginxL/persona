// @vitest-environment jsdom
// Side-panel conditional Back (←) button & narrow-screen browser Back stack behavior (#13174):
// single-level panels render zero extra chrome; drilling in from inside the side panel shows ← in PanelHeader (and
// nested task/agent headers), clicking ← pops to the previous panel, × closes everything, and narrow-screen browser
// Back pops one level at a time before closing the panel.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PanelHeader } from "../ui/PanelHeader";
import { Dialog, IconButton, Menu, useMenu } from "../ui/atoms";
import { t } from "../i18n";
import { closePanel, getPanel, getPanelHistory, openPanel, useSidePanelBack } from "../lib/panel";

vi.mock("./assistant/TaskPanel", () => ({
  TaskPanel: ({ todoId, onClose }: { todoId: string; onClose: () => void }) => {
    const back = useSidePanelBack();
    const menu = useMenu();
    return (
      <div data-testid="task-panel">
        <header>
          {back && <IconButton icon="arrowLeft" label={t("sidepanel.back")} onClick={back} />}
          <span>Task {todoId}</span>
          <IconButton icon="more" label="More" onClick={menu.open} />
          <IconButton icon="x" label={t("sidepanel.close")} onClick={onClose} />
        </header>
        {menu.anchor && (
          <Menu
            anchor={menu.anchor}
            onClose={menu.close}
            items={[
              {
                key: "review",
                label: "Review changes",
                onSelect: () => openPanel({ kind: "review", mid: "m1", sid: "s1", cwd: "/work" }),
              },
            ]}
          />
        )}
        <button
          type="button"
          data-testid="open-zip"
          onClick={() => openPanel({ kind: "file", mid: "m1", path: "/work/archive.zip", cwd: "/work", sid: "s1" })}
        >
          archive.zip
        </button>
        <button
          type="button"
          data-testid="open-nested-agent"
          onClick={() => openPanel({ kind: "agent", mid: "m1", sid: "s2" })}
        >
          Agent s2
        </button>
      </div>
    );
  },
  AgentPanel: ({ mid, sid, onClose }: { mid: string; sid: string; onClose: () => void }) => {
    const back = useSidePanelBack();
    return (
      <div data-testid="agent-panel">
        <header>
          {back && <IconButton icon="arrowLeft" label={t("sidepanel.back")} onClick={back} />}
          <span>Agent {mid}/{sid}</span>
          <IconButton icon="x" label={t("sidepanel.close")} onClick={onClose} />
        </header>
        <button
          type="button"
          data-testid="open-review"
          onClick={() => openPanel({ kind: "review", mid, sid, cwd: "/work" })}
        >
          Review
        </button>
      </div>
    );
  },
}));

vi.mock("./session/FileViewer", () => ({
  FileViewerPanel: ({ path, onClose }: { path: string; onClose: () => void }) => (
    <div data-testid="file-viewer">
      <PanelHeader icon="read" title={path} onClose={onClose} />
      <button
        type="button"
        data-testid="show-in-files"
        onClick={() => openPanel({ kind: "files", mid: "m1", sid: "s1", cwd: "/work", path: "/work" })}
      >
        Show in Files
      </button>
    </div>
  ),
}));

vi.mock("./session/FilesPanel", () => ({
  FilesPanel: ({ cwd, onClose }: { cwd: string; onClose: () => void }) => (
    <div data-testid="files-panel">
      <PanelHeader icon="folder" title={t("sidepanel.files")} subtitle={cwd} onClose={onClose} />
    </div>
  ),
}));

vi.mock("./session/ReviewPanel", () => ({
  ReviewPanel: ({ cwd, onClose }: { cwd: string; onClose: () => void }) => {
    const filesMenu = useMenu();
    const [dialogOpen, setDialogOpen] = useState(false);
    return (
      <div data-testid="review-panel">
        <PanelHeader icon="pullRequest" title={t("sidepanel.review")} subtitle={cwd} onClose={onClose} />
        <button type="button" data-testid="open-review-menu" onClick={filesMenu.open}>
          Review files
        </button>
        {filesMenu.anchor && (
          <Menu
            anchor={filesMenu.anchor}
            onClose={filesMenu.close}
            items={[{ key: "a", label: "src/a.ts", onSelect: () => {} }]}
          />
        )}
        <button type="button" data-testid="open-review-dialog" onClick={() => setDialogOpen(true)}>
          Open dialog
        </button>
        <Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} title="Confirm">
          <p>Dialog body</p>
        </Dialog>
      </div>
    );
  },
}));

vi.mock("./link/LinkPanel", () => ({
  LinkPanel: ({ url, onClose }: { url: string; onClose: () => void }) => (
    <div data-testid="link-panel">
      <PanelHeader icon="web" title={url} onClose={onClose} />
    </div>
  ),
}));

import { SidePanel } from "./SidePanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const until = async (ok: () => boolean) => {
  for (let i = 0; i < 200 && !ok(); i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 10));
    });
  }
};

const backButton = () => host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("sidepanel.back")}"]`);
const closeButton = () => host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("sidepanel.close")}"]`);

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

describe("SidePanel back stack & PanelHeader", () => {
  it("renders zero Back chrome for a single-level side panel, and restores prior task panel on Back", async () => {
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));

    // Single-level side panel: no Back button rendered.
    expect(backButton()).toBeNull();

    // Drill from TaskPanel into FileViewerPanel (clicking inside <aside data-side-panel>).
    const openZip = host.querySelector<HTMLButtonElement>('[data-testid="open-zip"]')!;
    act(() => openZip.click());
    await until(() => Boolean(host.querySelector('[data-testid="file-viewer"]')));

    // Back button now appears in PanelHeader with localized aria-label & tooltip.
    const back = backButton();
    expect(back).not.toBeNull();
    expect(back?.getAttribute("title")).toBe(t("sidepanel.back"));
    expect(getPanel()).toEqual({ kind: "file", mid: "m1", path: "/work/archive.zip", cwd: "/work", sid: "s1" });

    // Clicking Back returns to TaskPanel and removes the Back button.
    act(() => back!.click());
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));
    expect(getPanel()).toEqual({ kind: "task", todo: "todo-1" });
    expect(backButton()).toBeNull();
  });

  it("walks back multi-level drill-ins and closes everything on ×", async () => {
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));

    // Task -> File -> Files
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-zip"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="file-viewer"]')));
    expect(backButton()).not.toBeNull();

    act(() => host.querySelector<HTMLButtonElement>('[data-testid="show-in-files"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="files-panel"]')));
    expect(backButton()).not.toBeNull();

    // Clicking Back steps back to FileViewerPanel.
    act(() => backButton()!.click());
    await until(() => Boolean(host.querySelector('[data-testid="file-viewer"]')));
    expect(backButton()).not.toBeNull();

    // Clicking × closes the entire panel and clears the stack.
    act(() => closeButton()!.click());
    expect(host.querySelector("aside")).toBeNull();
    expect(getPanel()).toBeNull();

    // Opening a new panel afterwards starts with an empty stack (no Back button).
    act(() => openPanel({ kind: "link", url: "https://example.com" }));
    await until(() => Boolean(host.querySelector('[data-testid="link-panel"]')));
    expect(backButton()).toBeNull();
  });

  it("shows Back on nested AgentPanel drilled from TaskPanel and clears stack on external open", async () => {
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));

    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-nested-agent"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    expect(backButton()).not.toBeNull();

    // Opening another panel from outside clears the stack -> Back button disappears.
    const outside = document.createElement("button");
    outside.addEventListener("click", () => openPanel({ kind: "link", url: "https://example.com/fresh" }));
    document.body.append(outside);
    try {
      act(() => outside.click());
      await until(() => Boolean(host.querySelector('[data-testid="link-panel"]')));
      expect(backButton()).toBeNull();
    } finally {
      outside.remove();
    }
  });

  it("does not render a side-panel Back button in standalone PanelHeader outside SidePanel", () => {
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => openPanel({ kind: "file", mid: "m1", path: "/work/a.txt" }, { push: true }));

    act(() => root.render(<PanelHeader icon="terminal" title="Terminal" onClose={() => {}} />));
    expect(host.querySelector(`button[aria-label="${t("sidepanel.back")}"]`)).toBeNull();

    // Explicit onBack prop still renders the Back button when requested.
    const onBack = vi.fn();
    act(() => root.render(<PanelHeader icon="terminal" title="Terminal" onBack={onBack} onClose={() => {}} />));
    const btn = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("sidepanel.back")}"]`);
    expect(btn).not.toBeNull();
    act(() => btn!.click());
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("pushes onto the stack when opening from a portaled Menu anchored inside the side panel and restores opener focus on ×", async () => {
    const trigger = document.createElement("button");
    trigger.textContent = "Open task";
    trigger.addEventListener("click", () => openPanel({ kind: "task", todo: "todo-1" }));
    document.body.append(trigger);
    try {
      act(() => root.render(<SidePanel />));
      trigger.focus();
      act(() => trigger.click());
      await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));

      // Open portaled ⋯ Menu from inside <aside data-side-panel> and choose "Review changes".
      const moreBtn = host.querySelector<HTMLButtonElement>('aside button[aria-label="More"]')!;
      act(() => moreBtn.click());
      const menuItem = document.querySelector<HTMLButtonElement>('[data-side-panel][role="menu"] [role="menuitem"]');
      expect(menuItem).not.toBeNull();
      act(() => menuItem!.click());
      await until(() => Boolean(host.querySelector('[data-testid="review-panel"]')));

      expect(getPanelHistory()).toEqual([{ kind: "task", todo: "todo-1" }]);
      expect(backButton()).not.toBeNull();

      // Closing with × after drilling in still returns focus to the original opener button outside the panel.
      const x = closeButton()!;
      x.focus();
      act(() => x.click());
      expect(host.querySelector("aside")).toBeNull();
      expect(document.activeElement).toBe(trigger);
    } finally {
      trigger.remove();
    }
  });
});

describe("narrow-screen browser Back pops stack before closing", () => {
  const realMatchMedia = window.matchMedia;
  const realHistory = window.history;

  afterEach(() => {
    Object.defineProperty(window, "matchMedia", { configurable: true, writable: true, value: realMatchMedia });
    Object.defineProperty(window, "history", { configurable: true, writable: true, value: realHistory });
  });

  function installNarrowAndFakeHistory() {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      writable: true,
      value: (query: string) => ({
        matches: query === "(max-width: 1023px)",
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }),
    });

    const stack: unknown[] = [null];
    let at = 0;
    const fakeHistory = {
      get state() {
        return stack[at];
      },
      get length() {
        return at + 1;
      },
      pushState(s: unknown) {
        stack.splice(at + 1);
        stack.push(s);
        at++;
      },
      replaceState(s: unknown) {
        stack[at] = s;
      },
      back() {
        setTimeout(() => {
          if (at === 0) return;
          at--;
          window.dispatchEvent(new PopStateEvent("popstate", { state: stack[at] }));
        }, 0);
      },
    };
    Object.defineProperty(window, "history", { configurable: true, writable: true, value: fakeHistory });
    return fakeHistory;
  }

  it("pops each drilled level on browser Back before closing the side panel", async () => {
    const hist = installNarrowAndFakeHistory();
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));
    expect(hist.length).toBe(2);

    // Drill Task -> Agent -> Review (stack: [task, agent], current: review)
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-nested-agent"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-review"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="review-panel"]')));
    // Still only one overlay history entry.
    expect(hist.length).toBe(2);

    // 1st Back: Review -> Agent (panel stays open and re-arms Back entry).
    hist.back();
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s2" });
    expect(hist.length).toBe(2);

    // 2nd Back: Agent -> Task (panel stays open and re-arms Back entry).
    hist.back();
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));
    expect(getPanel()).toEqual({ kind: "task", todo: "todo-1" });
    expect(hist.length).toBe(2);

    // 3rd Back: Task -> panel closes cleanly and returns to base history entry.
    hist.back();
    await until(() => host.querySelector("aside") === null);
    expect(getPanel()).toBeNull();
    expect(hist.length).toBe(1);
  });

  it("closes an open Menu above the drilled-in panel before popping the panel stack", async () => {
    const hist = installNarrowAndFakeHistory();
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));

    // Drill Task -> Agent -> Review
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-nested-agent"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-review"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="review-panel"]')));
    expect(hist.length).toBe(2);

    // Open a Menu inside ReviewPanel (pushes its own overlay Back entry on NARROW).
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-review-menu"]')!.click());
    await until(() => Boolean(document.querySelector('[role="menu"]')));
    expect(hist.length).toBe(3);

    // 1st Back: closes only the Menu; ReviewPanel stays mounted with its back stack intact.
    hist.back();
    await until(() => document.querySelector('[role="menu"]') === null);
    expect(host.querySelector('[data-testid="review-panel"]')).not.toBeNull();
    expect(getPanel()?.kind).toBe("review");
    expect(hist.length).toBe(2);

    // 2nd Back: pops Review -> Agent.
    hist.back();
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s2" });
    expect(hist.length).toBe(2);

    // 3rd Back: pops Agent -> Task.
    hist.back();
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));
    expect(getPanel()).toEqual({ kind: "task", todo: "todo-1" });
    expect(hist.length).toBe(2);

    // 4th Back: closes the panel cleanly.
    hist.back();
    await until(() => host.querySelector("aside") === null);
    expect(getPanel()).toBeNull();
    expect(hist.length).toBe(1);
  });

  it("dismisses an open Dialog without its own Back entry (tablet narrow width) before popping the panel stack", async () => {
    const hist = installNarrowAndFakeHistory();
    act(() => openPanel({ kind: "task", todo: "todo-1" }));
    act(() => root.render(<SidePanel />));
    await until(() => Boolean(host.querySelector('[data-testid="task-panel"]')));

    // Drill Task -> Agent -> Review
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-nested-agent"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-review"]')!.click());
    await until(() => Boolean(host.querySelector('[data-testid="review-panel"]')));

    // Open a Dialog (installNarrowAndFakeHistory matches NARROW but not PHONE/SHEET_QUERY, so Dialog has no Back entry).
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="open-review-dialog"]')!.click());
    await until(() => Boolean(document.querySelector('[role="dialog"]')));
    expect(hist.length).toBe(2);

    // 1st Back: dismisses the Dialog and re-arms SidePanel's Back entry without popping ReviewPanel.
    hist.back();
    await until(() => !document.querySelector('[role="dialog"]') || Boolean(document.querySelector('[data-closing] [role="dialog"]')));
    expect(host.querySelector('[data-testid="review-panel"]')).not.toBeNull();
    expect(getPanel()?.kind).toBe("review");
    expect(hist.length).toBe(2);

    // 2nd Back: now pops Review -> Agent.
    hist.back();
    await until(() => Boolean(host.querySelector('[data-testid="agent-panel"]')));
    expect(getPanel()).toEqual({ kind: "agent", mid: "m1", sid: "s2" });
    expect(hist.length).toBe(2);
  });
});
