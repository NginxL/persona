package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.generated.*
import persona.core.contracts.stream.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/** COMPONENT evidence only: fixed server-schema payloads, not Deno/HackerNews or live transport E2E. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class NativeStreamCoverageTest {
    @get:Rule val rule = createComposeRule()
    private val identity = NativeStreamIdentity("owner", "group", "example", "page", "navigation")
    private val requests = mutableListOf<ExtensionUIStreamRequest>()
    private lateinit var session: NativeStreamSession
    private lateinit var adapter: NativeExtensionStreamAdapter
    private fun snapshot(items: String, extras: String = "", version: Int = 1) =
        """{"protocol":1,"session":"session","epoch":"epoch","version":$version,"root_key":"root","view":{$extras"sections":[{"key":"section","items":[$items]}]}}"""
    private var resumePayload: String? = null
    private fun show(payload: String, components: List<String> = NativeExtensionStreamAdapter.Components.toList()) {
        session = NativeStreamSession(identity, ExtensionUIContext(platform = "android", locale = "en", uiVersion = 6,
            stream = ExtensionUIStreamCapability(protocol = 1, components = components)),
            NativeStreamTransport { request ->
                requests.add(request)
                if (request is ExtensionUIStreamOpenRequest || request is ExtensionUIStreamResumeRequest) {
                    val id = when (request) { is ExtensionUIStreamOpenRequest -> request.requestId; is ExtensionUIStreamResumeRequest -> request.requestId; else -> error("unreachable") }
                    session.snapshot(identity, id, if (request is ExtensionUIStreamResumeRequest) resumePayload ?: payload else payload)
                }
            })
        adapter = NativeExtensionStreamAdapter(session, true)
    }
    private fun mount() = rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { NativeExtensionStreamPage(adapter, "Page", {}) } }
    private fun last(key: String, action: String, event: String = "press"): ExtensionUIStreamEventRequest {
        rule.waitForIdle()
        return requests.filterIsInstance<ExtensionUIStreamEventRequest>().last().also {
            assertEquals(key, it.interaction.key); assertEquals(action, it.interaction.action); assertEquals(event, it.interaction.event)
            assertEquals(1L, it.interaction.version)
            runBlocking { session.acknowledged(identity, it.requestId) }
        }
    }

    private val row = """{"type":"row","key":"row","title":"Lamp","subtitle":"Living room","value":"0","action":{"id":"primary"},"control":{"kind":"switch","name":"enabled","on":false,"action":{"id":"toggle","confirm":"Change lamp?","values":{"target":"lamp"}}},"actions":[{"title":"Remove","role":"destructive","swipe":true,"action":{"id":"remove","confirm":"Remove lamp?","values":{"target":"lamp"}}}]}"""
    private val grid = """{"type":"grid","key":"grid","max_columns":2,"tiles":[{"key":"tile-a","title":"Scene A","action":{"id":"pick"}},{"key":"tile-b","title":"Scene B","control":{"kind":"switch","name":"scene","on":false,"action":{"id":"switch"}},"actions":[{"title":"Reset","action":{"id":"reset"}}]}]}"""
    private val carousel = """{"type":"carousel","key":"carousel","title":"Rooms","width":"narrow","tiles":[{"key":"room","title":"Bedroom","action":{"id":"pick"}}]}"""
    private val image = """{"type":"image","key":"image","src":{"url":"https://example.com/picture.png"},"alt":"Picture unavailable","caption":"Demo picture","no_zoom":true}"""

    @Test fun rowPrimaryControlAndMenuCarryExactDisplayedBindingAndConfirmationValues() {
        show(snapshot(row)); mount()
        rule.onNodeWithText("Lamp").performClick(); last("row", "primary")
        rule.onNode(hasContentDescription("Lamp") and isToggleable()).performClick()
        rule.onNodeWithText("Change lamp?").assertExists()
        assertEquals(1, requests.filterIsInstance<ExtensionUIStreamEventRequest>().size)
        rule.onAllNodesWithText("Lamp").onLast().performClick()
        assertEquals("""{"enabled":"true","target":"lamp"}""", last("row", "toggle", "change").interaction.value.toString())
        rule.onNodeWithContentDescription("Actions").performClick()
        rule.onNodeWithText("Remove").performClick()
        rule.onNodeWithText("Remove lamp?").assertExists()
        rule.onAllNodesWithText("Remove").onLast().performClick()
        assertEquals("""{"target":"lamp"}""", last("row", "remove").interaction.value.toString())
        runBlocking { session.close() }
    }

    @Test fun gridCarouselAndTileControlActionsNeverUseTheirContainerOrAnotherTilesHandler() {
        show(snapshot("$grid,$carousel")); mount()
        rule.onNodeWithText("Scene A").performClick(); last("tile-a", "pick")
        rule.onNode(hasContentDescription("Scene B") and isToggleable()).performClick()
        assertEquals("""{"scene":"true"}""", last("tile-b", "switch", "change").interaction.value.toString())
        rule.onNodeWithContentDescription("Actions").performClick()
        rule.onNodeWithText("Reset").performClick(); last("tile-b", "reset")
        rule.onNodeWithText("Bedroom").performClick(); last("room", "pick")
        runBlocking {
            assertFalse(session.event("grid", "pick", "press", JsonNull))
            assertFalse(session.event("tile-b", "pick", "press", JsonNull))
            assertFalse(session.event("carousel", "pick", "press", JsonNull))
            session.close()
        }
    }

    @Test fun nativeGaugeTilesDrawRingsAndKeepExactMeterReadouts() {
        val gauges = """{"type":"grid","key":"gauges","max_columns":2,"tiles":[{"key":"free","title":"Used","gauge":{"ratio":0.5},"action":{"id":"open"}},{"key":"colored","title":"Storage","value":"42","gauge":{"ratio":0.424,"unit":"GB","color":"green"}}]}"""
        val view = Json.parseToJsonElement(snapshot(gauges)).jsonObject.getValue("view").jsonObject
        assertEquals(emptyList<String>(), NativeExtensionProfile.gaps(view))
        show(snapshot(gauges)); mount()
        rule.onNodeWithTag("native-gauge-free", useUnmergedTree = true).performScrollTo().assertIsDisplayed()
        rule.onNodeWithTag("native-gauge-colored", useUnmergedTree = true).assertExists()
        val free = rule.onNodeWithContentDescription("Used: 50%")
        free.assertIsDisplayed()
        assertEquals(ProgressBarRangeInfo(50f, 0f..100f), free.fetchSemanticsNode().config[SemanticsProperties.ProgressBarRangeInfo])
        free.performClick(); last("free", "open")
        val colored = rule.onNodeWithContentDescription("Storage: 42 GB, 42%")
        colored.assertIsDisplayed()
        assertEquals(ProgressBarRangeInfo(42f, 0f..100f), colored.fetchSemanticsNode().config[SemanticsProperties.ProgressBarRangeInfo])
        runBlocking { session.close() }
    }

    @Test fun nativeGaugeKeepsActiveStateAndWholeTileSwitch() {
        val gauge = """{"type":"grid","key":"gauges","tiles":[{"key":"active","title":"Lamp quota","active":true,"gauge":{"ratio":0.5},"control":{"kind":"switch","name":"enabled","on":false,"action":{"id":"toggle"}}}]}"""
        show(snapshot(gauge)); mount()
        val node = rule.onNodeWithContentDescription("Lamp quota: 50%")
        node.assertIsDisplayed()
        assertEquals("On", node.fetchSemanticsNode().config[SemanticsProperties.StateDescription])
        node.performClick()
        assertEquals("""{"enabled":"true"}""", last("active", "toggle", "change").interaction.value.toString())
        runBlocking { session.close() }
    }

    @Test fun disabledControlsCannotEditOrExecuteButIndependentRowActionsCan() {
        show(snapshot(row.replace("\"on\":false", "\"on\":false,\"disabled\":true"))); mount()
        rule.onNode(hasContentDescription("Lamp") and isToggleable()).assertIsNotEnabled().performClick()
        rule.waitForIdle()
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        assertTrue(session.state.value.drafts.isEmpty())
        rule.onNodeWithText("Lamp").performClick(); last("row", "primary")
        runBlocking { assertFalse(session.edit("row", JsonPrimitive("true"))); session.close() }
    }

    @Test fun rowAndTileDefaultFalseValuesAreIncludedInSubmitWithoutMutatingRawPresence() {
        val button = """{"type":"button","key":"submit","title":"Submit","action":{"id":"submit","submit":true}}"""
        show(snapshot("${row.replace("\"on\":false,", "")},${grid.replace("\"on\":false,", "")},$button")); mount()
        rule.onNodeWithText("Submit").performClick()
        assertEquals("""{"enabled":"false","scene":"false"}""", last("submit", "submit", "press").interaction.value.toString())
        assertFalse(session.state.value.tree!!.nodes.getValue("row").getValue("control").jsonObject.containsKey("on"))
        runBlocking { session.close() }
    }

    @Test fun exactFieldProfileFallsBackInsteadOfDroppingUnsupportedCallbacksOrMediaProvenance() {
        val inputs = listOf(
            """{"type":"button","key":"button","title":"Reply","action":{"id":"reply","reply":"Synthetic reply"}}""" to "reply",
            """{"type":"field","key":"secret","name":"secret","label":"Secret","kind":"secret","value":"must-not-prefill"}""" to "kind",
            """{"type":"image","key":"image","src":{"ref":"camera","path":"/api/v1/extensions/example/-/image/camera"},"alt":"Camera"}""" to "hub_ref",
            """{"type":"row","key":"row","title":"Trend","spark":{"values":[0,1]}}""" to "spark",
            """{"type":"text","key":"title","text":"Title","style":"title"}""" to "style",
            """{"type":"grid","key":"grid","tiles":[{"key":"active","title":"Active scene","active":true}]}""" to "active",
            """{"type":"grid","key":"grid","tiles":[{"key":"gauge","title":"Gauge","gauge":{"ratio":0.5},"image":{"url":"https://example.com/picture.png"}}]}""" to "image",
        )
        inputs.forEach { (item, gap) ->
            val view = Json.parseToJsonElement(snapshot(item)).jsonObject.getValue("view").jsonObject
            assertTrue(NativeExtensionProfile.gaps(view).any { it.endsWith(gap) })
        }
        show(snapshot(inputs[0].first))
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) {
            ExtensionUiPage(null, "example", "page", "Page", {}, availableAdapter = adapter)
        } }
        rule.onNodeWithText("This page needs a newer native renderer. Showing the compatible page instead.").assertExists()
        assertEquals(NativeStreamPhase.Closed, session.state.value.phase)
        assertNull(session.state.value.tree)
        rule.waitUntil(5000) {
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            rule.waitForIdle()
            requests.filterIsInstance<ExtensionUIStreamCloseRequest>().size == 1
        }
        assertEquals(1, requests.filterIsInstance<ExtensionUIStreamCloseRequest>().size)
    }

    @Test fun actionCarriedValuesAreNotInterpretedAsSchemaFieldsAndConfirmFreezesInputValues() {
        val field = """{"type":"field","key":"body","name":"body","label":"Body","kind":"text","value":"displayed"}"""
        val button = """{"type":"button","key":"send","title":"Send","action":{"id":"send","submit":true,"confirm":"Send saved values?","values":{"color":"blue","target":"selected"}}}"""
        show(snapshot("$field,$button")); mount()
        rule.onNodeWithText("Send").performClick()
        runBlocking { session.edit("body", JsonPrimitive("newer typing")) }
        rule.onAllNodesWithText("Send").onLast().performClick()
        assertEquals("""{"body":"displayed","color":"blue","target":"selected"}""", last("send", "send", "press").interaction.value.toString())
        runBlocking { session.close() }
    }

    @Test fun adapterRemountKeepsDraftScrollAndOpenReviewInputWithoutClosingTheSession() {
        val text = """{"type":"field","key":"draft","name":"body","label":"Draft","kind":"text","value":"initial"}"""
        show(snapshot(text))
        var mounted by mutableStateOf(true)
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { if (mounted) NativeExtensionStreamPage(adapter, "Page", {}) } }
        rule.onNodeWithText("initial").performTextReplacement("retained draft")
        rule.waitForIdle()
        assertEquals("draft" to "input", adapter.local.focused)
        mounted = false; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        assertEquals(NativeStreamPhase.Suspended, session.state.value.phase)
        assertEquals("draft" to "input", adapter.local.focused)
        assertTrue(requests.none { it is ExtensionUIStreamCloseRequest })
        mounted = true; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        rule.onNodeWithText("retained draft").assertExists().assertIsFocused()
        assertEquals(NativeStreamPhase.Ready, session.state.value.phase)
        runBlocking { session.close() }
    }

    private val diff = """{"type":"diff","key":"diff","path":"file.txt","status":"modified","revision":"first","hunks":[{"old_start":3,"new_start":3,"lines":[" first"," second"]}],"threads":[{"id":"thread","side":"new","line":3,"comments":[{"author":"Demo","body":"Review this line"}],"reply":{"id":"reply"},"resolve":{"id":"resolve"}}],"expand":{"id":"expand"},"comment":{"id":"comment"},"draft":{"id":"draft"},"mark_viewed":{"id":"mark"}}"""

    @Test fun searchFilterAndMoreRetainLocalStateAcrossRemountWithoutAutomaticReplay() {
        val extra = "\"search\":{\"placeholder\":\"Search items\",\"value\":\"\",\"action\":{\"id\":\"search\"}},\"filter\":{\"name\":\"scope\",\"title\":\"Scope\",\"value\":\"all\",\"choices\":[{\"value\":\"all\",\"title\":\"All\"},{\"value\":\"mine\",\"title\":\"Mine\"}],\"action\":{\"id\":\"filter\"}},"
        val payload = snapshot("""{"type":"text","key":"text","text":"Result"}""", extra).replace("\"items\":[", "\"more\":{\"title\":\"More results\",\"action\":{\"id\":\"more\"}},\"items\":[")
        show(payload)
        var mounted by mutableStateOf(true)
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { if (mounted) NativeExtensionStreamPage(adapter, "Page", {}) } }
        last("section", "more")
        rule.onNode(hasSetTextAction()).performTextInput("local query")
        rule.waitUntil(5000) { requests.filterIsInstance<ExtensionUIStreamEventRequest>().any { it.interaction.action == "search" } }
        assertEquals("""{"q":"local query"}""", last("root", "search", "change").interaction.value.toString())
        rule.onNodeWithText("All").performClick(); rule.onNodeWithText("Mine").performClick()
        assertEquals("""{"scope":"mine","q":"local query"}""", last("root", "filter", "change").interaction.value.toString())
        val count = requests.filterIsInstance<ExtensionUIStreamEventRequest>().size
        mounted = false; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        mounted = true; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        rule.onNodeWithText("local query").assertExists(); rule.onNodeWithText("Mine").assertExists()
        assertEquals(count, requests.filterIsInstance<ExtensionUIStreamEventRequest>().size)
        rule.onNodeWithText("More results").performClick(); last("section", "more")
        runBlocking { session.close() }
    }

    @Test fun untouchedSliderReadsNewAuthoritativeValueWhileAnEditableDraftKeepsPriority() {
        val slider = """{"type":"row","key":"slider","title":"Brightness","control":{"kind":"slider","name":"brightness","value":0,"min":0,"max":10,"step":1,"action":{"id":"slide"}}}"""
        show(snapshot(slider)); mount()
        fun current() = rule.onNodeWithContentDescription("Brightness").fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsProperties.ProgressBarRangeInfo].current
        assertEquals(0f, current())
        fun delta(value: Int, base: Int) = """{"protocol":1,"session":"session","epoch":"epoch","base_version":$base,"version":${base + 1},"operations":[{"op":"replace","key":"slider","item":${slider.replace("\"value\":0", "\"value\":$value")}}]}"""
        runBlocking { assertTrue(session.delta(identity, delta(2, 1))) }
        rule.waitForIdle(); assertEquals(2f, current())
        runBlocking { session.edit("slider", JsonPrimitive("4")); assertTrue(session.delta(identity, delta(8, 2))) }
        rule.waitForIdle(); assertEquals(4f, current())
        assertEquals(JsonPrimitive(8), session.state.value.tree!!.nodes.getValue("slider").getValue("control").jsonObject["value"])
        runBlocking { adapter.close() }
    }

    @Test fun sliderFinishAndStepperDebounceUseTheirOwnChangeSlotsAndKeepExactZero() {
        val slider = """{"type":"row","key":"slider","title":"Brightness","control":{"kind":"slider","name":"brightness","value":0,"min":0,"max":10,"step":1,"action":{"id":"slide"}}}"""
        val stepper = """{"type":"row","key":"stepper","title":"Temperature","control":{"kind":"stepper","name":"temperature","value":0,"min":0,"max":10,"step":0.5,"action":{"id":"step"}}}"""
        show(snapshot("$slider,$stepper")); mount()
        assertEquals(JsonPrimitive(0), session.state.value.tree!!.nodes.getValue("slider").getValue("control").jsonObject["value"])
        rule.onNodeWithContentDescription("Brightness").performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.SetProgress) { it(5f) }
        val slide = last("slider", "slide", "change")
        assertEquals("5.0", slide.interaction.value.jsonObject.getValue("brightness").jsonPrimitive.content)
        rule.onNodeWithContentDescription("Increase Temperature").performClick()
        rule.onNodeWithContentDescription("Increase Temperature").performClick()
        rule.waitUntil(5000) { requests.filterIsInstance<ExtensionUIStreamEventRequest>().any { it.interaction.action == "step" } }
        assertEquals("1", last("stepper", "step", "change").interaction.value.jsonObject.getValue("temperature").jsonPrimitive.content)
        runBlocking { session.close() }
    }

    @Test fun diffLineDraftReplyResolveExpandAndViewedActionsBindTheDiffNode() {
        show(snapshot(diff, "\"review\":{\"count\":1,\"name\":\"summary\",\"buttons\":[{\"title\":\"Approve\",\"action\":{\"id\":\"approve\"}}]},")); mount()
        rule.onNodeWithText("first").performClick()
        rule.onAllNodes(hasSetTextAction()).onFirst().performTextInput("line draft")
        rule.onNodeWithText("Add to review").performClick()
        assertEquals("""{"path":"file.txt","side":"new","line":"3","body":"line draft"}""", last("diff", "draft").interaction.value.toString())
        rule.onNodeWithText("first").performClick()
        rule.onNodeWithText("line draft").assertExists()
        rule.onNodeWithText("Comment now").performClick(); last("diff", "comment")
        rule.onNodeWithText("Resolve").performClick()
        assertEquals("""{"thread":"thread","resolved":"true"}""", last("diff", "resolve").interaction.value.toString())
        rule.onNodeWithText("Reply…").performClick()
        rule.onAllNodes(hasSetTextAction()).onFirst().performTextInput("reply body")
        rule.onNodeWithText("Comment").performClick()
        assertEquals("""{"thread":"thread","body":"reply body"}""", last("diff", "reply").interaction.value.toString())
        rule.onNodeWithText("Show lines below").performClick(); last("diff", "expand")
        rule.onNodeWithContentDescription("Viewed").performClick()
        assertEquals("""{"path":"file.txt","viewed":"true"}""", last("diff", "mark").interaction.value.toString())
        runBlocking { session.close() }
    }

    @Test fun failedViewedMarkRollsBackOnlyTheConfirmedNodesOverlay() {
        show(snapshot(diff)); mount()
        rule.onNodeWithContentDescription("Viewed").performClick()
        rule.waitForIdle()
        val request = requests.filterIsInstance<ExtensionUIStreamEventRequest>().single()
        assertEquals("diff", request.interaction.key)
        val id = ExtensionCodeReview.changeId("file.txt", "first", emptyList())
        assertEquals(true, adapter.local.review("diff").over[id])
        runBlocking { adapter.rejected(identity.copy(hubGroup = "other"), request.requestId) }
        assertEquals(true, adapter.local.review("diff").over[id])
        runBlocking { adapter.rejected(identity, request.requestId) }
        assertFalse(adapter.local.review("diff").over.containsKey(id))
        rule.onNodeWithContentDescription("Viewed").assertIsOff()
        runBlocking { adapter.close() }
        assertTrue(adapter.local.reviews.isEmpty())
    }

    @Test fun legacyFileTreeOnlyViewedMarksKeepTheirLocalOverlay() {
        val view = persona.core.contracts.JsonCodec.decodeFromString(persona.core.contracts.api.ExtensionView.serializer(),
            """{"sections":[{"items":[{"type":"file_tree","entries":[{"path":"file.txt","kind":"file"}]}]}]}""")
        val review = CodeReviewState(null) { _, _, _ -> fail("A local file-tree mark must not execute a callback") }
        review.setViewed("file.txt", view, true)
        assertTrue(review.viewed("file.txt", view))
        review.setViewed("file.txt", view, false)
        assertFalse(review.viewed("file.txt", view))
    }

    @Test fun cancelledViewedConfirmationRestoresTheOriginalOverlayWithoutSending() {
        show(snapshot(diff.replace("\"id\":\"mark\"", "\"id\":\"mark\",\"confirm\":\"Mark this file viewed?\""))); mount()
        rule.onNodeWithContentDescription("Viewed").performClick()
        rule.onNodeWithText("Mark this file viewed?").assertExists()
        rule.onNodeWithText("Cancel").performClick()
        rule.onNodeWithContentDescription("Viewed").assertIsOff()
        assertTrue(adapter.local.review("diff").over.isEmpty())
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        runBlocking { adapter.close() }
    }

    @Test fun retainedStepperDebounceUsesTheRemountedRendererAndSavedValue() {
        val stepper = """{"type":"row","key":"stepper","title":"Temperature","control":{"kind":"stepper","name":"temperature","value":0,"min":0,"max":10,"step":1,"action":{"id":"step"}}}"""
        show(snapshot(stepper))
        var mounted by mutableStateOf(true)
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { if (mounted) NativeExtensionStreamPage(adapter, "Page", {}) } }
        rule.onNodeWithContentDescription("Increase Temperature").assertExists()
        rule.mainClock.autoAdvance = false
        rule.onNodeWithContentDescription("Increase Temperature").performClick()
        rule.mainClock.advanceTimeByFrame(); rule.waitForIdle()
        assertEquals(JsonPrimitive("1"), session.state.value.drafts["stepper"]?.value)
        mounted = false; Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeByFrame(); rule.waitForIdle()
        assertEquals(NativeStreamPhase.Suspended, session.state.value.phase)
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        mounted = true; Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame(); Snapshot.sendApplyNotifications(); rule.waitForIdle() }
        assertEquals(NativeStreamPhase.Ready, session.state.value.phase)
        assertFalse("No rejection before debounce", adapter.local.rejected)
        rule.mainClock.advanceTimeBy(NativeStreamLayout.stepperPauseMillis + 100)
        rule.waitForIdle()
        assertNull("The resumed debounce must run", adapter.local.slot<Any?>("stepper", "stepper") { null }.value)
        assertFalse("A still-valid resumed binding must not be rejected", adapter.local.rejected)
        val request = requests.filterIsInstance<ExtensionUIStreamEventRequest>().single()
        assertEquals("1", request.interaction.value.jsonObject["temperature"]?.jsonPrimitive?.content)
        assertEquals("step", request.interaction.action)
        runBlocking { session.acknowledged(identity, request.requestId); adapter.close() }
    }

    @Test fun changedStepperBindingRejectsTheRetainedDebounceAndSettlesItsDraft() {
        val stepper = """{"type":"row","key":"stepper","title":"Temperature","control":{"kind":"stepper","name":"temperature","value":0,"min":0,"max":10,"action":{"id":"step"}}}"""
        show(snapshot(stepper))
        var mounted by mutableStateOf(true)
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { if (mounted) NativeExtensionStreamPage(adapter, "Page", {}) } }
        rule.onNodeWithContentDescription("Increase Temperature").assertExists()
        rule.mainClock.autoAdvance = false
        rule.onNodeWithContentDescription("Increase Temperature").performClick()
        rule.mainClock.advanceTimeByFrame(); rule.waitForIdle()
        mounted = false; Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeByFrame(); rule.waitForIdle()
        resumePayload = snapshot(stepper.replace("\"id\":\"step\"", "\"id\":\"replacement\""))
        mounted = true; Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame(); Snapshot.sendApplyNotifications(); rule.waitForIdle() }
        assertEquals(NativeStreamPhase.Ready, session.state.value.phase)
        rule.mainClock.advanceTimeBy(NativeStreamLayout.stepperPauseMillis + 100); rule.waitForIdle()
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        assertTrue(session.state.value.drafts.isEmpty()); assertTrue(adapter.local.rejected)
        runBlocking { adapter.close() }
    }

    @Test fun rejectedSelectionsAndNormalizedFormRenderAuthorityInAllStyles() {
        val field = """{"type":"field","key":"body","name":"body","label":"Title","kind":"text","value":"Original title"}"""
        val submit = """{"type":"button","key":"save","title":"Save","action":{"id":"save","submit":true}}"""
        val extras = "\"title\":\"Selection and form recovery\",\"tabs\":{\"name\":\"tab\",\"value\":\"first\",\"items\":[{\"value\":\"first\",\"title\":\"First tab\"},{\"value\":\"second\",\"title\":\"Second tab\"}],\"action\":{\"id\":\"tab\"}},\"filter\":{\"name\":\"scope\",\"value\":\"all\",\"choices\":[{\"value\":\"all\",\"title\":\"All items\"},{\"value\":\"mine\",\"title\":\"My items\"}],\"action\":{\"id\":\"filter\"}},"
        show(snapshot("$field,$submit", extras))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("repair-shot").fillMaxSize().background(PersonaTheme.palette.background)) { NativeExtensionStreamPage(adapter, "Page", {}) }
        } }
        rule.onNodeWithText("Second tab").performClick(); rule.waitForIdle()
        val tab = requests.filterIsInstance<ExtensionUIStreamEventRequest>().last()
        assertEquals("second", adapter.local.roots["tab"])
        runBlocking { adapter.rejected(identity, tab.requestId) }
        rule.waitForIdle(); assertFalse(adapter.local.roots.containsKey("tab")); assertTrue(adapter.targets.isEmpty())
        rule.onNodeWithText("All items").performClick(); rule.onNodeWithText("My items").performClick(); rule.waitForIdle()
        val filter = requests.filterIsInstance<ExtensionUIStreamEventRequest>().last()
        assertEquals("mine", adapter.local.roots["scope"])
        runBlocking { adapter.rejected(identity, filter.requestId) }
        rule.waitForIdle(); assertFalse(adapter.local.roots.containsKey("scope")); assertTrue(adapter.targets.isEmpty())
        rule.onNodeWithText("Original title").performTextReplacement("  Saved title  "); rule.waitForIdle()
        rule.onNodeWithText("Save").performClick(); rule.waitForIdle()
        val saved = requests.filterIsInstance<ExtensionUIStreamEventRequest>().last()
        assertEquals("  Saved title  ", saved.interaction.value.jsonObject["body"]?.jsonPrimitive?.content)
        runBlocking {
            assertTrue(session.delta(identity, """{"protocol":1,"session":"session","epoch":"epoch","base_version":1,"version":2,"operations":[{"op":"replace","key":"body","item":${field.replace("Original title", "Saved title")}}]}"""))
            session.received(identity, saved.requestId, 2)
        }
        rule.onNodeWithText("Saved title").assertExists(); assertTrue(session.state.value.drafts.isEmpty())
        val out = File("build/native-stream-repair-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) {
            val sums = mutableListOf<Long>()
            for (d in listOf(false, true)) {
                style = s; dark = d; Snapshot.sendApplyNotifications(); rule.waitForIdle()
                rule.onNodeWithText("Saved title").assertIsDisplayed(); rule.onNodeWithText("All items").assertIsDisplayed()
                val bitmap = rule.onNodeWithTag("repair-shot").captureToImage().asAndroidBitmap()
                var sum = 0L
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) sum += bitmap.getPixel(x, y)
                sums += sum
                File(out, "${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
            assertNotEquals(sums[0], sums[1])
        }
        runBlocking { adapter.close() }
    }

    @Test fun reviewSummaryConfirmationAndDiscardBindRootAndDoNotEraseLocalDraft() {
        val review = "\"review\":{\"count\":1,\"name\":\"summary\",\"discard\":{\"id\":\"discard\"},\"buttons\":[{\"title\":\"Approve\",\"action\":{\"id\":\"approve\",\"confirm\":\"Approve saved summary?\",\"values\":{\"review\":\"selected\"}}}]},"
        show(snapshot(diff, review)); mount()
        rule.onAllNodesWithText("Submit 1").onFirst().performClick()
        rule.onAllNodes(hasSetTextAction()).onFirst().performTextInput("summary draft")
        rule.onNodeWithText("Approve").performClick()
        rule.onNodeWithText("Approve saved summary?").assertExists()
        rule.onNodeWithText("OK").performClick()
        assertEquals("""{"summary":"summary draft","review":"selected"}""", last("root", "approve", "submit").interaction.value.toString())
        rule.onAllNodesWithText("Submit 1").onFirst().performClick()
        rule.onNodeWithText("summary draft").assertExists()
        rule.onNodeWithText("Discard drafts").performClick(); last("root", "discard")
        runBlocking { session.close() }
    }

    @Test fun sharedNativePresenceAndCompleteSchemaVocabularyAreRenderedOrExplicitlyRefused() {
        val examples = generateSequence(File(System.getProperty("user.dir"))) { it.parentFile }
            .map { File(it, "schema/extension/examples") }.first { it.isDirectory }
        val names = listOf("View.code-review.json", "View.v6-controls.json", "View.v6-media.json", "View.v6-tiles.json", "View.v6-inputs.json", "View.v6-charts.json", "showcase/row.json")
        names.forEach { name ->
            val view = Json.parseToJsonElement(File(examples, name).readText()).jsonObject
            val gaps = NativeExtensionProfile.gaps(view)
            assertTrue(name, gaps.all { it.startsWith("view.") && !it.contains("https://") })
            if (name.startsWith("View.v6-") && name != "View.v6-media.json") assertTrue("$name must disclose its missing optional fields", gaps.isNotEmpty())
        }
        // Graph stays on the legacy renderer so native streams receive the hub's downgraded rows (#9727).
        assertEquals(NativeExtensionStreamAdapter.Components, ExtensionComponent.TYPES.filterNot { it in setOf("html", "graph", "scene") }.toSet())
        assertTrue(listOf("html", "graph", "scene").none { it in NativeExtensionStreamAdapter.Components })
        val raw = Json.parseToJsonElement("""{"sections":[{"items":[{"type":"row","title":"Zero","control":{"kind":"stepper","name":"zero","value":0,"min":0,"max":10,"action":{"id":"step"}}},{"type":"image","key":"image","src":{"url":"https://example.com/a.png","width":1,"height":1},"alt":"Empty caption","caption":"","no_zoom":false}]}]}""").jsonObject
        assertTrue(NativeExtensionProfile.gaps(raw).isEmpty())
        assertEquals(JsonPrimitive(0), raw.getValue("sections").jsonArray[0].jsonObject.getValue("items").jsonArray[0].jsonObject.getValue("control").jsonObject["value"])
    }

    @Test fun aZoomablePictureSaysATapOpensIt() {
        val zoomable = """{"type":"image","key":"zoomable","src":{"url":"https://example.com/zoom.png"},"alt":"Zoomable picture"}"""
        show(snapshot("$image,$zoomable")); mount()
        rule.onNode(hasClickAction() and hasContentDescription("Zoomable picture")).assert(SemanticsMatcher("a picture a tap views (#4176)") {
            it.config.getOrNull(SemanticsActions.OnClick)?.label == "View image" && it.config.getOrNull(SemanticsProperties.Role) == Role.Image
        })
        rule.onNodeWithContentDescription("Picture unavailable").assert(!hasClickAction())
        runBlocking { adapter.close() }
    }

    @Test fun cancellingConfirmedControlDoesNotLeaveACancelledValueInSubmit() {
        val button = """{"type":"button","key":"submit","title":"Submit","action":{"id":"submit","submit":true}}"""
        show(snapshot("$row,$button")); mount()
        rule.onNode(hasContentDescription("Lamp") and isToggleable()).performClick()
        rule.onNodeWithText("Change lamp?").assertExists()
        rule.onNodeWithText("Cancel").performClick()
        rule.onNode(hasContentDescription("Lamp") and isToggleable()).assertIsOff()
        rule.onNodeWithText("Submit").performClick()
        assertEquals("""{"enabled":"false"}""", last("submit", "submit", "press").interaction.value.toString())
        runBlocking { adapter.close() }
    }

    @Test fun cancellingConfirmedNumericControlPreservesAnEarlierDraft() {
        val control = """{"type":"row","key":"stepper","title":"Temperature","control":{"kind":"stepper","name":"temperature","value":0,"min":0,"max":10,"step":1,"action":{"id":"step","confirm":"Change temperature?"}}}"""
        show(snapshot(control)); mount()
        runBlocking { session.edit("stepper", JsonPrimitive("3")) }
        rule.onNodeWithContentDescription("Increase Temperature").performClick()
        rule.waitUntil(5000) { rule.onAllNodesWithText("Change temperature?").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Cancel").performClick()
        assertEquals(JsonPrimitive("3"), session.state.value.drafts.getValue("stepper").value)
        rule.onNodeWithText("3").assertExists()
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        runBlocking { adapter.close() }
    }

    @Test fun numericSubmitDefaultsMatchTheDisplayedClampedValueAndKeepRawAbsence() {
        val slider = """{"type":"row","key":"slider","title":"Brightness","control":{"kind":"slider","name":"brightness","min":10,"max":20,"step":1,"action":{"id":"slide"}}}"""
        val stepper = """{"type":"row","key":"stepper","title":"Temperature","control":{"kind":"stepper","name":"temperature","min":-20,"max":-10,"action":{"id":"step"}}}"""
        val button = """{"type":"button","key":"submit","title":"Submit","action":{"id":"submit","submit":true}}"""
        show(snapshot("$slider,$stepper,$button")); mount()
        rule.onNodeWithText("10").assertExists(); rule.onNodeWithText("-10").assertExists()
        rule.onNodeWithText("Submit").performClick()
        assertEquals("""{"brightness":"10","temperature":"-10"}""", last("submit", "submit", "press").interaction.value.toString())
        assertFalse(session.state.value.tree!!.nodes.getValue("slider").getValue("control").jsonObject.containsKey("value"))
        runBlocking { adapter.close() }
    }

    @Test fun sliderRangesMustRemainDistinctAfterNativeFloatConversion() {
        val slider = """{"type":"row","key":"slider","title":"Range","control":{"kind":"slider","name":"range","min":16777216,"max":16777217,"step":1,"action":{"id":"slide"}}}"""
        val raw = Json.parseToJsonElement(snapshot(slider)).jsonObject.getValue("view").jsonObject
        assertTrue(NativeExtensionProfile.gaps(raw).any { it.endsWith("control.range") })
        val invisibleStep = Json.parseToJsonElement(snapshot(slider.replace("16777217", "16777316"))).jsonObject.getValue("view").jsonObject
        assertTrue(NativeExtensionProfile.gaps(invisibleStep).any { it.endsWith("control.range") })
    }

    @Test fun splitPaneDoesNotFilterOutNativeImagesGridsCarouselsOrMultilineInputs() {
        val field = """{"type":"field","key":"body","name":"body","label":"Body","kind":"multiline","value":"Side draft"}"""
        val payload = snapshot("$grid,$carousel,$image,$field").replace("\"key\":\"section\"", "\"key\":\"section\",\"title\":\"Sidebar\",\"pane\":\"side\"")
        show(payload.replace("\"view\":{", "\"view\":{\"layout\":\"split\",")); mount()
        rule.onNodeWithText("Sidebar").performClick()
        rule.onNodeWithText("Scene A").assertExists().performClick(); last("tile-a", "pick")
        rule.onNodeWithText("Bedroom").assertExists()
        rule.onNodeWithText("Picture unavailable").assertExists()
        rule.onNodeWithText("Side draft").assertExists()
        runBlocking { adapter.close() }
    }

    @Test fun automaticMoreCanLoadNextPageWithTheSameDescriptorWithoutRemountReplay() {
        val payload = snapshot("""{"type":"text","key":"text","text":"Result"}""").replace("\"items\":[", "\"more\":{\"title\":\"More results\",\"action\":{\"id\":\"more\"}},\"items\":[")
        show(payload); mount()
        last("section", "more")
        runBlocking { assertTrue(session.delta(identity, """{"protocol":1,"session":"session","epoch":"epoch","base_version":1,"version":2,"operations":[{"op":"insert","parent":"section","item":{"type":"text","key":"next","text":"Next result"}}]}""")) }
        rule.waitForIdle()
        assertEquals(2, requests.filterIsInstance<ExtensionUIStreamEventRequest>().size)
        assertEquals(2L, requests.filterIsInstance<ExtensionUIStreamEventRequest>().last().interaction.version)
        runBlocking { adapter.close() }
    }

    @Test fun remountedDiffDraftRequiresReselectingLinesAfterARevisionChange() {
        show(snapshot(diff))
        var mounted by mutableStateOf(true)
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { if (mounted) NativeExtensionStreamPage(adapter, "Page", {}) } }
        rule.onNodeWithText("first").performClick()
        rule.onAllNodes(hasSetTextAction()).onFirst().performTextInput("retained comment")
        mounted = false; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        resumePayload = snapshot(diff.replace("\"revision\":\"first\"", "\"revision\":\"second\"").replace(" first", " changed"), version = 2)
        mounted = true; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        rule.onNodeWithText("changed").assertExists()
        rule.onNodeWithText("Comment now").assertDoesNotExist()
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        rule.onNodeWithText("changed").performClick()
        rule.onNodeWithText("retained comment").assertExists()
        runBlocking { adapter.close() }
    }

    @Test fun aTerminalInvalidAllocationUsesFallbackRatherThanAnUnrecoverableSpinner() {
        show(snapshot("""{"type":"divider","key":"divider"}"""), components = listOf("text"))
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) {
            ExtensionUiPage(null, "example", "page", "Page", {}, availableAdapter = adapter)
        } }
        assertEquals(NativeStreamPhase.Closed, session.state.value.phase)
        rule.onNodeWithText("This page needs a newer native renderer. Showing the compatible page instead.").assertExists()
    }

    @Test fun allNativeHorizontalAnchorsSurviveRendererRemount() {
        val wide = "WideLabel".repeat(12)
        val code = """{"type":"code","key":"code","path":"file.txt","text":"$wide"}"""
        val table = """{"type":"table","key":"table","columns":[{"title":"$wide"}],"rows":[["Value"]]}"""
        val crumbs = """{"type":"breadcrumb","key":"crumbs","crumbs":[{"title":"$wide"},{"title":"Current"}]}"""
        val tiles = (1..5).joinToString(",") { """{"key":"tile-$it","title":"Tile $it"}""" }
        val carousel = """{"type":"carousel","key":"carousel","tiles":[$tiles]}"""
        val tabLabel = "TabTitle".repeat(5)
        val tabs = "\"tabs\":{\"name\":\"scope\",\"value\":\"first\",\"items\":[{\"value\":\"first\",\"title\":\"$tabLabel\"},{\"value\":\"second\",\"title\":\"$tabLabel\"}],\"action\":{\"id\":\"tab\"}},"
        show(snapshot("$code,$table,$crumbs,$carousel", tabs))
        var mounted by mutableStateOf(true)
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) { if (mounted) NativeExtensionStreamPage(adapter, "Page", {}) } }
        val horizontal = SemanticsMatcher.keyIsDefined(androidx.compose.ui.semantics.SemanticsProperties.HorizontalScrollAxisRange)
        rule.onAllNodes(horizontal).assertCountEquals(5)
        repeat(5) { i -> rule.onAllNodes(horizontal)[i].performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.ScrollBy) { it(100f, 0f) } }
        rule.waitForIdle()
        fun anchors() = rule.onAllNodes(horizontal).fetchSemanticsNodes().map { it.config[androidx.compose.ui.semantics.SemanticsProperties.HorizontalScrollAxisRange].value() }
        val before = anchors()
        assertTrue(before.toString(), before.all { it > 0 })
        mounted = false; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        mounted = true; Snapshot.sendApplyNotifications(); rule.waitForIdle()
        assertEquals(before, anchors())
        runBlocking { adapter.close() }
    }

    @Test fun tabChangesCarryTheCurrentSearchAndCancelledTabsDoNotAlterSubmit() {
        val extras = "\"search\":{\"value\":\"query\",\"action\":{\"id\":\"search\"}},\"tabs\":{\"name\":\"scope\",\"value\":\"first\",\"items\":[{\"value\":\"first\",\"title\":\"First\"},{\"value\":\"second\",\"title\":\"Second\"}],\"action\":{\"id\":\"tab\",\"confirm\":\"Change scope?\"}},"
        val button = """{"type":"button","key":"submit","title":"Submit","action":{"id":"submit","submit":true}}"""
        show(snapshot(button, extras)); mount()
        rule.onNodeWithText("Second").performClick(); rule.onNodeWithText("Cancel").performClick()
        rule.onNodeWithText("Submit").performClick()
        assertEquals("""{"scope":"first"}""", last("submit", "submit", "press").interaction.value.toString())
        rule.onNodeWithText("Second").performClick(); rule.onNodeWithText("OK").performClick()
        assertEquals("""{"scope":"second","q":"query"}""", last("root", "tab", "change").interaction.value.toString())
        runBlocking { adapter.close() }
    }

    @Test fun confirmedSliderCancelRestoresItsOriginalValueAndConfirmationPreservesLaterTyping() {
        val slider = """{"type":"row","key":"slider","title":"Brightness","control":{"kind":"slider","name":"brightness","value":0,"min":0,"max":10,"step":1,"action":{"id":"slide","confirm":"Change brightness?"}}}"""
        show(snapshot(slider)); mount()
        fun set(value: Float) = rule.onNodeWithContentDescription("Brightness").performSemanticsAction(androidx.compose.ui.semantics.SemanticsActions.SetProgress) { it(value) }
        fun current() = rule.onNodeWithContentDescription("Brightness").fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsProperties.ProgressBarRangeInfo].current
        set(5f); rule.onNodeWithText("Cancel").performClick()
        assertEquals(0f, current()); assertTrue(session.state.value.drafts.isEmpty())
        set(6f)
        runBlocking { session.edit("slider", JsonPrimitive("8")) }
        rule.onNodeWithText("OK").performClick()
        val event = last("slider", "slide", "change")
        assertEquals("6.0", event.interaction.value.jsonObject.getValue("brightness").jsonPrimitive.content)
        assertEquals(JsonPrimitive("8"), session.state.value.drafts.getValue("slider").value)
        assertEquals(8f, current())
        runBlocking { adapter.close() }
    }

    @Test fun autoImageDimensionsCannotProduceAnUnboundedNativeLayout() {
        val media = """{"type":"image","key":"image","src":{"url":"https://example.com/picture.png","width":1,"height":8192},"alt":"Tall picture","aspect":"auto"}"""
        show(snapshot(media))
        rule.setContent { PersonaTheme(style = AppStyle.Board, reduceMotion = true) {
            ExtensionUiPage(null, "example", "page", "Page", {}, availableAdapter = adapter)
        } }
        rule.onNodeWithText("This page needs a newer native renderer. Showing the compatible page instead.").assertExists()
        assertEquals(NativeStreamPhase.Closed, session.state.value.phase)
        val absentDimensions = Json.parseToJsonElement(snapshot(media.replace(",\"width\":1,\"height\":8192", ""))).jsonObject.getValue("view").jsonObject
        assertTrue(NativeExtensionProfile.gaps(absentDimensions).any { it.endsWith("media.aspect") })
    }

    @Test fun schemaIconMeaningsMustBeRenderedOrRefusedInsteadOfBecomingAnExtensionIcon() {
        val view = Json.parseToJsonElement(snapshot("""{"type":"row","key":"row","title":"Home","icon":"home"}""")).jsonObject.getValue("view").jsonObject
        assertEquals(PhIcons.House, ExtensionIcons.of("home"))
        assertFalse(NativeExtensionProfile.gaps(view).any { it.endsWith("icon") })
        val unknown = Json.parseToJsonElement(snapshot("""{"type":"row","key":"row","title":"Unknown","icon":"future-icon"}""")).jsonObject.getValue("view").jsonObject
        assertTrue(NativeExtensionProfile.gaps(unknown).any { it.endsWith("icon") })
        val schema = generateSequence(File(System.getProperty("user.dir"))) { it.parentFile }
            .map { File(it, "schema/extension/view.schema.json") }.first { it.isFile }
        Json.parseToJsonElement(schema.readText()).jsonObject.getValue("\$defs").jsonObject.getValue("NavIcon").jsonObject.getValue("enum").jsonArray.forEach { entry ->
            val icon = entry.jsonPrimitive.content
            val raw = Json.parseToJsonElement(snapshot("""{"type":"row","key":"row","title":"Meaning","icon":"$icon"}""")).jsonObject.getValue("view").jsonObject
            val needsFallback = icon !in setOf("plugin", "extension") && ExtensionIcons.of(icon) == ExtensionIcons.of("extension")
            assertEquals(icon, needsFallback, NativeExtensionProfile.gaps(raw).any { it.endsWith("icon") })
        }
    }

    @Test fun aRealRowSwipeUsesItsOwnActionAndStillRequiresConfirmation() {
        show(snapshot(row)); mount()
        rule.onNodeWithText("Lamp").performTouchInput { swipeLeft() }
        rule.onNodeWithText("Remove lamp?").assertExists()
        assertTrue(requests.none { it is ExtensionUIStreamEventRequest })
        rule.onAllNodesWithText("Remove").onLast().performClick()
        assertEquals("""{"target":"lamp"}""", last("row", "remove").interaction.value.toString())
        runBlocking { adapter.close() }
    }

    @Test fun allStylesLightDarkDrawNativeDiffReviewComponentEvidence() {
        show(snapshot(diff))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("diff-shot").fillMaxSize().background(PersonaTheme.palette.background)) { NativeExtensionStreamPage(adapter, "Review", {}) }
        } }
        val out = File("build/native-stream-coverage-shots").also { it.mkdirs() }
        AppStyle.entries.forEach { s ->
            val sums = mutableListOf<Long>()
            listOf(false, true).forEach { d ->
                style = s; dark = d; Snapshot.sendApplyNotifications(); rule.waitForIdle()
                rule.onNodeWithText("file.txt").assertExists(); rule.onNodeWithText("first").assertExists()
                val bitmap = rule.onNodeWithTag("diff-shot").captureToImage().asAndroidBitmap()
                var sum = 0L
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) sum += bitmap.getPixel(x, y)
                sums.add(sum)
                File(out, "diff-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
            assertNotEquals("Dark/light must differ", sums[0], sums[1])
        }
        runBlocking { session.close() }
    }

    @Test fun allStylesLightDarkRenderNativeRowsTilesAndMediaComponentEvidence() {
        show(snapshot("$row,$grid,$carousel,$image"))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("coverage-shot").fillMaxSize().background(PersonaTheme.palette.background)) { NativeExtensionStreamPage(adapter, "Page", {}) }
        } }
        val out = File("build/native-stream-coverage-shots").also { it.mkdirs() }
        AppStyle.entries.forEach { s ->
            val sums = mutableListOf<Long>()
            listOf(false, true).forEach { d ->
                style = s; dark = d; Snapshot.sendApplyNotifications(); rule.waitForIdle()
                rule.onNodeWithText("Lamp").assertExists(); rule.onNodeWithText("Scene A").assertExists()
                val bitmap = rule.onNodeWithTag("coverage-shot").captureToImage().asAndroidBitmap()
                var sum = 0L
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) sum += bitmap.getPixel(x, y)
                sums.add(sum)
                File(out, "${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
            assertNotEquals("Dark/light must differ", sums[0], sums[1])
        }
        runBlocking { session.close() }
    }
}
