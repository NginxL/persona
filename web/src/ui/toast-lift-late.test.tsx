// @vitest-environment jsdom
// The toasts' placement on a phone is its own chunk (ui/toast-lift.tsx, #11442), which the app loads on a narrow
// screen and hands to Toasts (App.tsx). A toast that comes before it is in shows at the bottom of the page, as on a wide
// screen, and moves into place once it arrives.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { lazyPage } from "../lib/lazy-page";
import { toast, Toasts } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("a toast before the chunk is in shows at bottom-5, and goes into place when it arrives", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: q === "(max-width: 1023px)", media: q, addEventListener() {}, removeEventListener() {} }));
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  // the chunk arrives when the test says so
  let arrive = () => {};
  const arrived = new Promise<void>((r) => (arrive = r));
  const lift = lazyPage(() => import("./toast-lift").then(async (m) => (await arrived, m.ToastLift)));
  act(() => root.render(<Toasts lift={lift} />));
  await act(async () => void toast("没发出去", "error"));
  const box = document.body.querySelector<HTMLElement>("[aria-live=polite]")!;
  expect(box.className).toContain("bottom-5");
  expect(box.style.top).toBe(""); // not placed yet: the chunk is on its way
  await act(async () => arrive());
  await vi.waitFor(() => expect(box.style.top).toBe("var(--kb-top)"));
  expect(box.style.justifyContent).toBe("flex-end");
  act(() => void vi.advanceTimersByTime(10_000));
  act(() => root.unmount());
  host.remove();
});
