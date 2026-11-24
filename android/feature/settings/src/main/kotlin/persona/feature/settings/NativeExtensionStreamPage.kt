package persona.feature.settings

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.*
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.generated.ExtensionComponent
import persona.core.contracts.stream.*
import persona.core.designsystem.*

private const val CloseTimeoutMillis = 5000L

/** One adapter per authenticated navigation identity. The entry owns final close;
 * renderer remounts only suspend, retaining bounded drafts, scroll and local review selections. */
class NativeExtensionStreamAdapter(val session: NativeStreamSession, shown: Boolean) {
    var shown by mutableStateOf(shown)
    internal val local = NativeExtensionLocal()
    internal var permission by mutableStateOf<persona.core.contracts.Interaction?>(null)
    internal var permissionBusy by mutableStateOf(false)
    internal var decidePermission: ((persona.core.contracts.Interaction, Boolean) -> Unit)? = null
    internal val targets = mutableMapOf<String, Pair<NativeStreamTree, (persona.core.contracts.api.ExtensionUiResult?) -> Unit>>()
    init {
        require(session.components.all { it in Components })
        session.rendererProfile(NativeExtensionProfile.check)
        local.discardStepper = session::discardDraft
        session.onDiscard { failed ->
            targets.remove(failed.eventId)?.second?.invoke(null)
            local.rejected = true
        }
    }
    /** Navigation owner calls this on final removal, not a transient renderer remount. */
    suspend fun close() { permission = null; permissionBusy = false; session.close(); local.clear(); targets.clear() }
    /** Authenticated owner supplies a definitive failure; transport uncertainty must never call this. */
    suspend fun rejected(source: NativeStreamIdentity, requestId: String) {
        val failed = session.rejected(source, requestId) ?: return
        targets.remove(failed.eventId)?.second?.invoke(null)
        local.rejected = true
    }
    companion object {
        // The hub downgrades these newer component types to rows/images for this native client. They are never
        // accepted into a native stream directly; keep that contract in one place for every new schema type.
        private val HubDowngradedComponents = setOf("html", "graph", "scene")
        val Components: Set<String> = ExtensionComponent.TYPES.filterNot { it in HubDowngradedComponents }.toSet()
    }
}

private data class NativeStreamConfirmation(
    val key: String, val item: ExtensionViewItem, val action: ExtensionViewAction,
    val value: JsonObject, val tree: NativeStreamTree, val event: String,
    val draft: NativeStreamDraft?, val drafts: Map<String, NativeStreamDraft>, val commit: () -> Unit,
    val done: ((persona.core.contracts.api.ExtensionUiResult?) -> Unit)?,
)
private data class NativeStreamChoice(val item: ExtensionViewItem, val tree: NativeStreamTree)

/** Display conversion only. Raw optional presence and generated models remain canonical in core/contracts. */
internal fun nativeExtensionDisplay(state: NativeStreamState): ExtensionViewState {
    val tree = state.tree ?: return ExtensionViewState(loading = state.phase != NativeStreamPhase.Closed)
    val view = JsonCodec.decodeFromJsonElement(ExtensionView.serializer(), tree.viewRaw())
    val display = view.copy(sections = view.sections.map { section -> section.copy(items = section.items.map { item ->
        if (item.type in setOf("field", "composer", "segmented")) item.copy(name = item.key) else item
    }) })
    val values = tree.nodes.mapNotNull { (key, node) ->
        if (node["type"]?.jsonPrimitive?.content !in setOf("field", "composer", "segmented") || node["kind"] == JsonPrimitive("secret")) null
        else key to ((state.drafts[key]?.value ?: node["value"]) as? JsonPrimitive)?.content.orEmpty()
    }.toMap()
    return ExtensionViewState(view = display, values = values, loading = state.phase != NativeStreamPhase.Ready)
}

/** Existing native renderer and design system only: no extension JS, effects, fetch or executable state. */
@Composable
internal fun NativeExtensionStreamPage(adapter: NativeExtensionStreamAdapter, title: String, onBack: () -> Unit,
    /** False at a regular-width sidebar root: the shell's sidebar button replaces Back (iOS sidebarRootPage). A stream pop still calls [onBack]. */
    showBack: Boolean = true,
    onPush: (persona.core.contracts.api.ExtensionViewPush) -> Unit = {},
    presentations: kotlinx.coroutines.flow.Flow<NativeStreamPresentation>? = null) {
    val session = adapter.session
    val native by session.state.collectAsState()
    val state = nativeExtensionDisplay(native)
    val displayedTree = native.tree
    val scope = rememberCoroutineScope()
    val local = remember(session) { adapter.local }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val uri = LocalUriHandler.current
    val resources = LocalResources.current
    val feedback = LocalAppFeedback.current
    LaunchedEffect(local.rejected) {
        if (local.rejected && feedback != null) {
            feedback.notice(resources.getString(R.string.settings_ext_native_rejected))
            local.rejected = false
        }
    }
    val push by rememberUpdatedState(onPush)
    val back by rememberUpdatedState(onBack)
    LaunchedEffect(session, presentations, adapter.shown) {
        if (!adapter.shown) return@LaunchedEffect
        presentations?.collect { presentation ->
            val result = presentation.result
            adapter.targets.remove(presentation.event.eventId)?.let { (tree, done) ->
                // The cache/closure belongs to the original node/revision, not the latest diff at this path.
                if (tree === presentation.tree) done(nativeStreamTargetResult(presentation))
            }
            result.toast?.let { feedback?.notice(it) }
            result.openUrl?.takeIf(ExtensionViewText::isHttps)?.let { runCatching { uri.openUri(it) } }
            result.push?.let { push(persona.core.contracts.api.ExtensionViewPush(ui = it.ui, state = it.state, title = it.title)) }
            if (result.pop == true) back()
        }
    }
    var confirming by local.slot<NativeStreamConfirmation?>("root", "confirmation") { null }
    var choosing by local.slot<NativeStreamChoice?>("root", "choice") { null }
    val currentShown by rememberUpdatedState(adapter.shown)
    DisposableEffect(local) {
        local.attached = true
        onDispose { local.attached = false }
    }
    val initialFocus = remember(session) { local.focused }
    var restoredFocus by remember(session) { mutableStateOf(false) }
    LaunchedEffect(session, adapter.shown, native.phase) {
        if (adapter.shown && native.phase == NativeStreamPhase.Ready && !restoredFocus) {
            restoredFocus = true
            // Capture on entry: a newly attached disabled input can report its initial blur
            // before the resumed snapshot enables it. That is not the user's blur.
            withFrameNanos { }
            initialFocus?.let { local.focus[it]?.requestFocus() }
        }
    }
    DisposableEffect(session, lifecycle) {
        val observer = LifecycleEventObserver { _, _ ->
            scope.launch { session.visibility(currentShown, lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED)) }
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    LaunchedEffect(session, adapter.shown) {
        session.visibility(adapter.shown, lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED))
    }
    LaunchedEffect(session) {
        try { awaitCancellation() }
        finally { withContext(NonCancellable) { withTimeoutOrNull(CloseTimeoutMillis) { session.visibility(false, false) } } }
    }
    LaunchedEffect(displayedTree) { displayedTree?.let { local.prune(it.nodes.keys + "root") } }
    val current by rememberUpdatedState(native)
    // How many columns of cards the page drew (UI version 8), read when a diff is revealed to find its place in the list.
    val drawnColumns = remember(session) { intArrayOf(1) }
    fun send(key: String, action: ExtensionViewAction, event: String, value: JsonObject, tree: NativeStreamTree,
             draft: NativeStreamDraft?, drafts: Map<String, NativeStreamDraft>, commit: () -> Unit, done: ((persona.core.contracts.api.ExtensionUiResult?) -> Unit)? = null) {
        fun refused() { done?.invoke(null); local.rejected = true }
        if (current.tree !== tree || !currentShown || current.phase != NativeStreamPhase.Ready || done != null && adapter.targets.size >= 64) { refused(); return }
        scope.launch {
            if (current.tree !== tree || !currentShown || current.phase != NativeStreamPhase.Ready) { refused(); return@launch }
            if (!action.confirm.isNullOrBlank() && event == "change" && current.drafts[key] === draft) {
                val node = tree.nodes[key]
                val input = node?.get("control") as? JsonObject ?: node?.takeIf { it["type"] == JsonPrimitive("segmented") }
                val name = input?.get("name")?.jsonPrimitive?.content
                // A confirmation's frozen value must not replace typing that came after it was shown.
                name?.let { value[it] }?.let { if (!session.edit(key, it, expectedTree = tree)) { refused(); return@launch } }
            }
            commit()
            action.push?.let { push(it); return@launch }
            if (action.pop) { back(); return@launch }
            action.open?.let { if (ExtensionViewText.isHttps(it)) { val _ = runCatching { uri.openUri(it) } }; return@launch }
            if (!session.event(key, action.id, event, value, expectedTree = tree,
                draftsAtPress = drafts.takeIf { action.submit },
                onQueued = { queued -> if (done != null && adapter.targets.size < 64) adapter.targets[queued.eventId] = tree to done })) refused()
        }
    }
    fun press(key: String, item: ExtensionViewItem, action: ExtensionViewAction, own: Map<String, String> = emptyMap(), event: String = "press",
              done: ((persona.core.contracts.api.ExtensionUiResult?) -> Unit)? = null, commit: () -> Unit = {}) {
        fun refused() { done?.invoke(null); local.rejected = true }
        val tree = displayedTree ?: run { refused(); return }
        val kind = tree.callbackEvent(key, action.id, event) ?: run { refused(); return }
        if (current.tree !== tree || !currentShown || native.phase != NativeStreamPhase.Ready) { refused(); return }
        val value = nativeStreamValues(native, key, action, own, local.roots)
        if (action.confirm.isNullOrBlank()) send(key, action, kind, value, tree, native.drafts[key], native.drafts, commit, done)
        else {
            confirming?.done?.invoke(null)
            confirming = NativeStreamConfirmation(key, item, action, value, tree, kind, native.drafts[key], native.drafts, commit, done)
        }
    }
    SideEffect { local.controlPress = { node, item, action, values, event -> press(node, item, action, values, event) } }
    fun rootSelection(key: String, name: String, action: ExtensionViewAction, value: String, own: Map<String, String>) {
        val attempt = Any()
        press(key, ExtensionViewItem(type = "button"), action, own, "change", done = {
            if (local.rootAttempts[name] === attempt) {
                local.rootAttempts.remove(name)
                local.roots.remove(name)
            }
        }, commit = { local.rootAttempts[name] = attempt; local.roots[name] = value })
    }
    fun handle(key: String): ExtensionViewHandle {
        val review = CodeReviewState(null, local.review(key),
            onlyKey = key.takeUnless { it == displayedTree?.snapshot?.rootKey }, nodeMemory = local::review) { action, values, done ->
            press(key, ExtensionViewItem(type = "button"), action, values, done = done)
        }
        return ExtensionViewHandle(
            press = { item, action -> press(key, item, action) },
            edit = { name, value -> scope.launch { session.edit(name, JsonPrimitive(value), expectedTree = displayedTree) } },
            choose = { item -> displayedTree?.let { choosing = NativeStreamChoice(item, it) } },
            open = { url -> if (currentShown && ExtensionViewText.isHttps(url)) { val _ = runCatching { uri.openUri(url) } } },
            segment = { item, value ->
                if (item.action?.confirm.isNullOrBlank()) scope.launch { session.edit(key, JsonPrimitive(value), expectedTree = displayedTree) }
                item.action?.let { press(key, item, it, mapOf(item.name.orEmpty() to value), "change") }
            },
            search = { value -> local.query = value; state.view?.search?.action?.let { press(key, ExtensionViewItem(type = "button"), it, mapOf("q" to value), "change") } },
            filter = { value -> state.view?.filter?.let { filter ->
                val search = state.view?.search
                val own = mapOf(filter.name to value) + if (search != null) mapOf("q" to (local.query ?: search.value.orEmpty())) else emptyMap()
                rootSelection(key, filter.name, filter.action, value, own)
            } },
            tab = { value -> state.view?.tabs?.let { tabs ->
                val own = mapOf(tabs.name to value) + if (state.view?.search != null) mapOf("q" to (local.query ?: state.view?.search?.value.orEmpty())) else emptyMap()
                rootSelection(key, tabs.name, tabs.action, value, own)
            } },
            more = { index, section -> displayedTree?.view?.sections?.getOrNull(index)?.key?.let { node -> section.more?.action?.let { press(node, ExtensionViewItem(type = "button"), it) } } },
            reveal = { path -> displayedTree?.let { tree -> if (current.tree === tree) nativeDiffIndex(tree, path, drawnColumns[0])?.let { (node, index) ->
                local.slot(node, "diff-open") { true }.value = true
                scope.launch { local.list.scrollToItem(index) }
            } } },
            act = { action, values -> press(key, ExtensionViewItem(type = "button"), action, values) },
            review = review, forNode = ::handle, nativeLocal = local, nodeKey = key,
            nativeItem = { item, display, modifier ->
                NativeStreamItem(item, display, native, local, modifier, ::handle,
                    press = { node, actionItem, action, values, event -> press(node, actionItem, action, values, event) },
                    edit = { node, value -> scope.launch { session.edit(node, JsonPrimitive(value), expectedTree = displayedTree) } })
            },
        )
    }
    val view = state.view
    val pageState = state.copy(values = state.values + local.roots, query = local.query)
    val base = handle(displayedTree?.snapshot?.rootKey.orEmpty())
    key(session.identity) {
        // UI version 8: a columns view takes the pane's whole width, as many columns of cards as it has room for
        ExtensionPageWidth(view) { columns, fullWidth ->
            SideEffect { drawnColumns[0] = columns }
            JListPage(title = view?.title?.takeIf { it.isNotBlank() } ?: title, onBack = if (showBack) onBack else null, largeTitle = false, state = local.list,
                refreshing = state.loading && view != null, onRefresh = { scope.launch { session.requestSnapshot() } }, fullWidth = fullWidth) {
                if (view == null) item("native-loading") { JLoading() }
                extensionViewItems("native-page", pageState, base, streamSectionKeys = displayedTree?.view?.sections?.map { it.key!! }, columns = columns)
            }
        }
    }
    confirming?.let { confirmation ->
        val (key, item, action, value, tree, event, draft, drafts, commit, done) = confirmation
        JDialog({ confirming = null; done?.invoke(null) }, title = ExtensionViewText.confirmTitle(resources, item), text = action.confirm,
            confirm = ExtensionViewText.confirmLabel(resources, item) to { confirming = null; send(key, action, event, value, tree, draft, drafts, commit, done) },
            destructive = item.role == "destructive")
    }
    if (adapter.shown) adapter.permission?.let { permission ->
        fun decide(allow: Boolean) { if (!adapter.permissionBusy) adapter.decidePermission?.invoke(permission, allow) }
        JDialog(onDismiss = {}, title = resources.getString(R.string.settings_ext_native_host_permission), text = permission.summary,
            confirm = resources.getString(R.string.settings_ext_native_allow) to { decide(true) },
            dismiss = if (adapter.permissionBusy) null else resources.getString(R.string.settings_ext_native_deny) to { decide(false) },
            confirmEnabled = !adapter.permissionBusy)
    }
    choosing?.let { choice ->
        val item = choice.item
        JActionSheet(item.choices.orEmpty().map { option ->
            JAction(option.label, checked = state.values[item.name] == option.value,
                onClick = { scope.launch { session.edit(item.key.orEmpty(), JsonPrimitive(option.value), expectedTree = choice.tree) } })
        }, { choosing = null }, title = item.label ?: item.name)
    }
}
