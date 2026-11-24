import { describe, expect, it } from "vitest";
import { ApiError } from "../lib/api";
import type { Session } from "../lib/types";
import { getLinkDialogTargetKey } from "./LinkTodoDialog";
import { isLegacySpacesRouteError, type Todo } from "../lib/todos";
import { resolveLinkCurrentTodoId } from "../lib/todo-details";

const mockSession = (mid: string, sid: string, patch: Partial<Session> = {}): Session => ({
  machine_id: mid,
  session_id: sid,
  title: sid,
  cwd: `/Users/me/${sid}`,
  status: "running",
  ...patch,
});

describe("LinkTodoDialog 目标识别 (getLinkDialogTargetKey)", () => {
  it("生成唯一的 targetKey 区分不同的项目空间与会话", () => {
    const space1 = { machineId: "mac-1", space: "weather" };
    const space2 = { machineId: "mac-1", space: "docs" };
    const session1 = mockSession("mac-1", "s1", { title: "Agent 1" });

    expect(getLinkDialogTargetKey(space1)).toBe("sp:mac-1/weather");
    expect(getLinkDialogTargetKey(space2)).toBe("sp:mac-1/docs");
    expect(getLinkDialogTargetKey(undefined, session1)).toBe("s:mac-1/s1");
    expect(getLinkDialogTargetKey(space1)).not.toBe(getLinkDialogTargetKey(space2));
  });
});

describe("LinkTodoDialog 解除关联控制与待办来源 (#874 item 4)", () => {
  it("当 session 已关联待办时，即便 todos 列表为空（未加载），currentTodoId 也保持为 session.todo.id", () => {
    const session = mockSession("mac-1", "sess-1", {
      todo: {
        id: "todo-abc",
        title: "待办 ABC",
      },
    });

    // empty todos list should still resolve session.todo.id
    const currentId = resolveLinkCurrentTodoId(undefined, session, []);
    expect(currentId).toBe("todo-abc");
  });

  it("当 todos 列表已加载时，匹配正确的待办 ID", () => {
    const todos: Todo[] = [
      {
        id: "todo-xyz",
        title: "待办 XYZ",
        created_at: "",
        updated_at: "",
        spaces: [{ machine_id: "mac-1", space: "frontend", linked_at: "" }],
      },
    ];

    expect(resolveLinkCurrentTodoId({ machineId: "mac-1", space: "frontend" }, undefined, todos)).toBe("todo-xyz");
  });
});

describe("LinkTodoDialog 旧 hub 路由 404 判定 (isLegacySpacesRouteError) (#881 review)", () => {
  it("真实 hub 未注册路由返回的 JSON 404 (code=not_found, error=Not Found) 判定为旧 hub 错误", () => {
    const route404Json = new ApiError(404, "Not Found", { error: "Not Found", code: "not_found", retryable: false }, true);
    expect(isLegacySpacesRouteError(route404Json)).toBe(true);
  });

  it("纯文本非 JSON 的 404 同样判定为旧 hub 错误", () => {
    const route404Text = new ApiError(404, "404 page not found", { error: "404 page not found" }, false);
    expect(isLegacySpacesRouteError(route404Text)).toBe(true);
  });

  it("如果是处理函数自己返回的 JSON 错误「todo not found」或「待办不存在」，不判定为旧 hub", () => {
    const todoNotFound = new ApiError(404, "todo not found", { error: "todo not found", code: "not_found" }, true);
    expect(isLegacySpacesRouteError(todoNotFound)).toBe(false);

    const todoNotFoundCn = new ApiError(404, "待办不存在", { error: "待办不存在", code: "not_found" }, true);
    expect(isLegacySpacesRouteError(todoNotFoundCn)).toBe(false);
  });

  it("其它非 404 状态码返回 false", () => {
    const error500 = new ApiError(500, "internal server error", { error: "internal error" }, true);
    expect(isLegacySpacesRouteError(error500)).toBe(false);
  });
});
