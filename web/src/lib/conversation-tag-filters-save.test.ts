import { beforeEach, describe, expect, it, vi } from "vitest";

const { state, request, reload } = vi.hoisted(() => ({
  state: { assistant: { conversation_tag_filters: null as string[] | null | undefined } as { conversation_tag_filters?: string[] | null } | null },
  request: vi.fn(), reload: vi.fn(),
}));
vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), api: (...args: unknown[]) => request(...args) }));
vi.mock("./store", () => ({
  getState: () => state,
  patchAssistant: (patch: { conversation_tag_filters?: string[] | null }) => {
    if (state.assistant) state.assistant = { ...state.assistant, ...patch };
  },
  refreshAssistant: () => reload(),
}));
import { saveConversationTagFilters } from "./conversation-tag-filters-save";

describe("saving conversation tag filters (#12018)", () => {
  beforeEach(() => {
    state.assistant = { conversation_tag_filters: ["old"] };
    request.mockReset();
    reload.mockReset().mockResolvedValue(undefined);
  });

  it("writes an exact custom empty array rather than converting it to automatic", async () => {
    request.mockResolvedValue({ conversation_tag_filters: [] });
    await saveConversationTagFilters([]);
    expect(request).toHaveBeenCalledWith("/assistant", { method: "PUT", json: { conversation_tag_filters: [] } });
    expect(state.assistant?.conversation_tag_filters).toEqual([]);
  });

  it("writes null to restore automatic mode", async () => {
    request.mockResolvedValue({ conversation_tag_filters: null });
    await saveConversationTagFilters(null);
    expect(state.assistant?.conversation_tag_filters).toBeNull();
  });

  it("rolls back a failed write, including an older hub silently ignoring the field", async () => {
    request.mockRejectedValueOnce(new Error("offline"));
    await expect(saveConversationTagFilters(["new"])).rejects.toThrow("offline");
    expect(state.assistant?.conversation_tag_filters).toEqual(["old"]);
    request.mockResolvedValueOnce({});
    await expect(saveConversationTagFilters(["new"])).rejects.toThrow("does not support");
    expect(state.assistant?.conversation_tag_filters).toEqual(["old"]);
    expect(reload).toHaveBeenCalledTimes(2);
  });
});
