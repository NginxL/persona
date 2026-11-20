import { useId } from "react";
import { formatDate, t } from "../../i18n";
import type { CandidateDraft, CandidateMode, ModelCandidates } from "../../lib/model-candidates";
import type { ModelProvider } from "../../lib/model-config";
import { Button, IconButton } from "../../ui/atoms";
import { Group, settingsInputClass } from "./SettingsParts";

const fieldClass = `${settingsInputClass} pointer-coarse:min-h-11`;

const modeLabels: Record<CandidateMode, string> = {
  extension: "settings-modelconfiguration.candidate_extension",
  off: "settings-modelconfiguration.candidate_off",
  custom: "settings-modelconfiguration.candidate_custom",
};

export function ExtensionCandidates({ setting, draft, providers, busy, problem, onChange }: {
  setting: ModelCandidates; draft: CandidateDraft; providers: ModelProvider[]; busy: boolean; problem: string;
  onChange: (value: CandidateDraft) => void;
}) {
  const id = useId();
  const rows = draft.custom;
  const newRow = () => ({ key: Math.max(-1, ...rows.map((r) => r.key)) + 1, provider: providers.length === 1 ? providers[0].id : "", model: "" });
  const update = (custom: CandidateDraft["custom"]) => onChange({ ...draft, custom });
  const move = (index: number, step: number) => {
    const copy = [...rows];
    [copy[index], copy[index + step]] = [copy[index + step], copy[index]];
    update(copy);
  };
  const setMode = (mode: CandidateMode) => onChange({ mode, custom: mode === "custom" && !rows.length && providers.length && setting.most > 0 ? [newRow()] : rows });

  return <Group title={t("settings-modelconfiguration.candidate_title")} anchor="extension-candidates">
    <fieldset disabled={busy} className="min-w-0 space-y-3 p-3">
      <select aria-label={t("settings-modelconfiguration.candidate_title")} className={fieldClass} value={draft.mode}
        onChange={(e) => setMode(e.target.value as CandidateMode)}>
        {setting.modes.filter((mode) => modeLabels[mode]).map((mode) => <option key={mode} value={mode}>{t(modeLabels[mode])}</option>)}
      </select>
      {draft.mode === "custom" && <>
        <p className="text-label text-ink-2">{t("settings-modelconfiguration.candidate_order_hint")}</p>
        <ol className="space-y-2">
          {rows.map((row, i) => {
            const index = i + 1;
            const provider = providers.find((p) => p.id === row.provider);
            const patch = (change: Partial<Pick<typeof row, "model" | "provider">>) => update(rows.map((r) => r.key === row.key ? { ...r, ...change } : r));
            const list = `${id}-models-${row.key}`;
            const saved = setting.custom.find((c) => c.provider === row.provider && (c.configured_model ?? c.model) === row.model);
            return <li key={row.key} className="min-w-0 rounded-control bg-inset px-3 pb-3">
              <div className="flex items-center gap-1">
                <span className="min-w-0 flex-1 text-label font-medium text-ink-2">{t("settings-modelconfiguration.candidate_number", { index })}</span>
                <IconButton icon="arrowUp" label={t("settings-modelconfiguration.candidate_up", { index })} size={44} disabled={busy || i === 0} onClick={() => move(i, -1)} />
                <IconButton icon="arrowDown" label={t("settings-modelconfiguration.candidate_down", { index })} size={44} disabled={busy || index === rows.length} onClick={() => move(i, 1)} />
                <IconButton icon="trash" label={t("settings-modelconfiguration.candidate_remove", { index })} size={44} disabled={busy} onClick={() => update(rows.filter((r) => r.key !== row.key))} />
              </div>
              <div className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2">
                <label className="min-w-0 text-label text-ink-2">{t("settings-modelconfiguration.provider")}
                  <select className={fieldClass} aria-label={t("settings-modelconfiguration.candidate_provider", { index })} value={row.provider} onChange={(e) => patch({ provider: e.target.value, model: "" })}>
                    <option value="">{t("settings-modelconfiguration.candidate_select")}</option>
                    {row.provider && !provider && <option value={row.provider} disabled>{saved?.provider_name || row.provider}</option>}
                    {providers.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                  </select>
                </label>
                <label className="min-w-0 text-label text-ink-2">{t("settings-modelconfiguration.model")}
                  <input className={fieldClass} aria-label={t("settings-modelconfiguration.candidate_model", { index })} spellCheck={false} autoComplete="off" list={list}
                    value={row.model} placeholder={t("settings-modelconfiguration.provider_default")} onChange={(e) => patch({ model: e.target.value })} />
                  <datalist id={list}>{provider?.models?.map((m) => <option key={m.id} value={m.id}>{m.name}</option>)}</datalist>
                </label>
              </div>
              {saved && !saved.available && <p className="mt-1 break-words text-label text-attention">{saved.problem || t("settings-modelconfiguration.candidate_unavailable")}</p>}
            </li>;
          })}
        </ol>
        <Button icon="plus" className="pointer-coarse:min-h-11" disabled={busy || rows.length >= setting.most || !providers.length} onClick={() => update([...rows, newRow()])}>{t("settings-modelconfiguration.candidate_add")}</Button>
        {problem && <p role="status" className="break-words text-label text-attention">{problem}</p>}
      </>}
    </fieldset>
    {setting.requested.length ? <details className="border-t border-line px-3">
      <summary className="min-h-11 content-center cursor-pointer text-label font-medium text-ink">{t("settings-modelconfiguration.candidate_requested", { count: setting.requested.length })}</summary>
      <div className="divide-y divide-line">{setting.requested.map((chain) => <section key={JSON.stringify(chain.models.map((c) => c.model))} className="space-y-1.5 py-2.5">
        <p className="text-label text-ink-3"><time dateTime={chain.last_used}>{t("settings-modelconfiguration.candidate_last", { time: formatDate(chain.last_used, { dateStyle: "medium", timeStyle: "short" }) })}</time></p>
        <ol className="list-decimal space-y-1.5 pl-5 text-label text-ink-2">{chain.models.map((c, i) => <li key={i} className="break-words [overflow-wrap:anywhere]">
          <span className="font-medium text-ink">{c.model}</span>
          <div>{c.available ? c.provider_name || c.provider : t("settings-modelconfiguration.candidate_unavailable")}{!c.available && c.problem && <> · {c.problem}</>}</div>
        </li>)}</ol>
      </section>)}</div>
    </details> : <p className="border-t border-line px-3 py-2 text-label text-ink-3">{t("settings-modelconfiguration.candidate_no_requests")}</p>}
  </Group>;
}
