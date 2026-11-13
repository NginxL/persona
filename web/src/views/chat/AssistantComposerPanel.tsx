// Keep execution-catalog I/O and skill presentation off the base panel's idle path.
import { useCallback, useEffect, useState, useSyncExternalStore, type ComponentProps } from "react";
import { api, enc, credentialOwner, subscribeCredentialOwner } from "../../lib/api";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import { matchingSkills, type SkillCatalog } from "../../lib/skill-mentions";
import { ComposerPanel, type MentionRow } from "./ComposerPanel";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";

type SkillCatalogState = { catalog?: SkillCatalog; status?: "loading" | "error" | "partial" };

export function AssistantComposerPanel(props: ComponentProps<typeof ComposerPanel>) {
  const source = props.source?.skills;
  const machine = props.machineId;
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, () => "");
  const key = JSON.stringify([owner, machine, props.source?.cwd, source?.manager, source?.session, source?.launcher, source?.environment, source?.agent]);
  const [state, setState] = useState<SkillCatalogState & { key: string }>({ key: "" });
  useEffect(() => {
    if (!source || !machine) return;
    const ac = new AbortController();
    const query = new URLSearchParams({ machine_id: machine });
    if (source.session) query.set("session_id", source.session);
    if (source.launcher) query.set("launcher", source.launcher);
    const path = source.agent ? `/sessions/${enc(machine)}/${enc(source.session ?? "")}/skills` : `/assistant/skills?${query}`;
    api<SkillCatalog>(path, { signal: ac.signal, headers: { [MANAGER_HEADER]: source.manager } })
      .then((catalog) => {
        if (!ac.signal.aborted) setState(catalog.catalog_version === 1 ? { key, catalog, status: catalog.complete ? undefined : "partial" } : { key, status: "error" });
      })
      .catch(() => { if (!ac.signal.aborted) setState({ key, status: "error" }); });
    return () => ac.abort();
    // Each request primitive is in key; keystrokes deliberately are not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const current: SkillCatalogState = !machine ? { status: "error" } : state.key === key ? state : { status: "loading" };
  const rows = useCallback((query: string): MentionRow[] => matchingSkills(current.catalog?.skills ?? [], query).map((skill) => ({ kind: "skill", id: `skill:${skill.id}`, skill })), [current.catalog]);
  return (
    <ComposerPanel {...props} skills={{
      rows,
      loading: current.status === "loading",
      status: current.status && (
        <div role="status" className="mt-1.5 shrink-0 border-t border-line px-[10px] pt-2 pb-1 text-xs text-ink-3">{t(`chat-composerpanel.skills_${current.status}`)}</div>
      ),
      row: (skill, common) => (
        <button key={skill.id} {...common} title={`${skill.name} · ${skill.source}\n${skill.description ?? ""}`} className="relative z-10 flex min-h-14 w-full items-center gap-2 rounded-[8px] px-[10px] py-2 text-left text-sm text-ink">
          <Icon name="spark" size={16} className="shrink-0 text-ink-3" />
          <span className="min-w-0 flex-1">
            <span className="flex items-baseline gap-2">
              <span className="truncate font-medium">{skill.name}</span>
              <span className="min-w-0 truncate text-xs text-ink-3">{skill.source}</span>
            </span>
            <span className="block truncate text-xs text-ink-3">{skill.description}</span>
          </span>
        </button>
      ),
    }} />
  );
}
