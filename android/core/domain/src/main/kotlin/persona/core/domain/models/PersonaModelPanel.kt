package persona.core.domain.models

import persona.core.contracts.ModelPick
import persona.core.contracts.Session
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.AssistantLaunchers
import persona.core.contracts.api.LauncherModels
import persona.core.contracts.api.launcherExplicitModel

/**
 * The model pill and panel of a Persona chat (#5055, the web's `lib/assistant-models.ts` and `lib/assistant-panel.ts`
 * from #5033; docs/assistant/model-config.md "A conversation's own model"). A Persona chat switches models freely: its
 * own pick is kept on the hub (`model_pick`, [MODEL_PICK]) and holds from the next message on every device; without one
 * the main chat follows Model configuration › Main chat and another chat keeps what it started with. The providers are
 * what the assistant's computer lists for the main chat, then what can't run it there, off with the hub's reason. A chat
 * that already runs keeps its engine. Every pickable provider switches the same way, from the next message, so
 * Recommended is the provider the chat runs on now.
 */
object PersonaModelPanel {
    /** The hub keeps a Persona chat's pick (`PUT` / `DELETE …/conversations/{id}/model`). */
    const val MODEL_PICK = "assistant_model_pick"
    /** The hub takes a model id typed by hand for a Persona chat. */
    const val TYPED_MODELS = "typed_models"

    /** Where a chat's setup comes from: its own pick; the default (the main chat, the new-chat page); or what it started with. */
    enum class Source { Pick, Default, Own }

    /** What the chat's next message runs on: [model] null is the launcher's default entry, [effort] null the default's. */
    data class Setup(val provider: String, val model: String? = null, val effort: String? = null, val source: Source)

    /** A Persona chat as its pill sees it: [session] null on the new-chat page; [kept] the hub has [MODEL_PICK]. */
    data class Chat(val session: Session?, val isMain: Boolean, val kept: Boolean, val config: String? = null, val pagePick: ModelPick? = null)

    /** The chat's own pick: `model_pick` on a hub that keeps it, else this page's (an older hub, the new-chat page). */
    fun pick(chat: Chat): ModelPick? {
        val s = chat.session
        if (s != null && chat.kept) return s.modelPick?.takeIf { it.launcher.isNotEmpty() }
        return chat.pagePick?.takeIf { it.launcher.isNotEmpty() }
    }

    /** The setup the chat's next message runs on, as the hub decides it (assistant/start.go `sendModel`). */
    fun setup(chat: Chat, info: AssistantLaunchers?): Setup {
        val s = chat.session
        pick(chat)?.let { return Setup(it.launcher, it.model?.takeIf(String::isNotEmpty), it.effort?.takeIf(String::isNotEmpty) ?: s?.effort, Source.Pick) }
        val def = info?.default
        if (s == null || chat.isMain) {
            val provider = def?.provider?.takeIf(String::isNotEmpty) ?: chat.config?.takeIf(String::isNotEmpty) ?: s?.launcher?.takeIf(String::isNotEmpty) ?: "claude"
            return Setup(provider, def?.model?.takeIf(String::isNotEmpty) ?: s?.model?.takeIf { s.launcher == provider },
                (if (chat.isMain) def?.effort?.takeIf(String::isNotEmpty) ?: s?.effort else s?.effort ?: def?.effort?.takeIf(String::isNotEmpty)), Source.Default)
        }
        // a launcher the list no longer offers is not what the next message runs on: the hub moves the chat to the assistant's
        val own = s.launcher?.takeIf { l -> l.isNotEmpty() && (info == null || info.launchers.any { it.name == l }) }
        return Setup(own ?: chat.config?.takeIf(String::isNotEmpty) ?: "claude", if (own != null) s.model else null, s.effort, Source.Own)
    }

    private fun engineOf(l: AgentLauncher) = AgentModelPanel.engineId(l.agentType)

    private fun panelModel(m: AgentModel, engine: String) = PanelModel(
        id = m.id, name = m.label, description = m.description?.takeIf(String::isNotBlank), resolved = m.resolved?.takeIf(String::isNotBlank),
        aliases = m.aliases,
        efforts = m.efforts.ifEmpty { if (m.custom && engine == "claude") AgentModelPanel.CLAUDE_EFFORTS else emptyList() },
        defaultEffort = m.defaultEffort?.takeIf(String::isNotBlank), custom = m.custom, providerDefault = m.isDefault,
    )

    /**
     * The panel's providers and state for a chat running [setup]: [info] the computer's launchers for the main chat (null
     * before they are read), [listOf] each launcher's list as read; [typed] the hub takes typed ids; [engine] the engine
     * the chat runs, [running] it already runs on it (another engine's providers are off; not on the new-chat page).
     */
    fun panel(setup: Setup, info: AssistantLaunchers?, listFor: (String) -> ModelList, typed: Boolean, engine: String = "claude", running: Boolean = false): PanelData {
        val runs = AgentModelPanel.engineId(engine)
        val launchers = info?.launchers.orEmpty()
        val providers = launchers.map { l ->
            val kind = engineOf(l)
            val e = listFor(l.name)
            val configuredModel = launcherExplicitModel(l)
            var models: List<PanelModel>? = null
            if (e.read) {
                var list = e.list?.models.orEmpty().map { panelModel(it, kind) }
                // a launcher that fixes its own --model takes no model with a pick: what it runs by default, only
                if (e.list?.locked != null) list = list.filter { it.id == AgentModelPanel.defaultModel(e.list.models)?.id }
                // no list to read (a direct API the computer can't ask, an older hub): the model it is set up with
                if (list.isEmpty() && configuredModel != null)
                    list = listOf(PanelModel(configuredModel, AgentModelPanel.modelLabel(configuredModel), efforts = if (kind == "claude") AgentModelPanel.CLAUDE_EFFORTS else emptyList()))
                models = list
            }
            // the provider it runs on stays as it is, whatever its kind: it is what the panel says the chat runs
            val unavailable = if (running && kind != runs && l.name != setup.provider) PanelReason.OtherEngine(runs, kind) else null
            PanelProvider(l.name, l.title, kind, models, loading = !e.read && (e.loading || e.error == null),
                error = e.error?.takeIf { !e.read && !e.loading },
                unavailable = unavailable, typable = unavailable == null && typed && AgentModelPanel.typedModelOffered(kind, e.list))
        }.toMutableList()
        for (u in info?.unavailable.orEmpty()) {
            if (providers.any { it.id == u.name }) continue
            providers += PanelProvider(u.name, u.title, AgentModelPanel.engineId(u.agent), null, unavailable = PanelReason.Hub(u.reason))
        }
        // If neither hub list mentions the current provider, keep the legacy Claude fallback or show the missing pick.
        if (info != null && providers.none { it.id == setup.provider }) {
            val claude = setup.provider == "claude"
            val e = listFor(setup.provider)
            val models = when {
                e.read -> e.list?.models.orEmpty().map { panelModel(it, runs) }
                claude -> null
                else -> listOfNotNull(setup.model?.let { PanelModel(it, AgentModelPanel.modelLabel(it)) })
            }
            providers.add(0, PanelProvider(setup.provider, setup.provider, runs, models,
                loading = claude && !e.read && (e.loading || e.error == null),
                error = e.error?.takeIf { claude && !e.read && !e.loading }, unavailable = if (claude) null else PanelReason.ProviderGone))
        }
        val own = providers.firstOrNull { it.id == setup.provider }
        val row = if (own?.models != null) AgentModelPanel.currentModelRow(listFor(setup.provider).list?.models.orEmpty(), setup.model) else null
        val model = row?.id ?: setup.model ?: launchers.firstOrNull { it.name == setup.provider }?.let(::launcherExplicitModel)
        val state = PanelState(
            current = model?.takeIf(String::isNotEmpty)?.let { PanelConfig(setup.provider, it, setup.effort).clean() },
            recommended = if (own != null && own.unavailable == null) listOf(setup.provider) else emptyList(),
            starting = false,
        )
        return PanelData(providers, state)
    }

    /**
     * What a pick of [config] keeps for the chat: the launcher, the model unless it is the launcher's default entry, the
     * effort when the setup has one. null: nothing to keep (an id the hub would refuse, or the setup the chat runs now).
     */
    fun pickOf(config: PanelConfig, list: LauncherModels?, current: PanelConfig?): ModelPick? {
        val row = list?.models?.firstOrNull { it.id == config.model }
        if (row == null && ModelPanel.typedModelId(config.model) == null) return null
        if (current != null && current.provider == config.provider && current.model == config.model && current.effort.orEmpty() == config.effort.orEmpty()) return null
        return ModelPick(config.provider, config.model.takeUnless { row?.isDefault == true }, config.effort?.takeIf(String::isNotEmpty))
    }

    /** What the pill draws: the launcher as people read it, the model as its list names it (none set: its default entry, else the model it is set up with), the effort. */
    fun pill(chat: Chat, setup: Setup, info: AssistantLaunchers?, own: ModelList): PillView {
        val launcher = info?.launchers?.firstOrNull { it.name == setup.provider }
        val row = AgentModelPanel.currentModelRow(own.list?.models.orEmpty(), setup.model)
        val id = setup.model ?: launcher?.let(::launcherExplicitModel)
        val model = row?.label ?: id?.takeIf(String::isNotEmpty)?.let(AgentModelPanel::modelLabel)
        val engine = launcher?.let(::engineOf) ?: chat.session?.let(AgentModelPanel::engine) ?: "claude"
        return PillView(PillSetup(engine, launcher?.title ?: setup.provider, model, setup.effort),
            loading = !own.read && own.loading && model == null, failed = !own.read && own.error != null && model == null)
    }
}
