// @vitest-environment jsdom
// Side-panel directory key identity and bounded navigation stack (lib/panel.ts).
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  canGoBack,
  clearPanelHistory,
  closePanel,
  directoryPanelKey,
  getPanel,
  getPanelHistory,
  MAX_PANEL_HISTORY,
  openPanel,
  popPanel,
  togglePanel,
  usePanelCanGoBack,
  type Panel,
} from "./panel";
import { showWorker } from "./team";
import { showSide, toggleSides } from "./side-panel";
import { toggleBackgroundPanel } from "./background-panel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("directory panel identity", () => {
  it("remounts files and Git for different Agent-free workspace directories", () => {
    const a = { mid: "computer", sid: "", cwd: "/projects/a" };
    const b = { ...a, cwd: "/projects/b" };
    expect(directoryPanelKey(a)).not.toBe(directoryPanelKey(b));
    expect(directoryPanelKey(a)).toBe(directoryPanelKey({ ...a }));
  });
  it("retains the actual machine and session identities without inventing a session ID", () => {
    const p = { mid: "computer", sid: "real-session", cwd: "/projects/a" };
    expect(JSON.parse(directoryPanelKey(p))).toEqual([p.mid, p.sid, p.cwd]);
    expect(directoryPanelKey(p)).not.toBe(directoryPanelKey({ ...p, mid: "other" }));
    expect(directoryPanelKey(p)).not.toBe(directoryPanelKey({ ...p, sid: "another" }));
  });
});

const taskA: Panel = { kind: "task", todo: "todo-1" };
const taskB: Panel = { kind: "task", todo: "todo-2" };
const agentA: Panel = { kind: "agent", mid: "m1", sid: "s1" };
const zipFile: Panel = { kind: "file", mid: "m1", path: "/work/report.zip", cwd: "/work", sid: "s1" };
const filesPanel: Panel = { kind: "files", mid: "m1", sid: "s1", cwd: "/work" };
const gitPanel: Panel = { kind: "git", mid: "m1", sid: "s1", cwd: "/work" };
const reviewPanel: Panel = { kind: "review", mid: "m1", sid: "s1", cwd: "/work", scope: "branch" };
const linkPanel: Panel = { kind: "link", url: "https://example.com/doc" };

describe("side panel navigation stack", () => {
  let aside: HTMLElement;
  let insideBtn: HTMLButtonElement;
  let outsideBtn: HTMLButtonElement;

  beforeEach(() => {
    closePanel();
    aside = document.createElement("aside");
    aside.dataset.sidePanel = "";
    insideBtn = document.createElement("button");
    insideBtn.textContent = "report.zip";
    aside.append(insideBtn);
    outsideBtn = document.createElement("button");
    outsideBtn.textContent = "Other task";
    document.body.append(aside, outsideBtn);
  });

  afterEach(() => {
    closePanel();
    aside.remove();
    outsideBtn.remove();
  });

  it("pushes previous panel when opened by a click inside [data-side-panel], even without moving focus", () => {
    openPanel(taskA);
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);

    // In Safari/iOS and jsdom, clicking a button does not move document.activeElement into the panel.
    outsideBtn.focus();
    insideBtn.addEventListener("click", () => openPanel(zipFile));
    insideBtn.click();

    expect(getPanel()).toEqual(zipFile);
    expect(canGoBack()).toBe(true);
    expect(getPanelHistory()).toEqual([taskA]);
  });

  it("pushes previous panel when triggered by keyboard inside [data-side-panel]", () => {
    openPanel(taskA);
    insideBtn.focus();
    insideBtn.addEventListener("keydown", () => openPanel(zipFile));
    insideBtn.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));

    expect(getPanel()).toEqual(zipFile);
    expect(getPanelHistory()).toEqual([taskA]);
  });

  it("pushes previous panel when triggered from a portaled menu marked [data-side-panel]", () => {
    openPanel(agentA);
    const portal = document.createElement("div");
    portal.setAttribute("role", "menu");
    portal.dataset.sidePanel = "";
    const item = document.createElement("button");
    item.addEventListener("click", () => openPanel(reviewPanel));
    portal.append(item);
    document.body.append(portal);
    try {
      item.click();
      expect(getPanel()).toEqual(reviewPanel);
      expect(getPanelHistory()).toEqual([agentA]);
    } finally {
      portal.remove();
    }
  });

  it("clears history on programmatic opens even when document.activeElement stayed inside [data-side-panel]", () => {
    openPanel(taskA);
    openPanel(zipFile, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);

    // Programmatic restore (no active click/keydown/submit event) must not infer push from focus alone.
    insideBtn.focus();
    openPanel(taskB);

    expect(getPanel()).toEqual(taskB);
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);
  });

  it("clears the stack when opened from outside the side panel, even if focus remained inside the panel", () => {
    openPanel(taskA);
    openPanel(zipFile, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);

    // Focus is still inside <aside>, but the click comes from an outside button (main chat).
    insideBtn.focus();
    outsideBtn.addEventListener("click", () => openPanel(taskB));
    outsideBtn.click();

    expect(getPanel()).toEqual(taskB);
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);
  });

  it("supports explicit push: true, push: false, replace: true, and clearPanelHistory()", () => {
    openPanel(taskA);
    openPanel(reviewPanel, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);

    // replace: true swaps current in place without pushing reviewPanel or clearing [taskA].
    openPanel(zipFile, { replace: true });
    expect(getPanel()).toEqual(zipFile);
    expect(getPanelHistory()).toEqual([taskA]);

    // clearPanelHistory drops the stack while leaving current open.
    clearPanelHistory();
    expect(getPanel()).toEqual(zipFile);
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);

    openPanel(reviewPanel, { push: true });
    expect(getPanelHistory()).toEqual([zipFile]);

    // push: false clears the stack even during an in-panel click.
    insideBtn.addEventListener("click", () => openPanel(linkPanel, { push: false }));
    insideBtn.click();
    expect(getPanel()).toEqual(linkPanel);
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);
  });

  it("keeps header togglePanel / toggleSides / toggleBackgroundPanel as replace-or-close without pushing history", () => {
    openPanel(filesPanel);
    insideBtn.focus();

    // Switching from Files to Git via togglePanel replaces without leaving a Back button.
    const toGit = document.createElement("button");
    toGit.addEventListener("click", () => togglePanel(gitPanel));
    aside.append(toGit);
    toGit.click();
    expect(getPanel()).toEqual(gitPanel);
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);

    // Toggling Git again closes the panel rather than reopening Files.
    toGit.click();
    expect(getPanel()).toBeNull();
    expect(canGoBack()).toBe(false);

    // toggleSides and toggleBackgroundPanel also replace/clear rather than pushing.
    openPanel(filesPanel);
    const toSides = document.createElement("button");
    toSides.addEventListener("click", () => toggleSides("m1", "s1", false));
    aside.append(toSides);
    toSides.click();
    expect(getPanel()).toEqual({ kind: "sides", mid: "m1", sid: "s1" });
    expect(canGoBack()).toBe(false);

    const toBg = document.createElement("button");
    toBg.addEventListener("click", () => toggleBackgroundPanel("m1", "s1"));
    aside.append(toBg);
    toBg.click();
    expect(getPanel()).toEqual({ kind: "background", mid: "m1", sid: "s1" });
    expect(canGoBack()).toBe(false);
  });

  it("replaces in place for strip selection (showWorker / showSide) without creating a Back button", () => {
    openPanel({ kind: "team", mid: "m1", sid: "lead", worker: "w1" });
    const pill = document.createElement("button");
    pill.addEventListener("click", () => showWorker("m1", "lead", "w2"));
    aside.append(pill);
    pill.click();

    expect(getPanel()).toEqual({ kind: "team", mid: "m1", sid: "lead", worker: "w2" });
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);

    // Same for Side sessions strip selection.
    openPanel({ kind: "sides", mid: "m1", sid: "p1", side: "c1" });
    const sidePill = document.createElement("button");
    sidePill.addEventListener("click", () => showSide("m1", "p1", "c2"));
    aside.append(sidePill);
    sidePill.click();

    expect(getPanel()).toEqual({ kind: "sides", mid: "m1", sid: "p1", side: "c2" });
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);

    // Preserves an existing outer back stack when switching pills inside a drilled-in Team panel.
    openPanel(taskA);
    openPanel({ kind: "team", mid: "m1", sid: "lead", worker: "w1" }, { push: true });
    pill.click();
    expect(getPanel()).toEqual({ kind: "team", mid: "m1", sid: "lead", worker: "w2" });
    expect(getPanelHistory()).toEqual([taskA]);
  });

  it("treats repeated same-kind/same-target opens as replacements without redundant history entries", () => {
    openPanel(taskA);
    openPanel({ kind: "file", mid: "m1", path: "/work/report.ts", line: 10 }, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);

    // Jumping to another line in the same file replaces in place.
    openPanel({ kind: "file", mid: "m1", path: "/work/report.ts", line: 42 }, { push: true });
    expect(getPanel()).toEqual({ kind: "file", mid: "m1", path: "/work/report.ts", line: 42 });
    expect(getPanelHistory()).toEqual([taskA]);

    // Updating turn/file selection on the same Changes panel replaces in place.
    openPanel({ kind: "changes", mid: "m1", sid: "s1", turn: "t1" }, { push: true });
    expect(getPanelHistory()).toHaveLength(2);
    openPanel({ kind: "changes", mid: "m1", sid: "s1", turn: "t2", at: { path: "a.ts" } }, { push: true });
    expect(getPanel()).toEqual({ kind: "changes", mid: "m1", sid: "s1", turn: "t2", at: { path: "a.ts" } });
    expect(getPanelHistory()).toHaveLength(2);
  });

  it("updates target fields and clears history when reopening the same directory/changes panel from outside", () => {
    // Task -> Review (history: [taskA])
    openPanel(taskA);
    openPanel(reviewPanel, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);

    // External click (e.g. changed-files card in main chat) opens the same session's review at a new scope/file.
    const nextReview: Panel = { kind: "review", mid: "m1", sid: "s1", cwd: "/work", scope: "uncommitted", at: { path: "src/app.ts", line: 12 } };
    const openFromChat = document.createElement("button");
    openFromChat.addEventListener("click", () => openPanel(nextReview));
    document.body.append(openFromChat);
    try {
      openFromChat.click();
      expect(getPanel()).toEqual(nextReview);
      expect(canGoBack()).toBe(false);
      expect(getPanelHistory()).toEqual([]);
    } finally {
      openFromChat.remove();
    }

    // Same for Files -> another folder and Changes -> another turn opened programmatically from outside.
    openPanel(taskA);
    openPanel(filesPanel, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);
    const nextFiles: Panel = { ...filesPanel, path: "/work/src", highlight: "main.ts" };
    openPanel(nextFiles);
    expect(getPanel()).toEqual(nextFiles);
    expect(getPanelHistory()).toEqual([]);

    openPanel(taskA);
    openPanel({ kind: "changes", mid: "m1", sid: "s1", turn: "t1" }, { push: true });
    expect(getPanelHistory()).toEqual([taskA]);
    const nextChanges: Panel = { kind: "changes", mid: "m1", sid: "s1", turn: "t2", at: { path: "b.ts" } };
    openPanel(nextChanges);
    expect(getPanel()).toEqual(nextChanges);
    expect(getPanelHistory()).toEqual([]);
  });

  it("pops panels in LIFO order and closes when the stack is empty", () => {
    openPanel(taskA);
    openPanel(zipFile, { push: true });
    openPanel(linkPanel, { push: true });
    expect(getPanelHistory()).toEqual([taskA, zipFile]);

    expect(popPanel()).toBe(true);
    expect(getPanel()).toEqual(zipFile);
    expect(canGoBack()).toBe(true);

    expect(popPanel()).toBe(true);
    expect(getPanel()).toEqual(taskA);
    expect(canGoBack()).toBe(false);

    // Stack is now empty: popPanel closes the panel and returns false.
    expect(popPanel()).toBe(false);
    expect(getPanel()).toBeNull();
    expect(canGoBack()).toBe(false);

    // Popping when already closed is a safe no-op.
    expect(popPanel()).toBe(false);
    expect(getPanel()).toBeNull();
  });

  it("clears the stack on closePanel()", () => {
    openPanel(taskA);
    openPanel(zipFile, { push: true });
    openPanel(linkPanel, { push: true });
    expect(canGoBack()).toBe(true);

    closePanel();
    expect(getPanel()).toBeNull();
    expect(canGoBack()).toBe(false);
    expect(getPanelHistory()).toEqual([]);
  });

  it("caps history at MAX_PANEL_HISTORY entries, dropping the oldest first", () => {
    openPanel({ kind: "file", mid: "m1", path: "/f0" });
    for (let i = 1; i <= MAX_PANEL_HISTORY + 4; i++) {
      openPanel({ kind: "file", mid: "m1", path: `/f${i}` }, { push: true });
    }
    const hist = getPanelHistory();
    expect(hist).toHaveLength(MAX_PANEL_HISTORY);
    expect(hist[0]).toEqual({ kind: "file", mid: "m1", path: "/f4" });
    expect(hist[hist.length - 1]).toEqual({ kind: "file", mid: "m1", path: `/f${MAX_PANEL_HISTORY + 3}` });
    expect(getPanel()).toEqual({ kind: "file", mid: "m1", path: `/f${MAX_PANEL_HISTORY + 4}` });
  });

  it("notifies usePanelCanGoBack() reactively", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root: Root = createRoot(host);
    const Probe = () => createElement("span", { "data-back": String(usePanelCanGoBack()) });
    try {
      act(() => root.render(createElement(Probe)));
      expect(host.querySelector("span")?.getAttribute("data-back")).toBe("false");

      act(() => openPanel(taskA));
      expect(host.querySelector("span")?.getAttribute("data-back")).toBe("false");

      act(() => openPanel(zipFile, { push: true }));
      expect(host.querySelector("span")?.getAttribute("data-back")).toBe("true");

      act(() => { popPanel(); });
      expect(host.querySelector("span")?.getAttribute("data-back")).toBe("false");
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  });
});
