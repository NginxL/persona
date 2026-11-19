import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { formatDate, t } from "../../i18n";
import { credentialOwner } from "../../lib/api";
import { BOTMUX_HIDDEN_KEY, withoutBotmux } from "../../lib/agent-filter";
import { storedValue } from "../../lib/compatibility";
import { agentId, sessionAgent, sessionAgentName } from "../../lib/agent-kind";
import { terminalResume, resumeInTerminal, useResume } from "../../lib/agent-resume";
import { useScope, useScoped, useScopedMachines } from "../../lib/current-assistant";
import { sessionTitle } from "../../lib/format";
import { machineWithHub } from "../../lib/mesh";
import { navigate } from "../../lib/router";
import { runtimeIdentity } from "../../lib/runtime-list";
import { RUNTIME_SEARCH_FEATURE, RuntimeSearchModel } from "../../lib/runtime-search";
import { historyGroups, historySessions } from "../../lib/session-history";
import { retryFullAgentSessions, useFullAgentSessions } from "../../lib/sessions";
import { onHubEvent, rememberSessionRow, useStore } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { Button, useNow } from "../../ui/atoms";
import { SearchField } from "../../ui/SearchField";
import { WindowedRows } from "../WindowedRows";
import { WorkSessionRow } from "./WorkSessionBrowser";

const botmuxHidden = () => storedValue(BOTMUX_HIDDEN_KEY) === "1";
const watchBotmux = (changed: () => void) => {
  window.addEventListener("storage", changed);
  window.addEventListener("persona-botmux-visibility", changed);
  return () => { window.removeEventListener("storage", changed); window.removeEventListener("persona-botmux-visibility", changed); };
};
const copy = (key: string) => t(`session-history.${key}`);
const selectClass = "min-h-8 min-w-0 flex-1 rounded-control bg-inset px-2 py-1 text-xs text-ink shadow-hairline focus-visible:outline-accent [@media(pointer:coarse)]:min-h-11";

function HistoryRow({ session, machines, active, now }: { session: Session; machines: Machine[]; active: boolean; now: number }) {
  const machine = machines.find(m => m.id === session.machine_id);
  const resume = useResume(session.machine_id, session.session_id);
  const available = terminalResume(session, machine) === "available" && machine?.online;
  return <div data-history-row={session.session_id}>
    <div className="flex min-w-0 items-center gap-1">
      <div className="min-w-0 flex-1"><WorkSessionRow session={{ ...session, harness_name: sessionAgentName(session) }} name={machineWithHub(session.machine_id, machines)} active={active} now={now} depth={0}
        onSelect={() => { rememberSessionRow(session); navigate({ name: "session", mid: session.machine_id, sid: session.session_id, work: true }); }} /></div>
      {available && <Button tone="quiet" busy={resume.phase === "resuming"} aria-label={t("session-history.resume_named", { title: sessionTitle(session) })}
        onClick={() => { rememberSessionRow(session); void resumeInTerminal(session.machine_id, session.session_id, () => navigate({ name: "session", mid: session.machine_id, sid: session.session_id, work: true, view: "term" })); }}>
        {t("session-terminalpanel.resume")}
      </Button>}
    </div>
    {resume.phase === "failed" && <p role="alert" className="px-2 text-xs text-red">{resume.error}</p>}
  </div>;
}

/** The complete sidebar snapshot owns visibility/state; content search uses the existing frozen search window. */
export function HistoryBrowser({ activeSession }: { activeSession?: { machineId: string; sessionId: string } }) {
  const full = useFullAgentSessions();
  const scoped = useScoped(full.sessions);
  const hideBotmux = useSyncExternalStore(watchBotmux, botmuxHidden, () => false);
  const machines = useScopedMachines();
  const scope = useScope();
  const features = useStore(s => s.features);
  const [query, setQuery] = useState("");
  const [machine, setMachine] = useState("");
  const [agent, setAgent] = useState("");
  const owner = credentialOwner();
  const model = useMemo(() => new RuntimeSearchModel(), [owner]);
  const search = useSyncExternalStore(model.subscribe, model.snapshot, model.snapshot);
  const capable = features.includes(RUNTIME_SEARCH_FEATURE);
  useEffect(() => { model.search(capable ? query : "", { manager: scope.several ? scope.id : undefined }); }, [model, query, capable, scope.several, scope.id]);
  useEffect(() => () => model.dispose(), [model]);
  useEffect(() => {
    if (!query.trim() || !capable) return;
    // Reconcile a resumed/removed row and fresh search evidence after the same list hint as the sidebar.
    const refresh = () => { void model.retry(); };
    const off = onHubEvent("sessions_list", refresh);
    const hello = onHubEvent("hello", refresh);
    return () => { off(); hello(); };
  }, [model, query, capable]);
  const now = useNow();
  const allHistory = useMemo(() => historySessions(withoutBotmux(scoped, hideBotmux)), [scoped, hideBotmux]);
  const current = search.query === query.trim();
  const rows = useMemo(() => {
    let source = scoped;
    if (query.trim()) {
      if (capable) {
        const live = new Map(scoped.map(s => [runtimeIdentity(s), s]));
        source = current ? search.hits.flatMap(hit => { const row = live.get(runtimeIdentity(hit.session)); return row ? [row] : full.loaded ? [] : [hit.session]; }).filter(s => scope.has(s.manager)) : [];
      } else {
        // Older hubs expose title/preview search only; say so rather than claiming a complete conversation search.
        const text = query.trim().toLocaleLowerCase();
        source = scoped.filter(s => [sessionTitle(s), s.last_text, s.activity].some(value => value?.toLocaleLowerCase().includes(text)));
      }
    }
    return historySessions(withoutBotmux(source, hideBotmux), { machine, agent });
  }, [scoped, query, capable, current, search.hits, scope, machine, agent, full.loaded, hideBotmux]);
  const groups = useMemo(() => historyGroups(rows), [rows]);
  const agents = new Map(allHistory.map(s => [agentId(sessionAgent(s).agent), sessionAgentName(s)]));
  const loading = !full.loaded || Boolean(query.trim() && capable && (!current || search.loading));
  const error = full.error || (query.trim() && capable && current ? search.error || search.moreError : "");
  return <section aria-label={copy("title")} data-history-browser className="@container/work-sessions min-w-0">
    <div className="space-y-2 px-2 py-2">
      <SearchField value={query} onChange={setQuery} placeholder={copy("search")} />
      <div className="flex flex-wrap gap-2">
        <select aria-label={copy("computer")} className={selectClass} value={machine} onChange={e => setMachine(e.target.value)}>
          <option value="">{copy("all_computers")}</option>
          {machines.map(m => <option key={m.id} value={m.id}>{m.name}</option>)}
        </select>
        <select aria-label={copy("agent")} className={selectClass} value={agent} onChange={e => setAgent(e.target.value)}>
          <option value="">{copy("all_agents")}</option>
          {[...new Set([...agents.keys(), ...(agent ? [agent] : [])])].map(id => <option key={id} value={id}>{agents.get(id) || agent}</option>)}
        </select>
      </div>
      {!capable && query.trim() && <p className="text-xs text-ink-3">{copy("older_search")}</p>}
    </div>
    {loading && <p role="status" className="px-2 py-2 text-xs text-ink-3">{t("sidebar.loading")}</p>}
    {error && <div role="alert" className="px-2 py-2 text-xs text-red">{error} <Button tone="quiet" onClick={() => { retryFullAgentSessions(); void model.retry(); }}>{t("sidebar.retry")}</Button></div>}
    {groups.map(group => <section key={group.key} data-history-day={group.key}>
      <h2 className="flex items-baseline gap-2 bg-inset px-2 py-1.5 text-xs font-medium text-ink-2">
        <span className="min-w-0 flex-1">{group.date ? formatDate(group.date, { dateStyle: "full" }) : copy("earlier")}</span>
        <span data-history-count className="tabular-nums">{group.sessions.length}</span>
      </h2>
      <WindowedRows items={group.sessions} holds={s => activeSession?.machineId === s.machine_id && activeSession.sessionId === s.session_id}
        render={s => <HistoryRow key={runtimeIdentity(s)} session={s} machines={machines} now={now} active={activeSession?.machineId === s.machine_id && activeSession.sessionId === s.session_id} />} />
    </section>)}
    {!loading && !error && !rows.length && <p className="px-2 py-2 text-xs text-ink-3">{query || machine || agent ? t("sidebar.no_match") : copy("empty")}</p>}
    {query.trim() && capable && current && search.hasMore && <div className="px-2 py-2"><Button busy={search.loadingMore} onClick={() => void model.more()}>{t("paged-agents.more")}</Button></div>}
  </section>;
}
