import { assistantSwitchNeeded } from "./assistant-execution";
// What a Persona chat's model pill says (#5033, docs/web/web.md "Model pill and model panel"; docs/assistant/model-config.md
// "A conversation's own model"): the launcher, model and effort its next message runs on, and where that comes from.
// A Persona chat switches models freely: its own pick is kept on the hub (`model_pick`, feature `assistant_model_pick`)
// and holds from the next message on every device, for every Persona chat (#9248); without one the main chat, a leader
// and the background supervisor follow Model configuration › Main chat and another chat keeps what it started with. On an older hub a pick lasts for this page and goes with each message
// (lib/assistant-chat.ts), as before; the new-conversation page's pick goes with the conversation's start. First-screen
// code: the panel's own data is lib/assistant-panel.ts, loaded with the panel.
import { agentId, sessionAgent } from "./agent-kind";
import { explicitLauncherModel } from "./compatibility";
import { conversationKey, launcherOffered, NEW_CONVERSATION, pickedEffort, pickedFast, pickedLauncher, pickedModel } from "./assistant-chat";
import { modelSupportsFast, type LauncherDefault } from "./launcher-models";
import { providerTitle } from "./provider-names";
import { currentModelRow, pillModel, type AssistantLaunchers, type ModelList, type PillView } from "./session-models";
import type { ModelPick, Session } from "./types";

/** The hub keeps a Persona chat's pick (`model_pick`, PUT / DELETE …/conversations/{id}/model). */
export const ASSISTANT_MODEL_PICK_FEATURE = "assistant_model_pick";

/** The hub accepts a native service tier with a conversation's first message. */
export const ASSISTANT_START_SERVICE_TIER_FEATURE = "assistant_start_service_tier";

/** A Persona chat as its pill sees it. */
export interface AssistantChat {
  /** Absent: the new-conversation page. */
  session?: Session;
  isMain: boolean;
  /** Without a pick, the next message runs on Model configuration › Main chat rather than on what the conversation ran
   *  before: the main conversation, a leader and the background supervisor (hub: sendModel). Absent: the main one only. */
  followsDefault?: boolean;
  /** The default it follows when that is not Main chat's: the background supervisor's own model purpose (`assistant.supervise`,
   *  which follows Main chat until it is set apart). Absent: the one `?purpose=assistant` gave. */
  def?: LauncherDefault | null;
  /** The hub keeps picks (ASSISTANT_MODEL_PICK_FEATURE). */
  kept: boolean;
  /** The assistant's own launcher (AssistantInfo.launcher), for when nothing else says. */
  config?: string;
}

/** Where a chat's setup comes from: its own pick; the default (the main chat, the new-conversation page); or what it
 *  started with (another chat). */
export type SetupSource = "pick" | "default" | "own";

/** What the chat's next message runs on: model absent is the launcher's default entry, effort absent the default's. */
export interface AssistantSetup {
  provider: string;
  model?: string;
  effort?: string;
  /** Fast selected for this start, or acknowledged on the running native thread. */
  fast?: boolean;
  source: SetupSource;
}

/** The key of this page's picks for the chat (an older hub's, the new-conversation page's). */
export const assistantPickKey = (c: Pick<AssistantChat, "session">) => (c.session ? conversationKey(c.session.machine_id, c.session.session_id) : NEW_CONVERSATION);

/** The chat's own pick: `model_pick` on a hub that keeps it, else this page's (an older hub, the new-conversation page). */
export function assistantPick(c: AssistantChat): ModelPick | undefined {
  if (c.session && c.kept) return c.session.model_pick?.launcher ? c.session.model_pick : undefined;
  const key = assistantPickKey(c);
  const launcher = pickedLauncher(key);
  if (!launcher) return undefined;
  const out: ModelPick = { launcher };
  const model = pickedModel(key);
  const effort = pickedEffort(key);
  if (model) out.model = model;
  if (effort) out.effort = effort;
  return out;
}

export interface MainChatAgentMismatch {
  configuredLauncher: string;
  configuredAgent: string;
  actualLauncher: string;
  actualAgent: string;
}

// Model configuration prefixes launcher providers; a session names the same launcher without that prefix.
const canonicalLauncher = (name: string) => name.startsWith("launcher.") ? name.slice("launcher.".length) : name;

/** A confirmed Main-chat-setting mismatch: both launchers are known and their agent kinds differ. An unloaded list,
 *  an unknown default, another launcher of the same kind, or a conversation's own pick changes nothing. */
export function mainChatAgentMismatch(c: AssistantChat, info: AssistantLaunchers | undefined): MainChatAgentMismatch | undefined {
  const s = c.session;
  if (!s || !c.isMain || !s.launcher || !info || assistantPick(c)) return undefined;
  const configuredLauncher = (c.def === undefined ? info.def : c.def)?.provider || c.config;
  if (!configuredLauncher || canonicalLauncher(configuredLauncher) === canonicalLauncher(s.launcher)) return undefined;
  const configured = info.launchers.find((l) => l.name === configuredLauncher)
    ?? info.launchers.find((l) => canonicalLauncher(l.name) === canonicalLauncher(configuredLauncher));
  if (!configured) return undefined;
  const configuredAgent = agentId(configured.agent_type || configured.agent);
  const actualAgent = agentId(sessionAgent(s).agent);
  if (configuredAgent === actualAgent && !assistantSwitchNeeded(s, configured, configuredAgent)) return undefined;
  return { configuredLauncher, configuredAgent, actualLauncher: s.launcher, actualAgent };
}

/** The setup the chat's next message runs on (as the hub decides it, assistant/start.go sendModel). */
export function assistantSetup(c: AssistantChat, info: AssistantLaunchers | undefined): AssistantSetup {
  const s = c.session;
  const pick = assistantPick(c);
  const fast = s?.service_tier === "priority" || (!s && pickedFast());
  // An Antigravity variant pick carries its effort in the new model ID.
  const sessionEffort = (model?: string) => s?.agent === "agy" && model && model !== s.model ? undefined : s?.effort;
  if (pick) return { provider: pick.launcher, model: pick.model, effort: pick.effort ?? sessionEffort(pick.model), ...(fast ? { fast: true } : {}), source: "pick" };
  const mismatch = mainChatAgentMismatch(c, info);
  if (s && mismatch) return { provider: mismatch.actualLauncher, model: s.model, effort: s.effort, ...(fast ? { fast: true } : {}), source: "own" };
  const def = c.def === undefined ? info?.def : c.def;
  if (!s || (c.followsDefault ?? c.isMain)) {
    const provider = def?.provider || c.config || s?.launcher || "claude";
    const model = def?.model || (s?.launcher === provider ? s.model : undefined);
    const effort = sessionEffort(model);
    return { provider, model, effort: c.isMain ? def?.effort || effort : effort ?? (def?.effort || undefined), ...(fast ? { fast: true } : {}), source: "default" };
  }
  // a launcher the list no longer offers is not what the next message runs on: the hub moves the chat to the assistant's
  const own = launcherOffered(info?.launchers ?? null, s.launcher) ? s.launcher : undefined;
  return { provider: own || c.config || "claude", model: own ? s.model : undefined, effort: s.effort, ...(fast ? { fast: true } : {}), source: "own" };
}

/** What the pill draws: the launcher (its name for people), the model as its list names it (none set: its default entry,
 *  else the model it is configured with), the effort, Fast. */
export function assistantPillView(c: AssistantChat, setup: AssistantSetup, info: AssistantLaunchers | undefined, e: ModelList): PillView {
  const launcher = info?.launchers.find((l) => l.name === setup.provider);
  const id = setup.model || explicitLauncherModel(launcher?.model);
  const models = e.list?.models ?? [];
  const row = currentModelRow(models, setup.model) ?? (id !== setup.model ? currentModelRow(models, id) : undefined);
  const out: PillView = {
    ...pillModel(row, setup.model, id),
    engine: agentId(launcher?.agent_type || launcher?.agent || c.session?.agent),
    provider: launcher ? providerTitle(launcher) : setup.provider,
    loading: e.list === undefined && Boolean(e.loading),
    failed: e.list === undefined && Boolean(e.error),
  };
  if (setup.effort) out.effort = setup.effort;
  if (setup.fast && (c.session || modelSupportsFast(row))) out.fast = true;
  return out;
}
