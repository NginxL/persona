// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi, type Mock } from "vitest";
import { initI18n } from "../i18n";
import { ApiError, setToken } from "../lib/api";
import { chooseManager, MANAGER_HEADER, resetManagerChoice, settleRosterDefault } from "../lib/manager-choice";
import { StarredBackupDialog } from "./StarredBackupDialog";

const io = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../lib/api", async (actual) => ({ ...await actual<typeof import("../lib/api")>(), api: io.request }));
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const path = "/assistant/starred/import";
const bundle = {
  format: "persona.starred",
  version: 1,
  messages: [{
    key: "sm" + "a".repeat(32), conversation: "assistant", role: "assistant", title: "Reply", speaker: "Persona",
    text: "A saved reply.", starred_at: "2026-11-21T03:04:00Z",
  }],
};
const raw = JSON.stringify(bundle, null, 2) + "\n";
const bytes = (value: string) => new TextEncoder().encode(value).buffer;
const preview = (revision: number) => ({ revision, total: 1, to_add: 1, already_saved: 0, available: 3 });
let root: Root;
let onClose: Mock<() => void>;
let onRestored: Mock<() => void>;
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((button) => button.textContent === label);
const input = () => document.querySelector<HTMLInputElement>('input[type="file"]');
const click = async (label: string) => { await act(async () => button(label)!.click()); };
const pick = async (size: number, read = vi.fn(async () => bytes(raw))) => {
  const file = new File([""], "example.json", { type: "application/json" });
  Object.defineProperties(file, { size: { value: size }, arrayBuffer: { value: read } });
  Object.defineProperty(input()!, "files", { configurable: true, value: [file] });
  await act(async () => input()!.dispatchEvent(new Event("change", { bubbles: true })));
  return read;
};
const show = () => act(async () => {
  root.render(<StarredBackupDialog stars={[]} canWrite speaker={(star) => star.speaker} onClose={onClose} onRestored={onRestored} />);
});

beforeEach(async () => {
  await initI18n("en");
  resetManagerChoice();
  setToken("backup-unit-test");
  settleRosterDefault();
  io.request.mockReset();
  onClose = vi.fn<() => void>();
  onRestored = vi.fn<() => void>();
  root = createRoot(document.createElement("div"));
});
afterEach(async () => {
  await act(async () => root.unmount());
});

it.each([404, 405])("shows just downloads when an older manager does not offer restore (HTTP %s)", async (status) => {
  io.request.mockRejectedValueOnce(new ApiError(status, "Not supported", {}));
  await show();
  expect(button("Download JSON")).toBeDefined();
  expect(button("Download Markdown")).toBeDefined();
  expect(input()).toBeNull();
  expect(io.request.mock.calls.map(([url]) => url)).toEqual([path]);
});

it("checks file size locally before reading or posting a preview", async () => {
  io.request.mockResolvedValueOnce({ can_import: true });
  await show();
  const read = await pick(8 * 1024 * 1024 + 1);
  expect(button("Preview restore")?.disabled).toBe(true);
  expect(document.querySelector('[role="alert"]')?.textContent).toContain("8 MB");
  expect(read).not.toHaveBeenCalled();
  expect(io.request).toHaveBeenCalledTimes(1);
});

it("asks separately to restore the exact reviewed revision and requires another review after 409", async () => {
  io.request.mockResolvedValueOnce({ can_import: true });
  await show();
  await pick(500);
  expect(button("Restore messages")).toBeUndefined();
  io.request.mockResolvedValueOnce(preview(1));
  await click("Preview restore");
  expect(io.request.mock.lastCall).toMatchObject([path + "/preview", { method: "POST", body: raw,
    headers: { "Content-Type": "application/json", [MANAGER_HEADER]: "assistant" },
  }]);
  expect(document.body.textContent).toContain("Will restore1");
  io.request.mockRejectedValueOnce(new ApiError(409, "Conflict", {}));
  await click("Restore messages");
  expect(io.request.mock.lastCall).toMatchObject([path, { method: "POST", json: { ...bundle, revision: 1 } }]);
  expect(io.request).toHaveBeenCalledTimes(3);
  expect(button("Restore messages")).toBeUndefined();
  expect(document.querySelector('[role="alert"]')?.textContent).toContain("Preview the file again");
  expect(onRestored).not.toHaveBeenCalled();

  io.request.mockResolvedValueOnce(preview(2));
  await click("Preview restore");
  io.request.mockResolvedValueOnce({ revision: 3, added: 1, already_saved: 0 });
  await click("Restore messages");
  expect(io.request.mock.lastCall).toMatchObject([path, { method: "POST", json: { ...bundle, revision: 2 } }]);
  expect(onRestored).toHaveBeenCalledOnce();
});

it("does not post malformed UTF-8 and preserves duplicate keys for manager validation", async () => {
  io.request.mockResolvedValueOnce({ can_import: true });
  await show();
  await pick(500, vi.fn(async () => new Uint8Array([0x7b, 0xc3, 0x28]).buffer));
  await click("Preview restore");
  expect(io.request).toHaveBeenCalledTimes(1);
  expect(document.querySelector('[role="alert"]')?.textContent).toContain("valid Persona Starred");

  const duplicate = raw.replace('"version": 1,', '"version": 1, "version": 1,');
  await pick(500, vi.fn(async () => bytes(duplicate)));
  io.request.mockRejectedValueOnce(new ApiError(400, "Duplicate key", {}));
  await click("Preview restore");
  expect(io.request.mock.lastCall).toMatchObject([path + "/preview", { method: "POST", body: duplicate }]);
  expect(button("Restore messages")).toBeUndefined();
});

it("retires a file read when the assistant changes, even after switching back", async () => {
  io.request.mockImplementation(async (url) => {
    if (url === path) return { can_import: true };
    return {};
  });
  await show();
  let finish: (raw: ArrayBuffer) => void = () => {};
  const read = vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finish = resolve; }));
  await pick(500, read);
  await click("Preview restore");
  expect(read).toHaveBeenCalledOnce();
  await act(async () => { chooseManager("other"); chooseManager(null); finish(bytes(raw)); });
  expect(onClose).toHaveBeenCalled();
  expect(io.request.mock.calls.some(([url]) => url === path + "/preview")).toBe(false);
});
