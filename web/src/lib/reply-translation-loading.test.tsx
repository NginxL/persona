// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

const loading = vi.hoisted(() => {
  let release!: () => void;
  const ready = new Promise<void>((done) => { release = done; });
  return { release, ready, started: vi.fn(), request: vi.fn() };
});
vi.mock("./api", async (original) => ({
  ...await original<typeof import("./api")>(),
  api: loading.request,
}));
vi.mock("./translate", async (original) => {
  loading.started();
  await loading.ready;
  return original<typeof import("./translate")>();
});

import { initI18n } from "../i18n";
import { setToken } from "./api";
import { LinkManager } from "./manager-choice";
import { useReplyTranslation } from "./reply-translation";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Control({ text }: { text: string }) {
  const reply = useReplyTranslation(text);
  return <button onClick={reply.toggle} disabled={reply.pending}>{reply.label}</button>;
}

it("loads requests only on intent and makes no model request after unmount during module loading", async () => {
  await initI18n("en");
  setToken("test-translation-module-before");
  loading.request.mockResolvedValue({ text: "Translated" });
  const views = Array.from({ length: 2 }, () => {
    const host = document.createElement("div");
    document.body.append(host);
    return { host, root: createRoot(host), mounted: true };
  });
  const draw = (index: number, text: string) => views[index].root.render(
    <LinkManager.Provider value="selected-assistant"><Control text={text} /></LinkManager.Provider>,
  );
  try {
    await act(async () => { draw(0, "Unmounted reply"); });
    expect(loading.started).not.toHaveBeenCalled();
    await act(async () => {
      views[0].host.querySelector("button")!.click();
    });
    await vi.waitFor(() => expect(loading.started).toHaveBeenCalledTimes(1));
    expect(loading.request).not.toHaveBeenCalled();
    expect(views[0].host.querySelector("button")!.disabled).toBe(true);
    await act(async () => {
      views[0].root.unmount();
      views[0].mounted = false;
    });
    await act(async () => {
      loading.release();
      await vi.dynamicImportSettled();
    });
    expect(loading.request).not.toHaveBeenCalled();
    await act(async () => { draw(1, "Current reply"); });
    await act(async () => {
      views[1].host.querySelector("button")!.click();
      await vi.dynamicImportSettled();
    });
    expect(loading.request).toHaveBeenCalledExactlyOnceWith("/translate/message", {
      method: "POST",
      json: { text: "Current reply", target: "en" },
      headers: { "X-Persona-Manager": "selected-assistant" },
    });
    expect(views[1].host.querySelector("button")!.textContent).toBe("Show original");
  } finally {
    loading.release();
    act(() => { for (const view of views) if (view.mounted) view.root.unmount(); });
    for (const view of views) view.host.remove();
  }
});
