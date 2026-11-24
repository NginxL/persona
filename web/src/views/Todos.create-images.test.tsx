// @vitest-environment jsdom
// #11084: pictures are part of the new to-do, not a chat upload or a local pathname. The real dialogs run against a
// synthetic manager; title/notes/selections and one operation id survive failure, for ordinary and repository creates.
// #13797: a picture over the manager's 1 MiB is made ready (lib/todo-image.ts) instead of refused, from synthetic PNG
// headers; jsdom has no decoder or canvas, so a stub decoder takes the header's size and a stub canvas answers each
// encoding with a blob of a given size.
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
const { imageHead, TODO_IMAGE_BYTES } = await import("../lib/todo-image");
const { MAX_PICTURE_SOURCE } = await import("../lib/image-limits");
const { imagePayload: readPayload } = await vi.importActual<typeof import("../lib/attachments")>("../lib/attachments");

// jsdom's Blob has no arrayBuffer() (the header and the prepared picture are read with it)
Blob.prototype.arrayBuffer ??= function (this: Blob) {
  return new Promise<ArrayBuffer>((ok, fail) => {
    const r = new FileReader();
    r.onload = () => ok(r.result as ArrayBuffer);
    r.onerror = () => fail(r.error);
    r.readAsArrayBuffer(this);
  });
};
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
const preparing = () => document.querySelector("[data-testid=todo-images-preparing]");
const until = async (ok: () => boolean) => {
  for (let i = 0; i < 400 && !ok(); i++) await act(async () => { await new Promise((r) => setTimeout(r, 5)); });
  expect(ok()).toBe(true);
};
/** Picks `files` and leaves them being made ready. */
const pickNow = async (files: File[]) => {
  const input = dialog().querySelector<HTMLInputElement>('input[type=file][accept*="image"]');
  expect(input, "image picker in New to-do").toBeTruthy();
  await act(async () => {
    Object.defineProperty(input!, "files", { value: files, configurable: true });
    input!.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
};
/** Picks `files` and waits until each is attached or refused. */
const pick = async (files: File[]) => {
  await pickNow(files);
  await until(() => !preparing());
};
const openNew = async () => {
  await act(async () => button(t("todos.new_todo"))!.click());
  await flush();
  await write(dialog().querySelector<HTMLInputElement>("input[type=text]")!, "Fix the screenshot");
  await write(dialog().querySelector("textarea")!, "Keep this explanation");
};
/** A synthetic PNG: its signature and IHDR for `width` × `height`, then `size` bytes in all (at least the name's). */
const png = (name: string, width = 2, height = 3, size = 0) => {
  const head = new Uint8Array(33);
  head.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52]);
  new DataView(head.buffer).setUint32(16, width);
  new DataView(head.buffer).setUint32(20, height);
  return new File([head, name, new Uint8Array(Math.max(0, size - 33 - name.length))], name, { type: "image/png" });
};
const file = (name: string) => png(name);
const thumbnails = () => [...dialog().querySelectorAll("img")].map((img) => img.alt);
const removeButton = (name: string) => [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label") === t("ui-attachmenttray.remove", { name }))!;
/** Decodes the stub decoder holds back (`holdDecodes`) until a test lets one go. */
let decodes: { name: string; go: () => void }[];
let holdDecodes: boolean;
let undecodable: Set<string>;
let decode: ReturnType<typeof vi.fn>;

beforeEach(async () => {
  await initI18n("en");
  api.mockReset();
  imagePayload.mockReset();
  // a File the dialog sends is read here as its name; a canvas's blob that fitPicture reads is read as it is
  imagePayload.mockImplementation(async (f) => (f instanceof File ? { mime: f.type, data: btoa(`bytes:${f.name}`) } : readPayload(f)));
  decodes = [];
  holdDecodes = false;
  undecodable = new Set();
  decode = vi.fn((src: File) => new Promise((ok, fail) => {
    const go = async () => {
      const head = await imageHead(src);
      if (!head || undecodable.has(src.name)) fail(new Error("undecodable"));
      else ok({ width: head.width, height: head.height, close: vi.fn() });
    };
    if (holdDecodes) decodes.push({ name: src.name, go: () => void go() });
    else void go();
  }));
  globalThis.createImageBitmap = decode as unknown as typeof createImageBitmap;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => ({ fillRect() {}, drawImage() {} }) as unknown as CanvasRenderingContext2D);
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((done: BlobCallback, type = "image/png") => done(new Blob([new Uint8Array(600_000)], { type })));
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

describe("New to-do images over 1 MiB (#13797)", () => {
  const MiB = 1024 * 1024;
  const createButton = () => button(t("todos.create"), dialog())!;
  const submitForm = () => act(async () => void dialog().querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));

  it("prepares a screenshot over 1 MiB, never creates while it does, and sends a small picture as it is", async () => {
    holdDecodes = true;
    await openNew();
    const small = file("small.png");
    await pickNow([png("screenshot.png", 2880, 1800, 3 * MiB), small]);
    expect(preparing()?.textContent).toBe(t("todos.images_preparing", { count: 2 }));
    expect(createButton().disabled).toBe(true);
    await submitForm();
    await flush();
    expect(posts()).toHaveLength(0);

    decodes[0].go();
    await until(() => !preparing());
    expect(thumbnails()).toEqual(["screenshot.jpg", "small.png"]);
    expect(dialog().querySelector("[role=alert]")).toBeNull();
    await act(async () => createButton().click());
    await flush();
    expect(bodies()).toHaveLength(1);
    expect(bodies()[0]).toMatchObject({ title: "Fix the screenshot", notes: "Keep this explanation", images: [{ mime: "image/jpeg", data: btoa("bytes:screenshot.jpg") }, { mime: "image/png", data: btoa("bytes:small.png") }] });
    const sent = imagePayload.mock.calls.map(([f]) => f).filter((f) => f instanceof File);
    expect(sent).toHaveLength(2);
    expect([sent[0].type, sent[0].size <= TODO_IMAGE_BYTES]).toEqual(["image/jpeg", true]);
    expect(sent[1]).toBe(small);
    expect(decode).toHaveBeenCalledTimes(1);
  });

  it("refuses an over-cap source, a decompression bomb and malformed pictures by name, keeping the draft and the others", async () => {
    undecodable.add("broken.png");
    await openNew();
    await pick([
      file("keep.png"),
      png("huge.png", 3000, 2000, MAX_PICTURE_SOURCE + 1),
      png("bomb.png", 100_000, 100_000),
      new File(["plain words, not a picture"], "words.png", { type: "image/png" }),
      png("broken.png", 3000, 2000, 2 * MiB),
    ]);
    expect(thumbnails()).toEqual(["keep.png"]);
    const alert = dialog().querySelector("[role=alert]")!.textContent;
    for (const name of ["huge.png", "bomb.png"]) expect(alert).toContain(t("todos.image_source_too_big", { name, bytes: 64, pixels: 50 }));
    expect(alert).toContain(t("todos.image_unreadable", { name: "words.png" }));
    expect(alert).toContain(t("todos.image_not_prepared", { name: "broken.png" }));
    expect(decode.mock.calls.map(([f]) => (f as File).name)).toEqual(["broken.png"]);
    expect(dialog().querySelector<HTMLInputElement>("input[type=text]")!.value).toBe("Fix the screenshot");
    expect(dialog().querySelector("textarea")!.value).toBe("Keep this explanation");
    await act(async () => createButton().click());
    await flush();
    expect(bodies()).toHaveLength(1);
    expect(bodies()[0]).toMatchObject({ images: [{ mime: "image/png", data: btoa("bytes:keep.png") }] });
    expect((bodies()[0].images as unknown[]).length).toBe(1);
  });

  it("drops a picture removed while it is prepared or waiting, and goes on with the rest in order", async () => {
    holdDecodes = true;
    await openNew();
    await pickNow([png("first.png", 3000, 2000, 2 * MiB), png("second.png", 3000, 2000, 2 * MiB), file("third.png")]);
    expect(preparing()?.textContent).toBe(t("todos.images_preparing", { count: 3 }));
    await act(async () => removeButton("first.png").click());
    await act(async () => removeButton("second.png").click());
    expect(preparing()?.textContent).toBe(t("todos.images_preparing", { count: 1 }));
    // a second pick waits its turn: one picture is decoded at a time
    await pickNow([png("fourth.png", 3000, 2000, 2 * MiB)]);
    expect(preparing()?.textContent).toBe(t("todos.images_preparing", { count: 2 }));
    expect(decodes.map((d) => d.name)).toEqual(["first.png"]);
    decodes[0].go();
    await until(() => decodes.length === 2);
    expect(decodes[1].name).toBe("fourth.png");
    expect(thumbnails()).toEqual(["third.png"]);
    decodes[1].go();
    await until(() => !preparing());
    expect(thumbnails()).toEqual(["third.png", "fourth.jpg"]);
    expect(decode).toHaveBeenCalledTimes(2);
    expect(URL.createObjectURL).toHaveBeenCalledTimes(2);
  });

  it("drops what was being prepared when the dialog closes, and creates nothing", async () => {
    holdDecodes = true;
    await openNew();
    await pickNow([png("late.png", 3000, 2000, 2 * MiB)]);
    await act(async () => button(t("common.cancel"), dialog())!.click());
    await flush();
    expect(document.querySelector("[role=dialog]")).toBeNull();
    await openNew();
    expect(preparing()).toBeNull();
    decodes[0].go();
    await flush();
    await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
    expect(thumbnails()).toEqual([]);
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    expect(posts()).toHaveLength(0);
  });
});
