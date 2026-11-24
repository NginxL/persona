// SPACE.md in the editor (#13500): the user's parts are fields, Persona's blocks go back byte for byte.
import { describe, expect, it } from "vitest";
import { assembleSpaceMd, editableParts, humanSpaceMd, joinSpaceMd, sameManagedBlocks, spaceMdBody, splitSpaceMd } from "./space-md";

const STATE = "<!-- persona:managed:state:begin -->\nTaxes filed; waiting for the refund.\n<!-- persona:managed:state:end -->";
const FILE = `# Money\n\nWhat this space is for.\n\n${STATE}\n`;

describe("SPACE.md parts", () => {
  it("splits into the user's parts and Persona's blocks, and joins back unchanged", () => {
    const parts = splitSpaceMd(FILE);
    expect(parts.map((p) => p.kind)).toEqual(["human", "managed", "human"]);
    expect(parts[1]).toMatchObject({ kind: "managed", name: "state", body: "\nTaxes filed; waiting for the refund.\n" });
    expect(joinSpaceMd(parts)).toBe(FILE);
  });

  it("keeps a block without its end marker read-only to the end of the file", () => {
    const file = "notes\n<!-- persona:managed:state:begin -->\nhalf";
    const parts = splitSpaceMd(file);
    expect(parts.map((p) => p.kind)).toEqual(["human", "managed"]);
    expect(joinSpaceMd(parts)).toBe(file);
  });

  it("shows the parts with words as fields, and a field at the start of a file with none", () => {
    expect(editableParts(splitSpaceMd(FILE)).editable).toEqual([0]);
    const onlyBlock = editableParts(splitSpaceMd(`${STATE}\n`));
    expect(onlyBlock.editable).toEqual([0]);
    expect(onlyBlock.parts[0]).toEqual({ kind: "human", text: "" });
    expect(editableParts(splitSpaceMd("")).parts).toEqual([{ kind: "human", text: "" }]);
  });

  it("puts the edits in and the blocks back byte for byte", () => {
    const parts = splitSpaceMd(FILE);
    const out = assembleSpaceMd(parts, { 0: "# Money\n\nBudget and taxes.\n\n" });
    expect(out).toBe(`# Money\n\nBudget and taxes.\n\n${STATE}\n`);
    expect(sameManagedBlocks(FILE, out)).toBe(true);
  });

  it("keeps a block's marker on a line of its own after a field written right before it", () => {
    const { parts } = editableParts(splitSpaceMd(STATE));
    expect(assembleSpaceMd(parts, { 0: "Family notes" })).toBe(`Family notes\n\n${STATE}`);
  });

  it("keeps a block's end marker on a line of its own before an edited field right after it", () => {
    const { parts } = editableParts(splitSpaceMd(`${STATE}\n\nAfter notes.\n`));
    expect(assembleSpaceMd(parts, { 1: "Updated notes" })).toBe(`${STATE}\n\nUpdated notes`);
  });

  it("treats blocks with uppercase or dotted names as managed just like the node validator", () => {
    const ext = "<!-- persona:managed:ext.Notes_1:begin -->\nSynced summary.\n<!-- persona:managed:ext.Notes_1:end -->";
    const file = `Intro\n\n${ext}\n`;
    const parts = splitSpaceMd(file);
    expect(parts.map((p) => p.kind)).toEqual(["human", "managed", "human"]);
    expect(parts[1]).toMatchObject({ kind: "managed", name: "ext.Notes_1", body: "\nSynced summary.\n" });
    expect(humanSpaceMd(file)).toBe("Intro");
  });

  it("notices a block changed or a marker typed into a field", () => {
    expect(sameManagedBlocks(FILE, FILE.replace("refund", "check"))).toBe(false);
    expect(sameManagedBlocks(FILE, `<!-- persona:managed:state:begin -->x<!-- persona:managed:state:end -->${FILE}`)).toBe(false);
    expect(sameManagedBlocks(FILE, `${FILE}\n<!-- persona:managed:state:end -->\n`)).toBe(false);
    expect(sameManagedBlocks(FILE, `<!-- persona:managed:state:end -->\n${FILE}`)).toBe(false);
    expect(sameManagedBlocks(FILE, FILE.replace(STATE, ""))).toBe(false);
  });
});

describe("what the space's home shows", () => {
  it("keeps only what the user wrote", () => {
    expect(humanSpaceMd(FILE)).toBe("# Money\n\nWhat this space is for.");
    expect(humanSpaceMd(`${STATE}\n`)).toBe("");
    expect(humanSpaceMd(`Before\n\n${STATE}\n\nAfter\n`)).toBe("Before\n\nAfter");
  });

  it("drops a leading title that only repeats the space's name", () => {
    expect(spaceMdBody("# Money\n\nWhat this space is for.\n", "money")).toBe("What this space is for.");
    expect(spaceMdBody("# Bills\n\nWhat this space is for.\n", "Money")).toBe("# Bills\n\nWhat this space is for.");
    expect(spaceMdBody("## Money\n\nText\n", "Money")).toBe("## Money\n\nText");
    expect(spaceMdBody("# Money\n", "Money")).toBe("");
  });
});
