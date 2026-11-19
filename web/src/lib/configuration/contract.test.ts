// The configuration contract as this client reads and checks it (contract.ts): every kind, mirrored validation (Unicode
// scalar lengths, safe-range numbers with decimal bounds, duplicate choices), unknown kinds and versions shown but never
// written, the API's error body, and the shared write-validation cases from schema/configuration (the same file the Go
// checks run; this test adds no copy of it).
import { describe, expect, it } from "vitest";
import example from "../../../../schema/configuration/all-types.json";
import cases from "../../../../schema/configuration/validation-cases.json";
import { checkUpdate, checkValue, ConfigurationError, fieldValue, parseDocument, parseField, sameValue, scalarLength, type ConfigurationUpdate, type KnownField } from "./contract";

const field = (raw: Record<string, unknown>) => {
  const f = parseField(raw);
  if (!f || f.kind === "unsupported") throw new Error(`not a known field: ${JSON.stringify(f)}`);
  return f as KnownField;
};

describe("a document", () => {
  it("reads every kind of the shared example, its groups, notices and values", () => {
    const { doc, writable } = parseDocument(example);
    expect(writable).toBe(true);
    expect(doc.id).toBe("example-preferences");
    expect(doc.revision).toBe("revision-1");
    expect(doc.title).toBe("Preview preferences");
    const kinds = doc.groups.flatMap((g) => g.fields.map((f) => `${f.id}:${f.kind}`));
    expect(kinds).toEqual(["compact:toggle", "label:text", "notes:text", "layout:select", "accent:select", "motion:select", "sections:multiselect", "interval:number", "managed:toggle"]);
    expect(doc.notices).toEqual({ notes: { kind: "info", message: "Notes are optional." } });
    expect(fieldValue(doc, doc.groups[0].fields[1] as KnownField)).toBe("Preview");
  });

  it("keeps a kind it doesn't know, and its value, as unsupported", () => {
    const raw = {
      ...example,
      groups: [{ ...example.groups[0], fields: [...example.groups[0].fields, { id: "future", kind: "slider", title: "From a newer version", default: 3, slider: { max: 5 } }] }],
      values: { ...example.values, future: 3 },
    };
    const { doc } = parseDocument(raw);
    const future = doc.groups[0].fields.at(-1)!;
    expect(future).toMatchObject({ id: "future", kind: "unsupported", reason: "kind", title: "From a newer version" });
    expect(doc.values.future).toBe(3);
  });

  it("is shown but not writable in a schema version it doesn't know", () => {
    const { doc, writable } = parseDocument({ ...example, schema_version: 2 });
    expect(writable).toBe(false);
    expect(doc.title).toBe("Preview preferences");
  });

  it("still reads a newer version whose groups it can't use", () => {
    const { doc, writable } = parseDocument({ schema_version: 2, id: "x", revision: "1", title: "Later", sections: [] });
    expect(writable).toBe(false);
    expect(doc.groups).toEqual([]);
  });

  it.each([
    ["not an object", null],
    ["no id", { ...example, id: 1 }],
    ["a whitespace id", { ...example, id: "  " }],
    ["no revision", { ...example, revision: undefined }],
    ["no values", { ...example, values: undefined }],
    ["a field id twice", { ...example, groups: [example.groups[0], { ...example.groups[0], id: "other", fields: [example.groups[0].fields[0]] }] }],
    ["a group id twice", { ...example, groups: [example.groups[0], example.groups[0]] }],
  ])("is refused when it is %s", (_, raw) => {
    expect(() => parseDocument(raw)).toThrow(ConfigurationError);
    try {
      parseDocument(raw);
    } catch (e) {
      expect((e as ConfigurationError).code).toBe("invalid_document");
    }
  });

  it("gives a field's value only when it is a valid one, leaving the rest alone", () => {
    const raw = { ...example, values: { ...example.values, layout: "future", interval: 1.5, sections: [] } };
    const { doc } = parseDocument(raw);
    const byId = (id: string) => doc.groups.flatMap((g) => g.fields).find((f) => f.id === id) as KnownField;
    expect(fieldValue(doc, byId("layout"))).toBeUndefined();
    expect(fieldValue(doc, byId("interval"))).toBeUndefined();
    expect(fieldValue(doc, byId("sections"))).toBeUndefined();
    expect(fieldValue(doc, byId("motion"))).toBe("standard");
    expect(doc.values.layout).toBe("future"); // kept as it came: nothing deletes it
  });
});

describe("field definitions", () => {
  it.each([
    ["a select without options", { id: "a", kind: "select", title: "A", default: "x", select: { options: [] } }],
    ["a select whose default isn't an option", { id: "a", kind: "select", title: "A", default: "z", select: { options: [{ value: "x", title: "X" }] } }],
    ["options with the same value", { id: "a", kind: "select", title: "A", default: "x", select: { options: [{ value: "x", title: "X" }, { value: "x", title: "Y" }] } }],
    ["a select presentation this client doesn't draw", { id: "a", kind: "select", title: "A", default: "x", select: { presentation: "carousel", options: [{ value: "x", title: "X" }] } }],
    ["a toggle whose default is a string", { id: "a", kind: "toggle", title: "A", default: "true" }],
    ["a toggle with a text member", { id: "a", kind: "toggle", title: "A", default: true, text: {} }],
    ["a text field without its text member", { id: "a", kind: "text", title: "A", default: "" }],
    ["a number field without its number member", { id: "a", kind: "number", title: "A", default: 1 }],
    ["a number with a step of 0", { id: "a", kind: "number", title: "A", default: 1, number: { step: 0 } }],
    ["a number bound past the safe range", { id: "a", kind: "number", title: "A", default: 1, number: { maximum: 2 ** 60 } }],
    ["a minimum above the maximum", { id: "a", kind: "number", title: "A", default: 1, number: { minimum: 5, maximum: 2 } }],
    ["a text minimum above its maximum", { id: "a", kind: "text", title: "A", default: "", text: { min_length: 3, max_length: 2 } }],
    ["a fractional length", { id: "a", kind: "text", title: "A", default: "", text: { max_length: 2.5 } }],
    ["a multiselect whose minimum exceeds its options", { id: "a", kind: "multiselect", title: "A", default: ["x"], multiselect: { min_selected: 2, options: [{ value: "x", title: "X" }] } }],
    ["a disabled reason without read_only", { id: "a", kind: "toggle", title: "A", default: true, disabled_reason: "Managed" }],
    ["details that aren't title and description", { id: "a", kind: "toggle", title: "A", default: true, details: [{ title: "x" }] }],
  ])("marks %s as unsupported", (_, raw) => {
    expect(parseField(raw)).toMatchObject({ kind: "unsupported", reason: "definition" });
  });

  it("drops a field with no id or title (nothing to show it by)", () => {
    expect(parseField({ kind: "toggle", title: "A", default: true })).toBeNull();
    expect(parseField({ id: "a", kind: "toggle", default: true })).toBeNull();
  });
});

describe("values", () => {
  it("toggle: a boolean", () => {
    const f = field({ id: "t", kind: "toggle", title: "T", default: false });
    expect(checkValue(f, true)).toBeNull();
    expect(checkValue(f, "true")).toBe("type");
    expect(checkValue(f, null)).toBe("type");
  });

  it("text: counts Unicode scalars, not UTF-16 units", () => {
    const f = field({ id: "x", kind: "text", title: "X", default: "ab", text: { min_length: 2, max_length: 3 } });
    expect(scalarLength("👋👋👋")).toBe(3);
    expect("👋👋👋".length).toBe(6);
    expect(checkValue(f, "👋👋👋")).toBeNull();
    expect(checkValue(f, "世界")).toBeNull();
    expect(checkValue(f, "👋")).toBe("min_length");
    expect(checkValue(f, "abcd")).toBe("max_length");
    expect(checkValue(f, 12)).toBe("type");
  });

  it("select: one of its options", () => {
    const f = field({ id: "s", kind: "select", title: "S", default: "a", select: { options: [{ value: "a", title: "A" }, { value: "b", title: "B" }] } });
    expect(checkValue(f, "b")).toBeNull();
    expect(checkValue(f, "c")).toBe("option");
    expect(checkValue(f, ["a"])).toBe("type");
  });

  it("multiselect: known, distinct choices within its counts", () => {
    const f = field({ id: "m", kind: "multiselect", title: "M", default: ["a"], multiselect: { min_selected: 1, max_selected: 2, options: [{ value: "a", title: "A" }, { value: "b", title: "B" }, { value: "c", title: "C" }] } });
    expect(checkValue(f, ["a", "b"])).toBeNull();
    expect(checkValue(f, ["a", "a"])).toBe("duplicate");
    expect(checkValue(f, ["z"])).toBe("option");
    expect(checkValue(f, [])).toBe("min_selected");
    expect(checkValue(f, ["a", "b", "c"])).toBe("max_selected");
    expect(checkValue(f, ["a", 1])).toBe("type");
    expect(checkValue(f, "a")).toBe("type");
  });

  it("number: finite, in the portable range, an integer when asked, inside decimal bounds", () => {
    const n = field({ id: "n", kind: "number", title: "N", default: 0.5, number: { minimum: 0.25, maximum: 1.75 } });
    expect(checkValue(n, 0.25)).toBeNull();
    expect(checkValue(n, 1.75)).toBeNull();
    expect(checkValue(n, 0.2)).toBe("minimum");
    expect(checkValue(n, 2)).toBe("maximum");
    expect(checkValue(n, Number.NaN)).toBe("range");
    expect(checkValue(n, Number.POSITIVE_INFINITY)).toBe("range");
    expect(checkValue(n, "1")).toBe("type");
    const i = field({ id: "i", kind: "number", title: "I", default: 1, number: { integer: true } });
    expect(checkValue(i, 1.5)).toBe("integer");
    expect(checkValue(i, Number.MAX_SAFE_INTEGER)).toBeNull();
    expect(checkValue(i, Number.MAX_SAFE_INTEGER + 2)).toBe("range");
  });

  it("compares a multiselect's choices in any order", () => {
    expect(sameValue(["a", "b"], ["b", "a"])).toBe(true);
    expect(sameValue(["a"], ["a", "b"])).toBe(false);
    expect(sameValue(1, 1)).toBe(true);
  });
});

describe("the shared write-validation cases (schema/configuration/validation-cases.json)", () => {
  const { doc } = parseDocument(example);

  it("has the 26 cases the Go checks run", () => {
    expect(cases.cases).toHaveLength(26);
    expect(new Set(cases.cases.map((c) => c.name)).size).toBe(26);
  });

  it.each(cases.cases.map((c) => [c.name, c] as const))("%s", (_, c) => {
    const got = checkUpdate(doc, c.update as ConfigurationUpdate);
    if (!c.code) {
      expect(got).toBeNull();
      return;
    }
    expect(got).not.toBeNull();
    expect(got!.code).toBe(c.code);
    const want = ("field_errors" in c ? c.field_errors : []) as { id: string; code: string }[];
    expect(got!.fieldErrors.map(({ id, code }) => ({ id, code }))).toEqual(want);
  });
});

describe("an update to a field this build can't check", () => {
  const withField = (raw: Record<string, unknown>) => {
    const doc = JSON.parse(JSON.stringify(example));
    doc.id = "with-unsupported";
    doc.groups[0].fields.push({ id: "future", title: "From a newer build", default: "#1f6feb", ...raw });
    doc.values.future = "#1f6feb";
    return parseDocument(doc).doc;
  };
  const update = (doc: ReturnType<typeof withField>, change: Partial<ConfigurationUpdate>) => ({ schema_version: 1, id: doc.id, revision: doc.revision, ...change });

  it("rejects a change and a reset to an unknown kind with unsupported", () => {
    const doc = withField({ kind: "colour" });
    for (const change of [{ changes: { future: "#000" } }, { reset: ["future"] }]) {
      const got = checkUpdate(doc, update(doc, change));
      expect(got?.code).toBe("configuration_invalid");
      expect(got?.fieldErrors).toEqual([{ id: "future", code: "unsupported" }]);
    }
  });

  it("rejects both for a known kind whose definition is malformed", () => {
    const doc = withField({ kind: "text", text: 42 });
    expect(doc.groups[0].fields.at(-1)).toMatchObject({ id: "future", kind: "unsupported", reason: "definition" });
    for (const change of [{ changes: { future: "text" } }, { reset: ["future"] }]) {
      const got = checkUpdate(doc, update(doc, change));
      expect(got?.fieldErrors).toEqual([{ id: "future", code: "unsupported" }]);
    }
  });

  it("still accepts an update that leaves the unsupported field alone", () => {
    const doc = withField({ kind: "colour" });
    expect(checkUpdate(doc, update(doc, { changes: { label: "Fine" } }))).toBeNull();
    expect(checkUpdate(doc, update(doc, { reset: ["label"] }))).toBeNull();
  });
});

describe("the error body", () => {
  it("reads error, code and field_errors", () => {
    const e = ConfigurationError.fromBody({ error: "Nope", code: "configuration_invalid", field_errors: [{ id: "a", code: "max_length" }, { id: "b", code: "custom", message: "Pick another" }, { bad: true }] });
    expect(e.code).toBe("configuration_invalid");
    expect(e.message).toBe("Nope");
    expect(e.fieldErrors).toEqual([{ id: "a", code: "max_length" }, { id: "b", code: "custom", message: "Pick another" }]);
  });

  it("stands in the root code its HTTP status names when the body has none", () => {
    expect(ConfigurationError.fromBody(undefined, 409).code).toBe("configuration_conflict");
    expect(ConfigurationError.fromBody(undefined, 503).code).toBe("configuration_unavailable");
    expect(ConfigurationError.fromBody({ error: "Too big" }, 413)).toMatchObject({ code: "configuration_too_large", message: "Too big" });
    expect(ConfigurationError.fromBody(undefined, 400).code).toBe("configuration_bad_request");
    expect(ConfigurationError.fromBody("<html>").code).toBe("failed");
    expect(ConfigurationError.fromBody(undefined, 418).code).toBe("failed");
    // any other server error is an unknown outcome, not a proven refusal
    expect(ConfigurationError.fromBody(undefined, 500).code).toBe("configuration_unavailable");
    expect(ConfigurationError.fromBody(undefined, 502).code).toBe("configuration_unavailable");
  });
});
