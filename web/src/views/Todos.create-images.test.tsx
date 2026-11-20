// @vitest-environment jsdom
// #11084: pictures are part of the new to-do, not a chat upload or a local pathname. The real dialogs run against a
// synthetic manager; title/notes/selections and one operation id survive failure, for ordinary and repository creates.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../lib/api";
import { initI18n, t } from "../i18n";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
const imagePayload = vi.hoisted(() => vi.fn<(file: File) => Promise<{ mime: string; data: string }>>());
const launched = vi.hoisted(() => vi.fn());
vi.mock("../lib/api", async (orig) => ({ ...(await orig<typeof import("../lib/api")>()), api: (path: string, init?: unknown) => api(path, init) }));
vi.mock("../lib/attachments", async (orig) => ({ ...(await orig<typeof import("../lib/attachments")>()), imagePayload: (file: File) => imagePayload(file) }));
vi.mock("../lib/new-agent", async (orig) => ({ ...(await orig<typeof import("../lib/new-agent")>()), launchAgent: (input: unknown) => { launched(input); return "local-image-todo"; } }));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const now = new Date().toISOString();
const existing = { id: "t1", title: "Existing", created_at: now, updated_at: now, agents: [] } as unknown as Todo;
let host: HTMLDivElement;
let root: Root;
let failNextCreate: ApiError | undefined;
let pendingCreates = 0;
let dropImages = false;
let deleteFails = false;
const before = { ...getState() };
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const button = (label: string, within: ParentNode = document) => [...within.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const posts = () => api.mock.calls.filter(([path, init]) => path === "/todos" && (init as { method?: string })?.method === "POST");
const bodies = () => posts().map(([, init]) => (init as { json: Record<string, unknown> }).json);
const opOf = ([, init]: (typeof api.mock.calls)[number]) => new Headers((init as RequestInit).headers).get("X-Persona-Op-Id");
const write = async (el: HTMLInputElement | HTMLTextAreaElement, value: string) => act(async () => {
  Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const pick = async (files: File[]) => {
  const input = dialog().querySelector<HTMLInputElement>('input[type=file][accept*="image"]');
  expect(input, "image picker in New to-do").toBeTruthy();
  await act(async () => {
    Object.defineProperty(input!, "files", { value: files, configurable: true });
    input!.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
};
const openNew = async () => {
  await act(async () => button(t("todos.new_todo"))!.click());
  await flush();
  await write(dialog().querySelector<HTMLInputElement>("input[type=text]")!, "Fix the screenshot");
  await write(dialog().querySelector("textarea")!, "Keep this explanation");
};
const file = (name: string) => new File([name], name, { type: "image/png" });

beforeEach(async () => {
  await initI18n("en");
  api.mockReset();
  imagePayload.mockReset();
  imagePayload.mockImplementation(async (f) => ({ mime: "image/png", data: btoa(`bytes:${f.name}`) }));
  launched.mockReset();
  failNextCreate = undefined;
  pendingCreates = 0;
  dropImages = false;
  deleteFails = false;
  api.mockImplementation(async (path, init) => {
    const method = (init as { method?: string })?.method;
    if (path === "/todos" && method === "POST") {
      if (failNextCreate) {
        const error = failNextCreate;
        failNextCreate = undefined;
        throw error;
      }
      if (pendingCreates > 0) {
        pendingCreates -= 1;
        return { state: "pending" };
      }
      const body = (init as { json: Record<string, unknown> }).json;
      const images = dropImages ? undefined : (body.images as { id: string; mime: string }[] | undefined)?.map((img) => ({ id: img.id, mime: img.mime, width: 2, height: 3, url: `/api/v1/todos/t2/images/${img.id}` }));
      return { todo: { ...existing, id: "t2", title: body.title, notes: body.notes, images } };
    }
    if (path === "/todos/t2" && method === "DELETE") {
      if (deleteFails) throw new ApiError(503, "cleanup unavailable", { error: "cleanup unavailable" });
      return {};
    }
    if (path === "/todos") return { todos: [existing] };
    if (path === "/assistant/repositories") return { manager: "assistant", catalog_revision: 1, repositories: [{ id: "web", provider: "github", instance_url: "https://github.com", path: "example/web", canonical_url: "https://github.com/example/web", name: "Web app", verified_at: "2026-01-01T00:00:00Z" }] };
    if (path.endsWith("/launchers")) return { launchers: [] };
    if (path.endsWith("/decisions")) return { decisions: [] };
    return {};
  });
  seed({ todos: [existing], todosLoaded: true, sessions: {}, interactions: [], answered: {}, features: ["todo_images"], machines: [{ id: "m1", name: "Remote box", online: true }] });
  let objectURL = 0;
  URL.createObjectURL = vi.fn(() => `blob:todo-${++objectURL}`);
  URL.revokeObjectURL = vi.fn();
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
  vi.restoreAllMocks();
});

describe("New to-do images", () => {
  it("previews, removes, and saves the selected image atomically without starting an Agent", async () => {
    await openNew();
    await pick([file("first.png"), file("second.png")]);
    expect([...dialog().querySelectorAll("img")].map((img) => img.alt)).toEqual(["first.png", "second.png"]);
    const removes = [...dialog().querySelectorAll("button")].filter((b) => (b.getAttribute("aria-label") ?? b.textContent ?? "").includes(t("todos.remove")));
    expect(removes.length).toBe(2);
    await act(async () => removes[0].click());
    await flush();
    expect([...dialog().querySelectorAll("img")].map((img) => img.alt)).toEqual(["second.png"]);
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(bodies()).toHaveLength(1);
    expect(bodies()[0]).toMatchObject({ title: "Fix the screenshot", notes: "Keep this explanation", images: [{ mime: "image/png", data: btoa("bytes:second.png") }] });
    expect(JSON.stringify(bodies()[0])).not.toContain("/Users/");
    expect(opOf(posts()[0])).toMatch(/^[0-9a-f-]{36}$/);
    expect(launched).not.toHaveBeenCalled();
  });

  it("shows creation progress and retains title, notes, image and operation id on failure/retry", async () => {
    await openNew();
    await pick([file("retry.png")]);
    failNextCreate = new ApiError(500, "synthetic upload failure", { error: "synthetic upload failure" });
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(dialog()).toBeTruthy();
    expect(dialog().querySelector<HTMLInputElement>("input[type=text]")!.value).toBe("Fix the screenshot");
    expect(dialog().querySelector("textarea")!.value).toBe("Keep this explanation");
    expect([...dialog().querySelectorAll("img")].map((img) => img.alt)).toEqual(["retry.png"]);
    expect(dialog().textContent).toContain("synthetic upload failure");
    expect(posts()).toHaveLength(1);
    const firstOp = opOf(posts()[0]);
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(posts()).toHaveLength(2);
    expect(bodies()[1]).toEqual(bodies()[0]);
    expect(opOf(posts()[1])).toBe(firstOp);
  });

  it("cancels a failed create without another write or an implicit Agent start", async () => {
    await openNew();
    await pick([file("cancel.png")]);
    failNextCreate = new ApiError(500, "synthetic upload failure", { error: "synthetic upload failure" });
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    await act(async () => button(t("common.cancel"), dialog())!.click());
    await flush();
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(posts()).toHaveLength(1);
    expect(launched).not.toHaveBeenCalled();
  });

  it("keeps the draft on a pending receipt and replays it instead of reporting success", async () => {
    await openNew();
    await pick([file("pending.png")]);
    pendingCreates = 1;
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(dialog()).toBeTruthy();
    expect(dialog().textContent).toContain(t("lib-todos.create_unconfirmed"));
    expect([...dialog().querySelectorAll("img")].map((img) => img.alt)).toEqual(["pending.png"]);
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(posts()).toHaveLength(2);
    expect(opOf(posts()[0])).toBe(opOf(posts()[1]));
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("uses a fresh operation ID after correcting a refused create", async () => {
    await openNew();
    await pick([file("wrong.png")]);
    failNextCreate = new ApiError(400, "invalid image", { error: "invalid image" });
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    const firstOp = opOf(posts()[0]);
    const removes = [...dialog().querySelectorAll("button")].filter((b) => (b.getAttribute("aria-label") ?? "").includes(t("todos.remove")));
    await act(async () => removes[0].click());
    await pick([file("valid.png")]);
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(posts()).toHaveLength(2);
    expect(opOf(posts()[1])).not.toBe(firstOp);
    expect(bodies()[1]).toMatchObject({ images: [{ mime: "image/png", data: btoa("bytes:valid.png") }] });
  });

  it("refuses silent image loss and removes the incomplete create", async () => {
    await openNew();
    await pick([file("old-manager.png")]);
    dropImages = true;
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(dialog()).toBeTruthy();
    expect(dialog().textContent).toContain(t("lib-todos.images_not_saved"));
    expect([...dialog().querySelectorAll("img")].map((img) => img.alt)).toEqual(["old-manager.png"]);
    expect(api.mock.calls.some(([path, init]) => path === "/todos/t2" && (init as { method?: string })?.method === "DELETE")).toBe(true);
    expect(launched).not.toHaveBeenCalled();
  });

  it("offers no image picker when any manager is too old to store them", async () => {
    seed({ features: [] });
    await openNew();
    expect(dialog().querySelector('input[type="file"][accept*="image"]')).toBeNull();
    expect(dialog().textContent).not.toContain(t("todos.add_images"));
  });

  it("says when fallback cleanup fails instead of hiding it", async () => {
    await openNew();
    await pick([file("old-manager.png")]);
    dropImages = true;
    deleteFails = true;
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(dialog()).toBeTruthy();
    expect(dialog().textContent).toContain(t("lib-todos.images_cleanup_failed"));
    expect([...dialog().querySelectorAll("img")].map((img) => img.alt)).toEqual(["old-manager.png"]);
    expect(launched).not.toHaveBeenCalled();
  });

  it("uses the same image contract with repository bindings", async () => {
    await openNew();
    await act(async () => button(t("task-repositories.title"), dialog())!.click());
    await flush();
    await act(async () => button(t("task-repositories.add"), dialog())!.click());
    await flush();
    await act(async () => dialog().querySelector<HTMLElement>("[role=option]")!.click());
    await flush();
    await pick([file("repository.png")]);
    await act(async () => button(t("todos.create"), dialog())!.click());
    await flush();
    expect(bodies()).toHaveLength(1);
    expect(bodies()[0]).toMatchObject({
      title: "Fix the screenshot", notes: "Keep this explanation",
      repository_bindings: { catalog_revision: 1, repositories: [{ repo_id: "web" }] },
      images: [{ mime: "image/png", data: btoa("bytes:repository.png") }],
    });
    expect(opOf(posts()[0])).toMatch(/^[0-9a-f-]{36}$/);
  });
});
