// @vitest-environment jsdom
// The live screen's video channel interface (#3954): the per-Mac picture, quality and overlay choices kept in this
// browser, and the latency segments the overlay draws. The iOS app's ScreenVideoUI keeps the same choices per Mac.
import { afterEach, describe, expect, it } from "vitest";
import {
  latencySegments,
  pictureChoice,
  qualityChoice,
  setPictureChoice,
  setQualityChoice,
  setStatsShown,
  statsShown,
  type ScreenStats,
} from "./video";

afterEach(() => localStorage.clear());

describe("per-Mac picture choice", () => {
  it("is null until one is picked, then carries, per Mac", () => {
    expect(pictureChoice("mac-a")).toBeNull();
    setPictureChoice("mac-a", "video");
    expect(pictureChoice("mac-a")).toBe("video");
    expect(pictureChoice("mac-b")).toBeNull(); // another Mac is its own
    setPictureChoice("mac-a", "vnc");
    expect(pictureChoice("mac-a")).toBe("vnc");
  });

  it("reads null for anything that is not a known channel", () => {
    localStorage.setItem("persona.screen.picture.mac-a", "holo");
    expect(pictureChoice("mac-a")).toBeNull();
  });
});

describe("per-Mac quality bias", () => {
  it("is auto until another is picked, then carries", () => {
    expect(qualityChoice("mac-a")).toBe("auto");
    setQualityChoice("mac-a", "smoother");
    expect(qualityChoice("mac-a")).toBe("smoother");
    expect(qualityChoice("mac-b")).toBe("auto");
  });

  it("falls back to auto for anything unknown", () => {
    localStorage.setItem("persona.screen.quality.mac-a", "cinematic");
    expect(qualityChoice("mac-a")).toBe("auto");
  });
});

describe("per-Mac overlay choice", () => {
  it("is off until turned on, then carries, and clears off", () => {
    expect(statsShown("mac-a")).toBe(false);
    setStatsShown("mac-a", true);
    expect(statsShown("mac-a")).toBe(true);
    expect(statsShown("mac-b")).toBe(false);
    setStatsShown("mac-a", false);
    expect(statsShown("mac-a")).toBe(false);
    expect(localStorage.getItem("persona.screen.stats.mac-a")).toBeNull(); // off leaves nothing behind
  });
});

describe("the latency segments the overlay draws", () => {
  it("keeps the measured segments in pipeline order and drops the rest", () => {
    // Capture ≈ 0 on the Mac (gate ①): it is left out, so the line starts at encode.
    const video: ScreenStats = { mode: "video", encodeMs: 9, networkMs: 6, decodeMs: 8, totalMs: 23 };
    expect(latencySegments(video)).toEqual([
      { key: "encode", ms: 9 },
      { key: "network", ms: 6 },
      { key: "decode", ms: 8 },
    ]);
  });

  it("is empty for VNC mode, which measures a round-trip, not segments", () => {
    const vnc: ScreenStats = { mode: "vnc", path: "relay", roundTripMs: 48 };
    expect(latencySegments(vnc)).toEqual([]);
  });

  it("keeps a zero segment but drops a missing one", () => {
    const some: ScreenStats = { mode: "video", captureMs: 0, networkMs: 6 };
    expect(latencySegments(some)).toEqual([
      { key: "capture", ms: 0 },
      { key: "network", ms: 6 },
    ]);
  });
});
