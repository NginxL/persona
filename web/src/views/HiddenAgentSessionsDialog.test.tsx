// @vitest-environment jsdom
// #8584: the hidden sessions dialog shows a failed read as its retryable error row; the read's rejection (the list has
// already put it in its snapshot) must not also escape as an unhandled rejection.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../i18n";
import type { Session } from "../lib/types";
import { HiddenAgentSessionsDialog } from "./HiddenAgentSessionsDialog";

const reads: { path: string; resolve: (v: unknown) => void; reject: (e: Error) => void }[] = [];
vi.mock("../lib/api", async (orig) => ({ ...await orig<typeof import("../lib/api")>(), api: (path: string) => new Promise((resolve, reject) => reads.push({ path, resolve, reject })) }));
vi.mock("../lib/current-assistant", () => ({ useScoped: <T,>(items: T[]) => items }));
vi.mock("../lib/store", () => ({ onHubEvent: () => () => {} }));

let host: HTMLDivElement, root: Root;
const unhandled: unknown[] = [];
const onUnhandled = (reason: unknown) => { unhandled.push(reason); };
beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  reads.length = 0; unhandled.length = 0;
  process.on("unhandledRejection", onUnhandled);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); process.off("unhandledRejection", onUnhandled); });

const row: Session = { machine_id: "demo", session_id: "s1", title: "Scratch work", cwd: "/demo/scratch", status: "idle", state: "idle", hidden_by_directory: true };
// a turn of the event loop: Node reports a rejection nobody handled after the microtasks have run
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });
const button = (label: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(label));
async function open() {
  await act(async () => root.render(<HiddenAgentSessionsDialog open onClose={() => {}} />));
  await flush();
  expect(reads.at(-1)!.path).toContain("include_hidden=1");
}

it("shows a failed load and a failed retry as the retryable error row, without an unhandled rejection", async () => {
  await open();
  await act(async () => reads.at(-1)!.reject(new Error("hub unreachable")));
  await flush();
  expect(document.body.textContent).toContain("hub unreachable");
  await act(async () => button(t("hidden-agent-dirs.retry"))!.click());
  await flush();
  await act(async () => reads.at(-1)!.reject(new Error("still unreachable")));
  await flush();
  expect(document.body.textContent).toContain("still unreachable");
  expect(button(t("hidden-agent-dirs.retry"))).toBeDefined();
  expect(unhandled).toEqual([]);
});

it("shows a failed next page as the retryable error row, without an unhandled rejection", async () => {
  await open();
  await act(async () => reads.at(-1)!.resolve({ sessions: [row], remove: [], total: 2, has_more: true, next_cursor: "c1", revision: "r1" }));
  await flush();
  expect(document.body.textContent).toContain("Scratch work");
  await act(async () => button(t("hidden-agent-dirs.more"))!.click());
  await flush();
  expect(reads.at(-1)!.path).toContain("cursor=c1");
  await act(async () => reads.at(-1)!.reject(new Error("page failed")));
  await flush();
  expect(document.body.textContent).toContain("page failed");
  expect(unhandled).toEqual([]);
});
