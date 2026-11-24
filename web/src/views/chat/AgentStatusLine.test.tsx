import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { t } from "../../i18n";
import type { Session } from "../../lib/types";
import type { UsageAccount } from "../../lib/usage-windows";
import { AgentStatusLine, sessionQuota, workingLocation } from "./AgentStatusLine";

const session: Session = { machine_id: "m", session_id: "s", title: "Example", cwd: "/work/topic/src", status: "idle", agent: "codex" };
const now = Date.parse("2026-11-16T16:00:00Z");
const quota: UsageAccount = {
  id: "codex", provider: "codex", name: "Codex", sampled_at: new Date(now).toISOString(),
  windows: [
    { label: "5h", window_minutes: 300, used_percent: 4 },
    { label: "7d", window_minutes: 10080, used_percent: 33, model: "example" },
  ],
};

describe("Agent status line", () => {
  it("uses the repository name and a folder relative to the linked worktree", () => {
    const linked = { ...session, repo_root: "/work/project", worktree: "/work/topic", branch: "topic" };
    expect(workingLocation(linked)).toEqual({ name: "project", subfolder: "src", branch: "topic", folder: "/work/topic/src" });
    expect(renderToStaticMarkup(<AgentStatusLine session={linked} />)).toContain("project");
    expect(workingLocation({ ...linked, cwd: "/work/topic-other/src" })?.subfolder).toBe("");
  });

  it("uses the physical directory where available and shows only the folder outside git", () => {
    expect(workingLocation({ ...session, cwd: "/alias", workspace_cwd: "/work/topic/src", repo_root: "/work/topic" })?.subfolder).toBe("src");
    expect(workingLocation(session)).toEqual({ name: "src", subfolder: "", branch: "", folder: "/work/topic/src" });
    expect(renderToStaticMarkup(<AgentStatusLine session={{ ...session, cwd: "" }} />)).toBe("");
  });

  it("uses a single unambiguous live subscription for the session's engine", () => {
    const claude = { ...quota, id: "claude", provider: "claude" };
    expect(sessionQuota([quota, claude], session, now)?.windows.map((window) => window.label)).toEqual(["5h"]);
    expect(sessionQuota([quota, { ...quota, id: "other" }], session, now)).toBeNull();
    expect(sessionQuota([claude], session, now)).toBeNull();
    expect(sessionQuota([{ ...quota, stale: true }], session, now)).toBeNull();
  });

  it("shows agy context before items load, with a progress bar only for a known window", () => {
    const agy = { ...session, cwd: "", agent: "agy", model: "gemini-3.8-flash-high", context_tokens: 32_768 };
    const known = renderToStaticMarkup(<AgentStatusLine session={{ ...agy, context_window: 131_072 }} />);
    expect(known).toContain('role="progressbar"');
    expect(known).toContain('aria-valuenow="25"');
    expect(known).toContain(t("context.label", { percent: 25 }));
    const unknown = renderToStaticMarkup(<AgentStatusLine session={{ ...agy, model: "custom", context_window: 0 }} />);
    expect(unknown).toContain(t("context.current_tokens", { tokens: "32.8k" }));
    expect(unknown).not.toMatch(/progressbar|%/);
  });

  it("shows ContextMeter in Assistant chats without folder/repo when context or item usage is known", () => {
    const asst: Session = { ...session, assistant: true, context_tokens: 40_000, context_window: 200_000 };
    const html = renderToStaticMarkup(<AgentStatusLine session={asst} assistant />);
    expect(html).toContain("data-composer-usage");
    expect(html).toContain('aria-valuenow="20"');
    expect(html).not.toContain("data-composer-source");

    const usageOnly = renderToStaticMarkup(
      <AgentStatusLine
        session={{ ...session, assistant: true }}
        assistant
        items={[{ id: "a1", seq: 1, role: "assistant", kind: "text", usage: [{ in: 1200, out: 800 }] }]}
      />,
    );
    expect(usageOnly).toContain("2.0k tok");

    expect(renderToStaticMarkup(<AgentStatusLine session={{ ...session, assistant: true }} assistant />)).toBe("");
  });
});
