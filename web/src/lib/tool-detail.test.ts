import { describe, expect, it } from "vitest";
import { askUserAnswers, baseToolName, optionDisplay, patchDiffs, permissionDetail, permissionReason, pickedOption, summaryExtra, toolQuestions, typedAnswer, withoutCutMark } from "./tool-detail";

describe("patchDiffs (a Codex file change, G001)", () => {
  it("is one block per hunk, the file headers left out", () => {
    const patch = ["--- a/src/a.ts", "+++ b/src/a.ts", "@@ -1,3 +1,3 @@", " keep", "-old", "+new", "@@ -10,2 +10,3 @@", " ten", "+eleven", "\\ No newline at end of file"].join("\n");
    expect(patchDiffs(patch)).toEqual([
      [{ kind: "same", text: "keep" }, { kind: "del", text: "old" }, { kind: "add", text: "new" }],
      [{ kind: "same", text: "ten" }, { kind: "add", text: "eleven" }],
    ]);
  });

  it("takes a whole new file as added, a deleted one as removed", () => {
    expect(patchDiffs("one\ntwo\n", "add")).toEqual([[{ kind: "add", text: "one" }, { kind: "add", text: "two" }]]);
    expect(patchDiffs("gone", "delete")).toEqual([[{ kind: "del", text: "gone" }]]);
    expect(patchDiffs("")).toEqual([]);
  });
});

describe("questions of an AskUserQuestion / ask_user", () => {
  const input = {
    questions: [
      { question: "用哪个方案？", header: "方案", options: [{ label: "A", description: "快" }, { label: "B" }] },
      { question: "要哪些？", multiSelect: true, options: [{ label: "x" }, { label: "y" }, { label: "z" }] },
      { question: "" },
    ],
  };

  it("reads the questions with their options, skipping empty ones", () => {
    const qs = toolQuestions(input);
    expect(qs).toHaveLength(2);
    expect(qs[0]).toEqual({ question: "用哪个方案？", header: "方案", multiSelect: false, options: [{ label: "A", description: "快" }, { label: "B", description: undefined }] });
    expect(qs[1].multiSelect).toBe(true);
    expect(toolQuestions(undefined)).toEqual([]);
  });

  it("finds each answer in the hub's ask_user result, picks and typed answers", () => {
    const qs = toolQuestions(input);
    const answers = askUserAnswers("The user answered:\n- 用哪个方案？ → 都不要，换个思路\n- 要哪些？ → x, z\n", qs);
    expect(answers).toEqual({ "用哪个方案？": "都不要，换个思路", "要哪些？": "x, z" });
    expect(pickedOption("x", answers["要哪些？"], true)).toBe(true);
    expect(pickedOption("y", answers["要哪些？"], true)).toBe(false);
    expect(typedAnswer(qs[1], answers["要哪些？"])).toBeUndefined();
    expect(typedAnswer(qs[0], answers["用哪个方案？"])).toBe("都不要，换个思路");
    expect(askUserAnswers("The user did not answer (dismissed or timed out).", qs)).toEqual({});
  });

  it("matches the hub's raw question and label, edge whitespace and all", () => {
    const qs = toolQuestions({ questions: [{ question: "继续吗？ ", options: [{ label: " 是" }, { label: "否" }] }, { question: "  " }] });
    expect(qs).toHaveLength(1);
    const answers = askUserAnswers("The user answered:\n- 继续吗？  →  是\n", qs);
    expect(answers).toEqual({ "继续吗？ ": "是" });
    expect(pickedOption(qs[0].options[0].label, answers["继续吗？ "], false)).toBe(true);
    expect(typedAnswer(qs[0], answers["继续吗？ "])).toBeUndefined();
  });

  it("parses Claude Code AskUserQuestion results alongside hub ask_user results", () => {
    const qs = toolQuestions(input);
    const claudeResult =
      'User has answered your questions: "用哪个方案？"="都不要，换个思路", "要哪些？"="x, z". You can now continue with the user\'s answers in mind.';
    const answers = askUserAnswers(claudeResult, qs);
    expect(answers).toEqual({ "用哪个方案？": "都不要，换个思路", "要哪些？": "x, z" });
    expect(pickedOption("x", answers["要哪些？"], true)).toBe(true);
    expect(pickedOption("y", answers["要哪些？"], true)).toBe(false);
    expect(typedAnswer(qs[0], answers["用哪个方案？"])).toBe("都不要，换个思路");
    expect(askUserAnswers("User has answered your questions: ", qs)).toEqual({});
  });

  it("names an MCP tool by its own name", () => {
    expect(baseToolName("mcp__persona__ask_user")).toBe("ask_user");
    expect(baseToolName("ExitPlanMode")).toBe("ExitPlanMode");
  });
});

describe("withoutCutMark (G002)", () => {
  it("drops the note the hub adds where it cut", () => {
    expect(withoutCutMark("开头\n…（已截断，点开看全文，共 90000 字节）")).toBe("开头");
    expect(withoutCutMark("完整的")).toBe("完整的");
  });
});

describe("optionDisplay (iOS QuestionOption.display)", () => {
  it("drops Claude's recommended suffix and says it was there", () => {
    expect(optionDisplay("Swift Testing (Recommended)")).toEqual({ text: "Swift Testing", recommended: true });
    expect(optionDisplay("取最长的那段(Recommended)")).toEqual({ text: "取最长的那段", recommended: true });
    expect(optionDisplay("JPEG（推荐）")).toEqual({ text: "JPEG", recommended: true });
    expect(optionDisplay("PNG (推荐) ")).toEqual({ text: "PNG", recommended: true });
  });

  it("leaves other labels alone, and a label that is only the suffix", () => {
    expect(optionDisplay("XCTest")).toEqual({ text: "XCTest", recommended: false });
    expect(optionDisplay("Recommended settings")).toEqual({ text: "Recommended settings", recommended: false });
    expect(optionDisplay("(Recommended)")).toEqual({ text: "(Recommended)", recommended: false });
  });
});

describe("permissionDetail", () => {
  it("splits a command from the agent's sentence", () => {
    expect(permissionDetail("Bash", { command: "swift test", description: "运行网络层测试" })).toEqual({ note: "运行网络层测试", fields: [{ label: "command", value: "swift test" }] });
    expect(permissionDetail("exec_command", { command: ["ls", "-la"] })).toEqual({ note: undefined, fields: [{ label: "command", value: "ls -la" }] });
    expect(permissionDetail("shell", { command: ["bash", "-lc", "echo hi && ls"] })?.fields[0].value).toBe("bash -lc 'echo hi && ls'");
  });
  it("names the file, the address, the search", () => {
    expect(permissionDetail("Edit", { file_path: "/a/b.ts", old_string: "x", new_string: "y" })?.fields).toEqual([{ label: "file", value: "/a/b.ts" }]);
    expect(permissionDetail("WebFetch", { url: "https://example.com", prompt: "读标题" })).toEqual({ note: "读标题", fields: [{ label: "url", value: "https://example.com" }] });
    expect(permissionDetail("Grep", { pattern: "foo", path: "src" })?.fields.map((f) => f.label)).toEqual(["pattern", "path"]);
  });
  it("gives up on a tool it does not know or an input without its field", () => {
    expect(permissionDetail("mail_send", { to: "me@example.com" })).toBeNull();
    expect(permissionDetail("Bash", {})).toBeNull();
    expect(permissionDetail("Bash", { description: "跑测试" })).toEqual({ note: "跑测试", fields: [] });
    expect(permissionDetail(undefined, undefined)).toBeNull();
  });
});

describe("summaryExtra", () => {
  const cmd = [{ label: "command" as const, value: "curl -s x" }];
  it("keeps only what the summary adds", () => {
    expect(summaryExtra("curl -s x", cmd)).toBe("");
    expect(summaryExtra("curl -s x\n\nneeds network", cmd)).toBe("needs network");
    expect(summaryExtra("给 me@example.com 发一封邮件", cmd)).toBe("给 me@example.com 发一封邮件");
    expect(summaryExtra(undefined, cmd)).toBe("");
    expect(summaryExtra("curl -s xyz", cmd)).toBe("curl -s xyz");
  });
});

describe("permissionReason", () => {
  it("is another agent's sentence, trimmed", () => {
    expect(permissionReason("codex", " Allow this command to access localhost:8080? ")).toBe("Allow this command to access localhost:8080?");
    expect(permissionReason("traex", "需要联网")).toBe("需要联网");
  });
  it("is empty for Claude Code or without a reason", () => {
    expect(permissionReason(undefined, "x")).toBe("");
    expect(permissionReason("claude", "x")).toBe("");
    expect(permissionReason("codex", undefined)).toBe("");
    expect(permissionReason("codex", "  ")).toBe("");
  });
});
