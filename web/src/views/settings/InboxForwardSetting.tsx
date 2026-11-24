// Opt-in toggle for deterministic PR/Issue Inbox auto-forwarding (#12492) surfaced on Settings › Inbox Sources.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { api, errorText } from "../../lib/api";
import { itemHeaders, otherManager, useChosenManager } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { Group, Row, Switch } from "./SettingsParts";

export function InboxForwardSetting() {
  const manager = otherManager(useChosenManager() ?? undefined);
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let dead = false;
    setEnabled(null);
    api<{ inbox_todo_forward?: boolean }>("/assistant", { headers: itemHeaders(manager) })
      .then((r) => {
        if (!dead && typeof r.inbox_todo_forward === "boolean") setEnabled(r.inbox_todo_forward);
      })
      .catch(() => {});
    return () => {
      dead = true;
    };
  }, [manager]);

  if (enabled === null) return null;

  const toggle = async (next: boolean) => {
    const before = enabled;
    setEnabled(next);
    setBusy(true);
    try {
      const r = await api<{ inbox_todo_forward?: boolean }>("/assistant", { method: "PUT", json: { inbox_todo_forward: next }, headers: itemHeaders(manager) });
      setEnabled(typeof r.inbox_todo_forward === "boolean" ? r.inbox_todo_forward : next);
    } catch (e) {
      setEnabled(before);
      toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-4">
      <Group anchor="inbox-todo-forward">
        <Row label={t("settings-assistantsettings.inbox_todo_forward")} hint={t("settings-assistantsettings.inbox_todo_forward_hint")} wrapHint>
          <Switch on={enabled} onChange={(on) => void toggle(on)} disabled={busy} label={t("settings-assistantsettings.inbox_todo_forward")} />
        </Row>
      </Group>
    </div>
  );
}
