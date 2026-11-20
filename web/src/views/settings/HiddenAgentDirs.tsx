// Settings › Agents › Agent folders: per computer, folders whose Agent sessions (every runtime) are blocked (that
// computer stops collecting them) or hidden (recorded, left out of Agents and the assistant's view). The parallel list
// to Claude Code's Blocked config folders (ClaudeDirs.tsx, further down this page): same per-computer rules and pattern
// syntax, and each rule has an action and can be switched off. A new rule blocks unless Hide is chosen (#10053). Each
// change is one PUT of the computer's whole list.
import { useEffect, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import { useScopedMachines } from "../../lib/current-assistant";
import { machineWithHub } from "../../lib/mesh";
import { composing } from "../../lib/send-history";
import { t } from "../../i18n";
import { Button, Loading } from "../../ui/atoms";
import { Group, Row, RowFailed, SettingsHint, Switch, settingsInputClass } from "./SettingsParts";

export type AgentDirAction = "block" | "hide";
/** A rule from a hub before blocking has no action: it hides. */
export interface HiddenAgentDirRule { pattern: string; enabled: boolean; action?: AgentDirAction }
export const ruleAction = (r: HiddenAgentDirRule): AgentDirAction => r.action ?? "hide";
export interface HiddenAgentDirsState { rules: HiddenAgentDirRule[]; home?: string }

/** The pattern as saved: trimmed, without trailing slashes, but never less than one character ("//" is "/", not blank). */
const cleanPattern = (input: string) => input.trim().replace(/(.)\/+$/, "$1");

/** Why a typed folder cannot be added ("" = it can): the server's rules, said before the round trip. */
export function hiddenPatternError(input: string, rules: HiddenAgentDirRule[]): string {
  const p = cleanPattern(input);
  if (!p) return "";
  // the home folder, the root, or a pattern matching every folder (as the hub refuses them)
  if (p === "~" || /^\/*\**$/.test(p)) return t("hidden-agent-dirs.pattern_root");
  if (!p.startsWith("/") && !p.startsWith("~/") && !p.startsWith("*")) return t("hidden-agent-dirs.pattern_prefix");
  if (rules.some((r) => r.pattern === p)) return t("hidden-agent-dirs.pattern_exists");
  return "";
}

export function HiddenAgentDirs() {
  const machines = useScopedMachines();
  const [mid, setMid] = useState("");
  const machine = machines.some((m) => m.id === mid) ? mid : machines[0]?.id ?? "";
  const [state, setState] = useState<HiddenAgentDirsState | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const saving = useRef(false);
  const [input, setInput] = useState("");
  const [action, setAction] = useState<AgentDirAction>("block");
  const [reload, setReload] = useState(0);
  const endpoint = `/machines/${enc(machine)}/agent-dirs/hidden`;
  // Each load (another computer, Retry) starts a new view: a save still out from the last one no longer belongs to it,
  // so its answer or rollback is dropped and it does not keep this view locked.
  const view = useRef(0);
  useEffect(() => {
    if (!machine) return;
    let dead = false;
    view.current++;
    saving.current = false;
    setBusy(false);
    setState(null);
    setError("");
    api<HiddenAgentDirsState>(endpoint).then((v) => { if (!dead) setState({ rules: v.rules ?? [], home: v.home }); }).catch((e) => { if (!dead) setError(errorText(e)); });
    return () => { dead = true; };
  }, [endpoint, machine, reload]);
  // Every save sends the whole list: a second one sent before the first lands would be built from the old list.
  const save = async (rules: HiddenAgentDirRule[]) => {
    if (!state || saving.current) return false;
    const previous = state;
    const started = view.current;
    const current = () => started === view.current;
    saving.current = true;
    setBusy(true);
    setError("");
    setState({ ...state, rules });
    try {
      const v = await api<HiddenAgentDirsState>(endpoint, { method: "PUT", json: { rules } });
      if (!current()) return false;
      setState({ rules: v.rules ?? [], home: v.home });
      return true;
    } catch (e) {
      if (!current()) return false;
      setState(previous);
      setError(errorText(e));
      return false;
    } finally { if (current()) { saving.current = false; setBusy(false); } }
  };
  const typed = state ? hiddenPatternError(input, state.rules) : "";
  const add = async () => {
    const pattern = cleanPattern(input);
    if (!state || !pattern || typed || saving.current) return;
    if (await save([...state.rules, { pattern, enabled: true, action }])) setInput("");
  };
  if (!machines.length) return null;
  return <Group title={t("hidden-agent-dirs.title")} footer={t("hidden-agent-dirs.footer")}>
    <Row label={t("hidden-agent-dirs.machine")}>
      <select aria-label={t("hidden-agent-dirs.machine")} value={machine} onChange={(e) => setMid(e.target.value)} disabled={busy} className={settingsInputClass}>
        {machines.map((m) => <option key={m.id} value={m.id}>{machineWithHub(m.id, machines)}</option>)}
      </select>
    </Row>
    {error && <RowFailed text={t("hidden-agent-dirs.failed", { error })} retry={t("hidden-agent-dirs.retry")} onRetry={() => setReload((n) => n + 1)} />}
    {!state ? (!error && <Row label={<Loading />} />) : <>
      {state.rules.map((rule, i) => <Row key={rule.pattern} label={rule.pattern} mono hint={t(!rule.enabled ? "hidden-agent-dirs.disabled" : ruleAction(rule) === "block" ? "hidden-agent-dirs.enabled_block" : "hidden-agent-dirs.enabled")}>
        <ActionPicker label={`${t("hidden-agent-dirs.action")}: ${rule.pattern}`} value={ruleAction(rule)} disabled={busy} onChange={(a) => void save(state.rules.map((r, n) => n === i ? { ...r, action: a } : r))} />
        <Switch label={rule.pattern} on={rule.enabled} disabled={busy} onChange={(enabled) => void save(state.rules.map((r, n) => n === i ? { ...r, enabled } : r))} />
        <Button tone="quiet" disabled={busy} onClick={() => void save(state.rules.filter((_, n) => n !== i))}>{t("hidden-agent-dirs.remove")}</Button>
      </Row>)}
      {state.rules.length === 0 && <SettingsHint>{t("hidden-agent-dirs.empty")}</SettingsHint>}
      <Row label={t("hidden-agent-dirs.pattern")} hint={input && typed ? typed : undefined} stackOnNarrow>
        <ActionPicker label={t("hidden-agent-dirs.action")} value={action} disabled={busy} onChange={setAction} />
        <input className={settingsInputClass} aria-label={t("hidden-agent-dirs.pattern")} placeholder={t("hidden-agent-dirs.placeholder")} value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && !composing(e.nativeEvent)) void add(); }} spellCheck={false} autoComplete="off" />
        <Button tone="secondary" disabled={busy || !input.trim() || !!typed} busy={busy} onClick={() => void add()}>{t("hidden-agent-dirs.add")}</Button>
      </Row>
    </>}
  </Group>;
}

function ActionPicker({ label, value, disabled, onChange }: { label: string; value: AgentDirAction; disabled: boolean; onChange: (a: AgentDirAction) => void }) {
  return <select aria-label={label} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value as AgentDirAction)} className={settingsInputClass}>
    <option value="block">{t("hidden-agent-dirs.action_block")}</option>
    <option value="hide">{t("hidden-agent-dirs.action_hide")}</option>
  </select>;
}
