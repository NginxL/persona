/// <reference types="node" />
// The Lark chat list (lark-chats.ts) is on the first screen and names an account in its requests. It needs only
// larkPath: importing it from lark-accounts.ts put the accounts store and its words on every first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import * as accounts from "./lark-accounts";
import { larkPath } from "./lark-path";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");

describe("larkPath", () => {
  it("keeps lark-accounts.ts and its words off the first screen", () => {
    expect(read("./lark-chats.ts")).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/lark-accounts["']/m);
    expect(read("./lark-path.ts")).not.toMatch(/^import\s+(?!type\b)[^;]*from\s+["']\.\/lark-accounts["']/m);
    expect(startup.filter((key) => key === "lib-lark-accounts" || key.startsWith("lib-lark-accounts."))).toEqual([]);
  });

  it("is the function lark-accounts.ts offers the inbox", () => {
    expect(accounts.larkPath).toBe(larkPath);
    expect(larkPath("/lark/chats", 2)).toBe("/lark/chats?ol_account=2");
    expect(larkPath("/lark/chats?x=1", 2)).toBe("/lark/chats?x=1&ol_account=2");
    expect(larkPath("/lark/chats?ol_account=3", 2)).toBe("/lark/chats?ol_account=3");
    expect(larkPath("/lark/chats")).toBe("/lark/chats");
  });
});
