// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { addContext, getContexts, newContext } from "../../lib/links";
import { blankTask } from "../../lib/routines";
import { splitSkillDraft } from "../../lib/skill-mentions";
import { TaskPromptEditor } from "../assistant/TaskPromptEditor";
import { Composer, type Outgoing } from "../chat/Composer";

vi.mock("./ContextChipList", () => { throw new Error("Failed to fetch dynamically imported module"); });
vi.mock("../../lib/error-reports", async (load) => ({
  ...(await load<typeof import("../../lib/error-reports")>()),
  reportBoundary: vi.fn(),
}));
vi.mock("../../lib/api", async (load) => ({
  ...(await load<typeof import("../../lib/api")>()),
  api: vi.fn(async () => ({ execution: { machine_id: "fixture", launcher: "claude" }, task_input: false })),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const caught = vi.fn();

beforeEach(() => {
  caught.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host, { onCaughtError: caught });
});
afterEach(() => { act(() => root.unmount()); host.remove(); localStorage.clear(); });

function type(text: string) {
  const field = host.querySelector("textarea")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function showsFallback(title: string) {
  await act(async () => { await import("./ContextChipList").catch(() => {}); });
  expect(caught).toHaveBeenCalled();
  expect(host.textContent).toContain(title);
}

it("keeps the chat input and its selected context usable when chips fail to load", async () => {
  const draftKey = "chip-failure-chat";
  const context = newContext("context", "Example quote", "", "A synthetic excerpt");
  const send = vi.fn(async (_message: Outgoing) => true);
  act(() => {
    addContext(draftKey, context);
    root.render(<Composer draftKey={draftKey} placeholder="" onSend={send} />);
  });
  await showsFallback(context.title);
  expect(getContexts(draftKey)).toEqual([context]);

  type("Explain the excerpt");
  await act(async () => host.querySelector<HTMLButtonElement>('button[data-testid="composer-send"]')!.click());
  expect(send).toHaveBeenCalledTimes(1);
  expect(send.mock.calls[0][0].text).toContain("A synthetic excerpt");
  expect(send.mock.calls[0][0].text).toContain("Explain the excerpt");
});

it("keeps saved references, typing, and explicit removal available in the task editor", async () => {
  const task = {
    ...blankTask(), id: "fixture-task", prompt_selections: true,
    prompt: '<persona-context kind="skill" ref="review" title="Review notes">Review</persona-context>\n\nRead the notes',
  };
  let latest = task;
  function Form() {
    const [draft, setDraft] = useState(task);
    latest = draft;
    return <TaskPromptEditor draft={draft} saved={task} disabled={false}
      onChange={(prompt, prompt_selections) => setDraft({ ...draft, prompt, prompt_selections })} />;
  }
  act(() => root.render(<Form />));
  await showsFallback("Review notes");
  expect(latest).toEqual(task);
  type("Read the revised notes");
  expect(splitSkillDraft(latest.prompt, true).skills[0].attrs?.ref).toBe("review");
  expect(splitSkillDraft(latest.prompt, true).text).toBe("Read the revised notes");

  const remove = [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.getAttribute("aria-label") === t("link-contextchips.remove_label"));
  expect(remove).toBeDefined();
  act(() => remove!.click());
  expect(latest.prompt_selections).toBe(false);
  expect(latest.prompt).toBe("Read the revised notes");
});
