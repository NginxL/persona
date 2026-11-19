// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

const request = vi.hoisted(() => vi.fn());
vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: request }));

import { setToken } from "./api";
import { initI18n } from "../i18n";
import { translateReply } from "./translate";

let owner = 0;
beforeEach(async () => {
  await initI18n("en");
  setToken(`test-reply-translation-${++owner}`);
  request.mockReset();
  request.mockResolvedValue({ text: "A translated reply" });
});

describe("reply translation cache", () => {
  it("shares both pending and completed requests without another model call", async () => {
    let resolve!: (value: { text: string }) => void;
    request.mockReturnValue(new Promise((done) => { resolve = done; }));
    const first = translateReply("Original **reply**", "zh-Hans", "assistant");
    expect(translateReply("Original **reply**", "zh-Hans", "assistant")).toBe(first);
    expect(request).toHaveBeenCalledExactlyOnceWith("/translate/message", {
      method: "POST", json: { text: "Original **reply**", target: "zh-Hans" },
      headers: { "X-Persona-Manager": "assistant" },
    });
    resolve({ text: "A translated reply" });
    await expect(first).resolves.toBe("A translated reply");
    await translateReply("Original **reply**", "zh-Hans", "assistant");
    expect(request).toHaveBeenCalledTimes(1);
  });

  it("partitions source edits, target languages, managers and signed-in owners", async () => {
    await translateReply("Original", "en", "assistant");
    await translateReply("Edited", "en", "assistant");
    await translateReply("Original", "ja", "assistant");
    await translateReply("Original", "en", "other-assistant");
    setToken("test-another-owner");
    await translateReply("Original", "en", "assistant");
    expect(request).toHaveBeenCalledTimes(5);
  });

  it("does not cache failures or empty translations, so an explicit retry can succeed", async () => {
    request.mockRejectedValueOnce(new Error("Offline")).mockResolvedValueOnce({ text: " " });
    await expect(translateReply("Original", "en", "assistant")).rejects.toThrow("Offline");
    await expect(translateReply("Original", "en", "assistant")).rejects.toThrow("Translation failed");
    await expect(translateReply("Original", "en", "assistant")).resolves.toBe("A translated reply");
    expect(request).toHaveBeenCalledTimes(3);
  });
});
