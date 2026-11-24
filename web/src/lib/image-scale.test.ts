// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_IMAGE_LIMITS } from "./image-limits";
import { fitPicture } from "./image-scale";

// jsdom's Blob cannot be read: the payload is the blob's type and size
vi.mock("./attachments", () => ({ imagePayload: async (b: Blob) => ({ mime: b.type, data: String(b.size) }) }));

// jsdom has no decoder or canvas: the picture's size comes from a stub bitmap, and a stub canvas records what was drawn
// and answers each JPEG with a blob of the size the test gives for that quality.
describe("fitPicture", () => {
  let drawn: { width: number; height: number; quality: number }[];
  let jpegBytes: (quality: number, width: number) => number;
  const bitmap = (width: number, height: number) => vi.fn(async () => ({ width, height, close: vi.fn() }));

  beforeEach(() => {
    drawn = [];
    jpegBytes = () => 300_000;
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => ({ fillRect() {}, drawImage() {} }) as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation(function (this: HTMLCanvasElement, done: BlobCallback, _type?: string, quality?: number) {
      drawn.push({ width: this.width, height: this.height, quality: quality ?? 0 });
      done(new Blob([new Uint8Array(jpegBytes(quality ?? 0, this.width))], { type: "image/jpeg" }));
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it("sends a picture within the limits as it is", async () => {
    globalThis.createImageBitmap = bitmap(1200, 800) as unknown as typeof createImageBitmap;
    const png = new Blob([new Uint8Array(2000)], { type: "image/png" });
    expect(await fitPicture(png, DEFAULT_IMAGE_LIMITS)).toEqual({ mime: "image/png", data: "2000" });
    expect(drawn).toEqual([]);
  });

  it("scales a camera photo to 1600 on its long side as a JPEG at 0.75", async () => {
    globalThis.createImageBitmap = bitmap(4032, 3024) as unknown as typeof createImageBitmap;
    const fit = await fitPicture(new Blob([new Uint8Array(5_000_000)], { type: "image/heic" }), DEFAULT_IMAGE_LIMITS);
    expect(fit?.mime).toBe("image/jpeg");
    expect(drawn).toEqual([{ width: 1600, height: 1200, quality: 0.75 }]);
  });

  it("compresses again, then scales to 1280, before it gives up", async () => {
    globalThis.createImageBitmap = bitmap(1000, 2000) as unknown as typeof createImageBitmap;
    jpegBytes = (quality, width) => (quality > 0.7 || width > 700 ? 4_000_000 : 1_000_000);
    const fit = await fitPicture(new Blob([new Uint8Array(9_000_000)], { type: "image/png" }), DEFAULT_IMAGE_LIMITS);
    expect(fit?.mime).toBe("image/jpeg");
    expect(drawn).toEqual([{ width: 800, height: 1600, quality: 0.75 }, { width: 800, height: 1600, quality: 0.6 }, { width: 640, height: 1280, quality: 0.6 }]);
    jpegBytes = () => 4_000_000;
    expect(await fitPicture(new Blob([new Uint8Array(9_000_000)], { type: "image/png" }), DEFAULT_IMAGE_LIMITS)).toBeNull();
  });

  it("tries the steps it is given, a PNG one without the white (#13797)", async () => {
    globalThis.createImageBitmap = bitmap(6000, 3000) as unknown as typeof createImageBitmap;
    const types: string[] = [];
    const fills = vi.fn();
    vi.mocked(HTMLCanvasElement.prototype.getContext).mockImplementation(() => ({ fillRect: fills, drawImage() {} }) as unknown as CanvasRenderingContext2D);
    vi.mocked(HTMLCanvasElement.prototype.toBlob).mockImplementation(function (this: HTMLCanvasElement, done: BlobCallback, type?: string, quality?: number) {
      types.push(type ?? "");
      drawn.push({ width: this.width, height: this.height, quality: quality ?? 0 });
      done(new Blob([new Uint8Array(type === "image/png" ? 2_000_000 : 900_000)], { type: type ?? "image/png" }));
    });
    const limits = { image_bytes: 1024 * 1024, max_side: 4096 };
    const fit = await fitPicture(new Blob([new Uint8Array(3_000_000)], { type: "image/png" }), limits, [{ side: 4096, png: true }, { side: 2560, quality: 0.8 }]);
    expect(fit).toEqual({ mime: "image/jpeg", data: "900000" });
    expect(types).toEqual(["image/png", "image/jpeg"]);
    expect(drawn).toEqual([{ width: 4096, height: 2048, quality: 0 }, { width: 2560, height: 1280, quality: 0.8 }]);
    expect(fills).toHaveBeenCalledTimes(1);
  });

  it("gives up on a picture the page cannot read", async () => {
    globalThis.createImageBitmap = vi.fn(async () => Promise.reject(new Error("unsupported"))) as unknown as typeof createImageBitmap;
    expect(await fitPicture(new Blob([new Uint8Array(10)], { type: "image/heic" }), DEFAULT_IMAGE_LIMITS)).toBeNull();
  });
});
