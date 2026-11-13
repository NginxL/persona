import { expect, it } from "vitest";
import en from "./locales/en/chat-chatview.json";
import zh from "./locales/zh-Hans/chat-chatview.json";
import ja from "./locales/ja/chat-chatview.json";

// The menu truncates at the end, so completion must precede the variable-length title.
it.each([
  ["en", en.todo_completed_label, "Completed"],
  ["zh-Hans", zh.todo_completed_label, "已完成"],
  ["ja", ja.todo_completed_label, "完了"],
])("keeps completed ownership visible before a long title in %s", (_, label, status) => {
  expect(label.indexOf(status)).toBeGreaterThanOrEqual(0);
  expect(label.indexOf(status)).toBeLessThan(label.indexOf("{title}"));
});
