// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { InboxForwardSetting } from "./InboxForwardSetting";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let stored: Record<string, unknown>;
let writes: Record<string, unknown>[];
let answer: (() => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const toggle = () => host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-assistantsettings.inbox_todo_forward")}"]`);

beforeEach(async () => {
  await initI18n("en");
  resetManagerChoice();
  setRosterDefault("assistant");
  vi.mocked(toast).mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  stored = { inbox_todo_forward: false };
  writes = [];
  answer = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        writes.push(body);
        if (answer) return answer();
        stored = { ...stored, ...body };
      }
      return json(stored);
    }
    throw new Error(`Unexpected path: ${path}`);
  }));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

async function render() {
  await act(async () => root.render(<InboxForwardSetting />));
  await flush();
}

it("defaults to off and toggles inbox_todo_forward via PUT /assistant", async () => {
  await render();
  expect(toggle()?.getAttribute("aria-checked")).toBe("false");
  await act(async () => { toggle()!.click(); });
  await flush();
  expect(writes).toEqual([{ inbox_todo_forward: true }]);
  expect(toggle()?.getAttribute("aria-checked")).toBe("true");
  expect(toast).not.toHaveBeenCalled();
});

it("reverts and shows toast when save fails", async () => {
  answer = () => json({ error: "Forbidden" }, 403);
  await render();
  await act(async () => { toggle()!.click(); });
  await flush();
  expect(toggle()?.getAttribute("aria-checked")).toBe("false");
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Forbidden"), "error");
});

it("renders nothing on older hubs that do not expose inbox_todo_forward", async () => {
  stored = {};
  await render();
  expect(toggle()).toBeNull();
});
