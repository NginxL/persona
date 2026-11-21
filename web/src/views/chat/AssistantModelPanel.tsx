// The model panel of a Persona chat (#5033, docs/web/web.md "Model pill and model panel"), loaded on its pill's first tap
// (ModelPill.tsx AssistantModelPill): the shared panel (ui/model-panel, scope "assistant") fed with the assistant
// computer's providers (lib/assistant-panel.ts), a strip on top saying whether the chat follows the default or has its
// own pick, with the way back. On a hub that keeps picks a pick is PUT for the conversation (it holds from the next
// message, on every device), drawn at once and put back with the hub's words if it says no; following the default again
// is a DELETE. An older hub: the pick lasts for this page and goes with each message, as before. The new-conversation
// page: the pick goes with the conversation's start.
import { useEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, ApiError, errorText, sessionPath } from "../../lib/api";
import { assistantPickKey, assistantSetup, ASSISTANT_START_SERVICE_TIER_FEATURE, type AssistantChat, type AssistantSetup } from "../../lib/assistant-models";
import { assistantPanel, assistantPickOf } from "../../lib/assistant-panel";
import { pickEffort, pickFast, pickLauncher, pickModel } from "../../lib/assistant-chat";
import { conversationModelPath, dropConversationPick } from "../../lib/assistant-pick";
import { modelSupportsFast } from "../../lib/launcher-models";
import { useChosenManager } from "../../lib/manager-choice";
import type { PanelConfig } from "../../lib/model-panel";
import { usePanelQuotas } from "../../lib/panel-quotas";
import { TYPED_MODELS_FEATURE } from "../../lib/model-id";
import { providerTitle } from "../../lib/provider-names";
import { speedStep } from "../../lib/session-panel";
import { assistantLaunchers, forgetModel, modelList, readModels, useModelLists } from "../../lib/session-models";
import { getSession, hubHas, refreshAssistant, upsertSession, useStore } from "../../lib/store";
import type { ModelPick, Session } from "../../lib/types";
import { Button, Dialog, toast } from "../../ui/atoms";
import { ModelPanel } from "../../ui/model-panel/ModelPanel";
import { ASSISTANT_SEGMENTS_FEATURE, assistantSwitchNeeded, conversationSwitchState, prepareConversationSwitch, submitConversationSwitch, type ConversationSwitchRequest } from "../../lib/conversation-switch";
import { setServiceTier } from "./SessionDialogs";

/** PUT …/model {launcher, model?, effort?, machine_id}: kept for the conversation. Drawn at once; put back, with the hub's
 *  words (out of the computer's scope, gone, another engine than the chat runs, can't run assistant chats…), if it says
 *  no. */
async function keepPick(s: Session, pick: ModelPick): Promise<boolean> {
  const before = s.model_pick;
  upsertSession({ ...s, model_pick: pick });
  try {
    await api(conversationModelPath(s), { method: "PUT", json: { ...pick, machine_id: s.machine_id } });
    return true;
  } catch (e) {
    upsertSession({ ...(getSession(s.machine_id, s.session_id) ?? s), model_pick: before });
    toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}

/** POST …/modes {effort} on an older hub (what the effort chip did): drawn at once, put back if it fails. */
async function setEffort(s: Session, effort: string): Promise<boolean> {
  const before = s.effort;
  upsertSession({ ...s, effort });
  try {
    await api(`${sessionPath(s.machine_id, s.session_id)}/modes`, { method: "POST", json: { effort } });
    return true;
  } catch (e) {
    upsertSession({ ...(getSession(s.machine_id, s.session_id) ?? s), effort: before });
    toast(t("chat-sessiondialogs.change_failed", { error: errorText(e) }), "error");
    return false;
  }
}

/** The strip on top: where the setup comes from and, for a pick, the way back. */
export function PickStrip({ chat, setup, named, onBack }: { chat: AssistantChat; setup: AssistantSetup; named: string; onBack?: () => void }) {
  const text = !chat.session
    ? t("chat-modelpill.header_new", { setup: named })
    : setup.source === "pick"
      ? t(chat.kept ? "chat-modelpill.header_picked" : "chat-modelpill.header_picked_page", { setup: named })
      : setup.source === "default"
        ? t("chat-modelpill.header_default", { setup: named })
        : t("chat-modelpill.header_own", { setup: named });
  // the main chat and the new one go back to the default; another chat's pick is undone (it then keeps what it runs)
  const back = chat.session && !(chat.followsDefault ?? chat.isMain) ? t("chat-modelpill.undo_pick") : t("chat-modelpill.follow_default");
  return (
    <div data-pick-strip={setup.source} className="flex min-w-0 items-baseline gap-2 border-b border-line px-3.5 py-2 text-[12px] leading-[1.45] text-ink-2">
      <span className="min-w-0 flex-1 break-words">{text}</span>
      {setup.source === "pick" && onBack && (
        <button type="button" onClick={onBack} className="shrink-0 font-medium whitespace-nowrap text-accent-ink hover:underline">
          {back}
        </button>
      )}
    </div>
  );
}

export function AssistantModelPanel({ chat, mid, anchor, onClose }: { chat: AssistantChat; mid: string; anchor: HTMLElement; onClose: () => void }) {
  const version = useModelLists();
  const canSwitch = useStore((s) => hubHas(s.features, ASSISTANT_SEGMENTS_FEATURE));
  const [switchPick, setSwitchPick] = useState<ModelPick | null>(null);
  const [switchError, setSwitchError] = useState("");
  const [switching, setSwitching] = useState(false);
  const [switchUncertain, setSwitchUncertain] = useState(false);
  const switchFlight = useRef(false);
  const switchRequest = useRef<ConversationSwitchRequest | null>(null);
  const typed = useStore((s) => hubHas(s.features, TYPED_MODELS_FEATURE));
  const chosen = useChosenManager();
  const startServiceTier = useStore((s) => hubHas(s.features, ASSISTANT_START_SERVICE_TIER_FEATURE)) && !chosen;
  const { info } = assistantLaunchers(mid);
  const setup = assistantSetup(chat, info);
  const engine = chat.session?.agent || "claude";
  const [supervisionRequired, setSupervisionRequired] = useState(false);
  const roleMachine = chat.session?.machine_id;
  const roleSession = chat.session?.session_id;
  useEffect(() => {
    let current = true;
    if (!canSwitch || !roleMachine || !roleSession) return;
    void conversationSwitchState({ machine_id: roleMachine, session_id: roleSession }).then((snapshot) => {
      if (current) setSupervisionRequired(Boolean(snapshot.supervision_required));
    }).catch(() => {});
    return () => { current = false; };
  }, [canSwitch, roleMachine, roleSession]);
  // a chat that runs keeps its engine; one whose agent isn't known is refused nothing on a guess, as on the hub
  const running = Boolean(chat.session?.agent);
  const fast = Boolean(chat.session?.service_tiers?.some((x) => x.id === "priority"));
  const fastOn = Boolean(setup.fast);
  const { providers, state } = useMemo(
    () => assistantPanel(setup, info, (name) => modelList(mid, name, "assistant"), { typed, engine, running, switching: Boolean(canSwitch && chat.kept && chat.session), supervision: supervisionRequired, starting: !chat.session && startServiceTier, fast, fastOn }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `version` stands for what was read
    [setup.provider, setup.model, setup.effort, setup.fast, info, mid, typed, engine, running, fast, fastOn, startServiceTier, canSwitch, supervisionRequired, version],
  );
  const latest = () => (chat.session ? (getSession(chat.session.machine_id, chat.session.session_id) ?? chat.session) : undefined);
  const key = assistantPickKey(chat);

  // the page's own pick (an older hub, the new-conversation page): launcher, then its model, then the effort
  const keepHere = (pick: ModelPick) => {
    pickLauncher(key, pick.launcher);
    pickModel(key, pick.model);
    if (!chat.session) pickEffort(key, pick.effort);
  };
  const apply = async (pick: ModelPick): Promise<boolean> => {
    const s = latest();
    const provider = providers.find((p) => p.id === pick.launcher);
    const target = info?.launchers.find((l) => l.name === pick.launcher);
    if (s && chat.kept && canSwitch && provider && target && assistantSwitchNeeded(s, target, provider.engine)) {
      setSwitchPick(pick);
      switchRequest.current = null;
      setSwitchError("");
      setSwitchUncertain(false);
      return false; // Confirmation, not a model pick, owns the explicit operation.
    }
    if (s && chat.kept) return keepPick(s, pick);
    keepHere(pick);
    // an older hub keeps the effort on the session (what the effort chip did)
    if (s && pick.effort && pick.effort !== s.effort) return setEffort(s, pick.effort);
    return true;
  };
  const onPick = async (config: PanelConfig) => {
    const list = modelList(mid, config.provider, "assistant").list;
    const pick = assistantPickOf(config, list, state.current);
    if (pick) {
      if (!(await apply(pick))) return false;
    } else if (config.provider !== state.current?.provider || config.model !== state.current.model) {
      return false;
    }
    if (!chat.session) {
      const row = list?.models.find((m) => m.id === config.model);
      const fast = Boolean(startServiceTier && config.fast && modelSupportsFast(row));
      if (!pick && fast) {
        // A Recent or Favorite can differ from the shown setup only by Fast. Pin every displayed start option anyway:
        // a cleared main must not revive its carried launcher, model or effort just because assistantPickOf saw no model edit.
        const pin = assistantPickOf(config, list, null);
        if (!pin) return false;
        keepHere(pin);
      }
      pickFast(fast);
    }
    return true;
  };
  const onApplyCurrent = async (patch: { effort?: string; fast?: boolean }) => {
    const s = latest();
    const now = state.current;
    if (!now || (patch.effort === undefined && patch.fast === undefined)) return false;
    let effortApplied = false;
    if (patch.fast !== undefined) {
      if (!s) {
        const list = modelList(mid, now.provider, "assistant").list;
        const row = list?.models.find((m) => m.id === now.model);
        if (patch.fast && (!startServiceTier || !modelSupportsFast(row))) return false;
        if (patch.fast) {
          // Pin every displayed start option: a cleared main must not revive its carried launcher, model or effort.
          const pick = assistantPickOf({ ...now, effort: patch.effort ?? now.effort }, list, null);
          if (!pick) return false;
          keepHere(pick);
          effortApplied = patch.effort !== undefined;
        }
        pickFast(patch.fast);
      } else {
        const speed = speedStep(s, patch.fast, true);
        if (!(speed && "service_tier" in speed && (await setServiceTier(s, speed.service_tier)))) return false;
      }
    }
    if (patch.effort === undefined || effortApplied) return true;
    const pick = assistantPickOf({ ...now, effort: patch.effort }, modelList(mid, now.provider, "assistant").list, null);
    return pick ? apply(pick) : false;
  };
  const back = () => {
    const s = latest();
    if (s && chat.kept) void dropConversationPick(s);
    else {
      pickLauncher(key, undefined);
      pickEffort(key, undefined);
      pickFast(false);
    }
  };
  const onReload = (provider: string) => void readModels(mid, provider, { purpose: "assistant", again: true, first: provider === setup.provider });
  const onForget = (provider: string, model: string) => {
    void forgetModel(mid, provider, model, "assistant").then((error) => error && toast(error, "error"));
  };
  const launcher = info?.launchers.find((l) => l.name === setup.provider);
  const row = providers.find((p) => p.id === setup.provider)?.models?.find((m) => m.id === state.current?.model);
  const named = [launcher ? providerTitle(launcher) : setup.provider, row?.name].filter(Boolean).join(" · ");

  const quotas = usePanelQuotas(mid, providers, info?.launchers);

  const confirmSwitch = async () => {
    const session = latest();
    if (!session || !switchPick || switchFlight.current) return;
    switchFlight.current = true;
    setSwitching(true);
    setSwitchError("");
    let submitted = false;
    try {
      switchRequest.current ??= await prepareConversationSwitch(session, switchPick);
      submitted = true;
      await submitConversationSwitch(session, switchRequest.current);
      setSwitchPick(null);
      toast(t("chat-modelpanel.switch_requested"));
      void refreshAssistant().catch(() => {});
      onClose();
    } catch (error) {
      const uncertain = submitted && !(error instanceof ApiError && error.body.delivered === "none");
      setSwitchUncertain(uncertain);
      setSwitchError(uncertain ? t("chat-modelpanel.switch_unconfirmed", { error: errorText(error) }) : errorText(error));
    }
    finally { switchFlight.current = false; setSwitching(false); }
  };
  const cancelSwitch = () => { if (!switchFlight.current) { setSwitchPick(null); switchRequest.current = null; } };

  return ( <>
    <ModelPanel
      anchor={anchor}
      held={Boolean(switchPick)}
      onClose={onClose}
      providers={providers}
      state={state}
      scope="assistant"
      quotas={quotas}
      onPick={onPick}
      onApplyCurrent={onApplyCurrent}
      onReload={onReload}
      onForget={onForget}
      header={<PickStrip chat={chat} setup={setup} named={named} onBack={back} />}
    />
    <Dialog open={Boolean(switchPick)} title={t("chat-modelpanel.switch_title")} onClose={cancelSwitch}
      footer={<><Button disabled={switching} onClick={cancelSwitch}>{t(switchUncertain ? "common.close" : "common.cancel")}</Button><Button disabled={switching} busy={switching} onClick={() => void confirmSwitch()}>{t("chat-modelpanel.switch_confirm")}</Button></>}>
      <p>{t("chat-modelpanel.switch_explanation", { name: providers.find((p) => p.id === switchPick?.launcher)?.name ?? switchPick?.launcher ?? "" })}</p>
      {switchError && <p role="alert" className="text-red">{switchError}</p>}
    </Dialog>
  </> );
}
