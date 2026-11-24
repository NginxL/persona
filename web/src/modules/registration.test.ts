// A module the host refuses (#13434, docs/web/web.md "Modules"): the list registers at App.tsx's load, so in a
// production build one bad descriptor is logged and left out while the others register; in development and tests the
// refusal throws, so a bad module cannot land.
import { afterEach, describe, expect, it, vi } from "vitest";
import { registeredModules, registerModule, resetModulesForTest, type ModuleDescriptor } from "../core/module-host";
import { MODULES, registerModules } from ".";

const module = (id: string, over: Partial<ModuleDescriptor> = {}): ModuleDescriptor => ({ id, title: () => id, description: () => id, load: async () => ({}), ...over });
// refused: its page would take a core address
const bad = module("garden", { routes: [{ base: "inbox", title: () => "x" }] });
const good = module("yard", { routes: [{ base: "yard", title: () => "Yard" }] });

afterEach(() => {
  resetModulesForTest();
  registerModules(); // back to the list as the app has it
});

describe("registering the list", () => {
  it("throws on a refused module in development and tests", () => {
    resetModulesForTest();
    expect(import.meta.env.PROD).toBe(false);
    expect(() => registerModules([bad, good])).toThrow(/core address/);
  });

  it("in production, logs a refused module and registers the rest", () => {
    resetModulesForTest();
    const report = vi.fn();
    registerModules([bad, good], false, report);
    expect(registeredModules().map((d) => d.id)).toEqual(["yard"]);
    expect(report).toHaveBeenCalledTimes(1);
    expect(report.mock.calls[0]![0]).toContain('module "garden"');
    expect(String(report.mock.calls[0]![1])).toMatch(/core address/);
  });

  it("registers each module once, however often it runs", () => {
    resetModulesForTest();
    registerModules();
    registerModules();
    expect(registeredModules()).toEqual(MODULES);
    expect(() => registerModule(MODULES[0]!)).toThrow(/twice/);
  });
});
