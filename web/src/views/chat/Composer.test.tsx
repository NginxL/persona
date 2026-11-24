// @vitest-environment jsdom
// Pictures put in the box and never sent free their object URLs when the box goes away or shows another
// conversation (#2378), including ones still being read or a send that fails after the box moved on.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { addQuote, getQuotes, sendDraft, setQuotes } from "../../lib/drafts";
import { toast } from "../../ui/atoms";
import { Composer, type Outgoing } from "./Composer";
import { onPermissionPicker } from "../../lib/permissions-command";
import { t } from "../../i18n";
import { setPublishedImageLimits } from "../../lib/image-limits";

// a picture is scaled on a canvas (lib/image-scale.ts), which jsdom has not: each read waits on a promise the test
// settles, with the picture as the message carries it or null for one the page cannot read
const reads: ((p: { mime: string; data: string } | null) => void)[] = [];
// a file upload (lib/composer-files.ts) waits on a promise the test settles too, with the path the machine stored it at
const uploads: ((path: string) => void)[] = [];
const failures: ((e: Error) => void)[] = [];
vi.mock("../../lib/image-scale", () => ({
  fitPicture: () => new Promise<{ mime: string; data: string } | null>((resolve) => reads.push(resolve)),
}));
/** Settles the `i`th read once it has started (the scaling module is imported first). */
const settle = async (i: number, p: { mime: string; data: string } | null = { mime: "image/png", data: btoa("png") }) => {
  for (let n = 0; n < 200 && !reads[i]; n++) await act(async () => void (await new Promise((ok) => setTimeout(ok, 2))));
  await act(async () => reads[i](p));
};
vi.mock("../../lib/file-ops", () => ({
  uploadAttachment: (_mid: string, file: File) =>
    new Promise((resolve, reject) => {
      uploads.push((path) => resolve({ path, name: path.split("/").pop(), size: file.size }));
      failures.push(reject);
    }),
}));

vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let urls = 0;
const create = vi.fn(() => `blob:test/${++urls}`);
const revoke = vi.fn();
let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  await loadPages();
});

beforeEach(() => {
  urls = 0;
  reads.length = 0;
  uploads.length = 0;
  failures.length = 0;
  create.mockClear();
  revoke.mockClear();
  URL.createObjectURL = create;
  URL.revokeObjectURL = revoke;
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const render = (draftKey: string, onSend: (m: Outgoing) => Promise<boolean> = async () => true) =>
  act(() => root.render(<Composer draftKey={draftKey} placeholder="" onSend={onSend} />));

// an edited picture from the chat: no canvas on this path
const putImage = (draftKey: string) =>
  act(() => {
    sendDraft(draftKey, { type: "image", image: { name: "a.png", mime: "image/png", data: btoa("png") } });
  });

const pickFile = (file: File) =>
  act(() => {
    const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });

const trayImages = () => host.querySelectorAll("img").length;

/** Waits inside act until `check` passes: the "/" palette is a lazy chunk. */
const until = async (check: () => void) => {
  for (let i = 0; i < 200; i++) {
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 5));
    });
    try {
      return check();
    } catch {
      // not yet
    }
  }
  check();
};

describe("double Esc recalls the last message", () => {
  const recall = vi.fn();
  const mount = (props: Partial<React.ComponentProps<typeof Composer>> = {}) => act(() => root.render(
    <Composer draftKey="recall" placeholder="" onSend={async () => true} onEditLastMessage={recall} {...props} />,
  ));
  const key = (name = "Escape", extra: KeyboardEventInit = {}) => act(() => {
    host.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: name, bubbles: true, cancelable: true, ...extra }));
  });
  beforeEach(() => recall.mockClear());

  it("requires two presses within 500 ms and ignores held-key repeats", () => {
    let now = 0;
    const clock = vi.spyOn(performance, "now").mockImplementation(() => now);
    try {
      mount(); key();
      expect(recall).not.toHaveBeenCalled();
      now = 501; key();
      expect(recall).not.toHaveBeenCalled();
      now = 600; key();
      expect(recall).toHaveBeenCalledTimes(1);
      key("Escape", { repeat: true });
      expect(recall).toHaveBeenCalledTimes(1);
    } finally { clock.mockRestore(); }
  });

  it.each([
    { working: true }, { needsYou: true }, { disabled: true }, { onEditLastMessage: undefined },
    { blocked: { label: "Not ready" } },
  ])("does not recall while unavailable: %j", (props) => {
    mount(props); key(); key();
    expect(recall).not.toHaveBeenCalled();
  });

  it("keeps draft text, quotes and attachments", () => {
    mount();
    act(() => sendDraft("recall", { type: "replace", text: "Unsent draft" }));
    key(); key();
    expect(recall).not.toHaveBeenCalled();
    expect(host.querySelector("textarea")!.value).toBe("Unsent draft");
    act(() => sendDraft("recall", { type: "replace", text: "" }));
    act(() => addQuote("recall", { text: "Unsent quote" }));
    key(); key();
    expect(recall).not.toHaveBeenCalled();
    act(() => setQuotes("recall", []));
    putImage("recall"); key(); key();
    expect(recall).not.toHaveBeenCalled();
    expect(trayImages()).toBe(1);
  });

  it("does not count Escape used by IME, modifiers, menus or another key", () => {
    mount();
    for (const extra of [{ isComposing: true }, { ctrlKey: true }, { repeat: true }]) {
      key("Escape", extra); key();
      expect(recall).not.toHaveBeenCalled();
      key("ArrowLeft");
    }
    key(); key("ArrowLeft"); key();
    expect(recall).not.toHaveBeenCalled();
    act(() => sendDraft("recall", { type: "replace", text: "/" }));
    key();
    act(() => sendDraft("recall", { type: "replace", text: "" })); key();
    expect(recall).not.toHaveBeenCalled();
  });

  it("clears the first press when focus or the conversation changes", () => {
    mount(); key();
    act(() => { host.querySelector("textarea")!.focus(); host.querySelector("textarea")!.blur(); }); key();
    expect(recall).not.toHaveBeenCalled();
    mount({ draftKey: "another" }); key();
    expect(recall).not.toHaveBeenCalled();
  });

  it("leaves Escape to an open dialog or menu", () => {
    mount();
    for (const role of ["dialog", "menu"]) {
      const overlay = document.createElement("div");
      overlay.setAttribute("role", role);
      host.append(overlay);
      key(); key();
      expect(recall).not.toHaveBeenCalled();
      overlay.remove();
    }
  });

  it("receives the recalled draft while fetching but sends only after completion", async () => {
    const send = vi.fn(async () => true);
    mount({ loadingDraft: true, onSend: send });
    act(() => sendDraft("recall", { type: "replace", text: "Restored prompt" }));
    expect(host.querySelector("textarea")!.readOnly).toBe(true);
    expect(host.querySelector("[inert]")).not.toBeNull();
    expect(host.querySelector("textarea")!.value).toBe("Restored prompt");
    key("Enter");
    expect(send).not.toHaveBeenCalled();
    mount({ loadingDraft: false, onSend: send });
    expect(host.querySelector("[inert]")).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    expect(send).toHaveBeenCalledWith({ text: "Restored prompt", images: [] });
  });
});

describe("Composer image object URLs", () => {
  it("are freed when the box unmounts", () => {
    render("a");
    putImage("a");
    expect(create).toHaveBeenCalledTimes(1);
    expect(revoke).not.toHaveBeenCalled();
    act(() => root.unmount());
    expect(revoke).toHaveBeenCalledWith("blob:test/1");
    root = createRoot(host); // afterEach unmounts again
  });

  it("are freed when the box switches to another conversation", () => {
    render("a");
    putImage("a");
    render("b");
    expect(revoke).toHaveBeenCalledWith("blob:test/1");
    expect(trayImages()).toBe(0);
  });

  it("are freed, not put back into the other conversation, when a send fails after the switch", async () => {
    let finish!: (ok: boolean) => void;
    render("a", () => new Promise<boolean>((resolve) => (finish = resolve)));
    putImage("a");
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    render("b", async () => true);
    expect(revoke).not.toHaveBeenCalled(); // the picture is in flight, not in the box
    await act(async () => finish(false));
    expect(revoke).toHaveBeenCalledWith("blob:test/1");
    expect(trayImages()).toBe(0);
  });

  it("are put back when a send fails in the same conversation", async () => {
    render("a", async () => false);
    putImage("a");
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    expect(revoke).not.toHaveBeenCalled();
    expect(trayImages()).toBe(1);
  });

  it("are freed for a picked file whose read finishes after the switch", async () => {
    render("a");
    await pickFile(new File(["png"], "b.png", { type: "image/png" }));
    render("b");
    await settle(0);
    expect(create).toHaveBeenCalledTimes(1);
    expect(revoke).toHaveBeenCalledWith("blob:test/1");
    expect(trayImages()).toBe(0);
  });

  it("are freed for a picked file whose read finishes after the box unmounted", async () => {
    render("a");
    await pickFile(new File(["png"], "b.png", { type: "image/png" }));
    act(() => root.unmount());
    await settle(0);
    expect(revoke).toHaveBeenCalledWith("blob:test/1");
    root = createRoot(host);
  });
});

// A file sent to the machine puts its path into the draft of the conversation it was picked in, not into the one
// the box shows when the upload finishes.
// Files picked for a message are tiles above the input, never words in it (#8361): they upload for their own
// conversation, hold the message until they are there, and go with it as <persona-file> tags after the words.
describe("Composer files", () => {
  const box = () => host.querySelector("textarea")!;
  const tiles = () => Array.from(host.querySelectorAll<HTMLElement>("[data-testid=composer-files] > span"));
  const renderOn = (draftKey: string, onSend: (m: Outgoing) => Promise<boolean> = async () => true) =>
    act(() => root.render(<Composer draftKey={draftKey} machineId="m1" placeholder="" onSend={onSend} />));
  const pickNotes = async () => {
    await pickFile(new File(["notes"], "notes.txt", { type: "text/plain" }));
    await until(() => expect(uploads).toHaveLength(1));
  };
  const sendButton = () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]');
  const forget = () => tiles().forEach((t) => act(() => t.querySelector<HTMLButtonElement>("button[data-remove]")!.click()));

  afterEach(() => {
    renderOn("a");
    forget();
    renderOn("b");
    forget();
  });

  it("puts nothing in the words and sends the file as a tag after them", async () => {
    const send = vi.fn(async () => true);
    renderOn("a", send);
    act(() => void sendDraft("a", { type: "replace", text: "look at" }));
    await pickNotes();
    expect(tiles().map((t) => t.dataset.state)).toEqual(["uploading"]);
    expect(sendButton()?.disabled ?? true).toBe(true); // holds the message while it uploads
    await act(async () => uploads[0]("/home/me/.persona/uploads/2026-11-15/notes.txt"));
    expect(box().value).toBe("look at");
    expect(tiles().map((t) => t.dataset.state)).toEqual(["ready"]);
    await act(async () => sendButton()!.click());
    expect(send).toHaveBeenCalledWith({
      text: 'look at\n\n<persona-file kind="file" path="/home/me/.persona/uploads/2026-11-15/notes.txt" name="notes.txt" size="5"/>',
      images: [],
    });
    expect(tiles()).toEqual([]);
  });

  it("goes on uploading for its own conversation while the box shows another, and is there when it comes back", async () => {
    renderOn("a");
    await pickNotes();
    renderOn("b");
    expect(tiles()).toEqual([]);
    await act(async () => uploads[0]("/u/notes.txt"));
    expect(box().value).toBe("");
    renderOn("a");
    expect(tiles().map((t) => t.dataset.state)).toEqual(["ready"]);
    expect(box().value).toBe("");
  });

  it("keeps a failed one as a tile to try again", async () => {
    renderOn("a");
    await pickNotes();
    await act(async () => failures[0](new Error("no space left on device")));
    expect(tiles().map((t) => t.dataset.state)).toEqual(["failed"]);
    expect(toast).toHaveBeenCalled();
    act(() => tiles()[0].querySelector<HTMLButtonElement>("button")!.click());
    await until(() => expect(uploads).toHaveLength(2));
    await act(async () => uploads[1]("/u/notes.txt"));
    expect(tiles().map((t) => t.dataset.state)).toEqual(["ready"]);
  });

  it("puts the files back over the box when the send does not go", async () => {
    renderOn("a", async () => false);
    await pickNotes();
    await act(async () => uploads[0]("/u/notes.txt"));
    await act(async () => sendButton()!.click());
    expect(tiles().map((t) => t.dataset.state)).toEqual(["ready"]);
  });
});

// The pictures a message holds are the hub's (hello's image_limits, #12973): past them a picture goes as a file to the
// session's computer, with a word on which limit it was, and is turned away where there is no computer.
describe("Composer image limits", () => {
  const MiB = 1024 * 1024;
  const hub = (l: Partial<{ request_bytes: number; per_message: number }>) =>
    setPublishedImageLimits({ request_bytes: 32 * MiB, image_bytes: 3_750_000, max_side: 1600, per_message: 4, ...l });
  const tiles = () => Array.from(host.querySelectorAll<HTMLElement>("[data-testid=composer-files] > span"));
  const renderOn = (draftKey: string) => act(() => root.render(<Composer draftKey={draftKey} machineId="m1" placeholder="" onSend={async () => true} />));
  beforeEach(() => {
    hub({});
    vi.mocked(toast).mockClear();
  });
  afterEach(() => {
    setPublishedImageLimits(undefined);
    renderOn("a");
    tiles().forEach((t) => act(() => t.querySelector<HTMLButtonElement>("button[data-remove]")!.click()));
  });

  it("turns away a picture past the hub's count where there is no computer", async () => {
    render("a");
    for (let i = 0; i < 5; i++) putImage("a");
    await act(async () => {}); // the word comes once the tick's pictures are in
    expect(trayImages()).toBe(4);
    expect(toast).toHaveBeenCalledWith(t("chat-composer.too_many_images", { max: 4 }), "error");
  });

  it("sends a picture past the hub's count to the session's computer as a file", async () => {
    renderOn("a");
    for (let i = 0; i < 6; i++) putImage("a");
    expect(trayImages()).toBe(4);
    await until(() => expect(uploads).toHaveLength(2));
    expect(tiles()).toHaveLength(2);
    expect(toast).toHaveBeenCalledTimes(1); // one word for the two of the same tick
    expect(toast).toHaveBeenCalledWith(t("chat-composer.images_as_files", { max: 4, count: 2 }));
  });

  it("holds 20 pictures with a hub that publishes none", () => {
    setPublishedImageLimits(undefined);
    render("a");
    for (let i = 0; i < 21; i++) putImage("a");
    expect(trayImages()).toBe(20);
  });

  it("sends a picture past the body's room as a file", async () => {
    hub({ request_bytes: MiB + 10 }); // 10 characters of base64 left beside the words
    renderOn("a");
    for (let i = 0; i < 3; i++) putImage("a"); // 4 characters each
    expect(trayImages()).toBe(2);
    await until(() => expect(uploads).toHaveLength(1));
    expect(toast).toHaveBeenCalledWith(t("chat-composer.images_bytes_as_files", { count: 1 }));
  });

  it("sends a picked picture the page cannot read as a file", async () => {
    renderOn("a");
    await pickFile(new File(["heic"], "IMG_0001.HEIC", { type: "image/heic" }));
    await settle(0, null);
    await until(() => expect(uploads).toHaveLength(1));
    expect(trayImages()).toBe(0);
  });

  it("reads only the pictures that fit when several are picked", async () => {
    render("a");
    putImage("a");
    act(() => {
      const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
      const files = Array.from({ length: 5 }, (_, i) => new File(["x"], `p${i}.png`, { type: "image/png" }));
      Object.defineProperty(input, "files", { value: files, configurable: true });
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    // one read at a time: settle each so the next starts
    for (let i = 0; i < 3; i++) await settle(i);
    await act(async () => {});
    expect(reads.length).toBe(3);
    expect(trayImages()).toBe(4);
  });

  it("holds when five pictures come from the chat in the same tick", () => {
    render("a");
    act(() => {
      for (let i = 0; i < 5; i++) sendDraft("a", { type: "image", image: { name: "a.png", mime: "image/png", data: btoa("png") } });
    });
    expect(trayImages()).toBe(4);
    expect(create).toHaveBeenCalledTimes(4);
  });

  it("counts pictures still being read when a second pick comes before they settle", async () => {
    render("a");
    putImage("a");
    putImage("a");
    putImage("a");
    await pickFile(new File(["x"], "p1.png", { type: "image/png" }));
    await pickFile(new File(["x"], "p2.png", { type: "image/png" }));
    await settle(0);
    expect(reads.length).toBe(1); // the second is turned away while the first is read
    expect(trayImages()).toBe(4);
  });
});

// Claude's suggestion as a pill over the box (iOS SuggestionPill): a tap fills it in, the arrow sends it.
describe("Composer suggestion", () => {
  const show = (o: { working?: boolean; needsYou?: boolean; onSend?: (m: Outgoing) => Promise<boolean> } = {}) =>
    act(() => root.render(<Composer draftKey="s" placeholder="" suggestion="跑一下测试" working={o.working} needsYou={o.needsYou} onSend={o.onSend ?? (async () => true)} />));
  const pill = () => host.querySelector('[data-testid="composer-suggestion"]');

  it("fills the box on a tap and goes away", () => {
    show();
    act(() => pill()!.querySelector("button")!.click());
    expect(host.querySelector("textarea")!.value).toBe("跑一下测试");
    expect(pill()).toBeNull();
  });

  it("sends it as it is", async () => {
    const onSend = vi.fn(async () => true);
    show({ onSend });
    await act(async () => pill()!.querySelectorAll("button")[1].click());
    expect(onSend).toHaveBeenCalledWith({ text: "跑一下测试", images: [] });
  });

  it("sends once while the send is on its way, and can come back after the agent has worked", async () => {
    const onSend = vi.fn(() => new Promise<boolean>(() => {}));
    show({ onSend });
    await act(async () => pill()!.querySelectorAll("button")[1].click());
    expect(pill()).toBeNull();
    expect(onSend).toHaveBeenCalledTimes(1);
    show({ onSend, working: true });
    show({ onSend });
    expect(pill()).not.toBeNull();
  });

  it("is spent by any message sent, until the agent has worked", async () => {
    show();
    act(() => pill()!.querySelector("button")!.click());
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    show();
    expect(pill()).toBeNull();
    show({ working: true });
    show();
    expect(pill()).not.toBeNull();
  });

  it("has no one-tap send when it is risky, and still fills in", () => {
    const onSend = vi.fn(async () => true);
    act(() => root.render(<Composer draftKey="s" placeholder="" suggestion="push to main" suggestionRisky onSend={onSend} />));
    expect(pill()!.querySelectorAll("button").length).toBe(1);
    act(() => pill()!.querySelector("button")!.click());
    expect(host.querySelector("textarea")!.value).toBe("push to main");
    expect(onSend).not.toHaveBeenCalled();
  });

  it("is not there while the agent works or something is in the box", () => {
    show({ working: true });
    expect(pill()).toBeNull();
    show({ needsYou: true }); // waiting on a card: the arrow must not send into it
    expect(pill()).toBeNull();
    show();
    putImage("s");
    expect(pill()).toBeNull();
  });
});

describe("Composer words of a send that fails after the box moved on", () => {
  const box = () => host.querySelector("textarea")!;
  const typeIn = (draftKey: string, text: string) =>
    act(() => {
      sendDraft(draftKey, { type: "replace", text });
    });
  const sendSlow = (draftKey: string) => {
    let finish!: (ok: boolean) => void;
    render(draftKey, () => new Promise<boolean>((resolve) => (finish = resolve)));
    return (ok: boolean) => act(async () => finish(ok));
  };

  it("go back to their own conversation's draft, not into the one the box shows now", async () => {
    const finish = sendSlow("a");
    typeIn("a", "hello a");
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    expect(box().value).toBe("");
    render("b", async () => true);
    await finish(false);
    expect(box().value).toBe("");
    render("a");
    expect(box().value).toBe("hello a");
  });

  it("are kept for the conversation when the box went away before the answer", async () => {
    const finish = sendSlow("a");
    typeIn("a", "hello a");
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    act(() => root.unmount());
    root = createRoot(host);
    await finish(false);
    expect(localStorage.getItem("draft:a")).toBe("hello a");
    render("a");
    expect(box().value).toBe("hello a");
  });

  it("go under what the conversation's open box has by then", async () => {
    const finish = sendSlow("a");
    typeIn("a", "hello a");
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    // the conversation opened again in a new box (a chat page mounts one per conversation) while the send was out
    act(() => root.unmount());
    root = createRoot(host);
    render("a");
    typeIn("a", "more");
    await finish(false);
    expect(box().value).toBe("hello a\nmore");
  });

  it("come back into the conversation's new box even while it cannot send", async () => {
    const finish = sendSlow("a");
    typeIn("a", "hello a");
    act(() => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    act(() => root.unmount());
    root = createRoot(host);
    act(() => root.render(<Composer draftKey="a" placeholder="" onSend={async () => true} disabled />));
    await finish(false);
    expect(box().value).toBe("hello a");
  });
});

describe("Composer draft typed just before leaving (#4591)", () => {
  const typeInto = (text: string) =>
    act(() => {
      const el = host.querySelector("textarea")!;
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });

  it("is saved when the box goes away before the debounce", () => {
    render("a");
    typeInto("still typing");
    act(() => root.unmount());
    root = createRoot(host);
    expect(localStorage.getItem("draft:a")).toBe("still typing");
  });

  it("is saved for its own conversation when the box moves to another", () => {
    render("a");
    typeInto("for a");
    render("b");
    expect(localStorage.getItem("draft:a")).toBe("for a");
    expect(localStorage.getItem("draft:b")).toBeNull();
    expect(host.querySelector("textarea")!.value).toBe("");
  });

  it("is saved when the page is hidden", () => {
    render("a");
    typeInto("closing the tab");
    act(() => {
      dispatchEvent(new Event("pagehide"));
    });
    expect(localStorage.getItem("draft:a")).toBe("closing the tab");
  });

  it("is not written back after a send emptied the box", async () => {
    render("a", () => new Promise<boolean>(() => {}));
    typeInto("send me");
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
    act(() => root.unmount());
    root = createRoot(host);
    expect(localStorage.getItem("draft:a")).toBeNull();
  });
});

describe("Composer Edit over unsent words (#2450)", () => {
  const box = () => host.querySelector("textarea")!;
  const typeInto = (text: string) =>
    act(() => {
      const el = box();
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  const edit = (text: string) => act(() => void sendDraft("e", { type: "replace", text }));
  const undoToast = () => vi.mocked(toast).mock.calls.find(([text]) => text === t("chat-composer.replaced"));
  beforeEach(() => {
    vi.mocked(toast).mockClear();
    setQuotes("e", []);
  });
  afterEach(() => vi.restoreAllMocks());

  it("replaces the draft without asking, and Undo brings back the words and the quotes", () => {
    const ask = vi.spyOn(window, "confirm");
    render("e");
    typeInto("half a thought");
    act(() => void addQuote("e", { text: "the quoted reply", from: "Claude" }, false));
    const quotes = getQuotes("e").list;
    edit("the sent message");
    expect(ask).not.toHaveBeenCalled();
    expect(box().value).toBe("the sent message");
    expect(getQuotes("e").list).toEqual([]);
    const [, tone, action] = undoToast()!;
    expect(tone).toBe("ok");
    expect(action?.label).toBe(t("chat-composer.undo"));
    act(() => action!.run());
    expect(box().value).toBe("half a thought");
    expect(getQuotes("e").list).toEqual(quotes);
  });

  it("says nothing when the box was empty or held the same words", () => {
    render("e");
    edit("the sent message");
    expect(box().value).toBe("the sent message");
    edit("the sent message ");
    expect(undoToast()).toBeUndefined();
  });

  it.each([
    ["a quote", { text: "the quoted reply", from: "Claude" }],
    ["a comment card", { text: "fetchAll", comment: { body: "rename fetchAll before merging" } }],
  ])("offers Undo for %s on the bar when the box is empty", (_, quote) => {
    render("e");
    act(() => void addQuote("e", quote, false));
    const quotes = getQuotes("e").list;
    edit("the sent message");
    expect(getQuotes("e").list).toEqual([]);
    act(() => undoToast()![2]!.run());
    expect(getQuotes("e").list).toEqual(quotes);
    expect(box().value).toBe("");
  });
});

// ChatView interrupts a working agent on an Escape that reaches the window: an Escape the box uses for itself
// (closing the / menu, cancelling an IME candidate) must stop there.
describe("Composer Escape the box uses itself", () => {
  const seen = vi.fn();
  const onWindow = (e: KeyboardEvent) => {
    if (e.key === "Escape") seen();
  };
  beforeEach(() => {
    seen.mockClear();
    addEventListener("keydown", onWindow);
  });
  afterEach(() => removeEventListener("keydown", onWindow));

  const box = () => host.querySelector("textarea")!;
  const typeInto = (text: string) =>
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), text);
      box().dispatchEvent(new Event("input", { bubbles: true }));
    });
  const escape = (init: KeyboardEventInit = {}) =>
    act(() => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, ...init }));
    });
  const menuOpen = () => host.querySelector("[data-slash-palette]") !== null;

  it("closes the / menu without reaching the window", async () => {
    render("a");
    typeInto("/");
    await until(() => expect(menuOpen()).toBe(true));
    escape();
    expect(menuOpen()).toBe(false);
    expect(seen).not.toHaveBeenCalled();
  });

  it("closes it with nothing matching too", async () => {
    render("a");
    typeInto("/zzz");
    await until(() => expect(menuOpen()).toBe(true));
    escape();
    expect(menuOpen()).toBe(false);
    expect(seen).not.toHaveBeenCalled();
  });

  it("does not reach the window while an IME composes", () => {
    render("a");
    typeInto("ni");
    escape({ isComposing: true });
    escape({ keyCode: 229 });
    expect(seen).not.toHaveBeenCalled();
  });

  it("still reaches the window from an empty box", () => {
    render("a");
    escape();
    expect(seen).toHaveBeenCalledTimes(1);
  });
});

describe("sending while another send is in flight", () => {
  const sendButton = () => host.querySelector<HTMLButtonElement>('button[data-testid="composer-send"]')!;
  const type = (key: string, text: string) => act(() => void sendDraft(key, { type: "replace", text }));
  const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));

  it("takes the next message and keeps Stop while the first is on its way", async () => {
    const onSend = vi.fn(() => new Promise<boolean>(() => {}));
    const onStop = vi.fn();
    act(() => root.render(<Composer draftKey="busy" placeholder="" working onStop={onStop} onSend={onSend} />));
    type("busy", "one");
    await act(async () => click(sendButton()));
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(host.querySelector(`button[aria-label="${t("chat-composer.interrupt")}"]`)).not.toBeNull();
    type("busy", "two");
    expect(sendButton().disabled).toBe(false);
    await act(async () => click(sendButton()));
    expect(onSend).toHaveBeenCalledTimes(2);
  });

  describe("focus after a send", () => {
    let fine = true;
    beforeEach(() => {
      vi.stubGlobal("matchMedia", (q: string) => ({ get matches() { return q.includes("fine") && fine; }, addEventListener() {}, removeEventListener() {} }));
    });
    afterEach(() => vi.unstubAllGlobals());

    const send = async () => {
      localStorage.setItem("draft:focus", "hello"); // not a draft event: that one focuses the box itself
      act(() => root.render(<Composer draftKey="focus" placeholder="" onSend={async () => true} />));
      await act(async () => click(sendButton()));
    };

    it("asks once while Enter is held down in a blocked box", () => {
      fine = true;
      const onNudge = vi.fn();
      act(() => root.render(<Composer draftKey="held" placeholder="" blocked={{ label: "x", onNudge }} onSend={async () => true} />));
      const press = (repeat: boolean) => act(() => host.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, repeat })));
      press(false);
      press(true);
      press(true);
      expect(onNudge).toHaveBeenCalledTimes(1);
    });

    it("returns to the box with a precise pointer, from the send button", async () => {
      fine = true;
      await send();
      expect(document.activeElement).toBe(host.querySelector("textarea"));
    });

    it("stays away on a touch screen", async () => {
      fine = false;
      await send();
      expect(document.activeElement).not.toBe(host.querySelector("textarea"));
    });

    it("does not take the focus from somewhere else", async () => {
      fine = true;
      const other = document.body.appendChild(document.createElement("button"));
      localStorage.setItem("draft:focus", "hello");
      act(() => root.render(<Composer draftKey="focus" placeholder="" onSend={async () => true} />));
      other.focus();
      await act(async () => click(sendButton()));
      expect(document.activeElement).toBe(other);
      other.remove();
    });
  });
});

// #5032: an Agent's model pill sits at the right of the bottom row, before voice and send, and the card is the container
// whose width it takes its modes from
describe("the trailing slot", () => {
  it("draws what it is given right before voice and send, inside the composer container", () => {
    localStorage.setItem("draft:trailing", "hello"); // a draft: voice is the small icon, send stays
    act(() => root.render(<Composer draftKey="trailing" placeholder="" onSend={async () => true} footer={<span data-footer />} trailing={<span data-pill />} action={{ icon: "voice", name: "Voice", label: "voice", primary: false, onClick: () => {}, attributes: { "data-testid": "composer-voice" } }} />));
    const pill = host.querySelector("[data-pill]")!;
    const row = pill.parentElement!;
    const order = [...row.children].map((c) => (c.matches("[data-pill]") ? "pill" : c.querySelector("[data-footer]") ? "footer" : c.getAttribute("data-testid") ?? c.getAttribute("aria-label") ?? c.tagName));
    const at = order.indexOf("pill");
    expect(order[at - 1]).toBe("footer");
    expect(order.slice(at + 1)).toEqual(["composer-voice", "composer-send"]);
    expect(row.closest("[class*='@container/composer']")).not.toBeNull();
  });
});

// #5325: the "/" palette (cindy's): ranked rows, the keys staying in the box, "No matching commands", and where it is offered
describe("the / palette", () => {
  const box = () => host.querySelector("textarea")!;
  const typeInto = (text: string) =>
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), text);
      box().dispatchEvent(new Event("input", { bubbles: true }));
    });
  const key = (name: string) =>
    act(() => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: name, bubbles: true, cancelable: true }));
    });
  const palette = () => host.querySelector("[data-slash-palette]");
  const rows = () => [...host.querySelectorAll("[role=option]")].map((o) => o.textContent);
  const focused = () => host.querySelector("[role=option][aria-selected=true]")?.textContent;
  beforeEach(() => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("fine"), addEventListener() {}, removeEventListener() {} }));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("lists the commands ranked for what follows the slash, without the hint line", async () => {
    render("p");
    typeInto("/co");
    await until(() => expect(rows()).toEqual(["compact", "context"]));
    typeInto("/");
    expect(rows()).toHaveLength(10); // with /workflows (#5421) and /permissions (#6476)
    expect(palette()!.textContent).not.toContain("Esc");
  });

  it("moves with ↑ ↓, wrapping, and Enter puts the focused command in the box", async () => {
    render("p");
    typeInto("/co");
    await until(() => expect(focused()).toBe("compact"));
    key("ArrowUp");
    expect(focused()).toBe("context");
    key("ArrowDown");
    expect(focused()).toBe("compact");
    key("Enter");
    expect(box().value).toBe("/compact");
    expect(palette()).toBeNull();
  });

  it("Tab picks too, and a text command puts its words in the box", async () => {
    render("p");
    typeInto(t("chat-composer.cmd_continue_word"));
    await until(() => expect(focused()).toBe(t("chat-composer.cmd_continue_word").slice(1)));
    key("Tab");
    expect(box().value).toBe(t("chat-composer.cmd_continue_text"));
  });

  it("says No matching commands, and Enter then sends the words as they are", async () => {
    const onSend = vi.fn(async () => true);
    render("p", onSend);
    typeInto("/review");
    await until(() => expect(palette()!.textContent).toBe(t("chat-slashpalette.no_match")));
    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "/review" }));
  });

  it("closes on a press outside the composer and opens again on typing", async () => {
    render("p");
    typeInto("/");
    await until(() => expect(palette()).not.toBeNull());
    act(() => box().dispatchEvent(new Event("pointerdown", { bubbles: true })));
    expect(palette()).not.toBeNull();
    act(() => document.body.dispatchEvent(new Event("pointerdown", { bubbles: true })));
    expect(palette()).toBeNull();
    typeInto("/c");
    expect(palette()).not.toBeNull();
  });

  // #6476: /permissions opens the conversation's permission menu, picked in the palette or typed and sent; with no
  // picker to open (an agent without modes), the words go as they are
  it("opens the permission menu for /permissions instead of sending it", async () => {
    const onSend = vi.fn(async () => true);
    const opened = vi.fn();
    const off = onPermissionPicker("perm", opened);
    render("perm", onSend);
    typeInto("/perm");
    await until(() => expect(rows()).toEqual(["permissions"]));
    key("Enter");
    expect(opened).toHaveBeenCalledTimes(1);
    expect(box().value).toBe("");
    expect(palette()).toBeNull();
    typeInto("/permissions");
    key("Escape");
    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(opened).toHaveBeenCalledTimes(2);
    expect(onSend).not.toHaveBeenCalled();
    off();
    typeInto("/permissions");
    key("Escape");
    await act(async () => {
      box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "/permissions" }));
  });

  it("is not offered where there are no commands", async () => {
    act(() => root.render(<Composer draftKey="none" placeholder="" onSend={async () => true} commands={[]} />));
    typeInto("/");
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 20));
    });
    expect(palette()).toBeNull();
  });

  it("keeps bare / off the skill chunk and keeps command rows usable while the keyword skill chunk is pending and after it fails", async () => {
    const chunk = { calls: 0, fail: (_error: Error) => {} };
    const held = new Promise<void>((_, reject) => { chunk.fail = reject; });
    void held.catch(() => {}); // every consumer still receives the rejection
    vi.resetModules();
    vi.doMock("./SlashSkillView", async () => {
      chunk.calls++;
      await held;
      return vi.importActual<typeof import("./SlashSkillView")>("./SlashSkillView");
    });
    try {
      const { initI18n } = await import("../../i18n");
      await initI18n("zh-Hans");
      const { Composer: DeferredComposer } = await import("./Composer");
      await act(async () => {
        root.render(<DeferredComposer
          draftKey="assistant-chunk"
          placeholder=""
          mention={{ files: () => [], skills: { manager: "assistant", environment: "test" } }}
          machineId="computer"
          commands={[{ word: "/alpha", desc: "First", text: "/alpha" }, { word: "/beta", desc: "Second", text: "/beta" }]}
          onSend={async () => true}
        />);
      });
      typeInto("/");
      await vi.waitFor(() => expect(rows()).toEqual(["alpha", "beta"]));
      expect(chunk.calls).toBe(0);

      typeInto("/a");
      await vi.waitFor(() => expect(chunk.calls).toBe(1));
      await vi.waitFor(() => expect(rows()).toEqual(["alpha", "beta"]));
      key("ArrowDown");
      expect(focused()).toBe("beta");
      key("Enter");
      await vi.waitFor(() => expect(box().value).toBe("/beta"));
      expect(palette()).toBeNull();

      typeInto("/a");
      await vi.waitFor(() => expect(rows()).toEqual(["alpha", "beta"]));
      await act(async () => {
        chunk.fail(new Error("stale assistant chunk"));
        await Promise.resolve();
      });
      await vi.waitFor(() => expect(rows()).toEqual(["alpha", "beta"]));
      expect(palette()).not.toBeNull();
      key("ArrowDown");
      expect(focused()).toBe("beta");
      key("Enter");
      await vi.waitFor(() => expect(box().value).toBe("/beta"));
      expect(palette()).toBeNull();
    } finally {
      vi.doUnmock("./SlashSkillView");
    }
  }, 20_000);
});

describe("Composer suggestion chunk", () => {
  it("appears and fills the box after its chunk resolves", async () => {
    const chunk = { calls: 0, open: () => {} };
    vi.resetModules();
    vi.doMock("./ComposerOverlays", async () => {
      chunk.calls++;
      await new Promise<void>((resolve) => { chunk.open = resolve; });
      return vi.importActual<typeof import("./ComposerOverlays")>("./ComposerOverlays");
    });
    try {
      const { initI18n } = await import("../../i18n");
      await initI18n("zh-Hans");
      const { Composer: DeferredComposer } = await import("./Composer");
      await act(async () => {
        root.render(<DeferredComposer draftKey="deferred-pill" placeholder="" suggestion="late suggestion" onSend={async () => true} />);
      });
      const pill = () => host.querySelector('[data-testid="composer-suggestion"]');
      expect(pill()).toBeNull();
      await vi.waitFor(() => expect(chunk.calls).toBe(1));

      await act(async () => { chunk.open(); });
      await vi.waitFor(async () => {
        await act(async () => { await Promise.resolve(); });
        expect(pill()).not.toBeNull();
      }, { timeout: 10_000 });
      act(() => pill()!.querySelector("button")!.click());
      expect(host.querySelector("textarea")!.value).toBe("late suggestion");
      expect(pill()).toBeNull();
    } finally {
      vi.doUnmock("./ComposerOverlays");
    }
  }, 20_000);
});
