// A failed start's reason in plain words on the hand-off card and the Needs you row (#8237, iOS StartFailureWords): the
// raw error stays in the task's details.
import { describe, expect, it } from "vitest";
import { t } from "../i18n";
import { startFailureCardLine, startFailureCause, startFailurePlain, startFailureRowLine } from "./start-failure";

describe("a failed start's reason", () => {
  it("names the known causes in a raw error", () => {
    expect(startFailureCause("rpc: connection closed")).toBe("connection_lost");
    expect(startFailureCause("和「MacBook」的连接断了，等了一阵也没重连上：rpc: connection closed")).toBe("connection_lost"); // i18n-ignore
    expect(startFailureCause("unexpected EOF")).toBe("connection_lost");
    expect(startFailureCause("context deadline exceeded")).toBe("timed_out");
    expect(startFailureCause("dial tcp: connect: connection refused")).toBe("offline");
    expect(startFailureCause("ssh: handshake failed: permission denied (publickey)")).toBe("not_allowed");
    expect(startFailureCause("rpc: unknown method agent.start")).toBe("needs_update");
    expect(startFailureCause("exit status 1")).toBeUndefined();
    expect(startFailureCause("the geoff error")).toBeUndefined(); // "eof" inside a word is no end of file
  });

  it("keeps a sentence the hub wrote for people, and says to open the task for any other error", () => {
    expect(startFailurePlain("这台电脑上没装 Claude Code，装好后再试")).toBe("这台电脑上没装 Claude Code，装好后再试"); // i18n-ignore
    const english = "Claude Code isn't installed on this computer. Install it, then retry.";
    expect(startFailurePlain(english)).toBe(english);
    expect(startFailurePlain("exit status 1")).toBeUndefined();
    expect(startFailurePlain("  ")).toBeUndefined();
    expect(startFailureRowLine("exit status 1")).toBe(t("assistant-tasks.start_failed_details"));
    expect(startFailureRowLine("rpc: connection closed")).toBe(t("assistant-tasks.start_cause_connection_lost"));
    expect(startFailureCardLine("rpc: connection closed", false)).toBe(t("assistant-tasks.start_failed", { error: t("assistant-tasks.start_cause_connection_lost") }));
    expect(startFailureCardLine("exit status 1", true)).toBe(t("assistant-tasks.start_failed_details"));
    expect(startFailureCardLine("exit status 1", false)).toBe(t("assistant-tasks.start_failed_short"));
  });

  it("uses the continuation cause without displaying the internal prefix", () => {
    const stop = "continues: cannot stop agent timeout-computer/session on to-do prior-task:";
    const confirm = "continues: cannot confirm agent example-computer/session stopped:";
    const reasons = [
      "Close the previous session, then try again.",
      "请先关闭之前的会话，然后重试。", // i18n-ignore: synthetic hub text
      "前の会話を閉じてからやり直してください。", // i18n-ignore: synthetic hub text
    ];
    for (const reason of reasons) expect(startFailurePlain(`${stop} ${reason}`)).toBe(reason);
    expect(startFailurePlain(`${confirm} rpc: connection closed`)).toBe(t("assistant-tasks.start_cause_connection_lost"));
    expect(startFailurePlain(`${confirm} its computer is unavailable`)).toBe(t("assistant-tasks.start_cause_offline"));
    expect(startFailurePlain(`${stop} exit status 1`)).toBeUndefined();
    expect(startFailurePlain("continues: unrecognized wrapper with 内部 ID")).toBeUndefined(); // i18n-ignore: synthetic hub text
  });
});
