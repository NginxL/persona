// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { beforeAll, expect, it, vi } from "vitest";
import { Composer } from "./Composer";
import type { MentionRow, PanelPick } from "./ComposerPanel";
import { getContexts, type ContextRef } from "../../lib/links";
import { loadPages } from "../../lib/lazy-page";
import { t } from "../../i18n";

vi.mock("./ComposerPanel", () => ({
  ComposerPanel: ({ onPick }: { onPick: (pick: PanelPick) => void }) => (
    <button onClick={() => onPick({
      kind: "row",
      // Model an unsupported row arriving through the lazy picker.
      row: { kind: "resource", id: "resource:notes.txt", name: "Notes", dir: false, insert: "@notes.txt " } as unknown as MentionRow,
    })}>Pick resource</button>
  ),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

beforeAll(() => loadPages());

it("ignores rows outside the task file picker types even when they carry file-like text", async () => {
  const onChange = vi.fn();
  const scope = { machine: "box", cwd: "/work", launcher: "claude", runtime: "" };
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(
      <Composer draftKey="task-resource" machineId={scope.machine} placeholder=""
        mention={{ cwd: scope.cwd, files: () => [], referenceFiles: scope }}
        editor={{ text: "Read it", refs: [], label: "Task prompt", onChange }} />,
    ));
    act(() => host.querySelector<HTMLButtonElement>("[data-composer-plus]")!.click());
    const resource = [...host.querySelectorAll("button")].find((button) => button.textContent === "Pick resource");
    expect(resource).toBeDefined();
    act(() => resource!.click());
    expect(onChange).not.toHaveBeenCalled();
    expect(host.querySelector("textarea")?.value).toBe("Read it");
  } finally {
    act(() => root.unmount());
    host.remove();
  }
});

it("edits and removes a task reference without chat sends, history or stored chat drafts", async () => {
  const sent = vi.fn(async () => true);
  const initial: ContextRef = {
    id: "file:notes.txt", kind: "file", title: "Notes", url: "", text: "notes.txt",
    attrs: { path: "notes.txt", machine: "box", cwd: "/work", launcher: "claude", runtime: "" },
  };
  function Form() {
    const [value, setValue] = useState({ text: "Read it", refs: [initial] });
    return <Composer draftKey="prompt-test" editor={{
      ...value, label: "Task prompt", onChange: (text, refs) => setValue({ text, refs }),
    }} placeholder="" onSend={sent} />;
  }
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<Form />));
    const box = host.querySelector("textarea")!;
    expect(box.value).toBe("Read it");
    expect(host.textContent).toContain("Notes");
    expect(host.querySelector('[data-testid="composer-send"]')).toBeNull();
    expect(getContexts("prompt-test")).toHaveLength(0);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, "  Read latest ");
      box.dispatchEvent(new Event("input", { bubbles: true }));
      box.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(box.value).toBe("  Read latest ");
    expect(sent).not.toHaveBeenCalled();
    const remove = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("link-contextchips.remove_label")}"]`)!;
    act(() => remove.click());
    expect(host.textContent).not.toContain("Notes");
    expect(box.value).toBe("  Read latest ");
  } finally {
    act(() => root.unmount());
    host.remove();
  }
  expect(localStorage.getItem("draft:prompt-test")).toBeNull();
});
