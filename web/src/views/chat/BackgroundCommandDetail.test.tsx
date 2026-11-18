// @vitest-environment jsdom
import { Blob as NodeBlob } from "node:buffer";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.mock("../../i18n", () => ({
  t: (key: string, vars?: Record<string, number>) => key === "lib-work-groups.dur_seconds" ? `${vars?.s}s`
    : key === "lib-tasks.exit_code" ? `Exit code ${vars?.code}`
      : key === "chat-activity.output_lines" ? `lines:${vars?.count}`
        : key === "chat-activity.output_tail_lines" ? `tail-lines:${vars?.count}` : key,
  currentLanguage: () => "en",
  formatNumber: String,
  formatDate: (value: number) => new Date(value).toISOString(),
}));
vi.hoisted(() => {
  window.matchMedia = (() => ({ matches: false, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
});
import { credentialOwner, handleUnauthorized, setToken } from "../../lib/api";
import { reset, upsertSession } from "../../lib/store";
import type { BackgroundTask, Session } from "../../lib/types";
import { BackgroundCommandDetail } from "./BackgroundCommandDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const task: BackgroundTask = { id: "task-a", kind: "shell", description: "Synthetic command", command: "printf sample", output_file: "/tmp/sample.log" };
type Props = Parameters<typeof BackgroundCommandDetail>[0];
let host: HTMLDivElement, root: Root, props: Props;
let fetcher: ReturnType<typeof vi.fn>;
const back = vi.fn(), open = vi.fn(), parentKey = vi.fn();
const response = (text: string, total = new TextEncoder().encode(text).length) => {
  const length = new TextEncoder().encode(text).length;
  return new Response(text, { status: total ? 206 : 200, headers: {
    "Content-Type": "text/plain", "Content-Length": String(length),
    ...(total ? { "Content-Range": `bytes ${total - length}-${total - 1}/${total}` } : {}),
  } });
};
const put = (manager = "manager-a") => act(() => upsertSession({ machine_id: "machine-a", session_id: "session-a", manager } as Session));
const draw = (over: Partial<Props> = {}) => act(() => { props = { ...props, ...over }; root.render(<section onKeyDown={parentKey}><BackgroundCommandDetail {...props} /></section>); });
const flush = async () => { await act(async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); }); };
const tick = async (ms = 2000) => { act(() => vi.advanceTimersByTime(ms)); await flush(); };
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find(el => el.getAttribute("aria-label") === label || el.textContent === label)!;
const text = () => host.querySelector("[data-command-output]")?.textContent;
const signal = (n = 0) => (fetcher.mock.calls[n][1] as RequestInit).signal!;
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  vi.setSystemTime("2026-11-18T10:00:00Z");
  vi.stubGlobal("Blob", NodeBlob);
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  fetcher = vi.fn(async () => response("sample output")); vi.stubGlobal("fetch", fetcher);
  setToken("synthetic-owner-a"); put(); back.mockReset(); open.mockReset(); parentKey.mockReset();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  props = { mid: "machine-a", sid: "session-a", cwd: "/tmp", task, running: true, focus: true, owner: credentialOwner(), manager: "manager-a", onBack: back, onOpenFile: open };
});
afterEach(async () => {
  act(() => root.unmount()); await flush(); host.remove(); reset(); vi.useRealTimers(); vi.unstubAllGlobals();
});

it("reads a bounded suffix through authenticated API and renders literal output with Back/copy/open controls", async () => {
  fetcher.mockResolvedValueOnce(response("<img src=x onerror=alert(1)>"));
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  draw(); await flush();
  expect(text()).toBe("<img src=x onerror=alert(1)>"); expect(host.querySelector("img")).toBeNull();
  const [url, init] = fetcher.mock.calls[0];
  expect(url).toBe("/api/v1/machines/machine-a/file/download?path=%2Ftmp%2Fsample.log&cwd=%2Ftmp");
  const headers = new Headers(init.headers);
  expect(headers.get("Range")).toBe("bytes=-65536"); expect(headers.get("Authorization")).toBe("Bearer synthetic-owner-a");
  expect(headers.get("X-Persona-Manager")).toBe("manager-a"); expect(document.activeElement).toBe(button("chat-activity.back"));
  act(() => button("chat-activity.copy_command").click()); await flush(); expect(writeText).toHaveBeenCalledWith("printf sample");
  act(() => button("chat-activity.open_output_file").click()); expect(open).toHaveBeenCalledOnce();
  act(() => button("chat-activity.back").click()); expect(back).toHaveBeenCalledOnce();
});

it("distinguishes empty running output from an ended empty read without claiming success", async () => {
  fetcher.mockImplementation(async () => response("")); draw(); await flush();
  expect(text()).toBe("chat-activity.output_empty_running");
  expect(host.querySelector("[data-command-lines]")?.textContent).toBe("lines:0");
  draw({ running: false }); await flush(); expect(fetcher).toHaveBeenCalledTimes(2);
  expect(text()).toBe("chat-activity.output_empty_ended"); expect(host.textContent).toContain("chat-activity.output_not_running");
  await tick(10_000); expect(fetcher).toHaveBeenCalledTimes(2);
});

it("polls only when visible and keeps at most one read in flight", async () => {
  let resolve!: (r: Response) => void;
  fetcher.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; })); draw(); await tick(6000);
  expect(fetcher).toHaveBeenCalledTimes(1); resolve(response("first")); await flush();
  Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
  act(() => document.dispatchEvent(new Event("visibilitychange"))); await tick(6000); expect(fetcher).toHaveBeenCalledTimes(1);
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  act(() => document.dispatchEvent(new Event("visibilitychange"))); await flush(); expect(fetcher).toHaveBeenCalledTimes(2);
});

it("aborts the running read and performs the final read even when it was pending", async () => {
  let resolve!: (r: Response) => void;
  fetcher.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; })); draw();
  draw({ running: false }); expect(signal().aborted).toBe(true); await flush(); expect(text()).toBe("sample output");
  resolve(response("old pending output")); await flush(); expect(text()).toBe("sample output");
});

it.each(["task", "session", "owner", "manager"])("retires pending output on a changed %s scope", async kind => {
  let resolve!: (r: Response) => void;
  fetcher.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; })); draw();
  if (kind === "task") draw({ task: { ...task, id: "task-b", output_file: "/tmp/new.log" } });
  if (kind === "session") { act(() => upsertSession({ machine_id: "machine-a", session_id: "session-b", manager: "manager-a" } as Session)); draw({ sid: "session-b" }); }
  if (kind === "owner") act(() => setToken("synthetic-owner-b"));
  if (kind === "manager") put("manager-b");
  expect(signal().aborted).toBe(true); await flush(); resolve(response("retired secret")); await flush();
  expect(host.textContent).not.toContain("retired secret");
  if (kind === "owner" || kind === "manager") expect(host.textContent).toBe("");
  else expect(text()).toBe("sample output");
});

it.each([
  () => new Response("ignored range", { headers: { "Content-Type": "text/plain", "Content-Length": "13" } }),
  () => new Response("bad", { status: 206, headers: { "Content-Type": "text/plain", "Content-Range": "bytes 0-2/99" } }),
  () => new Response("x", { status: 206, headers: { "Content-Type": "text/plain", "Content-Length": "65537", "Content-Range": "bytes 0-65535/65536" } }),
  () => new Response("not found", { status: 404 }),
  () => new Response("unavailable", { status: 502 }),
  () => new Response("", { status: 416 }),
  () => new Response("not empty", { headers: { "Content-Type": "text/plain", "Content-Length": "0" } }),
])("retains last good output and offers Retry after a real response failure", async bad => {
  draw(); await flush(); fetcher.mockResolvedValueOnce(bad()); await tick();
  expect(text()).toBe("sample output"); expect(host.querySelector('[role="alert"]')).not.toBeNull();
  fetcher.mockResolvedValueOnce(response("recovered")); act(() => button("app.retry").click()); await flush();
  expect(text()).toBe("recovered"); expect(host.querySelector('[role="alert"]')).toBeNull();
});

it("shows a bounded tail and removes its incomplete first line", async () => {
  const suffix = "x".repeat(65_520) + "\nlast line here!";
  expect(new TextEncoder().encode(suffix).length).toBe(65_536);
  fetcher.mockResolvedValueOnce(response(suffix, 90_000)); draw(); await flush();
  expect(text()).toBe("last line here!"); expect(host.textContent).toContain("chat-activity.output_tail");
  expect(host.querySelector("[data-command-lines]")?.textContent).toBe("tail-lines:1");
});

it("preserves a nonempty long-line suffix ending in newline instead of claiming no output", async () => {
  const suffix = `${"a".repeat(65_535)}\n`;
  fetcher.mockResolvedValueOnce(response(suffix, 90_000)); draw(); await flush();
  expect(text()).toBe(suffix); expect(host.querySelector("pre")).not.toBeNull();
  expect(host.textContent).not.toContain("chat-activity.output_empty_running");
  expect(host.textContent).toContain("chat-activity.output_tail");
});

it("only follows new output when the reader has stayed near the bottom", async () => {
  draw(); await flush(); const el = host.querySelector<HTMLElement>("[data-command-output]")!;
  Object.defineProperties(el, { scrollHeight: { value: 1000, configurable: true }, clientHeight: { value: 200, configurable: true } });
  el.scrollTop = 100; act(() => el.dispatchEvent(new Event("scroll", { bubbles: true })));
  fetcher.mockResolvedValueOnce(response("new output")); await tick(); expect(el.scrollTop).toBe(100);
  el.scrollTop = 800; act(() => el.dispatchEvent(new Event("scroll", { bubbles: true })));
  fetcher.mockResolvedValueOnce(response("latest output")); await tick(); expect(el.scrollTop).toBe(1000);
});

it("handles one local Escape while respecting menus, dialogs, composition and consumed keys", async () => {
  draw(); await flush(); const el = button("chat-activity.back");
  const escape = (init: KeyboardEventInit = {}) => { const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, ...init }); act(() => el.dispatchEvent(event)); return event; };
  expect(escape({ isComposing: true }).defaultPrevented).toBe(false); expect(back).not.toHaveBeenCalled(); expect(parentKey).not.toHaveBeenCalled();
  expect(escape({ keyCode: 229 }).defaultPrevented).toBe(false); expect(back).not.toHaveBeenCalled(); expect(parentKey).not.toHaveBeenCalled();
  const menu = document.createElement("div"); menu.setAttribute("role", "menu"); document.body.append(menu);
  escape(); expect(back).not.toHaveBeenCalled(); menu.remove();
  menu.setAttribute("role", "dialog"); document.body.append(menu); escape(); expect(back).not.toHaveBeenCalled(); menu.remove();
  const consume = (e: KeyboardEvent) => e.preventDefault(); el.addEventListener("keydown", consume);
  escape(); expect(back).not.toHaveBeenCalled(); el.removeEventListener("keydown", consume);
  parentKey.mockClear(); const handled = escape();
  expect(handled.defaultPrevented).toBe(true); expect(back).toHaveBeenCalledOnce(); expect(parentKey).not.toHaveBeenCalled();
});

it("aborts a pending read on Back and never applies its late result", async () => {
  let resolve!: (r: Response) => void;
  fetcher.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; })); draw();
  act(() => button("chat-activity.back").click()); expect(signal().aborted).toBe(true);
  resolve(response("late result")); await flush(); expect(text()).not.toContain("late result");
});

it("accepts an actually empty 200 body without requiring Content-Length", async () => {
  fetcher.mockResolvedValueOnce(new Response("", { headers: { "Content-Type": "text/plain" } }));
  draw(); await flush(); expect(text()).toBe("chat-activity.output_empty_running"); expect(host.querySelector('[role="alert"]')).toBeNull();
});

it("cancels a chunked response beyond the cap without buffering its remaining body", async () => {
  let pulls = 0; const cancel = vi.fn();
  const stream = new ReadableStream<Uint8Array>({ pull(controller) { pulls++; controller.enqueue(new Uint8Array(pulls === 1 ? 65_536 : 1)); }, cancel }, { highWaterMark: 0 });
  fetcher.mockResolvedValueOnce(new Response(stream, { status: 206, headers: { "Content-Type": "text/plain", "Content-Range": "bytes 0-65535/65536" } }));
  draw(); await flush(); expect(pulls).toBe(2); expect(cancel).toHaveBeenCalledOnce(); expect(host.querySelector('[role="alert"]')).not.toBeNull();
});

it("decodes a suffix beginning inside a UTF-8 character without a fabricated replacement glyph", async () => {
  const bytes = new Uint8Array(65_536).fill(97); bytes[0] = 0x82; bytes[1] = 0xac;
  fetcher.mockResolvedValueOnce(new Response(bytes, { status: 206, headers: { "Content-Type": "text/plain", "Content-Range": "bytes 24464-89999/90000" } }));
  draw(); await flush(); expect(text()?.length).toBe(65_534); expect(text()).not.toContain("\ufffd");
});

it.each(["owner", "manager"])("does not send a late retired %s 401 into sign-in recovery", async kind => {
  let resolve!: (r: Response) => void;
  const unauthorized = vi.fn(); const previous = handleUnauthorized(unauthorized);
  try {
    fetcher.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; })); draw();
    if (kind === "owner") act(() => setToken("synthetic-owner-b")); else put("manager-b");
    resolve(new Response(JSON.stringify({ error: "retired credential" }), { status: 401 })); await flush();
    expect(unauthorized).not.toHaveBeenCalled(); expect(host.textContent).toBe("");
  } finally { handleUnauthorized(previous); }
});

it("shows live runtime while running, then the retained result, exit code, final runtime and start time", async () => {
  const startedAt = new Date(Date.now() - 5000).toISOString();
  draw({ task: { ...task, started_at: startedAt } }); await flush();
  expect(host.querySelector("[data-command-runtime]")?.textContent).toBe("5s");
  expect(host.querySelector('[role="status"]')?.textContent).toBe("chat-subagentcard.status_running");
  expect(host.querySelector("[data-command-started]")?.textContent).toBe(startedAt);
  await tick(1000); expect(host.querySelector("[data-command-runtime]")?.textContent).toBe("6s");
  const beforeEnd = fetcher.mock.calls.length;
  draw({ running: false, task: { ...task, started_at: startedAt, status: "completed", ended_at: new Date(Date.now()).toISOString(), exit_code: 0 } });
  await flush();
  expect(host.querySelector('[role="status"]')?.textContent).toBe("chat-subagentcard.status_completed");
  expect(host.querySelector("[data-command-exit-code]")?.textContent).toBe("Exit code 0");
  expect(host.querySelector("[data-command-runtime]")?.textContent).toBe("6s");
  await tick(10_000);
  expect(host.querySelector("[data-command-runtime]")?.textContent).toBe("6s");
  expect(fetcher).toHaveBeenCalledTimes(beforeEnd + 1);
});

it.each([
  { status: "failed", exitCode: 23, word: "chat-subagentcard.status_failed" },
  { status: "stopped", exitCode: undefined, word: "chat-subagentcard.status_stopped" },
])("keeps a $status command's outcome without inventing an exit code", async ({ status, exitCode, word }) => {
  draw({ running: false, task: { ...task, started_at: "2026-11-18T09:59:48Z", ended_at: "2026-11-18T10:00:00Z", status, exit_code: exitCode } });
  await flush();
  expect(host.querySelector('[role="status"]')?.textContent).toBe(word);
  expect(host.querySelector("[data-command-runtime]")?.textContent).toBe("12s");
  expect(host.querySelector("[data-command-exit-code]")?.textContent).toBe(exitCode === undefined ? undefined : `Exit code ${exitCode}`);
  const reads = fetcher.mock.calls.length;
  await tick(10_000); expect(fetcher).toHaveBeenCalledTimes(reads);
});

it("expands the full command and labels the output lines actually read", async () => {
  const command = "printf 'a long command'\n&& npm test -- --runInBand";
  fetcher.mockResolvedValueOnce(response("first\nsecond\n"));
  draw({ task: { ...task, command } }); await flush();
  const full = host.querySelector<HTMLElement>("[data-command-full]")!;
  expect(full.textContent).toBe(command); expect(full.className).toContain("line-clamp-3");
  expect(host.querySelector("[data-command-lines]")?.textContent).toBe("lines:2");
  const more = button("chat-activity.command_show_more");
  expect(more.getAttribute("aria-expanded")).toBe("false"); expect(more.getAttribute("aria-controls")).toBe(full.id);
  expect(more.className).toContain("touch-hit");
  act(() => more.click());
  expect(full.className).not.toContain("line-clamp-3");
  expect(full.className).toContain("max-h-48"); expect(full.className).toContain("overflow-y-auto");
  expect(button("chat-activity.command_show_less").getAttribute("aria-expanded")).toBe("true");
  act(() => button("chat-activity.command_show_less").click());
  expect(full.className).toContain("line-clamp-3");
});

it("disables Retry immediately during a pending retry and preserves the last good output", async () => {
  draw(); await flush(); fetcher.mockResolvedValueOnce(new Response("failed", { status: 502 })); await tick();
  let resolve!: (r: Response) => void;
  fetcher.mockImplementationOnce(() => new Promise<Response>(r => { resolve = r; }));
  act(() => button("app.retry").click());
  expect(button("app.retry").disabled).toBe(true); expect(button("app.retry").getAttribute("aria-busy")).toBe("true");
  expect(text()).toBe("sample output"); act(() => button("app.retry").click()); await tick(6000);
  expect(fetcher).toHaveBeenCalledTimes(3); expect(text()).toBe("sample output");
  resolve(response("recovered")); await flush(); expect(text()).toBe("recovered"); expect(host.querySelector('[role="alert"]')).toBeNull();
});

it.each(["owner", "manager", "session"])("fences stale Copy and Open handlers at action time after the %s changes", async kind => {
  const writeText = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  draw(); await flush();
  const copy = button("chat-activity.copy_command"), file = button("chat-activity.open_output_file");
  act(() => {
    if (kind === "owner") setToken("synthetic-owner-b");
    else if (kind === "manager") upsertSession({ machine_id: "machine-a", session_id: "session-a", manager: "manager-b" } as Session);
    else reset();
    // These are still the old live DOM handlers: React has not committed the scope update yet.
    expect(copy.isConnected).toBe(true); expect(file.isConnected).toBe(true);
    copy.click(); file.click();
  });
  await flush(); expect(writeText).not.toHaveBeenCalled(); expect(open).not.toHaveBeenCalled();
});
