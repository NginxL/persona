import { afterEach, expect, it, vi } from "vitest";

vi.mock("./store", () => {
  const state = { launches: {} as Record<string, import("./new-agent").AgentLaunch> };
  return { getState: () => state, putLaunch: (launch: import("./new-agent").AgentLaunch) => { state.launches[launch.id] = launch; } };
});
vi.mock("./router", () => ({ navigate: vi.fn() }));
vi.mock("./new-agent-delivery", () => { throw new Error("launch module unavailable"); });

import { deliverLaunch, launchAgent } from "./new-agent";
import { getState } from "./store";

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

it("keeps the draft and operation IDs retryable when the delivery chunk cannot load", async () => {
  vi.useFakeTimers();
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  const id = launchAgent({ machineId: "desktop", cwd: "C:\\Projects\\Sample", launcher: "codex-app", text: "Continue this project", attachments: [] });
  const before = getState().launches[id];
  await deliverLaunch(id);
  expect(getState().launches[id]).toMatchObject({ ...before, phase: "failed" });
  expect(getState().launches[id].error).toBeTruthy();
  await deliverLaunch(id);
  expect(getState().launches[id]).toMatchObject({ ...before, phase: "failed" });
  expect(fetcher).not.toHaveBeenCalled();
});
