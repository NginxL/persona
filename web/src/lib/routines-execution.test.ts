import { expect, it } from "vitest";
import { blankTask, runsWith, sameTask, taskBody, validateTask } from "./routines";

it("keeps legacy title edits sparse after canonical empty fields enter the draft", () => {
  for (const binding of [{ launcher: "claude" }, { effort: "high" }, { launcher: "claude", effort: "high" }]) {
    const saved = { ...blankTask(), id: "job", title: "Before", prompt: "Check files", repeat: "interval" as const, every_minutes: 30, session_id: "origin", ...binding };
    const draft = { ...saved, title: "After", launcher: saved.launcher ?? "", effort: saved.effort ?? "" };
    expect(validateTask(draft, new Date(), saved)).toBeNull();
    const body = taskBody(draft, saved);
    for (const key of ["launcher", "model", "effort", "cwd", "execution_machine_id", "model_revision"]) expect(body).not.toHaveProperty(key);
    expect(validateTask({ ...draft, effort: "low" }, new Date(), saved)).not.toBeNull();
  }
});

it("saves task execution edits and clears overrides explicitly", () => {
  const saved = { ...blankTask(), id: "task", launcher: "codex", model: "test-model", cwd: "/work/project", execution_machine_id: "remote", model_revision: 3 };
  const draft = { ...saved, model: "another-model", cwd: "/work/other" };
  expect(sameTask(saved, draft)).toBe(false);
  expect(taskBody(draft, saved)).toMatchObject({ model: "another-model", cwd: "/work/other", model_revision: 3 });
  expect(taskBody({ ...saved, launcher: "", model: "", cwd: "", execution_machine_id: "" }, saved)).toMatchObject({ launcher: "", model: "", cwd: "", execution_machine_id: "" });
  expect(taskBody({ ...saved, cwd: "/work/project " }, saved).cwd).toBe("/work/project ");
  expect(sameTask(saved, { ...saved, cwd: "/work/project " })).toBe(false);
  expect(taskBody(saved, saved)).not.toHaveProperty("model");
  expect(taskBody(blankTask())).not.toHaveProperty("cwd");
});

it("shows resolved inherited and overridden execution without losing model or backend", () => {
  const names = [{ name: "assistant", display_name: "Assistant", agent: "claude" }, { name: "agents", agent: "codex" }];
  const inherited = { ...blankTask(), execution: { machine_id: "box", launcher: "assistant", model: "opus", effort: "high" } };
  expect(runsWith(inherited, names)?.text).toBe("Assistant · claude · opus · high");
  expect(runsWith({ ...inherited, launcher: "agents", execution: { machine_id: "box", launcher: "agents", model: "test-model", effort: "high" } }, names)?.text).toBe("agents · codex · test-model · high");
});

it("keeps historical unconfigured tasks sparse and preserves their legacy display", () => {
  const legacy = JSON.parse('{"id":"old-task","title":"Daily check","prompt":"Check files","repeat":"daily","time":"09:00","enabled":true}');
  expect(runsWith(legacy)).toBeNull();
  expect(runsWith({ ...legacy, model: "old-unused-model" })).toBeNull();
  const body = taskBody({ ...legacy, title: "Updated title" }, legacy);
  for (const key of ["launcher", "model", "effort", "execution_machine_id", "cwd", "model_revision"]) expect(body).not.toHaveProperty(key);
  expect(runsWith({ ...legacy, execution: { machine_id: "box", launcher: "claude" } })?.text).not.toMatch(/undefined|null/);
});
