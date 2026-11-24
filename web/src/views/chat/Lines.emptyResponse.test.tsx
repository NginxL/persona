// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { chatLines, type UserMessage } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { ChatLineView } from "./Lines";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const start = "2026-01-02T12:00:00Z";
const end = "2026-01-02T12:00:04Z";
const items: Item[] = [
  { id: "1#0", seq: 1, role: "user", kind: "text", text: "Check this", ts: start },
  { id: "2#0", seq: 2, role: "assistant", kind: "thinking", text: "Considering it", ts: end },
  { id: "2#1", seq: 2, role: "assistant", kind: "text", text: "\n", ts: end },
];
const expandFold = async () => {};

it.each([
  ["en", "No response returned", "Retry", "Interrupted"],
  ["zh-Hans", "未返回正文", "重试", "已中断"],
  ["ja", "応答がありませんでした", "再試行", "中断しました"],
] as const)("renders a localized blank-turn outcome and retry in %s", async (lang, empty, retry, interrupted) => {
  await initI18n(lang);
  const host = document.createElement("div");
  const root = createRoot(host);
  const retryEmpty = vi.fn(async (_user: UserMessage, _signal: AbortSignal) => true);
  const render = (page: Item[], live = false) => act(async () => {
    root.render(chatLines(page, { live }).map((line) => (
      <ChatLineView key={line.id} line={line} mid="m" sid="s" expandFold={expandFold} retryEmpty={retryEmpty} />
    )));
  });
  try {
    await render(items);
    await act(async () => vi.dynamicImportSettled());
    expect(host.textContent).toContain(empty);
    expect(host.querySelector('[data-work-group="done"] button')?.getAttribute("aria-expanded")).toBe("false");
    const button = [...host.querySelectorAll("button")].find((control) => control.textContent === retry);
    expect(button).toBeDefined();
    await act(async () => { button!.click(); });
    expect(retryEmpty).toHaveBeenCalledOnce();
    expect(retryEmpty.mock.calls[0][0]).toMatchObject({ text: "Check this" });

    await render([...items, { id: "3#0", seq: 3, role: "system", kind: "notice", text: "已中断", ts: end }]);
    expect(host.textContent).toContain(interrupted);
    expect(host.textContent).not.toContain(empty);
    expect([...host.querySelectorAll("button")].some((control) => control.textContent === retry)).toBe(false);

    await render(items, true);
    expect(host.textContent).not.toContain(empty);
  } finally {
    act(() => root.unmount());
    await initI18n("zh-Hans");
  }
});
