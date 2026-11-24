// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { t } from "../../i18n";
import type { ComposerCommand } from "../../lib/extension-prompts";
import type { MentionSource } from "./ComposerPanel";
import { Composer } from "./Composer";

const chunk = vi.hoisted(() => {
  let reject!: (error: Error) => void;
  const promise = new Promise<never>((_, r) => { reject = r; });
  void promise.catch(() => {}); // this chunk is expected to fail; every consumer still receives the same rejection
  return { promise, fail: () => reject(new Error("stale skill chunk")) };
});
vi.mock("./SlashSkillPalette", () => chunk.promise);
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const COMMANDS: ComposerCommand[] = [
  { word: "/alpha", desc: "First command", text: "/alpha" },
  { word: "/beta", desc: "Second command", text: "/beta" },
];
const source: MentionSource = { files: () => [], skills: { manager: "assistant", environment: "test" } };
let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  void loadPages().catch(() => {}); // the held skill chunk is released by the test; every other chunk can finish
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
});
beforeEach(() => {
  Element.prototype.scrollIntoView ??= () => {};
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

function render(commands: ComposerCommand[]) {
  act(() => root.render(<Composer draftKey="skill-chunk-test" placeholder="" mention={source} machineId="computer" commands={commands} onSend={async () => true} />));
}

it("keeps bare / clean and preserves the command fallback through keyword skill-chunk loading and failure", async () => {
  render(COMMANDS);
  const box = host.querySelector("textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, "/");
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await vi.waitFor(() => expect(host.querySelectorAll("[role=option]")).toHaveLength(2));
  expect(host.textContent).not.toContain(t("chat-composerpanel.skills_loading"));

  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, "/a");
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await vi.waitFor(() => expect(host.querySelectorAll("[role=option]")).toHaveLength(2));
  await vi.waitFor(() => expect(host.textContent).toContain(t("chat-composerpanel.skills_loading")));
  await act(async () => { chunk.fail(); await Promise.resolve(); });
  await vi.waitFor(() => expect(host.textContent).toContain(t("chat-composerpanel.skills_error")));
  expect([...host.querySelectorAll("[role=option]")].map((row) => row.textContent)).toEqual(["alpha", "beta"]);
  expect(host.querySelectorAll("[role=status]")).toHaveLength(1);

  render([]);
  expect(host.querySelectorAll("[role=option]")).toHaveLength(0);
  expect(host.textContent).toContain(t("chat-composerpanel.skills_error"));
  expect(host.textContent).toContain(t("chat-slashpalette.no_match_skills"));
});
