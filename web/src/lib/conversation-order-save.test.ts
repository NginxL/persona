import { beforeEach, describe, expect, it, vi } from "vitest";

const { state, request, reload } = vi.hoisted(() => ({
  state: { assistant: { conversation_order: ["a", "b"] } as { conversation_order?: string[] } | null },
  request: vi.fn(),
  reload: vi.fn(),
}));
vi.mock("./api", async (orig) => ({ ...(await orig<typeof import("./api")>()), api: (...args: unknown[]) => request(...args) }));
vi.mock("./store", () => ({
  getState: () => state,
  patchAssistant: (patch: { conversation_order?: string[] }) => {
    if (state.assistant) state.assistant = { ...state.assistant, ...patch };
  },
  refreshAssistant: () => reload(),
}));

import { saveConversationOrder } from "./conversation-order-save";

describe("saving the shared conversation order (#12017)", () => {
  beforeEach(() => {
    state.assistant = { conversation_order: ["a", "b"] };
    request.mockReset();
    reload.mockReset().mockResolvedValue(undefined);
  });

  it("updates immediately, sends only the order, and accepts the hub's normalization", async () => {
    request.mockResolvedValue({ conversation_order: ["b", "a"] });
    const saving = saveConversationOrder([" b ", "a", "b"]);
    expect(state.assistant?.conversation_order).toEqual([" b ", "a", "b"]);
    await saving;
    expect(request).toHaveBeenCalledWith("/assistant", { method: "PUT", json: { conversation_order: [" b ", "a", "b"] } });
    expect(state.assistant?.conversation_order).toEqual(["b", "a"]);
  });

  it("rolls back and reloads when the hub refuses", async () => {
    request.mockRejectedValue(new Error("offline"));
    await expect(saveConversationOrder(["b", "a"])).rejects.toThrow("offline");
    expect(state.assistant?.conversation_order).toEqual(["a", "b"]);
    expect(reload).toHaveBeenCalledOnce();
  });

  it("rejects an unacknowledged move but accepts an empty reset from the omitempty hub", async () => {
    request.mockResolvedValue({});
    await expect(saveConversationOrder(["b", "a"])).rejects.toThrow("does not support");
    expect(state.assistant?.conversation_order).toEqual(["a", "b"]);
    expect(reload).toHaveBeenCalledOnce();

    await saveConversationOrder([]);
    expect(state.assistant?.conversation_order).toEqual([]);
  });

  it("serializes rapid moves and never lets an older response replace a newer one", async () => {
    const answers: ((value: unknown) => void)[] = [];
    request.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));
    const first = saveConversationOrder(["b", "a"]);
    const second = saveConversationOrder(["c", "b", "a"]);
    await vi.waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    answers[0]({ conversation_order: ["b", "a"] });
    await first;
    expect(state.assistant?.conversation_order).toEqual(["c", "b", "a"]);
    await vi.waitFor(() => expect(request).toHaveBeenCalledTimes(2));
    answers[1]({ conversation_order: ["c", "b", "a"] });
    await second;
    expect(state.assistant?.conversation_order).toEqual(["c", "b", "a"]);
  });

  it("keeps a newer optimistic move when an older queued write fails", async () => {
    let refuse: (error: Error) => void = () => {};
    request.mockImplementationOnce(() => new Promise((_, reject) => { refuse = reject; })).mockResolvedValueOnce({ conversation_order: ["c", "b", "a"] });
    const first = saveConversationOrder(["b", "a"]);
    const second = saveConversationOrder(["c", "b", "a"]);
    await vi.waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    refuse(new Error("offline"));
    await expect(first).rejects.toThrow("offline");
    await second;
    expect(state.assistant?.conversation_order).toEqual(["c", "b", "a"]);
    expect(reload).not.toHaveBeenCalled();
  });
});
