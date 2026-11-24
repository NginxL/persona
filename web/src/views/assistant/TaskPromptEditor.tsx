// The chat Composer in prompt-editing mode. The server resolves draft execution through TaskExecution.
import { useEffect, useId, useMemo, useState, useSyncExternalStore } from "react";
import { api, credentialOwner, errorText, subscribeCredentialOwner } from "../../lib/api";
import { MANAGER_HEADER, useShownManager } from "../../lib/manager-choice";
import { taskInputSettings, type Task, type TaskExecution } from "../../lib/routines";
import { splitSkillDraft, type SkillCatalog } from "../../lib/skill-mentions";
import { contextXML } from "../../lib/context-xml";
import { AGENT_COMMANDS, Composer } from "../chat/Composer";
import { WORKFLOWS_COMMAND } from "../../lib/background-panel";
import { PERMISSIONS_COMMAND } from "../../lib/permissions-command";
import type { MentionSource } from "../chat/ComposerPanel";
import { t } from "../../i18n";

interface InputContext { execution: TaskExecution; task_input?: boolean; input_error?: string; catalog?: SkillCatalog; skill_error?: string; skill_scope?: string }
const commands = AGENT_COMMANDS.filter((command) => !command.agent && command.word !== WORKFLOWS_COMMAND && command.word !== PERMISSIONS_COMMAND);
const fileScope = (ex: TaskExecution) => ({ machine: ex.machine_id, cwd: ex.cwd ?? "", launcher: ex.launcher, runtime: ex.runtime ?? "" });
// Older Claude sessions omit the agent in the session protocol.
const agentOf = (ex: TaskExecution) => ex.agent || "claude";
const scopeOf = ({ execution: ex, skill_scope }: InputContext) =>
  JSON.stringify([fileScope(ex), agentOf(ex), ex.model, ex.effort, ex.fingerprint, skill_scope]);

export function TaskPromptEditor({ draft, saved, disabled, onChange }: { draft: Task; saved: Task | null; disabled: boolean; onChange: (value: string, hasSelections: boolean) => void }) {
  const id = useId();
  const manager = useShownManager();
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner, () => "");
  const request = JSON.stringify({ id: saved?.id, ...taskInputSettings(draft, saved) });
  const key = JSON.stringify([owner, manager, request]);
  const [state, setState] = useState<{ key: string; context?: InputContext; error?: string }>({ key: "" });
  useEffect(() => {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      api<InputContext>("/assistant/tasks/input-context", { method: "POST", json: JSON.parse(request), signal: controller.signal, headers: { [MANAGER_HEADER]: manager } })
        .then((context) => { if (!controller.signal.aborted) setState({ key, context }); })
        .catch((error) => { if (!controller.signal.aborted) setState({ key, error: errorText(error) }); });
    }, 150);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [key, request, manager]);
  const current = state.key === key ? state : undefined;
  const context = current?.context;
  const ex = context?.execution;
  const scope = context ? scopeOf(context) : "";
  const { text, skills: refs } = useMemo(() => draft.prompt_selections ? splitSkillDraft(draft.prompt, true) : { text: draft.prompt, skills: [] }, [draft.prompt, draft.prompt_selections]);
  const invalidFile = ex && refs.some((ref) => ref.kind === "file" && Object.entries(fileScope(ex)).some(([field, value]) => (ref.attrs?.[field] ?? "") !== value));
  const inputError = current?.error || current?.context?.input_error;
  const supported = ex && context?.task_input === true && ["claude", "codex"].includes(agentOf(ex));
  const mention = useMemo<MentionSource | undefined>(() => ex && supported ? {
    cwd: ex.cwd, files: () => [], sessions: false,
    referenceFiles: fileScope(ex),
    fileManager: manager,
    allowsFileQuery: (query) => !/^(?:[~/]|[A-Za-z]:)/.test(query) && !query.includes("\\") && !query.split("/").includes(".."),
    skills: { manager, launcher: ex.launcher, environment: JSON.stringify([owner, manager, scope]), catalog: async (signal) => {
      const reply = await api<InputContext>("/assistant/tasks/input-context", { method: "POST", json: { ...JSON.parse(request), catalog: true }, signal, headers: { [MANAGER_HEADER]: manager } });
      if (reply.task_input !== true) {
        if (!signal.aborted) setState((previous) => previous.key === key ? { key, context: reply } : previous);
        throw new Error(reply.input_error || t("assistant-taskeditor.input_unsupported"));
      }
      if (scopeOf(reply) !== scope || !reply.catalog) throw new Error(reply.skill_error || t("chat-composer.skills_changed"));
      return reply.catalog;
    } },
  } : undefined, [ex, supported, key, manager, owner, request, scope]);
  return (
    <div>
      <fieldset disabled={disabled} className="min-w-0">
        <Composer
          draftKey={`task-prompt:${id}`}
          placeholder={t("assistant-taskeditor.prompt_placeholder")}
          editor={{
            text, refs, label: t("assistant-taskeditor.section_prompt"),
            onChange: (body, selected) => onChange(selected.length ? `${selected.map(contextXML).join("\n")}\n\n${body}` : body, selected.length > 0),
          }}
          disabled={disabled}
          machineId={ex?.machine_id}
          mention={mention}
          commands={commands}
        />
      </fieldset>
      <p className="mt-1 text-submeta leading-relaxed text-ink-3">{t("assistant-taskeditor.input_hint")}</p>
      <p role={inputError || invalidFile ? "alert" : "status"} className="mt-1 text-submeta text-ink-3">
        {inputError || (invalidFile ? t("assistant-taskeditor.input_changed") : ex && !supported ? t("assistant-taskeditor.input_unsupported") : !current ? t("assistant-taskeditor.input_loading") : "")}
      </p>
    </div>
  );
}
