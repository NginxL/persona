package persona.feature.settings

import android.graphics.Bitmap
import android.os.Handler
import android.os.Looper
import kotlinx.coroutines.android.asCoroutineDispatcher
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.*
import org.junit.*
import org.junit.Assert.*
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.contracts.generated.*
import persona.core.contracts.stream.*
import persona.core.designsystem.*
import persona.core.hub.*

/** Opt-in genuine manager → existing OkHttp app connection → production controller → native Compose evidence.
 * Native-graphics JVM E2E, not a device/emulator. The external fixture installs the real, unpublished showcase bundle. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ExtensionUIStreamLiveTest {
    @get:Rule val rule = createComposeRule()
    @get:Rule val testName = org.junit.rules.TestName()
    private lateinit var connection: OkHttpHubConnection
    private val controllers = mutableListOf<ExtensionUIStreamController>()
    private val frames = CopyOnWriteArrayList<JsonObject>()
    private val observer = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var evidence: File
    private lateinit var uiDispatcher: CoroutineDispatcher
    private val store = androidx.lifecycle.ViewModelStore()
    private val storeOwner = object : androidx.lifecycle.ViewModelStoreOwner { override val viewModelStore = store }

    @Before fun connect() {
        // Keep live network collectors on Android's UI looper, not Compose's nested virtual test scheduler.
        uiDispatcher = Handler(Looper.getMainLooper()).asCoroutineDispatcher("live-android-ui")
        val path = System.getenv("PERSONA_ANDROID_STREAM_FIXTURE")
        Assume.assumeTrue("External real-showcase fixture was not requested", !path.isNullOrBlank())
        val cfg = JsonCodec.parseToJsonElement(File(path!!).readText()).jsonObject
        evidence = File(File(File(path).parentFile, "android-evidence"), testName.methodName).also { it.mkdirs() }
        connection = OkHttpHubConnection(HubConnectionConfig(cfg.getValue("hub").jsonPrimitive.content, null,
            cfg.getValue("token").jsonPrimitive.content, listOf(Endpoint(cfg.getValue("url").jsonPrimitive.content, "lan")), "test"), HubNetwork("owned-fixture"))
        val rawFrames = mutableListOf<JsonObject>()
        connection.phoneMessageHandler = { raw ->
            // Observe actual inbound bytes before validation; never consume or substitute a frame.
            if (raw["type"] == JsonPrimitive("extension_ui_stream")) {
                rawFrames.add(raw)
                File(evidence, "raw-network-frames.json").writeText(JsonArray(rawFrames.toList()).toString())
                runCatching {
                    val checked = NativeStreamSchema.parse(raw.toString(), "UIStreamServerEnvelope")
                    JsonCodec.decodeFromJsonElement(ExtensionUIStreamServerEnvelope.serializer(), checked)
                    JsonCodec.decodeFromJsonElement(ServerEvent.serializer(), raw)
                }.exceptionOrNull()?.let { File(evidence, "raw-validation-failure.txt").writeText(it.stackTraceToString()) }
            }
            false
        }
        observer.launch(start = CoroutineStart.UNDISPATCHED) { connection.events.filterIsInstance<ExtensionUiStreamEvent>().collect {
            frames.add(it.raw)
            File(evidence, "received-frames.json").writeText(JsonArray(frames.toList()).toString())
        } }
        runBlocking { withTimeout(30000) { connection.state.first { it == ConnectionState.Online } } }
        assertNotNull("Actual authenticated hello is retained", connection.lastHello)
        assertTrue("Actual authenticated hello negotiated streams", "extension_ui_stream_v1" in connection.serverFeatures.value)
        File(evidence, "authenticated-hello.json").writeText(JsonCodec.encodeToString(Hello.serializer(), checkNotNull(connection.lastHello)))
    }

    private fun controller(state: String, deadlineMillis: Long = 30000): ExtensionUIStreamController {
        val manager = runBlocking { HttpHubManagers(connection).roster().defaultId }
        return ExtensionUIStreamController(ExtensionStreamBinding(connection, "synthetic-owner", "synthetic-group", manager, approvals = HubChatTransport(connection)), "showcase", "showcase", state,
            ExtensionUIContext(uiVersion = 7, platform = "android", locale = "en", tz = "UTC",
                stream = ExtensionUIStreamCapability(protocol = 1, components = NativeExtensionStreamAdapter.Components.toList())), dispatcher = uiDispatcher,
            responseDeadlineMillis = deadlineMillis).also { controllers.add(it) }
    }
    private fun await(condition: () -> Boolean) = rule.waitUntil(30000) {
        // waitUntil advances Compose frames but does not drain Robolectric's paused Android looper.
        org.robolectric.Shadows.shadowOf(Looper.getMainLooper()).idle()
        rule.waitForIdle()
        condition()
    }

    private fun ready(c: ExtensionUIStreamController) {
        try {
            await {
                assertTrue("Unexpected renderer gaps: ${c.adapter.session.state.value.unsupported}", c.adapter.session.state.value.unsupported.isEmpty())
                c.adapter.session.state.value.phase == NativeStreamPhase.Ready
            }
        } catch (error: Throwable) {
            File(evidence, "failed-ready-phase.txt").writeText("${c.adapter.session.state.value.phase}, tree=${c.adapter.session.state.value.tree?.view?.title}, gaps=${c.adapter.session.state.value.unsupported}")
            File(evidence, "failed-ready-frames.json").writeText(JsonArray(frames.toList()).toString())
            throw error
        }
    }
    private fun tree(c: ExtensionUIStreamController) = checkNotNull(c.adapter.session.state.value.tree)
    private fun title(c: ExtensionUIStreamController, key: String) = tree(c).nodes.getValue(key).getValue("title").jsonPrimitive.content
    private fun click(c: ExtensionUIStreamController, key: String) {
        val node = tree(c).nodes.getValue(key)
        val text = (node["title"] ?: node["label"])!!.jsonPrimitive.content
        rule.onNodeWithText(text).performScrollTo().performClick()
    }

    @Test fun actualShowcaseListSegmentsInvalidateAndSixNativeScreenshots() {
        val list = controller("{\"p\":\"list\"}")
        val segments = controller("{\"p\":\"segmented\"}")
        val invalidation = controller("{\"p\":\"invalidate\"}")
        var active by mutableStateOf(list)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val pushed = mutableListOf<ExtensionViewPush>()
        var popped = 0
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("real-showcase").fillMaxSize().background(PersonaTheme.palette.background)) {
                NativeExtensionStreamPage(active.adapter, "Showcase", { popped++ }, onPush = { pushed.add(it) }, presentations = active.presentations)
            }
        } }
        ready(list)
        assertTrue(tree(list).nodes.keys.any { it.startsWith("item-") })
        val first = tree(list).nodes.keys.first { it.startsWith("item-") }
        click(list, first)
        assertEquals(1, pushed.size)
        assertTrue(pushed.single().state.orEmpty().contains("detail"))
        // Saved push navigation is native and sends no entered callback, hence no event fence.
        assertTrue(frames.none { it["kind"] == JsonPrimitive("receipt") })
        val detail = controller(checkNotNull(pushed.first().state))
        rule.runOnIdle { list.adapter.shown = false; active = detail }
        Snapshot.sendApplyNotifications(); ready(detail)
        click(detail, "detail-edit")
        assertEquals(2, pushed.size)
        val edit = controller(checkNotNull(pushed.last().state))
        rule.runOnIdle { detail.adapter.shown = false; active = edit }
        Snapshot.sendApplyNotifications(); ready(edit)
        val originalTitle = tree(edit).nodes.getValue("edit-title").getValue("value").jsonPrimitive.content
        val editedTitle = "Edited sample item ${java.util.UUID.randomUUID().toString().take(8)}"
        rule.onNode(hasSetTextAction() and hasText(originalTitle)).performScrollTo().performTextReplacement(editedTitle)
        await { edit.adapter.session.state.value.drafts["edit-title"]?.value == JsonPrimitive(editedTitle) }
        click(edit, "edit-save")
        await { popped == 1 }
        assertTrue(frames.any { it["kind"] == JsonPrimitive("receipt") && it["receipt"]?.jsonObject?.get("result")?.jsonObject?.get("pop") == JsonPrimitive(true) })
        // Protocol-only network replay, separate from the native Save click above: send the exact recorded event
        // through the real authenticated connection with new request correlation, never inject a server frame.
        val sentField = ExtensionUIStreamController::class.java.getDeclaredField("sent").apply { isAccessible = true }
        val savedEvent = (sentField.get(edit) as Map<*, *>).values.map { record ->
            record!!.javaClass.getDeclaredField("request").apply { isAccessible = true }.get(record)
        }.filterIsInstance<ExtensionUIStreamEventRequest>().single()
        val replay = savedEvent.copy(requestId = java.util.UUID.randomUUID().toString())
        val replayEnvelope = JsonCodec.encodeToJsonElement(ExtensionUIStreamClientEnvelope.serializer(),
            ExtensionUIStreamClientEnvelope(type = "extension_ui_stream", manager = edit.binding.manager, request = replay)).jsonObject
        File(evidence, "protocol-replay-request.json").writeText(replayEnvelope.toString())
        assertTrue(connection.send(replayEnvelope))
        await { frames.any { it["kind"] == JsonPrimitive("receipt") && it["request_id"] == JsonPrimitive(replay.requestId) } }
        assertEquals(1, popped)
        rule.runOnIdle { edit.adapter.shown = false; detail.adapter.shown = true; active = detail }
        Snapshot.sendApplyNotifications(); ready(detail)
        assertEquals(editedTitle, tree(detail).viewRaw().getValue("title").jsonPrimitive.content)
        rule.runOnIdle { detail.adapter.shown = false; list.adapter.shown = true; active = list }
        Snapshot.sendApplyNotifications(); ready(list)
        val initialCount = tree(list).nodes.keys.count { it.startsWith("item-") }
        val before = tree(list).snapshot.version
        rule.onNode(hasScrollAction()).performScrollToIndex(list.adapter.local.list.layoutInfo.totalItemsCount - 1)
        await { tree(list).nodes.keys.count { it.startsWith("item-") } > initialCount }
        assertTrue(tree(list).snapshot.version > before)
        val query = title(list, first).substringBefore(' ')
        rule.onNode(hasScrollAction()).performScrollToIndex(0)
        await { list.adapter.session.state.value.pendingEvents == 0 }
        rule.onNode(hasSetTextAction()).performTextReplacement(query)
        await { tree(list).viewRaw()["search"]?.jsonObject?.get("value") == JsonPrimitive(query) && list.adapter.session.state.value.pendingEvents == 0 }
        rule.onNode(hasSetTextAction()).performTextClearance()
        await { tree(list).viewRaw()["search"]?.jsonObject?.get("value") in listOf(null, JsonPrimitive("")) && list.adapter.session.state.value.pendingEvents == 0 }
        val filter = tree(list).viewRaw().getValue("filter").jsonObject
        val choices = filter.getValue("choices").jsonArray.map { it.jsonObject }
        rule.onNodeWithText((choices.first()["title"] ?: choices.first().getValue("label")).jsonPrimitive.content).performClick()
        val option = choices[1]
        rule.onAllNodesWithText((option["title"] ?: option.getValue("label")).jsonPrimitive.content).onLast().performClick()
        val kind = option.getValue("value")
        await { tree(list).viewRaw().getValue("filter").jsonObject["value"] == kind && list.adapter.session.state.value.pendingEvents == 0 }
        // Native effects stop while covered; reconnect/resume must supply a snapshot before another delta.
        rule.runOnIdle { list.adapter.shown = false; active = segments }
        Snapshot.sendApplyNotifications(); ready(segments)
        val choice = tree(segments).nodes.getValue("range").getValue("choices").jsonArray.first().jsonObject
        rule.onNodeWithText((choice["title"] ?: choice.getValue("label")).jsonPrimitive.content).performClick()
        await { tree(segments).viewRaw()["state"]!!.jsonPrimitive.content.contains("1d") }
        val buttons = controller("{\"p\":\"button\"}")
        rule.runOnIdle { segments.adapter.shown = false; active = buttons }
        Snapshot.sendApplyNotifications(); ready(buttons)
        val buttonVersion = tree(buttons).snapshot.version
        val buttonSession = tree(buttons).snapshot.session
        click(buttons, "button-button-item-0-0")
        await { tree(buttons).snapshot.version == buttonVersion + 1 && buttons.adapter.session.state.value.pendingEvents == 0 }
        assertTrue(frames.any { it["kind"] == JsonPrimitive("delta") && it["delta"]?.jsonObject?.get("session") == JsonPrimitive(buttonSession) && it["delta"]?.jsonObject?.get("operations") == JsonArray(emptyList()) })
        val receiptsBeforeCancel = frames.count { it["kind"] == JsonPrimitive("receipt") }
        click(buttons, "button-button-item-0-1")
        val confirm = tree(buttons).nodes.getValue("button-button-item-0-1").getValue("action").jsonObject.getValue("confirm").jsonPrimitive.content
        rule.onNodeWithText(confirm).assertExists()
        rule.onNodeWithText("Cancel").performClick(); rule.waitForIdle()
        assertEquals(receiptsBeforeCancel, frames.count { it["kind"] == JsonPrimitive("receipt") })
        assertEquals(buttonVersion + 1, tree(buttons).snapshot.version)
        rule.runOnIdle { buttons.adapter.shown = false; active = invalidation }
        Snapshot.sendApplyNotifications(); ready(invalidation)
        click(invalidation, "ask")
        await { tree(invalidation).nodes.containsKey("ask-loading") }
        await { tree(invalidation).nodes.containsKey("ask-data") }
        val metric = tree(invalidation).nodes.getValue("ask-data").getValue("value").jsonPrimitive.content
        rule.onNodeWithText(metric).assertIsDisplayed()
        for (s in AppStyle.entries) {
            val sums = mutableListOf<Long>()
            for (d in listOf(false, true)) {
                style = s; dark = d; Snapshot.sendApplyNotifications(); rule.waitForIdle()
                rule.onNodeWithText(metric).assertIsDisplayed()
                val bitmap = rule.onNodeWithTag("real-showcase").captureToImage().asAndroidBitmap()
                var sum = 0L
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) sum += bitmap.getPixel(x,y)
                sums.add(sum)
                File(evidence, "${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG,100,it) }
            }
            assertNotEquals(sums[0],sums[1])
        }
        val snapshotBefore = frames.count { it["kind"] == JsonPrimitive("snapshot") }
        connection.reconnect()
        await { frames.count { it["kind"] == JsonPrimitive("snapshot") } > snapshotBefore }
        ready(invalidation)
        assertEquals(metric, tree(invalidation).nodes.getValue("ask-data").getValue("value").jsonPrimitive.content)
        assertEquals(1, popped)
        File(evidence, "showcase-live-frames.json").writeText(JsonArray(frames.toList()).toString())
    }

    @Test fun actualExtensionPageRouteUsesConnectionAndLegacyHttpRetainsItsFallback() {
        val api = HttpExtensionManagement(connection, flowOf(true))
        rule.setContent { CompositionLocalProvider(androidx.lifecycle.viewmodel.compose.LocalViewModelStoreOwner provides storeOwner) {
            PersonaTheme(style = AppStyle.Board, reduceMotion = true) {
                ExtensionUiPage(api, "showcase", "showcase", "Showcase", {}, initialState = "{\"p\":\"invalidate\"}")
            }
        } }
        await { frames.any { it["kind"] == JsonPrimitive("snapshot") } }
        val snapshot = frames.first { it["kind"] == JsonPrimitive("snapshot") }.getValue("snapshot").jsonObject
        val button = snapshot.getValue("view").jsonObject.getValue("sections").jsonArray.flatMap { it.jsonObject.getValue("items").jsonArray }.first { it.jsonObject["key"] == JsonPrimitive("ask") }.jsonObject
        rule.onNodeWithText(button.getValue("title").jsonPrimitive.content).performClick()
        await { frames.any { it["kind"] == JsonPrimitive("delta") } }
        val legacy = runBlocking { api.renderUi("showcase", "showcase", ExtensionUiContext(uiVersion = 2, platform = "android", locale = "en"), "{\"p\":\"list\"}") }
        assertNotNull(legacy.state)
        assertNull(legacy.search)
        assertTrue("HTTP whole-page fallback retains drawable sections", legacy.sections.flatMap { it.items }.any(ExtensionViewText::drawable))
        File(evidence, "route-live-frames.json").writeText(JsonArray(frames.toList()).toString())
    }

    @Test fun actualHostPermissionRequiresASecondHumanDecisionAndDenialDoesNotWrite() {
        val hostFrames = CopyOnWriteArrayList<JsonObject>()
        val observe = connection.phoneMessageHandler
        connection.phoneMessageHandler = { raw ->
            val _ = observe?.invoke(raw)
            if (raw["type"] in setOf(JsonPrimitive("interaction"), JsonPrimitive("interaction_resolved"))) {
                hostFrames.add(raw)
                File(evidence, "actual-host-interactions.json").writeText(JsonArray(hostFrames.toList()).toString())
            }
            false
        }
        val controller = controller("{\"p\":\"button\"}")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            NativeExtensionStreamPage(controller.adapter, "Showcase", {}, presentations = controller.presentations)
        } }
        ready(controller)
        val key = "button-button-item-0-1"
        fun localConsent() {
            val node = tree(controller).nodes.getValue(key)
            val title = node.getValue("title").jsonPrimitive.content
            val confirm = node.getValue("action").jsonObject.getValue("confirm").jsonPrimitive.content
            click(controller, key)
            rule.onNodeWithText(confirm).assertExists()
            rule.onAllNodesWithText(if (title.isNotBlank() && title.length <= 8) title else "OK").onLast().performClick()
            await { controller.adapter.permission != null }
            rule.onNodeWithText("Hub approval required").assertExists()
            assertEquals(tree(controller).snapshot.session, controller.adapter.permission!!.sessionId)
            assertEquals(controller.binding.manager, controller.adapter.permission!!.manager)
            assertEquals(confirm, controller.adapter.permission!!.summary)
        }
        val before = tree(controller).snapshot.version
        localConsent()
        assertEquals(before, tree(controller).snapshot.version)
        assertTrue(frames.none { it["kind"] == JsonPrimitive("receipt") })
        for (s in AppStyle.entries) {
            val sums = mutableListOf<Long>()
            for (d in listOf(false, true)) {
                style = s; dark = d; Snapshot.sendApplyNotifications(); rule.waitForIdle()
                rule.onNodeWithText("Hub approval required").assertIsDisplayed()
                // Robolectric's Compose capture crops the Activity bitmap for a dialog root.
                // Draw the actual dialog decor instead, so this evidence contains the host card.
                lateinit var bitmap: Bitmap
                rule.runOnIdle {
                    val decor = checkNotNull(org.robolectric.shadows.ShadowDialog.getLatestDialog().window).decorView
                    bitmap = Bitmap.createBitmap(decor.width, decor.height, Bitmap.Config.ARGB_8888)
                    decor.draw(android.graphics.Canvas(bitmap))
                }
                var sum = 0L
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) sum += bitmap.getPixel(x, y)
                sums += sum
                File(evidence, "host-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
            assertNotEquals(sums[0], sums[1])
        }
        rule.onAllNodesWithText("Approve").onLast().performClick()
        await { controller.adapter.session.state.value.pendingEvents == 0 && tree(controller).snapshot.version == before + 1 }
        assertTrue(frames.any { it["kind"] == JsonPrimitive("receipt") && it["receipt"]?.jsonObject?.get("status") == JsonPrimitive("accepted") })
        val accepted = frames.count { it["kind"] == JsonPrimitive("receipt") && it["receipt"]?.jsonObject?.get("status") == JsonPrimitive("accepted") }
        val deltaCount = frames.count { it["kind"] == JsonPrimitive("delta") }
        localConsent()
        rule.onAllNodesWithText("Deny").onLast().performClick()
        await { controller.adapter.permission == null && controller.adapter.session.state.value.pendingEvents == 0 && controller.adapter.session.state.value.phase == NativeStreamPhase.Ready }
        assertEquals(before + 1, tree(controller).snapshot.version)
        assertEquals(accepted, frames.count { it["kind"] == JsonPrimitive("receipt") && it["receipt"]?.jsonObject?.get("status") == JsonPrimitive("accepted") })
        assertEquals(deltaCount, frames.count { it["kind"] == JsonPrimitive("delta") })
        assertEquals(2, hostFrames.count { it["type"] == JsonPrimitive("interaction") })
        File(evidence, "actual-host-interactions.json").writeText(JsonArray(hostFrames.toList()).toString())
        File(evidence, "host-approve-deny-stream.json").writeText(JsonArray(frames.toList()).toString())
    }

    @Test fun actualLostOpenSnapshotFinalRemovalRecoversOnlyTheOriginalAllocation() {
        rule.setContent { PersonaTheme(reduceMotion = true) { Box(Modifier.fillMaxSize()) } }
        val dropped = CopyOnWriteArrayList<JsonObject>()
        val snapshots = CopyOnWriteArrayList<JsonObject>()
        val observe = connection.phoneMessageHandler
        connection.phoneMessageHandler = { raw ->
            val _ = observe?.invoke(raw)
            if (raw["kind"] == JsonPrimitive("snapshot")) {
                snapshots.add(raw)
                if (dropped.isEmpty()) { dropped.add(raw); true } else false
            } else false
        }
        val controller = controller("{\"p\":\"list\"}", deadlineMillis = 1000)
        val ui = observer.launch(uiDispatcher) { controller.adapter.session.visibility(true, true); awaitCancellation() }
        await { dropped.isNotEmpty() }
        val original = dropped.single()
        val allocation = original.getValue("snapshot").jsonObject
        ui.cancel()
        observer.launch(uiDispatcher) { controller.close() }
        await { frames.any { it["kind"] == JsonPrimitive("closed") && it["session"] == allocation["session"] } }
        assertEquals(NativeStreamPhase.Closed, controller.adapter.session.state.value.phase)
        assertNull(controller.adapter.session.state.value.tree)
        assertEquals(1, snapshots.map { it.getValue("snapshot").jsonObject.getValue("session") }.toSet().size)
        assertEquals(original["request_id"], snapshots.last()["request_id"])
        assertEquals(allocation["epoch"], snapshots.last().getValue("snapshot").jsonObject["epoch"])
        File(evidence, "deliberately-dropped-first-snapshot.json").writeText(original.toString())
        File(evidence, "recovered-original-allocation.json").writeText(JsonArray(snapshots.toList()).toString())
    }

    @Test fun actualLostOpenWhileShownReconnectsWithTheOriginalFrozenRequest() {
        rule.setContent { PersonaTheme(reduceMotion = true) { Box(Modifier.fillMaxSize()) } }
        val snapshots = CopyOnWriteArrayList<JsonObject>()
        val observe = connection.phoneMessageHandler
        connection.phoneMessageHandler = { raw ->
            val _ = observe?.invoke(raw)
            if (raw["kind"] == JsonPrimitive("snapshot")) { snapshots.add(raw); snapshots.size == 1 } else false
        }
        val controller = controller("{\"p\":\"list\"}")
        observer.launch(uiDispatcher) { controller.adapter.session.visibility(true, true) }
        await { snapshots.isNotEmpty() }
        val original = snapshots.single()
        connection.reconnect(); ready(controller)
        val allocation = original.getValue("snapshot").jsonObject
        assertEquals(allocation["session"]?.jsonPrimitive?.content, tree(controller).snapshot.session)
        assertEquals(allocation["epoch"]?.jsonPrimitive?.content, tree(controller).snapshot.epoch)
        assertEquals(setOf(original["request_id"]), snapshots.map { it["request_id"] }.toSet())
        File(evidence, "shown-lost-open-reconnect.json").writeText(JsonArray(snapshots.toList()).toString())
    }

    @Test fun actualFinalRemovalDuringReconnectHandsOffOwnershipWithoutNativeRecovery() {
        rule.setContent { PersonaTheme(reduceMotion = true) { Box(Modifier.fillMaxSize()) } }
        val controller = controller("{\"p\":\"list\"}")
        observer.launch(uiDispatcher) { controller.adapter.session.visibility(true, true) }
        ready(controller)
        val allocation = tree(controller).snapshot
        val before = connection.lastHello
        // Cancel the genuine socket, then remove the navigation owner before the replacement hello.
        observer.launch(uiDispatcher) { connection.reconnect(); controller.close() }
        await { frames.any { it["kind"] == JsonPrimitive("closed") && it["session"] == JsonPrimitive(allocation.session) } }
        assertNull(controller.adapter.session.state.value.tree)
        assertEquals(NativeStreamPhase.Closed, controller.adapter.session.state.value.phase)
        val allocated = frames.mapNotNull { it["snapshot"]?.jsonObject?.get("session") }.toSet()
        assertEquals(setOf(JsonPrimitive(allocation.session)), allocated)
        assertTrue("Genuine connection generation changed", connection.lastHello !== before)
        File(evidence, "reconnect-final-removal.json").writeText(JsonArray(frames.toList()).toString())
    }

    @Test fun actualUnsupportedFieldProfileClosesAllocationAndRendersHttpFallback() {
        val api = HttpExtensionManagement(connection, flowOf(true))
        rule.setContent { CompositionLocalProvider(androidx.lifecycle.viewmodel.compose.LocalViewModelStoreOwner provides storeOwner) {
            PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                ExtensionUiPage(api, "showcase", "showcase", "Showcase", {}, initialState = "{\"p\":\"field\"}")
            }
        } }
        await { frames.any { it["kind"] == JsonPrimitive("closed") } }
        rule.onNodeWithText("This page needs a newer native renderer. Showing the compatible page instead.").assertExists()
        await { rule.onAllNodes(hasSetTextAction()).fetchSemanticsNodes().size >= 2 }
        File(evidence, "unsupported-http-fallback-frames.json").writeText(JsonArray(frames.toList()).toString())
    }

    @After fun stopOwnedClients() {
        if (!::connection.isInitialized) return
        val sentField = ExtensionUIStreamController::class.java.getDeclaredField("sent").apply { isAccessible = true }
        val sentRequests = controllers.flatMap { controller ->
            (sentField.get(controller) as Map<*, *>).values.map { record ->
                val requestField = record!!.javaClass.getDeclaredField("request").apply { isAccessible = true }
                JsonCodec.encodeToJsonElement(ExtensionUIStreamRequest.serializer(), requestField.get(record) as ExtensionUIStreamRequest)
            }
        }
        File(evidence, "actual-sent-requests.json").writeText(JsonArray(sentRequests).toString())
        File(evidence, "final-phases.txt").writeText(controllers.joinToString("\n") { "${it.adapter.session.identity.contribution}: ${it.adapter.session.state.value.phase}, gaps=${it.adapter.session.state.value.unsupported}" })
        val allocated = frames.mapNotNull { it["snapshot"]?.jsonObject?.get("session")?.jsonPrimitive?.content }.toSet()
        rule.runOnIdle { store.clear() }
        observer.launch(uiDispatcher) { controllers.forEach { it.close() } }
        if (allocated.isNotEmpty()) await {
            val closed = frames.filter { it["kind"] == JsonPrimitive("closed") }.mapNotNull { it["session"]?.jsonPrimitive?.content }.toSet()
            allocated.all { it in closed }
        }
        connection.close(); observer.cancel()
    }
}
