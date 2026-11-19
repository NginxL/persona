// @vitest-environment jsdom
// The real New Todo dialog submit path with a controlled repository child: a normalized duplicate draft must be
// actionable and never post, and correcting it must send one atomic request with the retained title.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
const fixture = vi.hoisted(() => ({ extension: false }));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));
vi.mock("../ui/TaskRepositoryBindingsEditor", async () => {
  const React = await import("react");
  const identity = { provider: "github", instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web" };
  const catalogData = { manager: "assistant", catalog_revision: 1, repositories: [{ ...identity, id: "web", name: "Web app", verified_at: "2026-01-01T00:00:00Z" }] };
  const duplicate = { catalog_revision: 1, repositories: [{ repo_id: "web", requested_ref: { kind: "default" }, links: [
    { kind: "issue", number: "7", title: "Structured title" },
    { url: "http://github.com/Example/Web/issues/7", title: "Alias title" },
  ] }] };
  function NewTodoRepositoryBindings({ value, onChange, onCatalog }: {
    value?: typeof duplicate; onChange: (value: typeof duplicate) => void; onCatalog?: (catalog: typeof catalogData) => void;
  }) {
    const once = React.useRef(false);
    React.useEffect(() => {
      if (once.current) return;
      once.current = true;
      if (fixture.extension) {
        const source = { provider: "ext:acme-code/acme", instance_url: "https://code.acme.test", path: "team/app", canonical_url: "https://code.acme.test/team/app" };
        onCatalog?.({ ...catalogData, repositories: [{ ...catalogData.repositories[0], ...source }] });
        onChange({ ...duplicate, repositories: [{ ...duplicate.repositories[0], links: [
          { url: source.canonical_url + "/reviews/13", title: "First review" },
          { url: source.canonical_url + "/issues/14", title: "Second issue" },
        ] }] });
      } else {
        onCatalog?.(catalogData);
        onChange(duplicate);
      }
    }, []);
    return React.createElement("div", { "data-duplicate-draft": "" },
      React.createElement("span", null, JSON.stringify(value?.repositories[0].links ?? [])),
      React.createElement("button", { type: "button", onClick: () => onChange({ catalog_revision: 1, repositories: [{
        repo_id: "web", requested_ref: { kind: "default" }, links: [{ kind: "issue", number: "7", title: "Structured title" }],
      }] }) }, "Use only the structured link"));
  }
  return {
    NewTodoRepositoryBindings,
    TodoRepositoryBindings: () => null,
    useRepositoryRequestContext: () => ({ manager: "assistant", owner: "", token: "", isCurrent: () => true }),
  };
});

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
const { t } = await import("../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const iso = (ago: number) => new Date(Date.now() - ago).toISOString();
const todo = { id: "t1", title: "Plan the trip", created_at: iso(7_200_000), updated_at: iso(3_600_000), agents: [] } as unknown as Todo;
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(async () => {
  fixture.extension = false;
  await initI18n("en");
  api.mockReset();
  api.mockImplementation(async (path, init) => {
    if (path === "/todos" && (init as { method?: string })?.method === "POST") return { todo: { ...todo, id: "t2", title: "Pack" } };
    return {};
  });
  seed({ todos: [todo], todosLoaded: true, sessions: {}, interactions: [], answered: {}, machines: [] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<TodosView selectedId="t1" />));
  await flush();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  await initI18n("zh-Hans");
});

const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const posts = () => api.mock.calls.filter(([path, init]) => path === "/todos" && (init as { method?: string })?.method === "POST").map(([, init]) => (init as { json: unknown }).json);
const write = async (el: HTMLInputElement | HTMLTextAreaElement, value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("New Todo with a duplicate repository-link draft", () => {
  it("creates once with two distinct pending extension URLs and retains the title", async () => {
    fixture.extension = true;
    await act(async () => button(t("todos.new_todo"))!.click());
    await flush();
    await write(dialog()!.querySelector<HTMLInputElement>("input[type=text]")!, "Pack");
    await flush();
    expect(button(t("todos.create"))!.disabled).toBe(false);
    const form = dialog()!.querySelector("form")!;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    await flush();
    expect(posts()).toEqual([{
      title: "Pack", repository_bindings: { catalog_revision: 1, repositories: [{
        repo_id: "web", requested_ref: { kind: "default" }, links: [
          { url: "https://code.acme.test/team/app/reviews/13", title: "First review" },
          { url: "https://code.acme.test/team/app/issues/14", title: "Second issue" },
        ],
      }] },
    }]);
  });

  it("shows the normalized duplicate, guards every submit path and creates once after correction", async () => {
    await act(async () => button(t("todos.new_todo"))!.click());
    await flush();
    await write(dialog()!.querySelector<HTMLInputElement>("input[type=text]")!, "Pack");
    await flush();
    expect(dialog()!.textContent).toContain("Structured title");
    expect(dialog()!.textContent).toContain("Alias title");
    expect(dialog()!.textContent).toContain("Remove one of the duplicate issue or PR links.");
    expect(button(t("todos.create"))!.disabled).toBe(true);
    const form = dialog()!.querySelector("form")!;
    await act(async () => form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await flush();
    expect(posts()).toEqual([]);
    await act(async () => dialog()!.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true })));
    await flush();
    expect(posts()).toEqual([]);
    await act(async () => button("Use only the structured link")!.click());
    await flush();
    expect(dialog()!.textContent).not.toContain("Remove one of the duplicate issue or PR links.");
    expect(button(t("todos.create"))!.disabled).toBe(false);
    await act(async () => button(t("todos.create"))!.click());
    await flush();
    expect(posts()).toEqual([{
      title: "Pack",
      repository_bindings: { catalog_revision: 1, repositories: [{
        repo_id: "web", requested_ref: { kind: "default" },
        links: [{ kind: "issue", number: "7", title: "Structured title" }],
      }] },
    }]);
  });
});
