// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { archiveSession } from "../../lib/agent-archives";
import { api } from "../../lib/api";
import { apiOp } from "../../lib/ops";
import { toast } from "../../ui/atoms";
import { navigate } from "../../lib/router";
import { getState, reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import { ContextRestartNotice } from "./ContextRestartNotice";

vi.mock("../../lib/router", async (original) => ({
  ...(await original<typeof import("../../lib/router")>()),
  navigate: vi.fn(),
}));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: vi.fn().mockResolvedValue({}),
}));
vi.mock("../../lib/ops", async (original) => ({
  ...(await original<typeof import("../../lib/ops")>()),
  apiOp: vi.fn(),
}));
vi.mock("../../lib/agent-archives", () => ({ archiveSession: vi.fn().mockResolvedValue({ ok: true }) }));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: vi.fn(),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const main: Session = { machine_id: "hub", session_id: "main", title: "Main", cwd: "/project", status: "idle" };
let host: HTMLDivElement;
let root: Root;
const notice = () => host.querySelector("[data-context-restart-notice]");
const action = (label: string) => [...notice()!.querySelectorAll("button")].find((button) => button.textContent === label)!;

beforeEach(() => {
  reset();
  vi.clearAllMocks();
  localStorage.clear();
  getState().features = ["assistant_main_new"];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

const show = (s: Session, assistant: boolean, isMain: boolean) =>
  act(async () => root.render(<ContextRestartNotice key={s.session_id} session={s} assistant={assistant} isMain={isMain} />));

describe("post-compaction context usage notice", () => {
  it("opens the existing new-main dialog with summary carry selected", async () => {
    await show(main, true, true);
    await act(async () => action(t("assistant-assistantchat.new_main_ellipsis")).click());
    await act(async () => vi.dynamicImportSettled());
    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog?.textContent).toContain(t("assistant-assistantchat.new_main_title"));
    expect(dialog?.querySelector('[role="switch"]')?.getAttribute("aria-checked")).toBe("true");
    expect(navigate).not.toHaveBeenCalled();
  });

  it.each([true, false])("carries a side chat or Agent in one click (assistant=%s)", async (assistant) => {
    const side = { ...main, session_id: "source", assistant };
    const next = { ...side, session_id: "fresh" };
    vi.mocked(apiOp).mockResolvedValueOnce({ state: "pending" }).mockResolvedValueOnce({ session: next });
    await show(side, assistant, false);
    await act(async () => action(t("chat-contextrestart.continue")).click());
    expect(apiOp).toHaveBeenCalledTimes(2);
    expect(apiOp).toHaveBeenNthCalledWith(1, "/sessions/hub/source/continue-in-new-chat",
      expect.objectContaining({ method: "POST", opId: expect.any(String) }));
    expect(vi.mocked(apiOp).mock.calls[0]).toEqual(vi.mocked(apiOp).mock.calls[1]);
    expect(navigate).toHaveBeenCalledWith(assistant ? { name: "persona", sid: "fresh" } : { name: "session", mid: "hub", sid: "fresh" });
    expect(archiveSession).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalled();
    const archive = vi.mocked(toast).mock.calls[0]?.[2];
    expect(archive?.label).toBe(t("chat-contextrestart.archive"));
    await act(async () => archive?.run());
    if (assistant) {
      expect(api).toHaveBeenCalledWith("/assistant/conversations/source/archive", { method: "POST", json: {} });
    } else {
      expect(archiveSession).toHaveBeenCalledWith(side);
    }
  });

  it("leaves the source open and never offers archive when the new chat failed to start", async () => {
    vi.mocked(apiOp).mockRejectedValueOnce(new Error("offline"));
    await show({ ...main, session_id: "source", assistant: true }, true, false);
    await act(async () => action(t("chat-contextrestart.continue")).click());
    expect(navigate).not.toHaveBeenCalled();
    expect(archiveSession).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(expect.any(String), "error");
    expect(notice()).not.toBeNull();
  });

  it("remembers a dismissal across remounts without dismissing another session", async () => {
    await show(main, true, true);
    await act(async () => notice()!.querySelector<HTMLButtonElement>('button[aria-label]')!.click());
    expect(notice()).toBeNull();
    await act(async () => root.render(null));
    await show(main, true, true);
    expect(notice()).toBeNull();
    await show({ ...main, session_id: "side" }, true, false);
    expect(notice()).not.toBeNull();
  });

  it("stays hidden while compacting or when the main action is unavailable", async () => {
    await show({ ...main, compaction: { state: "running", started_at: "2026-01-01T00:00:00Z" } }, true, true);
    expect(notice()).toBeNull();
    getState().features = [];
    await show(main, true, true);
    expect(notice()).toBeNull();
    await show({ ...main, archived: true }, true, false);
    expect(notice()).toBeNull();
  });
});
