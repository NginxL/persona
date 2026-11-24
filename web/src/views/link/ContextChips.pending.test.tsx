// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { preloadPages } from "../../lib/lazy-page";
import { newContext, type ContextRef } from "../../lib/links";
import { ContextChipList } from "./ContextChips";

const chunk = vi.hoisted(() => {
  let started!: () => void;
  let release!: () => void;
  let delivered!: () => void;
  const requested = new Promise<void>((resolve) => { started = resolve; });
  const ready = new Promise<void>((resolve) => { release = resolve; });
  const imported = new Promise<void>((resolve) => { delivered = resolve; });
  return { requested, ready, imported, started, release, delivered, load: vi.fn() };
});
vi.mock("./ContextChipList", async (load) => {
  chunk.load();
  chunk.started();
  await chunk.ready;
  const module = await load<typeof import("./ContextChipList")>();
  chunk.delivered();
  return module;
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("keeps references removable during the lazy load and switches to the full list", async () => {
  const file = newContext("file", "Q&A.md", "", "Q&A.md");
  const skill = newContext("skill", "Review notes", "", "Project");
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  let add!: (refs: ContextRef[]) => void;
  function Selection() {
    const [refs, setRefs] = useState<ContextRef[]>([]);
    add = setRefs;
    return <ContextChipList refs={refs} onRemove={(id) => setRefs((current) => current.filter((ref) => ref.id !== id))} />;
  }
  const removeButtons = () => [...host.querySelectorAll("button")].filter((button) => button.getAttribute("aria-label") === t("link-contextchips.remove_label"));
  const fullList = () => [...host.querySelectorAll("[aria-label]")].find((element) => element.getAttribute("aria-label") === t("link-contextchips.label"));
  try {
    await act(async () => root.render(<Selection />));
    const cancel = preloadPages({ requestIdleCallback: (run) => { run(); return 1; }, cancelIdleCallback: () => {} });
    cancel();
    await vi.dynamicImportSettled();
    expect(chunk.load).not.toHaveBeenCalled();
    expect(host.textContent).toBe("");

    await act(async () => add([file, skill]));
    await chunk.requested;
    expect(fullList()).toBeUndefined();
    expect(host.textContent).toContain(file.title);
    expect(host.textContent).toContain(skill.title);
    expect(removeButtons()).toHaveLength(2);
    expect(removeButtons()[0].type).toBe("button");
    await act(async () => {
      removeButtons()[0].focus();
      expect(document.activeElement).toBe(removeButtons()[0]);
      removeButtons()[0].click();
    });
    expect(host.textContent).not.toContain(file.title);
    expect(host.textContent).toContain(skill.title);

    await act(async () => {
      chunk.release();
      await chunk.imported;
    });
    await vi.waitFor(async () => {
      await act(async () => {});
      expect(fullList()?.textContent).toContain(skill.title);
    });
    expect(host.textContent).not.toContain(file.title);
    await act(async () => removeButtons()[0].click());
    expect(host.textContent).toBe("");
    expect(chunk.load).toHaveBeenCalledTimes(1);
  } finally {
    chunk.release();
    await act(async () => root.unmount());
    host.remove();
  }
});
