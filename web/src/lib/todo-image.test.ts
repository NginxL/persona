// @vitest-environment jsdom
// #13797: a new to-do's picture over the manager's 1 MiB is made ready instead of refused, from synthetic PNG and JPEG
// headers. jsdom has no decoder or canvas: a stub bitmap has the header's size, and a stub canvas answers each
// encoding with a blob of the size the test gives.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../i18n";
import { MAX_PICTURE_SOURCE } from "./image-limits";
import { imageHead, prepareTodoImage, TODO_IMAGE_BYTES, TODO_IMAGE_SIDE, TODO_IMAGE_SOURCE_PIXELS } from "./todo-image";

// jsdom's Blob has no arrayBuffer() (the header and imagePayload read with it)
Blob.prototype.arrayBuffer ??= function (this: Blob) {
  return new Promise<ArrayBuffer>((ok, fail) => {
    const r = new FileReader();
    r.onload = () => ok(r.result as ArrayBuffer);
    r.onerror = () => fail(r.error);
    r.readAsArrayBuffer(this);
  });
};

/** A PNG's signature and IHDR for `width` × `height`, padded to `size` bytes. */
function pngBytes(width: number, height: number, size = 64): Uint8Array {
  const b = new Uint8Array(Math.max(size, 33));
  b.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52]);
  new DataView(b.buffer).setUint32(16, width);
  new DataView(b.buffer).setUint32(20, height);
  b.set([8, 6], 24);
  return b;
}

/** A JPEG's SOI, an APP0, an APP1 of `exif` bytes and a DQT before its SOF0 for `width` × `height`, padded to `size`. */
function jpegBytes(width: number, height: number, size = 256, exif = 40): Uint8Array {
  const parts = [[0xff, 0xd8], [0xff, 0xe0, 0, 16, ...Array(14).fill(0)], [0xff, 0xe1, (exif + 2) >> 8, (exif + 2) & 255, ...Array(exif).fill(0)], [0xff, 0xdb, 0, 4, 0, 0], [0xff, 0xc0, 0, 17, 8, height >> 8, height & 255, width >> 8, width & 255]].flat();
  const b = new Uint8Array(Math.max(size, parts.length + 16));
  b.set(parts);
  return b;
}

const file = (bytes: Uint8Array, name: string, type = "image/png") => new File([bytes as BlobPart], name, { type });

let drawn: { type: string; width: number; height: number; quality?: number }[];
let encoded: (type: string, quality: number | undefined, width: number) => number;
let decode: ReturnType<typeof vi.fn>;

beforeAll(async () => {
  await initI18n("en");
});
beforeEach(() => {
  drawn = [];
  encoded = () => 700_000;
  // the stub decoder reads the size from the header, as the page's does from the picture
  decode = vi.fn(async (src: Blob) => {
    const head = await imageHead(src);
    if (!head) throw new Error("undecodable");
    return { width: head.width, height: head.height, close: vi.fn() };
  });
  globalThis.createImageBitmap = decode as unknown as typeof createImageBitmap;
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => ({ fillRect() {}, drawImage() {} }) as unknown as CanvasRenderingContext2D);
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation(function (this: HTMLCanvasElement, done: BlobCallback, type = "image/png", quality?: number) {
    drawn.push({ type, width: this.width, height: this.height, ...(quality === undefined ? {} : { quality }) });
    done(new Blob([new Uint8Array(encoded(type, quality, this.width))], { type }));
  });
});
afterEach(() => vi.restoreAllMocks());

describe("imageHead", () => {
  it("reads a PNG's and a JPEG's size without decoding", async () => {
    expect(await imageHead(file(pngBytes(2880, 1800), "a.png"))).toEqual({ type: "image/png", width: 2880, height: 1800 });
    expect(await imageHead(file(jpegBytes(4032, 3024, 300, 60_000), "b.jpg", "image/jpeg"))).toEqual({ type: "image/jpeg", width: 4032, height: 3024 });
  });

  it("gives null for anything else, a cut-short header among them", async () => {
    expect(await imageHead(file(new TextEncoder().encode("not a picture at all, only words"), "c.png"))).toBeNull();
    expect(await imageHead(file(pngBytes(2, 3).subarray(0, 20), "d.png"))).toBeNull();
    expect(await imageHead(file(jpegBytes(400, 300).subarray(0, 30), "e.jpg", "image/jpeg"))).toBeNull();
    expect(await imageHead(file(pngBytes(0, 3), "f.png"))).toBeNull();
  });
});

describe("prepareTodoImage", () => {
  it("passes a PNG or JPEG within the limits as it is, without decoding it", async () => {
    const png = file(pngBytes(4096, 2000, 900_000), "small.png");
    const jpeg = file(jpegBytes(1200, 800, TODO_IMAGE_BYTES), "photo.jpg", "image/jpeg");
    expect(await prepareTodoImage(png)).toEqual({ file: png });
    expect((await prepareTodoImage(jpeg) as { file: File }).file).toBe(jpeg);
    expect(decode).not.toHaveBeenCalled();
  });

  it("relabels a small picture whose bytes are the other type", async () => {
    const done = await prepareTodoImage(file(jpegBytes(800, 600), "named.png"));
    expect("file" in done && done.file.type).toBe("image/jpeg");
  });

  it("makes a screenshot over 1 MiB a JPEG on white at its own size when that fits", async () => {
    const done = await prepareTodoImage(file(pngBytes(2880, 1800, 3 * 1024 * 1024), "screenshot.png"));
    expect(drawn).toEqual([{ type: "image/jpeg", width: 2880, height: 1800, quality: 0.8 }]);
    expect("file" in done && [done.file.name, done.file.type, done.file.size]).toEqual(["screenshot.jpg", "image/jpeg", 700_000]);
  });

  it("keeps a PNG that only had to be scaled down a PNG, transparency and all", async () => {
    encoded = (type) => (type === "image/png" ? 600_000 : 700_000);
    const done = await prepareTodoImage(file(pngBytes(6000, 3000, 900_000), "diagram.png"));
    expect(drawn).toEqual([{ type: "image/png", width: TODO_IMAGE_SIDE, height: 2048 }]);
    expect("file" in done && [done.file.name, done.file.type]).toEqual(["diagram.png", "image/png"]);
  });

  it("steps down to 1600 px but never below a third of the picture, then fails with its name", async () => {
    encoded = (type, _quality, width) => (type === "image/jpeg" && width <= 1600 ? 500_000 : 2_000_000);
    const done = await prepareTodoImage(file(pngBytes(4800, 2700, 8 * 1024 * 1024), "wide.png"));
    expect(drawn.map((d) => [d.type, d.width, d.quality])).toEqual([["image/png", 4096, undefined], ["image/jpeg", 4096, 0.8], ["image/jpeg", 4096, 0.6], ["image/jpeg", 2560, 0.6], ["image/jpeg", 1600, 0.6]]);
    expect("file" in done && done.file.size).toBe(500_000);

    drawn = [];
    const eightK = await prepareTodoImage(file(pngBytes(7680, 4320, 20 * 1024 * 1024), "8k.png"));
    expect(drawn.map((d) => d.width)).toEqual([4096, 4096, 4096, 2560]);
    expect(eightK).toEqual({ error: t("todos.image_not_prepared", { name: "8k.png" }) });
  });

  it("refuses a picture too long to stay readable at 4096 px before decoding it", async () => {
    const done = await prepareTodoImage(file(pngBytes(1170, 25_000, 900_000), "scroll.png"));
    expect(done).toEqual({ error: t("todos.image_too_long", { name: "scroll.png", max: TODO_IMAGE_SIDE }) });
    expect(decode).not.toHaveBeenCalled();
  });

  it("refuses a source over the cap and a decompression bomb before decoding them", async () => {
    const tooLarge = (name: string) => ({ error: t("todos.image_source_too_big", { name, bytes: MAX_PICTURE_SOURCE / 1024 / 1024, pixels: TODO_IMAGE_SOURCE_PIXELS / 1e6 }) });
    expect(await prepareTodoImage(file(pngBytes(3000, 2000, MAX_PICTURE_SOURCE + 1), "huge.png"))).toEqual(tooLarge("huge.png"));
    expect(await prepareTodoImage(file(pngBytes(100_000, 100_000), "bomb.png"))).toEqual(tooLarge("bomb.png"));
    expect(await prepareTodoImage(file(jpegBytes(10_000, 6_000, 2 * 1024 * 1024), "bomb.jpg", "image/jpeg"))).toEqual(tooLarge("bomb.jpg"));
    expect(decode).not.toHaveBeenCalled();
  });

  it("fails a malformed picture safely", async () => {
    expect(await prepareTodoImage(file(new TextEncoder().encode("plain words"), "words.png"))).toEqual({ error: t("todos.image_unreadable", { name: "words.png" }) });
    decode.mockRejectedValueOnce(new Error("broken body"));
    expect(await prepareTodoImage(file(pngBytes(3000, 2000, 2 * 1024 * 1024), "broken.png"))).toEqual({ error: t("todos.image_not_prepared", { name: "broken.png" }) });
    expect(drawn).toEqual([]);
  });
});
