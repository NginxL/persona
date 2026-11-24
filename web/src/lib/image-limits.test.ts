import { afterEach, describe, expect, it } from "vitest";
import { DEFAULT_IMAGE_LIMITS, imageLimitsFor, pictureRoom, setPublishedImageLimits } from "./image-limits";

// The pictures a message may carry come from the hub's hello (#12973); a hub without them, or with a value no client can
// use, leaves the defaults, which are the hub's own.
describe("image limits", () => {
  afterEach(() => setPublishedImageLimits(undefined));

  it("are the hub's defaults when the hub publishes none", () => {
    expect(imageLimitsFor()).toEqual({ request_bytes: 33554432, image_bytes: 3750000, max_side: 1600, per_message: 20 });
    expect(imageLimitsFor("codex")).toEqual(DEFAULT_IMAGE_LIMITS);
  });

  it("follow hello, an agent's own set first, an empty agent being Claude Code's", () => {
    setPublishedImageLimits({ request_bytes: 1000, image_bytes: 500, max_side: 800, per_message: 6, agents: { codex: { request_bytes: 2000, image_bytes: 900, max_side: 900, per_message: 9 }, claude: { per_message: 3 } } });
    expect(imageLimitsFor("codex")).toEqual({ request_bytes: 2000, image_bytes: 900, max_side: 900, per_message: 9 });
    expect(imageLimitsFor("pi")).toEqual({ request_bytes: 1000, image_bytes: 500, max_side: 800, per_message: 6 });
    expect(imageLimitsFor().per_message).toBe(3);
  });

  it("keep the default for a value that is missing, not a number or not above zero", () => {
    setPublishedImageLimits({ request_bytes: "lots", per_message: 0, max_side: -1 });
    expect(imageLimitsFor()).toEqual(DEFAULT_IMAGE_LIMITS);
    setPublishedImageLimits("nonsense");
    expect(imageLimitsFor()).toEqual(DEFAULT_IMAGE_LIMITS);
  });

  it("leave a megabyte of the body for the words and count every picture", () => {
    const l = { ...DEFAULT_IMAGE_LIMITS, request_bytes: 1024 * 1024 + 100, per_message: 3 };
    expect(pictureRoom([], 100, l)).toBe("ok");
    expect(pictureRoom([60], 41, l)).toBe("bytes");
    expect(pictureRoom([1, 1, 1], 1, l)).toBe("count");
  });
});
