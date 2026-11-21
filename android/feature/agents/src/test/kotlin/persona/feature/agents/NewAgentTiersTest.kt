package persona.feature.agents

import androidx.activity.ComponentActivity
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.lifecycle.viewModelScope
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient
import okhttp3.Protocol
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import org.junit.Assert.*
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HttpAgentCreationService
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList

/**
 * New Agent's Agent presets (#2225), through the real view model and page. Responses are decoded from their wire shape so
 * the untouched base compiles and fails because the rows and behavior are absent, not because tests name future Kotlin APIs.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class NewAgentTiersTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Test
    @Config(sdk = [35], qualifiers = "en-w400dp-h840dp-xhdpi")
    fun ompPresetAndTypedModelUseTheOrdinaryNewAgentPageWithoutACatalogue() = checkOmpPicker(largeText = false)

    @Test
    @Config(sdk = [35], qualifiers = "en-w400dp-h840dp-xhdpi", fontScale = 2f)
    fun ompTypedModelIsAvailableAtTheLargestFontSize() = checkOmpPicker(largeText = true)

    private fun checkOmpPicker(largeText: Boolean) {
        assertEquals(if (largeText) 2f else 1f, rule.activity.resources.configuration.fontScale, 0.001f)
        val answer = """{
            "launchers":[{"name":"claude","agent":"claude"},{"name":"omp","agent":"omp","source":"zshrc"}],
            "tiers":[{"name":"Review setup","launcher":"omp","model":"studio/review","effort":"high"}]
        }"""
        val service = Service(answer, mapOf("omp" to JsonCodec.decodeFromString<LauncherModels>(
            """{"agent":"omp","models":[],"supports_start_model":true}""")))
        val model = show(service)
        try {
            pick(model, "Review setup", "omp")
            assertEquals("studio/review", model.state.value.sentModel)
            assertNull(model.state.value.sentEffort)
            rule.onNode(hasScrollToNodeAction()).performScrollToKey("model")
            rule.onNode(hasText("studio/review") and hasClickAction() and !hasText("Review setup")).performClick()
            val dialogInput = hasSetTextAction() and hasAnyAncestor(isDialog())
            rule.waitUntil(10_000) { rule.onAllNodes(dialogInput).fetchSemanticsNodes().size == 1 }
            val search = rule.onNode(dialogInput)
            search.performTextInput("-option")
            rule.onNodeWithText("Use “-option”").assertDoesNotExist()
            search.performTextReplacement("openai/gpt-5.2")
            val choose = rule.onNodeWithText("Use “openai/gpt-5.2”")
            rule.onNode(hasScrollToNodeAction() and hasAnyAncestor(isDialog())).performScrollToNode(hasText("Use “openai/gpt-5.2”"))
            choose.assertIsDisplayed()
            val out = File(System.getProperty("persona.shots") ?: "build/new-agent-omp-shots").also { it.mkdirs() }
            for (night in listOf(false, true)) {
                rule.runOnIdle { dark.value = night }
                rule.waitForIdle()
                val size = if (largeText) "-large" else ""
                val file = out.resolve("omp-model$size-${if (night) "dark" else "light"}.png")
                val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
                file.outputStream().use { bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it) }
            }
            choose.performClick()
            rule.onNodeWithText("openai/gpt-5.2").assertIsDisplayed()
            assertNull(model.state.value.pickedTier)
            rule.onNodeWithText("Start chat").performClick()
            rule.waitUntil(10_000) { service.requests.isNotEmpty() }
            val request = service.requests.single()
            assertEquals("omp", request.launcher)
            assertEquals("omp", request.agentType)
            assertEquals("openai/gpt-5.2", request.model)
            assertNull(request.permissionMode)
            assertNull(request.effort)
            assertNull(request.ultracode)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun ompExplicitLauncherShowsAndSendsTheComputerDefault() = checkOmpDefault(follow = false)
    @Test fun ompFollowingModelSettingsShowsTheBoundModelAndLeavesTheLauncherToTheHub() = checkOmpDefault(follow = true)

    private fun checkOmpDefault(follow: Boolean) {
        val posts = CopyOnWriteArrayList<String>()
        val client = OkHttpClient.Builder().addInterceptor { chain ->
            val request = chain.request()
            val path = request.url.encodedPath
            val body = when (path) {
                "/api/v1/model-config/purposes/agent.new" ->
                    """{"purpose":{"id":"agent.new","effective":{"provider":"launcher.omp","model":"studio/bound"}},"providers":[{"id":"launcher.omp","name":"omp"}]}"""
                "/api/v1/machines/computer/launchers" -> """{"launchers":[{"name":"omp","agent":"omp","source":"path","display_name":"Native omp launcher"}]}"""
                "/api/v1/machines/computer/launchers/omp/models" -> """{"agent":"omp","models":[],"supports_start_model":true}"""
                "/api/v1/machines/computer/dirs" -> """{"path":"/example/project"}"""
                "/api/v1/sessions" -> {
                    if (request.method == "POST") posts += Buffer().also { request.body!!.writeTo(it) }.readUtf8()
                    """{"session":{"machine_id":"computer","session_id":"new"}}"""
                }
                else -> null
            }
            Response.Builder().request(request).protocol(Protocol.HTTP_1_1).code(if (body == null) 404 else 200)
                .message(if (body == null) "Not Found" else "OK").body((body ?: "{}").toResponseBody()).build()
        }.build()
        val model = show(HttpAgentCreationService("test/paired", "http://127.0.0.1/", "fixture", client))
        try {
            pick(model, "Native omp launcher", "omp")
            if (follow) pick(model, "Follow model settings", "omp")
            val detail = if (follow) "Model settings: studio/bound" else "omp’s default on this computer"
            scrollTo(detail)
            rule.onNodeWithText(detail).assertIsDisplayed()
            rule.onNodeWithText("Start chat").performClick()
            rule.waitUntil(10_000) { posts.isNotEmpty() }
            val body = JsonCodec.parseToJsonElement(posts.single()).jsonObject
            assertEquals(if (follow) "" else "omp", body.getValue("launcher").jsonPrimitive.content)
            assertEquals("omp", body.getValue("agent_type").jsonPrimitive.content)
            assertTrue(listOf("model", "effort", "permission_mode", "ultracode", "work_session").none(body::containsKey))
        } finally { model.viewModelScope.cancel() }
    }

    @After fun resetAppearance() {
        style.value = AppStyle.Glass
        dark.value = false
    }

    private val computers = listOf(Machine("computer", "Studio", online = true))
    private val style = mutableStateOf(AppStyle.Glass)
    private val dark = mutableStateOf(false)
    private val reviewNote = "Use for design reviews and a careful second pass"
    private val defaultTiers = """[
        {"name":"quick","launcher":"claude","description":"Small, well-scoped changes"},
        {"name":"review","launcher":"fast","model":"m-review","effort":"high","description":"$reviewNote"}
    ]"""

    private fun offeredAnswer(tiers: String = defaultTiers, fastHealth: String = "") = """{
        "launchers":[{"name":"claude","agent":"claude"},{"name":"fast","agent":"claude"$fastHealth}],
        "default":{"provider":"claude","model":"","effort":""},
        "tiers":$tiers,"tiers_profile":{"id":"performance","name":"Performance"}
    }"""
    private val olderAnswer = """{"launchers":[{"name":"claude","agent":"claude"}],"default":{"provider":"claude","model":"","effort":"high"}}"""

    private class Store(var saved: AgentDefaults = AgentDefaults("/code/project", "claude")) : AgentCreationStore {
        var operation: AgentStartOperation? = null
        override suspend fun lastMachine(scope: String) = "computer"
        override suspend fun defaults(scope: String, machine: String) = saved
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) { saved = defaults }
        override suspend fun pending(scope: String): AgentStartOperation? = operation
        override suspend fun pending(scope: String, value: AgentStartOperation?) { operation = value }
    }

    private class Service(
        private val answer: String,
        private val lists: Map<String, LauncherModels>,
        private val fastGate: CompletableDeferred<Unit>? = null,
    ) : AgentCreationService {
        override val scope = "hub/pairing"
        val requests = mutableListOf<AgentStartRequest>()
        override suspend fun launchers(machineId: String) = launcherList(machineId).launchers
        override suspend fun launcherList(machineId: String) = JsonCodec.decodeFromString(AssistantLaunchers.serializer(), answer)
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code/project" })
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean): LauncherModels? {
            if (launcher == "fast") fastGate?.await()
            return lists[launcher]
        }
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            requests += request
            return AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "created-${requests.size}", cwd = request.cwd))
        }
        override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
    }

    private val claudeModels = LauncherModels("claude", listOf(
        AgentModel("default", "Default", isDefault = true, efforts = listOf("low", "medium", "high", "max")),
    ))
    private val fastModels = LauncherModels("claude", listOf(
        // A preset must send even the launcher's own default model: Model Settings could otherwise run another model.
        AgentModel("m-review", "Review model", isDefault = true, efforts = listOf("low", "medium", "high")),
        AgentModel("m-other", "Other model", efforts = listOf("low", "medium", "high")),
    ))

    private fun service(
        answer: String = offeredAnswer(),
        fast: LauncherModels = fastModels,
        fastGate: CompletableDeferred<Unit>? = null,
    ) = Service(answer, mapOf("claude" to claudeModels, "fast" to fast), fastGate)

    private fun show(service: AgentCreationService, store: Store = Store()): NewAgentViewModel {
        val model = NewAgentViewModel(service, store, MutableStateFlow(computers))
        rule.setContent {
            PersonaTheme(style = style.value, darkTheme = dark.value, reduceMotion = true) {
                NewAgentPage(model, computers, online = true, onBack = {})
            }
        }
        rule.waitUntil(10_000) {
            model.state.value.initialized && model.state.value.launchers.isNotEmpty() && !model.state.value.loading
        }
        rule.waitForIdle()
        return model
    }

    private fun scrollTo(text: String) {
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(text))
        rule.waitForIdle()
    }

    private fun tap(text: String) {
        scrollTo(text)
        rule.onNodeWithText(text).performClick()
        rule.waitForIdle()
    }

    private fun waitForLauncher(model: NewAgentViewModel, launcher: String) {
        rule.waitUntil(10_000) {
            val state = model.state.value
            state.modelLauncher == launcher && state.models.launcher == launcher && !state.models.loading
        }
        rule.waitForIdle()
    }

    private fun pick(model: NewAgentViewModel, name: String, launcher: String) {
        tap(name)
        waitForLauncher(model, launcher)
    }

    /** The radio may be merged into the row or remain its sibling; either accessible representation is fine. */
    private fun tierMark(name: String): SemanticsNodeInteraction {
        val text = hasText(name)
        val marked = SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected)
        return rule.onAllNodes(marked and (text or hasAnyDescendant(text) or hasAnySibling(text) or hasAnySibling(hasAnyDescendant(text))),
            useUnmergedTree = true).onFirst()
    }

    private fun start(model: NewAgentViewModel, service: Service): AgentStartRequest {
        rule.runOnIdle {
            model.prompt("Review the changes")
            model.submit()
        }
        rule.waitUntil(10_000) { service.requests.isNotEmpty() }
        rule.waitForIdle()
        val request = service.requests.single()
        assertFalse("tier" in JsonCodec.encodeToJsonElement(AgentStartRequest.serializer(), request).jsonObject)
        return request
    }

    private fun assertExplains(first: String, second: String) {
        rule.onNode(hasText(first, substring = true, ignoreCase = true) and
            hasText(second, substring = true, ignoreCase = true), useUnmergedTree = true).assertExists()
    }

    @Test fun offeredPresetFillsFieldsAndManualEditsUnmarkItBeforeReselectionRestoresIt() {
        val service = service()
        val model = show(service)
        assertEquals(2, model.state.value.tiers.size)
        try {
            scrollTo("review")
            rule.onNodeWithText("Performance").assertExists()
            rule.onNodeWithText("quick").assertExists()
            rule.onNodeWithText(reviewNote).assertExists()

            pick(model, "review", "fast")
            assertEquals("fast", model.state.value.selection.launcher)
            assertEquals("m-review", model.state.value.model)
            assertEquals("high", model.state.value.selection.effort)
            tierMark("review").assertIsSelected()

            // Effort stays editable; only the preset mark goes away.
            rule.runOnIdle { model.selection(model.state.value.selection.copy(effort = "low")) }
            rule.waitForIdle()
            assertEquals("low", model.state.value.selection.effort)
            assertEquals("m-review", model.state.value.model)
            tierMark("review").assertIsNotSelected()
            pick(model, "review", "fast")
            assertEquals("high", model.state.value.selection.effort)
            tierMark("review").assertIsSelected()

            // So does the model, and picking the same preset after that edit puts its own model back.
            rule.runOnIdle { model.pickModel("m-other") }
            rule.waitForIdle()
            assertEquals("m-other", model.state.value.model)
            assertEquals("fast", model.state.value.selection.launcher)
            tierMark("review").assertIsNotSelected()
            pick(model, "review", "fast")
            assertEquals("m-review", model.state.value.model)
            tierMark("review").assertIsSelected()

            // A launcher edit does not leave the old preset marked either.
            rule.runOnIdle { model.selection(model.state.value.selection.copy(launcher = "claude")) }
            waitForLauncher(model, "claude")
            assertEquals("", model.state.value.model)
            tierMark("review").assertIsNotSelected()
            pick(model, "review", "fast")

            val request = start(model, service)
            assertEquals("fast", request.launcher)
            assertEquals("m-review", request.model)
            assertEquals("high", request.effort)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun aPresetWithNoModelOrEffortClearsThePreviousPresetsChoices() {
        val service = service()
        val model = show(service)
        try {
            pick(model, "review", "fast")
            pick(model, "quick", "claude")
            val request = start(model, service)
            assertEquals("claude", request.launcher)
            assertNull(request.model)
            assertNull(request.effort)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun startingBeforeThePresetModelListAnswersKeepsLauncherModelAndEffort() {
        val gate = CompletableDeferred<Unit>()
        val service = service(fastGate = gate)
        val model = show(service)
        try {
            // The fast launcher's model answer is still suspended when Start posts. The preset must not degrade to defaults.
            tap("review")
            rule.waitUntil(10_000) {
                model.state.value.selection.launcher == "fast" && model.state.value.model == "m-review" &&
                    model.state.value.models.loading
            }
            rule.onNodeWithText(rule.activity.getString(R.string.agents_new_start)).assertIsNotEnabled()
            assertTrue(service.requests.isEmpty())
            gate.complete(Unit)
            rule.waitUntil(10_000) { startable(model.state.value, computers) }
            val request = start(model, service)
            assertEquals("fast", request.launcher)
            assertEquals("m-review", request.model)
            assertEquals("high", request.effort)
        } finally {
            gate.complete(Unit)
            model.viewModelScope.cancel()
        }
    }

    @Test fun anUnsupportedPresetEffortIsExplainedAndNotSent() {
        val tiers = """[{"name":"deepest","launcher":"fast","model":"m-review","effort":"max","description":"Deep reasoning"}]"""
        val service = service(offeredAnswer(tiers))
        val model = show(service)
        try {
            pick(model, "deepest", "fast")
            assertExplains("effort", "max")
            val request = start(model, service)
            assertEquals("fast", request.launcher)
            assertEquals("m-review", request.model)
            assertNull(request.effort)
        } finally { model.viewModelScope.cancel() }
    }

    private fun unsupportedModelIsExplainedAndNotSent(fast: LauncherModels, lockedReason: String?) {
        val service = service(fast = fast)
        val model = show(service)
        try {
            pick(model, "review", "fast")
            lockedReason?.let { rule.onNodeWithText(it, substring = true).assertExists() }
            assertExplains("model", "m-review")
            val request = start(model, service)
            assertEquals("fast", request.launcher)
            assertNull(request.model)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun aLauncherFixingItsOwnModelExplainsThePresetModelItCannotTake() =
        unsupportedModelIsExplainedAndNotSent(LauncherModels("claude", locked = "Launcher fixes its own model"),
            "Launcher fixes its own model")

    @Test fun aLauncherWithoutTypedModelsExplainsThePresetModelItCannotTake() =
        unsupportedModelIsExplainedAndNotSent(LauncherModels("claude"), null)

    @Test fun aBlockedPresetExplainsItsLauncherAndOnlyTryAnywayAppliesItsDefaults() {
        val health = ""","health":{"state":"blocked","kind":"auth","error":"Sign in again","checked_at":"2026-11-19T00:00:00Z"}"""
        val service = service(offeredAnswer(fastHealth = health))
        val model = show(service)
        try {
            tap("review")
            rule.onNodeWithTag("launcher-help").assertIsDisplayed()
            rule.onNodeWithText(rule.activity.getString(R.string.agents_launcher_blocked_auth), substring = true).assertExists()
            rule.onNodeWithText(rule.activity.getString(R.string.agents_launcher_help_details)).performClick()
            rule.onNodeWithText("Sign in again").assertExists()
            assertEquals("claude", model.state.value.selection.launcher)
            assertTrue(service.requests.isEmpty())

            rule.onNodeWithText(rule.activity.getString(R.string.agents_launcher_help_try_anyway)).performClick()
            waitForLauncher(model, "fast")
            assertEquals("fast", model.state.value.selection.launcher)
            assertEquals("m-review", model.state.value.model)
            assertEquals("high", model.state.value.selection.effort)
            tierMark("review").assertIsSelected()
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun anOlderHubShowsNoPresetRowAndStartsWithTheExistingDefaults() {
        val service = service(olderAnswer)
        val store = Store(AgentDefaults("/code/project", "claude", effort = "high"))
        val model = show(service, store)
        try {
            scrollTo(rule.activity.getString(R.string.agents_model))
            rule.onAllNodesWithText("Performance").assertCountEquals(0)
            rule.onAllNodesWithText("quick").assertCountEquals(0)
            rule.onAllNodesWithText("review").assertCountEquals(0)
            assertEquals("claude", model.state.value.selection.launcher)
            assertEquals("", model.state.value.model)
            assertEquals("high", model.state.value.selection.effort)
            val request = start(model, service)
            assertEquals("claude", request.launcher)
            assertNull(request.model)
            assertEquals("high", request.effort)
        } finally { model.viewModelScope.cancel() }
    }

    /** JVM UI evidence for one Android style in both appearances; no emulator or fixed-delay waits. */
    private fun capturePresets(appearance: AppStyle, largeText: Boolean = false) {
        style.value = appearance
        dark.value = false
        val service = service()
        val model = show(service)
        try {
            val out = File(System.getProperty("persona.shots") ?: "build/new-agent-tier-shots").also { it.mkdirs() }
            for (night in listOf(false, true)) {
                rule.runOnIdle { dark.value = night }
                scrollTo("review")
                rule.onNodeWithText("Performance").assertExists()
                rule.onNodeWithText("quick").assertExists()
                rule.onNodeWithText(reviewNote).assertIsDisplayed()
                val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
                val size = if (largeText) "-large" else ""
                out.resolve("new-agent-tiers-${appearance.name.lowercase()}$size-${if (night) "dark" else "light"}.png").outputStream().use {
                    assertTrue(bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it))
                }
            }
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun presetsAndWhenToUseNotesGlassLightAndDark() = capturePresets(AppStyle.Glass)
    @Test fun presetsAndWhenToUseNotesBriefLightAndDark() = capturePresets(AppStyle.Brief)
    @Test fun presetsAndWhenToUseNotesBoardLightAndDark() = capturePresets(AppStyle.Board)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun presetsAndWhenToUseNotesGlassAtLargestText() = capturePresets(AppStyle.Glass, largeText = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun presetsAndWhenToUseNotesBriefAtLargestText() = capturePresets(AppStyle.Brief, largeText = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi", fontScale = 2f)
    @Test fun presetsAndWhenToUseNotesBoardAtLargestText() = capturePresets(AppStyle.Board, largeText = true)
}
