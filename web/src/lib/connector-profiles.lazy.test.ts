/// <reference types="node" />
// The inbox, a first-screen file, names a connector's reply button ("Reply in Telegram"). It needs only the connector
// table, not the connector pages: importing it from lib/connectors.ts put all of that file and its strings on every
// first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import * as profiles from "./connector-profiles";
import * as connectors from "./connectors";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const importsConnectors = /from\s+["']\.\/connectors["']/;

describe("connector profiles", () => {
  it("keep lib/connectors.ts and its strings off the first screen", () => {
    expect(read("./inbox.ts")).not.toMatch(importsConnectors);
    expect(read("./connector-profiles.ts")).not.toMatch(importsConnectors);
    expect(startup).not.toContain("lib-connectors");
  });

  it("have every string the table reads at load on the first screen", () => {
    const keys = [...read("./connector-profiles.ts").matchAll(/\bt\("(lib-connectors\.[a-z_]+)"/g)].map((m) => m[1]);
    expect(keys.length).toBeGreaterThan(0);
    expect(startup).toEqual(expect.arrayContaining(keys));
  });

  it("are the same functions lib/connectors.ts offers", () => {
    expect(connectors.connectorBrand).toBe(profiles.connectorBrand);
    expect(connectors.connectorOpenTitle).toBe(profiles.connectorOpenTitle);
    expect(profiles.connectorProfile("constructor")).toEqual({});
    expect(profiles.connectorProfile("telegram").logoutEndsRemoteSession).toBe(true);
  });
});
