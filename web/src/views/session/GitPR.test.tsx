// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GitStatus } from "../../lib/files";
import type { GitPR } from "../../lib/git-pr";
import { t } from "../../i18n";
import { PRCard, useGitPR, type GitPRState } from "./GitPR";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// The Git panel's pull request card (#6121) is the checked-out branch's: after a switch, the previous branch's pull
// request is not shown while the new branch's is read, nor brought back by a write that answers late (#9940).
type Pending = { path: string; answer: (value: unknown, status?: number) => void };
let pending: Pending[];
let host: HTMLDivElement;
let root: Root;
let state: GitPRState;

const status = (branch: string, over: Partial<GitStatus> = {}): GitStatus => ({
  is_repo: true,
  root: "/projects/demo",
  branch,
  upstream: `origin/${branch}`,
  ahead: 0,
  behind: 0,
  head: { hash: `${branch}-1`, short: "1", subject: "Start", author: "Author" },
  commits: [],
  files: [],
  ...over,
});

const pr12: GitPR = {
  number: 12,
  url: "https://github.com/example/repo/pull/12",
  title: "Handle the empty password",
  state: "OPEN",
  base: "main",
  head: "fix/login",
  merge_state: "CLEAN",
  checks: [{ name: "test", state: "pass" }],
  threads: [],
};

function Card({ s }: { s: GitStatus }) {
  state = useGitPR("/machines/computer/git", "/projects/demo", s, true);
  return <PRCard s={s} pr={state} onOpen={() => {}} onCreate={() => {}} />;
}

const show = (s: GitStatus) => act(() => root.render(<Card s={s} />));
const reads = () => pending.filter((p) => p.path.includes("/git/pr?"));
const card = () => host.textContent ?? "";

beforeEach(() => {
  pending = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      (input: RequestInfo | URL) =>
        new Promise<Response>((resolve) =>
          pending.push({
            path: String(input),
            answer: (value, code = 200) => resolve(new Response(JSON.stringify(value), { status: code, headers: { "Content-Type": "application/json" } })),
          }),
        ),
    ),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the pull request card", () => {
  it("does not show the previous branch's pull request while the new branch's is read", async () => {
    show(status("fix/login"));
    await act(async () => reads().at(-1)!.answer({ branch: "fix/login", pr: pr12 }));
    expect(card()).toContain("#12");

    show(status("feature/signup"));
    expect(reads()).toHaveLength(2);
    expect(card()).not.toContain("#12");
    expect(card()).not.toContain(pr12.title);

    await act(async () => reads().at(-1)!.answer({ branch: "feature/signup", pr: null }));
    expect(card()).toContain(t("session-gitpr.none"));
  });

  it("keeps the card while the same branch is read again after a commit", async () => {
    show(status("fix/login"));
    await act(async () => reads().at(-1)!.answer({ branch: "fix/login", pr: pr12 }));

    show(status("fix/login", { ahead: 1, head: { hash: "fix/login-2", short: "2", subject: "More", author: "Author" } }));
    expect(reads()).toHaveLength(2);
    expect(card()).toContain("#12");
  });

  it("a merge that answers after a switch does not put the old branch's pull request back", async () => {
    show(status("fix/login"));
    await act(async () => reads().at(-1)!.answer({ branch: "fix/login", pr: pr12 }));
    act(() => void state.merge(pr12, { method: "squash" }, "Merged"));
    const merge = pending.find((p) => p.path.includes("/git/pr/merge"))!;

    show(status("feature/signup"));
    await act(async () => reads().at(-1)!.answer({ branch: "feature/signup", pr: null }));
    expect(card()).toContain(t("session-gitpr.none"));

    await act(async () => merge.answer({ branch: "fix/login", pr: { ...pr12, state: "MERGED" } }));
    expect(card()).not.toContain("#12");
    expect(card()).toContain(t("session-gitpr.none"));
  });
});
