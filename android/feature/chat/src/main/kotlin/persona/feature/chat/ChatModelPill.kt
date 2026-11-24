package persona.feature.chat

import android.content.Context
import android.content.res.Resources
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.SnapshotStateMap
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.serialization.json.*
import persona.core.contracts.ModelPick
import persona.core.contracts.PaseoAgent
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Session
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AssistantLaunchers
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.LauncherModels
import persona.core.contracts.isTraeCliId
import persona.core.contracts.unknownAgentName
import persona.core.designsystem.*
import persona.core.domain.models.AgentModelPanel
import persona.core.domain.models.ModeStep
import persona.core.domain.models.ModelList
import persona.core.domain.models.PanelConfig
import persona.core.domain.models.PanelFavorite
import persona.core.domain.models.PanelNote
import persona.core.domain.models.PanelOverride
import persona.core.domain.models.PanelPrefs
import persona.core.domain.models.PanelReason
import persona.core.domain.models.PanelScope
import persona.core.domain.models.PersonaModelPanel
import persona.core.domain.models.PillSetup
import persona.core.domain.models.PillView
import java.io.IOException

/**
 * What a chat's model pill and panel read (#5055, the web's `lib/session-models.ts`): the computers' launchers and each
 * launcher's /model list, kept for the page's life so a later opening shows them at once and reads them again behind,
 * at most three lists at a time (each may start the agent's CLI on the computer).
 */
internal class ModelLists(private val scope: CoroutineScope, private val errorText: (Exception) -> String) {
    private val reads = Semaphore(3)
    private val lists: SnapshotStateMap<String, ModelList> = mutableStateMapOf()
    private val running = mutableSetOf<String>()
    /** A computer's launchers (`agent.new`), by machine; absent until read. */
    val launchers: SnapshotStateMap<String, List<AgentLauncher>> = mutableStateMapOf()
    /** What a computer runs Persona chats on (`?purpose=assistant`), by machine; absent until read. */
    private val assistantCatalogs = mutableStateMapOf<Pair<String, Boolean>, JLoadable<AssistantLaunchers>>()

    private fun catalogKey(machine: String, main: Boolean) = machine to main
    fun assistantCatalog(machine: String, main: Boolean = false): JLoadable<AssistantLaunchers> =
        assistantCatalogs[catalogKey(machine, main)] ?: JLoadable.Loading

    fun readAssistantCatalog(machine: String, main: Boolean = false, fetch: suspend () -> AssistantLaunchers) {
        val key = catalogKey(machine, main)
        val cached = assistantCatalogs[key]
        if (cached == JLoadable.Loading || cached?.isRefreshing == true) return
        assistantCatalogs[key] = (cached as? JLoadable.Loaded)?.copy(refreshing = true, failure = null) ?: JLoadable.Loading
        scope.launch {
            try {
                assistantCatalogs[key] = JLoadable.Loaded(readAssistantRoute(fetch))
            } catch (cancelled: CancellationException) {
                if (cached == null) assistantCatalogs.remove(key) else assistantCatalogs[key] = cached
                throw cancelled
            } catch (error: Exception) {
                val message = errorText(error)
                assistantCatalogs[key] = if (cached is JLoadable.Loaded) cached.copy(failure = message) else JLoadable.Failed(message)
            }
        }
    }

    fun readAssistantModels(machine: String, launcher: String, again: Boolean, fetch: suspend () -> LauncherModels?) {
        read(machine, launcher, again) { readAssistantRoute(fetch) }
    }

    private suspend fun <T> readAssistantRoute(fetch: suspend () -> T): T = try {
        fetch()
    } catch (cancelled: CancellationException) {
        if (!currentCoroutineContext().isActive) throw cancelled
        // The Assistant API also rejects a response from an old route without cancelling the caller's coroutine.
        throw IOException(cancelled)
    }

    fun list(machine: String, launcher: String): ModelList = lists["$machine/$launcher"] ?: ModelList()

    /** Reads a launcher's list once, or [again] behind the one shown (an opening, Retry). */
    fun read(machine: String, launcher: String, again: Boolean = false, fetch: suspend () -> LauncherModels?) {
        val key = "$machine/$launcher"
        val had = lists[key]
        if (key in running || (had?.read == true && !again)) return
        running += key
        lists[key] = (had ?: ModelList()).copy(loading = true)
        scope.launch {
            try { lists[key] = ModelList(read = true, list = reads.withPermit { fetch() }) }
            catch (cancelled: CancellationException) { throw cancelled }
            // a list already shown stays: a newer one just didn't come
            catch (error: Exception) { lists[key] = if (had?.read == true) had.copy(loading = false) else ModelList(error = errorText(error)) }
            finally { running -= key }
        }
    }

    fun <T> readMachine(into: SnapshotStateMap<String, T>, machine: String, fetch: suspend () -> T, then: () -> Unit = {}) {
        val key = "m:$machine:" + System.identityHashCode(into)
        if (key in running) return
        running += key
        scope.launch {
            try { into[machine] = fetch(); then() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { /* the ones shown stay; with none, each group says it is loading */ }
            finally { running -= key }
        }
    }
}

/**
 * Favorites, Recent and per-model setups of one [scope], kept on this phone (the web keeps them in the browser,
 * `persona.modelPanel.<base>.agent.*`): the hub has no store for such preferences, so they don't follow you to another device.
 */
internal class PanelPrefsStore(context: Context, private val scope: PanelScope) {
    private val preferences = context.applicationContext.getSharedPreferences("persona.modelPanel", Context.MODE_PRIVATE)

    fun read(): PanelPrefs = runCatching { decode(preferences.getString(scope.key, null)) }.getOrNull() ?: PanelPrefs()
    fun write(prefs: PanelPrefs) { preferences.edit().putString(scope.key, encode(prefs).toString()).apply() }

    fun skipsEngineConfirmation(): Boolean = preferences.getBoolean("agent.skipEngineConfirmation", false)
    fun setSkipEngineConfirmation(skip: Boolean) { preferences.edit().putBoolean("agent.skipEngineConfirmation", skip).apply() }

    companion object {
        private fun config(c: PanelConfig) = buildJsonObject {
            put("provider", c.provider); put("model", c.model)
            c.effort?.let { put("effort", it) }
            c.fast?.let { put("fast", it) }
        }
        private fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull
        private fun configOf(o: JsonObject) = PanelConfig(o.text("provider").orEmpty(), o.text("model").orEmpty(), o.text("effort"),
            (o["fast"] as? JsonPrimitive)?.booleanOrNull)

        fun encode(p: PanelPrefs): JsonObject = buildJsonObject {
            put("favorites", JsonArray(p.favorites.map { f -> JsonObject(config(f.setup) + ("uid" to JsonPrimitive(f.uid))) }))
            put("recent", JsonArray(p.recent.map(::config)))
            put("overrides", JsonObject(p.overrides.mapValues { (_, o) -> buildJsonObject { o.effort?.let { put("effort", it) }; o.fast?.let { put("fast", it) } } }))
        }

        fun decode(raw: String?): PanelPrefs {
            // what an older or damaged copy left is no reason to lose the panel: it starts empty
            val o = raw?.let { runCatching { JsonCodecs.json.parseToJsonElement(it) }.getOrNull() as? JsonObject } ?: return PanelPrefs()
            return PanelPrefs(
                favorites = (o["favorites"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }
                    .mapNotNull { f -> f.text("uid")?.let { PanelFavorite(it, configOf(f)) } }.filter { it.setup.provider.isNotEmpty() },
                recent = (o["recent"] as? JsonArray).orEmpty().mapNotNull { (it as? JsonObject)?.let(::configOf) }.filter { it.provider.isNotEmpty() },
                overrides = (o["overrides"] as? JsonObject).orEmpty().mapValues { (_, v) ->
                    val e = v as? JsonObject
                    PanelOverride(e?.text("effort"), (e?.get("fast") as? JsonPrimitive)?.booleanOrNull)
                },
            )
        }
    }
}

private object JsonCodecs { val json = Json { ignoreUnknownKeys = true } }

@Composable
internal fun rememberPanelPrefs(scope: PanelScope): MutableState<PanelPrefs> {
    val context = LocalContext.current
    val store = remember(context, scope) { PanelPrefsStore(context, scope) }
    val state = remember(store) { mutableStateOf(store.read()) }
    LaunchedEffect(store) { snapshotFlow { state.value }.collect { store.write(it) } }
    return state
}

/**
 * An engine's mark (claude, codex…) as the pill and the rows draw it: the design system's own mark where it has one
 * ([knownAgentMark]), else the letter of [name], the name the chat knows the engine by (a Paseo provider's,
 * [AgentModelPanel.engineNames]), or of the engine's id.
 */
internal fun engineMark(engine: String, name: String? = null): AgentMarkSpec {
    val kind = knownAgentMark(engine)
    val title = when {
        isTraeCliId(engine) -> unknownAgentName(PaseoAgent, name, engine)
        kind == AgentMarkKind.Claude -> "Claude Code"
        kind == AgentMarkKind.Codex -> "Codex"
        kind == AgentMarkKind.Trae -> "TRAE"
        else -> name?.takeIf(String::isNotBlank) ?: engine
    }
    return AgentMarkSpec(kind, title, null)
}

/** An effort as the pill says it: Very high is "Extra" there (the web's pill-only word), the rest their names. */
internal fun pillEffort(id: String, res: Resources): String =
    if (id == "xhigh") res.getString(R.string.chat_modelpill_effort_xhigh_short) else SessionModes.effortLabel(id, res) ?: id

/**
 * The panel's words of a chat: why rows are off, the efforts' and engines' names, the waiting switch's note. [names]:
 * what the chat calls the engines that are a provider of their own ([AgentModelPanel.engineNames]).
 */
internal fun panelWords(res: Resources, names: Map<String, String> = emptyMap()): ModelPanelWords {
    fun mark(engine: String) = engineMark(engine, names[engine])
    return ModelPanelWords(
        reason = { reason ->
            when (reason) {
                PanelReason.OtherProvider -> res.getString(R.string.chat_modelpanel_other_provider)
                is PanelReason.OtherEngine -> res.getString(R.string.chat_modelpanel_other_engine, mark(reason.from).name, mark(reason.to).name)
                is PanelReason.ModelFixed -> res.getString(R.string.chat_modelpanel_model_fixed, mark(reason.engine).name)
                PanelReason.Locked -> res.getString(R.string.chat_modelpanel_locked)
                PanelReason.Busy -> res.getString(R.string.chat_modelpanel_busy)
                PanelReason.ProviderGone -> res.getString(R.string.chat_modelpill_provider_gone)
                is PanelReason.Hub -> reason.words
            }
        },
        effort = { SessionModes.effortLabel(it, res) ?: it },
        engine = { mark(it).name },
        note = { note ->
            when (note) {
                is PanelNote.Pending -> res.getString(R.string.chat_modelpanel_pending_note, note.provider)
                PanelNote.Engine -> res.getString(R.string.chat_modelpanel_engine_note)
            }
        },
        mark = { mark(it) },
    )
}

/**
 * The pill's content and spoken words for a setup ([pending]: the next message's, while a switch waits); [names] as in
 * [panelWords].
 */
internal fun pillContent(view: PillView, pending: PillSetup?, ultracode: Boolean, res: Resources,
    names: Map<String, String> = emptyMap()): Pair<ModelPillContent, String> {
    val shown = pending ?: view.setup
    val model = shown.model
    val mark = engineMark(shown.engine, names[shown.engine])
    val handoff = pending != null && pending.engine != view.setup.engine
    val provider = if (handoff) mark.name else shown.provider
    val content = ModelPillContent(provider, model, mark, shown.effort?.let { pillEffort(it, res) },
        ultracode = ultracode && pending == null, pending = pending != null, pendingEngine = handoff,
        loading = res.getString(R.string.chat_modelpill_loading).takeIf { view.loading && pending == null },
        failed = view.failed && pending == null)
    val effort = shown.effort?.let { SessionModes.effortLabel(it, res) ?: it }
    val loading = content.loading
    val spoken = when {
        loading != null -> loading
        content.failed -> res.getString(R.string.chat_modelpill_failed)
        pending != null -> if (effort != null) res.getString(R.string.chat_modelpill_spoken_pending_effort, provider, model.orEmpty(), effort)
            else res.getString(R.string.chat_modelpill_spoken_pending, provider, model.orEmpty())
        effort != null -> res.getString(R.string.chat_modelpill_spoken_effort, model ?: shown.provider, effort)
        else -> res.getString(R.string.chat_modelpill_spoken, model ?: shown.provider)
    }
    return content to spoken
}

/** Immediate local feedback until a fresh authority observes the switch, independent of transcript size. */
internal data class DrawnSwitch(val switch: PendingSwitch?, val observation: Long)

internal fun DrawnSwitch?.over(session: Session, observed: Long): Session =
    if (this == null || observed != observation) session else session.copy(pendingSwitch = switch)

/** The user's next message from the chat carries out a launcher switch waiting on [session] ([on]: where the pill shows it). */
internal fun switchesNext(session: Session?, on: Boolean): Boolean = on && session != null &&
    session.pendingSwitch?.launcher?.let { it.isNotEmpty() && it != AgentModelPanel.launcher(session) } == true

/**
 * An Agent chat's model pill and its panel (#5055, the web's `ModelPill` and `lib/session-panel.ts`; Glass): what runs,
 * or the switch the next message carries out; a tap opens the panel. A pick on the session's own launcher switches its
 * model at once (`POST …/modes {model}` then `{effort}`); on a hub with `launcher_switch` a Claude Code Agent may pick a
 * model of another launcher of its engine, which is recorded for the user's next message ([onSwitch] draws it at once).
 * The flyout's effort of the model that runs applies at once, and ultracode is there too.
 */
@Composable
internal fun AgentModelPill(
    session: Session,
    state: ChatChromeState,
    creation: AgentCreationService?,
    online: Boolean,
    width: ModelPillWidth,
    onSwitch: (DrawnSwitch) -> Unit,
    onOverlay: (Boolean) -> Unit,
    switchObservation: Long = 0L,
    openRequested: Boolean = false,
    onOpenHandled: () -> Unit = {},
    onConfigure: (() -> Unit)? = null,
) {
    val res = LocalResources.current
    val features = LocalHubFeatures.current
    val scope = rememberCoroutineScope()
    val lists = remember(creation) { ModelLists(scope) { operationFeedback(it).text.resolve(res) } }
    val machine = session.machineId
    val own = AgentModelPanel.launcher(session)
    var open by remember { mutableStateOf(false) }
    var picking by remember { mutableStateOf(false) }
    var prefs by rememberPanelPrefs(PanelScope.Agent)
    val context = LocalContext.current.applicationContext
    val panelSettings = remember(context) { PanelPrefsStore(context, PanelScope.Agent) }
    var skipConfirmation by remember(panelSettings) { mutableStateOf(panelSettings.skipsEngineConfirmation()) }
    LaunchedEffect(openRequested) { if (openRequested) { open = true; onOpenHandled() } }
    LaunchedEffect(open) { onOverlay(open) }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    LaunchedEffect(machine, own, creation) { creation?.let { c -> lists.read(machine, own) { c.launcherModels(machine, own) } } }
    // the launcher a waiting switch moves to: its list names the model the next message runs on
    val switchTo = session.pendingSwitch?.launcher?.takeIf { it.isNotEmpty() && it != own }
    LaunchedEffect(machine, switchTo, creation) { val to = switchTo ?: return@LaunchedEffect; creation?.let { c -> lists.read(machine, to) { c.launcherModels(machine, to) } } }
    LaunchedEffect(open, creation, machine) {
        val c = creation ?: return@LaunchedEffect
        if (!open) return@LaunchedEffect
        // each opening reads every list again behind the ones shown, the session's own first
        lists.read(machine, own, again = true) { c.launcherModels(machine, own) }
        lists.readMachine(lists.launchers, machine, { c.launchers(machine) }) {
            lists.launchers[machine].orEmpty().filter { it.name != own }.forEach { l -> lists.read(machine, l.name, again = true) { c.launcherModels(machine, l.name) } }
        }
    }
    val launchers = lists.launchers[machine]
    // A Paseo provider is named and marked as itself, never as Paseo (#8143): its own by its label, the others by their launcher's.
    val names = remember(session.agent, session.agentLabelHint, session.launcher, launchers) { AgentModelPanel.engineNames(session, launchers) }
    val view = AgentModelPanel.pill(session, lists.list(machine, own), launchers)
    val pending = AgentModelPanel.pendingPill(session, { lists.list(machine, it) }, launchers, res.getString(R.string.chat_modelpill_default_model))
    val (content, spoken) = pillContent(view, pending, Ultracode.on(session), res, names)
    val (model, effort) = AgentModelPanel.switches(session)
    val canOpen = model || effort || pending != null || AgentModelPanel.switchesEngine(session, features)
    val editable = online && state.available && canOpen
    JModelPill(content, spoken, if (canOpen) ({ open = true }) else null, Modifier.testTag("model-pill"), width = width, open = open,
        enabled = editable && !picking)
    if (!open) return
    val panel = AgentModelPanel.panel(session, launchers, { lists.list(machine, it) }, features)
    fun step(s: ModeStep) = when (s) {
        is ModeStep.Model -> ChatAction.Modes(model = s.model)
        is ModeStep.Effort -> ChatAction.Modes(effort = s.effort)
        is ModeStep.Launcher -> ChatAction.Modes(launcher = s.launcher, from = s.from, model = s.model, effort = s.effort)
    }
    fun run(steps: List<ModeStep>, done: (Boolean) -> Unit) {
        if (steps.isEmpty()) return done(true)
        if (!state.available) return done(false)
        state.run(session, step(steps.first())) { ok -> if (ok) run(steps.drop(1), done) else done(false) }
    }
    var ultracodeAsk by remember(session.key) { mutableStateOf<Ultracode.Ask?>(null) }
    val latest by rememberUpdatedState(session)
    LaunchedEffect(session, ultracodeAsk) { ultracodeAsk?.let { ask -> if (Ultracode.spent(session, ask)) ultracodeAsk = null } }
    var ask by remember(session.key) { mutableStateOf<Pair<PanelConfig, ModeStep.Launcher>?>(null) }
    fun plan(config: PanelConfig): List<ModeStep>? =
        AgentModelPanel.pickPlan(session, config, { lists.list(machine, it).list }, features, launchers)
    fun apply(config: PanelConfig, steps: List<ModeStep>) {
        picking = true
        run(steps) { ok ->
            picking = false
            if (!ok) return@run
            prefs = prefs.pushRecent(config)
            open = false
            steps.filterIsInstance<ModeStep.Launcher>().firstOrNull()?.let { s ->
                if (s.launcher == s.from) onSwitch(DrawnSwitch(null, switchObservation))
                else {
                    onSwitch(DrawnSwitch(PendingSwitch(s.launcher, s.model, s.effort, agent = s.agent), switchObservation))
                    val target = s.agent?.let { engineMark(it, names[it]).name }
                        ?: launchers.orEmpty().firstOrNull { it.name == s.launcher }?.title ?: s.launcher
                    val models = lists.list(machine, s.launcher).list?.models.orEmpty()
                    val modelName = s.model?.let { id -> models.firstOrNull { it.id == id }?.label ?: AgentModelPanel.modelLabel(id) }
                        ?: AgentModelPanel.defaultModel(models)?.label
                    state.notice = res.getString(R.string.chat_modelpanel_switch_scheduled, listOfNotNull(target, modelName).joinToString(" · "))
                }
            }
        }
    }
    JModelPanel(panel.providers, panel.state, prefs, { prefs = it }, remember(res, names) { panelWords(res, names) },
        onPick = { config ->
            if (editable && !picking) {
                plan(config)?.let { steps ->
                    val switch = steps.filterIsInstance<ModeStep.Launcher>().singleOrNull()
                    if (!skipConfirmation && switch != null && AgentModelPanel.needsEngineConfirmation(session, switch)) ask = config to switch
                    else apply(config, steps)
                }
            }
        },
        onDismiss = { open = false }, picking = picking,
        header = {
            if (pending != null && pending.engine != view.setup.engine) {
                JTextButton(stringResource(R.string.chat_engine_switch_cancel_pending),
                    onClick = {
                        picking = true
                        run(listOf(ModeStep.Launcher(own, own))) { ok ->
                            picking = false
                            if (ok) onSwitch(DrawnSwitch(null, switchObservation))
                        }
                    }, modifier = Modifier.padding(horizontal = Tokens.Space.m),
                    enabled = editable && !picking && AgentModelPanel.busy(session) == null)
            }
            if (skipConfirmation && AgentModelPanel.switchesEngine(session, features)) {
                JTextButton(stringResource(R.string.chat_engine_switch_ask_again), onClick = {
                    panelSettings.setSkipEngineConfirmation(false)
                    skipConfirmation = false
                }, modifier = Modifier.padding(horizontal = Tokens.Space.m))
            }
        },
        onApplyCurrent = if (effort && editable) ({ e -> state.run(session, ChatAction.Modes(effort = e)) }) else null,
        currentExtra = if (Ultracode.offered(session, features)) ({
            UltracodeSwitchRow(session, ultracodeAsk, editable) { on ->
                ultracodeAsk = Ultracode.Ask(on)
                state.run(session, ChatAction.Modes(ultracode = on)) { ok -> ultracodeAsk = if (ok) Ultracode.Ask(on, sending = false, answered = latest) else null }
            }
        }) else null,
        onReload = { provider -> creation?.let { c -> lists.read(machine, provider, again = true) { c.launcherModels(machine, provider) } } },
        onConfigure = onConfigure)
    ask?.let { (config, requested) ->
        val steps = plan(config)?.takeIf { it.singleOrNull() == requested }
        EngineSwitchConfirmation(
            onDismiss = { ask = null },
            confirmEnabled = editable && !picking && steps != null,
            onConfirm = { skip ->
                ask = null
                if (editable && !picking && steps != null) {
                    if (skip) {
                        panelSettings.setSkipEngineConfirmation(true)
                        skipConfirmation = true
                    }
                    apply(config, steps)
                }
            },
        )
    }
}

@Composable
internal fun EngineSwitchConfirmation(onDismiss: () -> Unit, confirmEnabled: Boolean = true, onConfirm: (Boolean) -> Unit) {
    var skip by remember { mutableStateOf(false) }
    JDialog(
        onDismiss = onDismiss,
        title = stringResource(R.string.chat_engine_switch_title),
        text = stringResource(R.string.chat_engine_switch_explanation),
        confirm = stringResource(R.string.chat_engine_switch_confirm) to { onConfirm(skip) },
        dismiss = stringResource(R.string.chat_engine_switch_keep) to onDismiss,
        confirmEnabled = confirmEnabled,
    ) {
        JRow(stringResource(R.string.chat_engine_switch_skip),
            trailing = { JCheckbox(skip, null) }, onClick = { skip = !skip }, role = Role.Checkbox, titleLines = 2)
    }
}

/**
 * A pick this page drew for a Persona chat ([pick] null: dropped) until the session's next push ([rev] the row's
 * revision when it was drawn) says what the hub kept.
 */
internal data class DrawnPick(val pick: ModelPick?, val rev: Long?)

/**
 * A Persona chat's model pill and its panel (#5055, the web's `AssistantModelPill` and `AssistantModelPanel` from #5033;
 * Glass): what the chat's next message runs on and, in the panel's strip, where that comes from. On a hub with
 * `assistant_model_pick` a pick is kept for the conversation (`PUT …/model`, drawn at once, put back with the hub's
 * words if it says no) and Follow default / Undo pick drops it (`DELETE`); on an older hub the pick lives on this page
 * ([pagePick]) and goes with each message. [session] null: the new-chat page, whose pick goes with the start.
 */
@Composable
fun PersonaModelPill(
    session: Session?,
    machineId: String,
    management: AssistantManagement?,
    width: ModelPillWidth,
    pagePick: ModelPick?,
    onPagePick: (ModelPick?) -> Unit,
    onNotice: (String) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    onOverlay: (Boolean) -> Unit = {},
    onConfigure: (() -> Unit)? = null,
    /** An effort on an older hub, which keeps it on the session (`POST …/modes`); null: not offered. */
    onEffort: ((String) -> Unit)? = null,
    /** A one-off request from this page's model-route remedy to open this same picker. */
    openRequested: Boolean = false,
    onOpenHandled: () -> Unit = {},
    /** A one-off request to follow the default, using the same optimistic clear as the panel's strip. */
    clearRequested: Boolean = false,
    onClearHandled: () -> Unit = {},
) {
    val res = LocalResources.current
    val features = LocalHubFeatures.current
    val scope = rememberCoroutineScope()
    val lists = remember(management) { ModelLists(scope) { operationFeedback(it).text.resolve(res) } }
    var open by remember { mutableStateOf(false) }
    var prefs by rememberPanelPrefs(PanelScope.Assistant)
    var config by remember(management) { mutableStateOf<persona.core.contracts.api.AssistantConfig?>(null) }
    var drawn by remember(session?.key) { mutableStateOf<DrawnPick?>(null) }
    LaunchedEffect(open) { onOverlay(open) }
    LaunchedEffect(openRequested) {
        if (openRequested) { open = true; onOpenHandled() }
    }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    LaunchedEffect(management) {
        try { config = management?.config() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
    }
    val loadedConfig = config
    val isMain = session != null && loadedConfig?.main == session.sessionId
    fun readCatalog(refresh: Boolean = false) {
        if (session != null && loadedConfig == null) return // Wait until the conversation's role is known.
        val api = management ?: return
        if (machineId.isNotBlank()) lists.readAssistantCatalog(machineId, main = isMain) {
            if (isMain) api.mainAssistantLauncherList(machineId, refresh) else api.assistantLauncherList(machineId, refresh)
        }
    }
    fun readModels(provider: String, again: Boolean = false) {
        val api = management ?: return
        if (machineId.isNotBlank()) lists.readAssistantModels(machineId, provider, again) { api.launcherModels(machineId, provider) }
    }
    LaunchedEffect(management, machineId, session?.rev, isMain) {
        // Hub pushes can change the effective Main chat model even while the panel is closed (#6542).
        readCatalog()
    }
    LaunchedEffect(management, machineId, open, isMain) {
        if (open) {
            val previous = lists.assistantCatalog(machineId, main = isMain)
            readCatalog(refresh = previous is JLoadable.Failed || previous is JLoadable.Loaded && previous.failure != null)
        }
    }
    val kept = PersonaModelPanel.MODEL_PICK in features
    val shownSession = session?.let { s -> drawn?.takeIf { it.rev == s.rev }?.let { s.copy(modelPick = it.pick) } ?: s }
    val chat = PersonaModelPanel.Chat(shownSession, isMain = isMain, kept = kept,
        config = config?.launcher, pagePick = pagePick)
    // Only the main conversation has a per-conversation model pick (#6911): a leader, the background supervisor or
    // another assistant conversation draws no pill and follows the default or what it started with. The new-chat page
    // (session null) keeps its starter pick, which goes with the main conversation's start. A model-route remedy may
    // still open this chat's existing panel; once it closes (and after its clear request is handled), the non-main chat
    // goes back to drawing no pill.
    if (session != null && !chat.isMain && !open && !clearRequested) return
    val catalog = lists.assistantCatalog(machineId, main = isMain)
    val info = (catalog as? JLoadable.Loaded)?.value
    val setup = PersonaModelPanel.setup(chat, info)
    LaunchedEffect(management, machineId, setup.provider) {
        readModels(setup.provider)
    }
    LaunchedEffect(open, management, machineId, info) {
        if (open) info?.launchers.orEmpty().forEach { readModels(it.name, again = true) }
    }
    val view = PersonaModelPanel.pill(chat, setup, info, lists.list(machineId, setup.provider))
    val (content, spoken) = pillContent(view, null, false, res)
    JModelPill(content, spoken, if (management != null) ({ open = true }) else null, modifier.testTag("model-pill"), width = width, open = open,
        enabled = enabled)
    fun keep(pick: ModelPick?, after: () -> Unit = {}) {
        val s = session
        if (s == null || !kept) { onPagePick(pick); after(); return }
        val before = drawn
        drawn = DrawnPick(pick, s.rev)
        scope.launch {
            try {
                if (pick == null) management?.clearModelPick(s.key) else management?.setModelPick(s.key, pick)
                after()
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { drawn = before; onNotice(operationFeedback(error).text.resolve(res)) }
        }
    }
    LaunchedEffect(clearRequested) {
        if (clearRequested) { keep(null); onClearHandled() }
    }
    if (!open) return
    val running = session?.agent?.isNotBlank() == true
    val panel = PersonaModelPanel.panel(setup, info, { lists.list(machineId, it) }, typed = PersonaModelPanel.TYPED_MODELS in features,
        engine = session?.let(AgentModelPanel::engine) ?: "claude", running = running)
    val named = listOfNotNull(info?.launchers?.firstOrNull { it.name == setup.provider }?.title ?: setup.provider,
        panel.providers.firstOrNull { it.id == setup.provider }?.models?.firstOrNull { it.id == panel.state.current?.model }?.name ?: view.setup.model)
        .joinToString(" · ")
    JModelPanel(panel.providers, panel.state, prefs, { prefs = it }, remember(res) { panelWords(res) },
        onPick = { picked ->
            val pick = PersonaModelPanel.pickOf(picked, lists.list(machineId, picked.provider).list, panel.state.current)
            if (pick == null) { open = false; return@JModelPanel }
            prefs = prefs.pushRecent(picked)
            keep(pick)
            // an older hub keeps the effort on the session (what the effort chip did)
            if ((!kept || session == null) && pick.effort != null && pick.effort != session?.effort) onEffort?.invoke(pick.effort!!)
            open = false
        },
        onDismiss = { open = false },
        header = { PickStrip(chat, setup, named) { keep(null) } },
        onApplyCurrent = panel.state.current?.let { now -> { effort: String ->
            PersonaModelPanel.pickOf(now.copy(effort = effort), lists.list(machineId, now.provider).list, null)?.let { pick ->
                if (session != null && kept) keep(pick) else { onPagePick(pick); onEffort?.invoke(effort) }
            }
            Unit
        } },
        onReload = { readModels(it, again = true) },
        onConfigure = onConfigure,
        catalog = when {
            machineId.isBlank() -> JLoadable.Loaded(Unit)
            catalog is JLoadable.Failed -> catalog.copy(title = stringResource(R.string.chat_conversation_models_failed))
            else -> catalog
        },
        onRetryCatalog = if (machineId.isNotBlank()) ({ readCatalog(refresh = true) }) else null)
}

/** The strip on top of a Persona chat's panel (the web's `PickStrip`): where the setup comes from and, for a pick, the way back. */
@Composable
private fun PickStrip(chat: PersonaModelPanel.Chat, setup: PersonaModelPanel.Setup, named: String, onBack: () -> Unit) {
    val palette = PersonaTheme.palette
    val text = when {
        chat.session == null -> stringResource(R.string.chat_modelpill_header_new, named)
        setup.source == PersonaModelPanel.Source.Pick ->
            stringResource(if (chat.kept) R.string.chat_modelpill_header_picked else R.string.chat_modelpill_header_picked_page, named)
        setup.source == PersonaModelPanel.Source.Default -> stringResource(R.string.chat_modelpill_header_default, named)
        else -> stringResource(R.string.chat_modelpill_header_own, named)
    }
    // the main chat and the new one go back to the default; another chat's pick is undone (it then keeps what it runs)
    val back = stringResource(if (chat.session != null && !chat.isMain) R.string.chat_modelpill_undo_pick else R.string.chat_modelpill_follow_default)
    Column(Modifier.fillMaxWidth().testTag("pick-strip")) {
        Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(text, Modifier.weight(1f), style = PersonaTheme.type.micro, color = palette.ink2)
            if (setup.source == PersonaModelPanel.Source.Pick) JTextButton(back, onBack)
        }
        JDivider()
    }
}
