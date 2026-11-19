// @vitest-environment jsdom
// Settings › Labs' terminal renderer module as a configuration source (labs-terminal.ts): its Document, a revision that
// follows what is stored (another tab too), updates checked against id and revision, resets, invalid saved values
// named and kept, and a browser that doesn't keep the write reported with nothing changed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { ConfigurationError, parseDocument, type ConfigurationDocument } from "./configuration/contract";
import { terminalLabsSource } from "./labs-terminal";

const KEY = "persona.terminal.webgl";
const source = terminalLabsSource();
const read = async () => (await source.read()) as ConfigurationDocument;
const upd = (doc: ConfigurationDocument, more: Record<string, unknown>) => source.update({ schema_version: 1, id: doc.id, revision: doc.revision, ...more });

beforeEach(() => localStorage.clear());
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("the terminal renderer's Labs document", () => {
  it("is a valid document: one segmented choice, Standard by default, with its module's details", async () => {
    const doc = await read();
    expect(parseDocument(doc).writable).toBe(true);
    expect(source.key).toBe("labs:terminal");
    const field = doc.groups[0].fields[0];
    expect(field).toMatchObject({ id: "renderer", kind: "select", default: "standard", select: { presentation: "segmented" } });
    expect(field.kind === "select" && field.select.options.map((o) => o.value)).toEqual(["standard", "webgl"]);
    expect(field.details?.map((d) => d.title)).toEqual([t("settings-labs.scope"), t("settings-labs.activation"), t("settings-labs.rollback")]);
    expect(doc.values).toEqual({ renderer: "standard" });
    expect(doc.notices).toBeUndefined();
  });

  it("reads the WebGL switch saved before Labs", async () => {
    localStorage.setItem(KEY, "1");
    expect((await read()).values).toEqual({ renderer: "webgl" });
  });

  it("names a saved value it doesn't recognize, shows the default and leaves the value", async () => {
    localStorage.setItem(KEY, "webgl2");
    const doc = await read();
    expect(doc.values).toEqual({ renderer: "standard" });
    expect(doc.notices).toEqual({ renderer: { kind: "warning", message: t("settings-labs.invalid") } });
    expect(localStorage.getItem(KEY)).toBe("webgl2");
  });

  it("says so when the browser won't let it read", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    expect((await read()).notices).toEqual({ renderer: { kind: "warning", message: t("settings-labs.unreadable") } });
  });
});

describe("updates", () => {
  it("commits a choice and answers with the new document", async () => {
    const doc = (await upd(await read(), { changes: { renderer: "webgl" } })) as ConfigurationDocument;
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(doc.values).toEqual({ renderer: "webgl" });
    expect(doc.revision).not.toBe("default"); // the revision follows what is stored
  });

  it("reset removes the override", async () => {
    localStorage.setItem(KEY, "1");
    const doc = (await upd(await read(), { reset: ["renderer"] })) as ConfigurationDocument;
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(doc.values).toEqual({ renderer: "standard" });
  });

  it("replaces an unrecognized value once a choice is made", async () => {
    localStorage.setItem(KEY, "webgl2");
    await upd(await read(), { changes: { renderer: "standard" } });
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it("refuses a stale revision (another tab changed it) and writes nothing", async () => {
    const seen = await read();
    localStorage.setItem(KEY, "1"); // another tab
    await expect(upd(seen, { changes: { renderer: "standard" } })).rejects.toMatchObject({ code: "configuration_conflict" });
    expect(localStorage.getItem(KEY)).toBe("1");
  });

  it.each([
    ["another document", { id: "other" }, "configuration_conflict", []],
    ["another version", { schema_version: 2 }, "configuration_version", []],
    ["an empty update", { changes: {} }, "configuration_invalid", []],
    ["an unknown field", { changes: { colour: "red" } }, "configuration_invalid", [{ id: "colour", code: "unknown" }]],
    ["a value it doesn't offer", { changes: { renderer: "canvas" } }, "configuration_invalid", [{ id: "renderer", code: "option" }]],
    ["a change and a reset of the same field", { changes: { renderer: "webgl" }, reset: ["renderer"] }, "configuration_invalid", [{ id: "renderer", code: "duplicate" }]],
    ["the same reset twice", { reset: ["renderer", "renderer"] }, "configuration_invalid", [{ id: "renderer", code: "duplicate" }]],
  ])("refuses %s", async (_, more, code, fieldErrors) => {
    const doc = await read();
    const err = (await source.update({ schema_version: 1, id: doc.id, revision: doc.revision, ...more } as never).catch((e) => e)) as ConfigurationError;
    expect(err).toBeInstanceOf(ConfigurationError);
    expect(err).toMatchObject({ code });
    expect(err.fieldErrors.map(({ id, code: c }: { id: string; code: string }) => ({ id, code: c }))).toEqual(fieldErrors);
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it("reports a browser that doesn't keep it, in its own words", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    const err = await upd(await read(), { changes: { renderer: "webgl" } }).catch((e) => e);
    expect(err).toBeInstanceOf(ConfigurationError);
    expect(err).toMatchObject({ code: "storage_failed", message: t("settings-labs.save_failed") });
  });
});

describe("changes elsewhere", () => {
  it("calls back for a write here and another tab's change", () => {
    const heard = vi.fn();
    const off = source.subscribe!(heard);
    window.dispatchEvent(new StorageEvent("storage", { key: KEY }));
    expect(heard).toHaveBeenCalledTimes(1);
    off();
  });
});
