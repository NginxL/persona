// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import { reset, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { CloseAgentDialog } from "./SessionDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const session: Session = {
  machine_id: "m", session_id: "runtime-session", title: "Example", cwd: "", status: "working",
  source_kind: "runtime", agent: "agy", capabilities: { read: true, terminal: false, close: true },
};
let host: HTMLDivElement;
let root: Root;
const closed = vi.fn();
const completed = vi.fn();
const fetcher = vi.fn();

beforeEach(async () => {
  await initI18n("en");
  setToken("synthetic-close-owner");
  reset();
  upsertSession(session);
  closed.mockClear();
  completed.mockClear();
  fetcher.mockReset().mockResolvedValue(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<CloseAgentDialog session={session} machineName="Example computer" open onClose={closed} onClosed={completed} />));
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

async function confirm() {
  const button = [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((element) => element.textContent === "Close agent");
  expect(button).toBeDefined();
  await act(async () => button!.click());
}

it("closes a runtime with the close capability through the existing confirmation", async () => {
  expect(document.querySelector("[role=dialog]")?.textContent).toContain("The conversation history stays available.");
  expect(document.querySelector("[role=dialog]")?.textContent).not.toContain("quits Claude");
  await confirm();
  expect(fetcher).toHaveBeenCalledWith(expect.stringContaining("/sessions/m/runtime-session/close"), expect.objectContaining({ method: "POST" }));
  expect(completed).toHaveBeenCalledOnce();
});

it("dismisses the confirmation if the runtime went offline meanwhile", async () => {
  act(() => upsertSession({ ...session, status: "offline" }));
  await confirm();
  expect(fetcher).not.toHaveBeenCalled();
  expect(closed).toHaveBeenCalledOnce();
  expect(completed).not.toHaveBeenCalled();
});

it("asks the engine an agent ran in about its own worktree, and has that engine remove it", async () => {
  const paseo: Session = { ...session, session_id: "paseo-1", host: "paseo", cwd: "/w/trees/fix", agent: "claude" };
  const reply = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  fetcher.mockReset().mockImplementation(async (url: string) => String(url).endsWith("/worktree/status")
    ? reply({ worktree: true, ours: true, path: "/w/trees/fix", branch: "persona/fix", dirty: 0, unmerged: 0 })
    : String(url).endsWith("/worktree/remove") ? reply({ removed: true, branch_deleted: false }) : reply({}));
  act(() => upsertSession(paseo));
  await act(async () => root.render(<CloseAgentDialog session={paseo} machineName="Example computer" open onClose={closed} onClosed={completed} />));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
  const body = (path: string) => JSON.parse(fetcher.mock.calls.find(([url]) => String(url).endsWith(path))![1].body);
  expect(body("/worktree/status")).toEqual({ cwd: "/w/trees/fix", kind: "paseo" });
  const remove = [...document.querySelectorAll<HTMLButtonElement>("[role=radio]")].find((b) => b.textContent === "Remove it");
  await act(async () => remove!.click());
  await confirm();
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
  expect(body("/worktree/remove")).toEqual({ path: "/w/trees/fix", kind: "paseo" });
});
