import { useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { toast } from "../../ui/atoms";
import { Group, Row, settingsInputClass, Switch } from "./SettingsParts";

export interface CacheKeepaliveState {
  cache_keepalive?: boolean;
  cache_keepalive_minutes?: number;
  cache_keepalive_max?: number;
}

export function CacheKeepaliveSettings({ value, save }: { value: CacheKeepaliveState; save: (patch: CacheKeepaliveState) => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  if (typeof value.cache_keepalive !== "boolean") return null;
  const commit = async (patch: CacheKeepaliveState) => {
    setBusy(true);
    try { await save(patch); }
    catch (e) { toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error"); }
    finally { setBusy(false); }
  };
  return <Group anchor="cache-keepalive" title={t("settings-cachekeepalive.title")} footer={t("settings-cachekeepalive.hint")}>
    <Row label={t("settings-cachekeepalive.enabled")}>
      <Switch on={value.cache_keepalive} disabled={busy} label={t("settings-cachekeepalive.enabled")} onChange={(on) => void commit({ cache_keepalive: on })} />
    </Row>
    <Row label={t("settings-cachekeepalive.minutes")}>
      <BoundedNumber key={`minutes-${value.cache_keepalive_minutes}`} value={value.cache_keepalive_minutes ?? 55} max={55} disabled={busy || !value.cache_keepalive} label={t("settings-cachekeepalive.minutes")} save={(n) => void commit({ cache_keepalive_minutes: n })} />
    </Row>
    <Row label={t("settings-cachekeepalive.maximum")}>
      <BoundedNumber key={`max-${value.cache_keepalive_max}`} value={value.cache_keepalive_max ?? 24} max={144} disabled={busy || !value.cache_keepalive} label={t("settings-cachekeepalive.maximum")} save={(n) => void commit({ cache_keepalive_max: n })} />
    </Row>
  </Group>;
}
function BoundedNumber({ value, max, disabled, label, save }: { value: number; max: number; disabled: boolean; label: string; save: (n: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  const commit = () => {
    const n = Number(draft);
    if (Number.isInteger(n) && n >= 1 && n <= max && n !== value) save(n);
    else setDraft(String(value));
  };
  return <input className={settingsInputClass} type="number" min={1} max={max} step={1} aria-label={label} value={draft} disabled={disabled}
    onChange={(e) => setDraft(e.target.value)} onBlur={commit} onKeyDown={(e) => { if (e.key === "Enter") e.currentTarget.blur(); }} />;
}
