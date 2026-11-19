// @vitest-environment jsdom
// #8163: a tool call's own elapsed time is a quiet tabular-nums suffix on wide rows and the row's hover title on
// compact layouts; sub-second and unfinished calls show nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Item } from "../../lib/types";
import type { ToolStep } from "../../lib/work-groups";
import { ActionRow } from "./ActionRow";
import { forgetExpanded } from "./expanded";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const T0 = Date.parse("2026-11-04T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();
const call = (id: string, startS: number): Item => ({ id: `${id}#0`, seq: 1, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: `c-${id}`, tool_input: { command: "npm test", description: "跑单测" }, ts: at(startS) });
const result = (id: string, endS: number): Item => ({ id: `${id}-r#0`, seq: 2, role: "user", kind: "tool_result", tool_use_id: `c-${id}`, text: "ok", ts: at(endS) });
const step = (id: string, startS: number, endS?: number): ToolStep => {
  const u = call(id, startS);
  const r = endS === undefined ? undefined : result(id, endS);
  return { kind: "tool", id: u.id, use: u, ...(r ? { result: r } : {}), images: [] };
};

let host: HTMLDivElement;
let root: Root;
let narrow = false;
const mql = { get matches() { return narrow; }, addEventListener() {}, removeEventListener() {} };

beforeEach(() => {
  forgetExpanded();
  narrow = false;
  (globalThis as { matchMedia?: (q: string) => unknown }).matchMedia = (q: string) => (q === "(max-width: 1023px)" ? mql : { matches: false, addEventListener() {}, removeEventListener() {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  delete (globalThis as { matchMedia?: unknown }).matchMedia;
});

const render = (s: ToolStep, running = false) => act(() => root.render(<ActionRow step={s} running={running} mid="m" sid="s" />));
const row = () => host.querySelector<HTMLButtonElement>("[data-action-row] > button")!;
const suffix = () => host.querySelector("[data-tool-duration]");

describe("ActionRow per-call duration (#8163)", () => {
  it("a real span renders the suffix on a wide row, without changing the row's own title", () => {
    render(step("t1", 0, 30));
    expect(suffix()?.textContent).toBe("30s");
    expect(suffix()?.className).toContain("tabular-nums");
    expect(suffix()?.className).toContain("max-lg:hidden");
    // the parameter's title stays as it was; the duration is not duplicated into it on wide layouts
    expect(row().title).toBe("跑单测");
  });

  it("on a compact layout the duration moves into the hover title while the suffix stays CSS-hidden", () => {
    narrow = true;
    render(step("t1", 0, 30));
    expect(row().title).toBe("跑单测 · 30s");
    expect(suffix()?.className).toContain("max-lg:hidden");
  });

  it("a sub-second or unfinished call shows no suffix and no duration in the title", () => {
    render(step("t2", 0, 0.4));
    expect(suffix()).toBeNull();
    expect(row().title).toBe("跑单测");

    render(step("t3", 0), true);
    expect(suffix()).toBeNull();
    expect(row().title).toBe("跑单测");
  });
});
