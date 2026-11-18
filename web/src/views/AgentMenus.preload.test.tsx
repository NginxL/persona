// @vitest-environment jsdom
// An Agent row's menu and a space's menu offer Rename… and Delete…, whose dialogs are a chunk of their own (#10382):
// opening either menu starts that chunk, so the click that picks one draws its dialog at once.
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import type { Session } from "../lib/types";

const preload = vi.hoisted(() => vi.fn());
vi.mock("./chat/SessionDialogs", async (original) => ({ ...await original<typeof import("./chat/SessionDialogs")>(), preloadSessionDialogs: preload }));
vi.mock("./NodeMenu", () => ({ NodeMenu: () => null }));
vi.mock("./SpaceMenu", () => ({ SpaceMenu: () => null }));
const { AgentRowMenu, SpaceMenu } = await import("./AgentMenus");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

it("an Agent row's menu and a space's menu start the Rename and Delete dialogs' chunk as they open", async () => {
  const root = createRoot(document.createElement("div"));
  const anchor = document.createElement("button");
  const s = { machine_id: "m", session_id: "s", title: "Synthetic agent", status: "idle", space_id: "sp", space_name: "Synthetic space" } as Session;
  await act(async () => root.render(<AgentRowMenu s={s} anchor={anchor} onClose={() => {}} onRename={() => {}} onDelete={() => {}} />));
  expect(preload).toHaveBeenCalledTimes(1);
  await act(async () => root.render(<SpaceMenu sp={{ key: "m/sp", machineId: "m", name: "Synthetic space", sessions: [s] }} anchor={anchor} onClose={() => {}} onRename={() => {}} onMarkSeen={() => {}} />));
  expect(preload).toHaveBeenCalledTimes(2);
  await act(async () => root.unmount());
});
