import { describe, expect, it } from "vitest";
import { contextXML } from "./context-xml";
import { skillContext, skillDraft, splitSkillDraft, matchingSkills, skillEditText } from "./skill-mentions";
import { parseUserText } from "./turns";
import type { ContextRef } from "./links";

const review = { id: "opaque-a", name: "review", description: "Inspect security boundaries", source: "project · ." };

describe("explicit skill references", () => {
  it("matches descriptions and keeps duplicate source identities", () => {
    const other = { ...review, id: "opaque-b", source: "user" };
    expect(matchingSkills([review, other], "SECURITY")).toEqual([review, other]);
    expect(skillContext(review).attrs?.ref).not.toBe(skillContext(other).attrs?.ref);
  });

  it("round trips drafts, edit/queue input and send history without turning prose into selections", () => {
    const selection = skillContext(review);
    const text = "Email me@example.com, literal @review and @src/file.ts";
    const saved = skillDraft([selection], text);
    const restored = splitSkillDraft(saved);
    expect(restored.text).toBe(text);
    expect(restored.skills).toEqual([selection]);
    expect(skillDraft(restored.skills, restored.text)).toBe(saved);
    expect(splitSkillDraft(text)).toEqual({ text, skills: [] });
    expect(splitSkillDraft(`Quoted:\n${contextXML(selection)}`).skills).toEqual([]);
    expect(splitSkillDraft(`> ${contextXML(selection)}`).skills).toEqual([]);
    expect(parseUserText(saved).contexts[0].attrs?.ref).toBe(review.id);
  });

  it("restores scheduled selections only until the blank line before literal prose", () => {
    const selected = skillContext(review);
    const body = contextXML(skillContext({ ...review, id: "literal" })) + "\n\n  Explain this example";
    expect(splitSkillDraft(skillDraft([selected], body), true)).toEqual({ text: body, skills: [selected] });
  });

  it.each(["\n \n", "\n\t\n", " \t\r\n\t \r\n", "\n\n"])("preserves literal scheduled body after separator %j", (separator) => {
    const selected: ContextRef[] = [skillContext(review), {
      id: "file:notes.txt", kind: "file", title: "Notes", url: "", text: "notes.txt",
      attrs: { path: "notes.txt", machine: "test", cwd: "/work", launcher: "claude" },
    }];
    const literalSkill = contextXML(skillContext({ ...review, id: "literal" }));
    const literalFile = contextXML({ ...selected[1]!, attrs: { ...selected[1]!.attrs, path: "example.txt" } });
    const body = `${literalSkill}\n${literalFile}\n\n  Keep this example verbatim.\n`;
    const encode = (refs: ContextRef[], text: string, gap: string) => refs.map(contextXML).join("\n") + gap + text;
    const restored = splitSkillDraft(encode(selected, body, separator), true);
    expect(restored).toEqual({ text: body, skills: selected });
    expect(splitSkillDraft(encode(restored.skills, restored.text, "\n\n"), true)).toEqual(restored);
    expect(splitSkillDraft(encode(selected, " \t\n" + body, "\n\n"), true).text).toBe(" \t\n" + body);
  });

  it("keeps text beyond the scheduled ASCII boundary and uses the ordinary chat parser when requested", () => {
    const selected = skillContext(review);
    const literal = skillContext({ ...review, id: "literal" });
    const body = "\u00a0" + contextXML(literal);
    expect(splitSkillDraft(contextXML(selected) + "\n" + body, true)).toEqual({ text: "\n" + body, skills: [selected] });
    expect(splitSkillDraft(contextXML(selected) + "\n \n" + contextXML(literal))).toEqual({ text: "", skills: [selected, literal] });

    const ordinary = contextXML({ kind: "context", title: "Note", url: "", text: "Quoted example" });
    const raw = ordinary + "\n \n" + contextXML(literal);
    expect(splitSkillDraft(raw, true)).toEqual({ text: raw, skills: [] });
  });

  it("preserves existing context tags and removes only explicit skill tags from the editor", () => {
    const file = contextXML({ kind: "context", title: "file", url: "", text: "@src/main.ts" });
    const saved = file + "\n" + skillDraft([skillContext(review)], "Check this");
    expect(splitSkillDraft(saved)).toEqual({ text: file + "\n\nCheck this", skills: [skillContext(review)] });
    expect(skillDraft([], "")).toBe("");
  });

  it("preserves a sent selection when editing history, without turning ordinary quoted contexts into calls", () => {
    const selection = skillContext(review);
    const contexts = parseUserText(skillDraft([selection], "Check this")).contexts;
    expect(splitSkillDraft(skillEditText(contexts, "Check this")).skills).toEqual([selection]);
    expect(skillEditText([{ kind: "context", title: "@review", text: "@review" }], "Ordinary")).toBe("Ordinary");
  });
});
