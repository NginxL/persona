// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { HubExtension } from "./extensions";
import { openerIcon } from "./opener-icons";

// The installed extensions as the hub's merged list stamps them (docs/protocol/protocol.md "Per-manager routing").
const icon = (id: string, version = "1.0.0") => `/api/v1/extensions/${id}/-/icon?v=${version}`;
const list: (Pick<HubExtension, "id" | "name" | "manager" | "icon_url" | "contributes">)[] = [
  { id: "bridge", name: "Chat Bridge", manager: "assistant", icon_url: icon("bridge") },
  { id: "plain", name: "Plain", manager: "assistant" },
  { id: "public", name: "Public", manager: "assistant", icon_url: "https://images.example/public.png" },
  { id: "bridge", name: "Chat Bridge 2", manager: "m-2", icon_url: icon("bridge", "2.0.0") },
  { id: "only-m2", name: "Only M2", manager: "m-2", icon_url: icon("only-m2") },
  // the extension Lark's mark stands for: it brings the `lark` connector (lib/connectors.ts connectorBrand)
  { id: "lark-ext", name: "Lark", manager: "assistant", icon_url: icon("lark-ext"), contributes: { connectors: ["lark"], ui: ["sidebar"] } },
  { id: "lark-messages", name: "Lark messages", manager: "assistant", icon_url: icon("lark-messages"), contributes: { messaging: ["lark"] } },
  { id: "unnamed", name: " ", manager: "assistant", icon_url: icon("unnamed") },
];
const pick = (id: string, manager?: string, brand?: "lark", shown = "assistant") => openerIcon(list, id, manager, brand, shown, "assistant");

describe("the icon of the extension that opened a conversation (#10358)", () => {
  it("is that extension's own hub icon and name, asked of the manager whose extension it is", () => {
    expect(pick("bridge")).toEqual({ url: icon("bridge"), manager: "assistant", name: "Chat Bridge" });
    // a lark_agent conversation another extension opened: its own icon, not Lark's
    expect(pick("bridge", undefined, "lark")).toEqual({ url: icon("bridge"), manager: "assistant", name: "Chat Bridge" });
    expect(pick("bridge", "assistant", undefined, "m-2")).toEqual({ url: icon("bridge"), manager: "assistant", name: "Chat Bridge" });
    // another manager's conversation: that manager's copy, never the default's
    expect(pick("bridge", "m-2")).toEqual({ url: icon("bridge", "2.0.0"), manager: "m-2", name: "Chat Bridge 2" });
    // a conversation without a manager is the shown assistant's
    expect(pick("only-m2", undefined, undefined, "m-2")).toEqual({ url: icon("only-m2"), manager: "m-2", name: "Only M2" });
    // an item without a manager is the default's (an older hub's list); the request then names none
    expect(openerIcon([{ id: "bridge", name: "B", icon_url: icon("bridge") }], "bridge", undefined, undefined, "assistant", "assistant")).toEqual({ url: icon("bridge"), manager: "", name: "B" });
    // no name to say: the icon still, the words stay the origin's
    expect(pick("unnamed")).toEqual({ url: icon("unnamed"), manager: "assistant", name: undefined });
  });

  it("is none for the extension the origin's own mark stands for: Lark /agent of the Lark extension stays Lark's", () => {
    expect(pick("lark-ext", undefined, "lark")).toBeUndefined();
    expect(pick("lark-messages", undefined, "lark")).toBeUndefined();
    // the same extension opening an `extension` conversation (no brand there) is drawn with its icon
    expect(pick("lark-ext")).toEqual({ url: icon("lark-ext"), manager: "assistant", name: "Lark" });
    // a contribution that is not a list of ids names no brand
    expect(openerIcon([{ id: "odd", name: "Odd", icon_url: icon("odd"), contributes: { connectors: "lark" as unknown as string[] } }], "odd", undefined, "lark", "assistant", "assistant")).toEqual({ url: icon("odd"), manager: "", name: "Odd" });
  });

  it("is none while the list is unknown, and for an extension without a hub icon or not installed for that manager", () => {
    expect(openerIcon(undefined, "bridge", undefined, undefined, "assistant", "assistant")).toBeUndefined();
    expect(pick("plain")).toBeUndefined();
    // public artwork stays off the rows: only the hub's own icon routes (lib/extension-logo.ts)
    expect(pick("public")).toBeUndefined();
    expect(pick("removed")).toBeUndefined();
    expect(pick("only-m2", "assistant")).toBeUndefined();
    expect(openerIcon([{ id: "bad", icon_url: "/api/v1/extensions/bad/-/icon?v=1&token=x" }], "bad", undefined, undefined, "assistant", "assistant")).toBeUndefined();
  });
});
