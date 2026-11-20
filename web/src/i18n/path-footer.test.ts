import { describe, expect, it } from "vitest";
import en from "./locales/en/settings.json";
import zh from "./locales/zh-Hans/settings.json";
import ja from "./locales/ja/settings.json";

// PATH wrappers and runtime-provided launchers share the Model providers group; neither implies Claude (#10141).
describe("the Model providers PATH footer", () => {
  it.each([
    ["en", en.path_footer, [/^Agent/, /wrapper scripts/, /each computer/, /PATH/, /runtime/, /launchers?/]],
    ["zh-Hans", zh.path_footer, [/Agent/, /包装脚本/, /各台电脑/, /PATH/, /运行时/, /启动器/]],
    ["ja", ja.path_footer, [/エージェント/, /ラッパースクリプト/, /各コンピューター/, /PATH/, /ランタイム/, /ランチャー/]],
  ])("describes Agent PATH wrappers and runtime launchers without assuming Claude in %s", (_, footer, required) => {
    expect(footer).not.toMatch(/claude/i);
    for (const description of required) expect(footer).toMatch(description);
  });
});
