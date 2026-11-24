/// <reference types="node" />
// Home, a first-screen file, says why an offline computer is offline. It needs only classifyConnection: importing it
// from node-health.ts put that module and the words of its check and feature labels (built with t() at load) on every
// first screen.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import { classifyConnection } from "./connection-failure";
import * as health from "./node-health";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const runtimeImport = (from: string) => new RegExp(`^import\\s+(?!type\\b)[^;]*from\\s+["']${from}["']`, "m");

describe("classifyConnection", () => {
  it("keeps node-health.ts and its check and feature words off the first screen", () => {
    expect(read("../views/Home.tsx")).not.toMatch(runtimeImport("\\.\\./lib/node-health"));
    expect(read("./connection-failure.ts")).not.toMatch(runtimeImport("\\./node-health"));
    const healthKeys = startup.filter((key) => key.startsWith("node-health."));
    expect(healthKeys.sort()).toEqual(["node-health.auth_", "node-health.timeout_", "node-health.unreachable_", "node-health.version_"]);
  });

  it("is the function node-health.ts offers the machine pages", () => {
    expect(health.classifyConnection).toBe(classifyConnection);
  });
});
