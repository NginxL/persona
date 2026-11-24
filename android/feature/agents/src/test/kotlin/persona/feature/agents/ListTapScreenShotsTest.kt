package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import kotlin.math.abs
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentGrouping
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * The real Agents list (#9600), Glass, phone and foldable. One section fold and one pin, start / mid / end.
 * PNGs in `build/list-tap-r1/` (copied out of the repo for the PR).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w800dp-h1400dp-mdpi")
class ListTapScreenShotsTest {
    @get:Rule val rule = createComposeRule()

    private val sessions = listOf(
        """"session_id":"needs","title":"Approve the migration","state":"needs_you"""",
        """"session_id":"working","title":"Fix the login bug","state":"working"""",
        """"session_id":"seen1","title":"Update the docs","state":"idle"""",
        """"session_id":"seen2","title":"Old refactor","state":"idle"""",
    ).map { JsonCodec.decodeFromString(Session.serializer(), """{"machine_id":"box",$it,"updated_at":"2026-01-01T00:00:00Z"}""") }

    @Test fun fold() = shoot("fold", "Seen") {
        val group = model.page.value.list.groups.first { it.id == "state/working" }
        model.fold(group)
    }

    @Test fun pin() = shoot("pin", "Fix the login bug") {
        val row = model.page.value.list.groups.flatMap { it.rows }.first { it.session.sessionId == "working" }
        model.pin(row)
    }

    @Test fun codexSubtaskCollapseArmsBeforeRemovalSoChildrenFadeOut() {
        val subtaskSessions = listOf(
            Session(
                sessionId = "root", machineId = "box", title = "Main Codex task",
                sourceKind = "codex_history", sourceThreadId = "root",
                cwd = "/code/app", repoRoot = "/code/app",
                state = "working", status = "working", updatedAt = "2026-01-01T02:00:00Z",
            ),
            Session(
                sessionId = "child1", machineId = "box", title = "Child subtask alpha",
                sourceKind = "codex_history", sourceThreadId = "child1", parentThreadId = "root",
                cwd = "/code/app", repoRoot = "/code/app",
                state = "working", status = "working", updatedAt = "2026-01-01T01:30:00Z",
            ),
            Session(
                sessionId = "child2", machineId = "box", title = "Child subtask beta",
                sourceKind = "codex_history", sourceThreadId = "child2", parentThreadId = "root",
                cwd = "/code/app", repoRoot = "/code/app",
                state = "working", status = "working", updatedAt = "2026-01-01T01:00:00Z",
            ),
            Session(
                sessionId = "after", machineId = "box", title = "Following task",
                sourceKind = "codex_history", sourceThreadId = "after",
                cwd = "/code/app", repoRoot = "/code/app",
                state = "working", status = "working", updatedAt = "2026-01-01T00:00:00Z",
            ),
        )
        model = newModel(subtaskSessions)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                    Box(Modifier.size(411.dp, 900.dp).testTag("shot")) {
                        AgentsScreen(model, Nowhere, AgentsMetadata())
                    }
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        rule.runOnIdle { model.grouping(AgentGrouping.State) }
        rule.waitForIdle()

        // Expand Codex subtasks and wait for the child rows to settle.
        rule.onNodeWithText("Subtasks (2)").performClick()
        rule.waitForIdle()
        val childNode = rule.onNodeWithText("Child subtask alpha").assertIsDisplayed()
        val childBounds = childNode.getUnclippedBoundsInRoot()
        val followingBefore = top("Following task")

        // Freeze clock and collapse subtasks: AgentsScreen must arm 1 frame before mutating expandedTasks
        // so exitFade attaches fadeOutSpec and populates the child row's GraphicsLayer before removal.
        rule.mainClock.autoAdvance = false
        rule.onNodeWithText("Subtasks (2)").performClick()
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeByFrame()
        // Child row is still composed on the armed frame; draw it (mirroring Choreographer's per-frame draw).
        rule.onNodeWithText("Child subtask alpha").assertIsDisplayed()
        rule.onNodeWithTag("shot").captureToImage()

        // Next frame removes the child rows: semantics removes Child subtask alpha, while its GraphicsLayer
        // is fading out (still drawing ink pixels in its slot) and Following task starts springing up.
        rule.mainClock.advanceTimeByFrame()
        rule.onAllNodesWithText("Child subtask alpha").assertCountEquals(0)
        val midBitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val earlyInk = darkInkPixels(midBitmap, childBounds.top.value.toInt(), childBounds.bottom.value.toInt())

        rule.mainClock.advanceTimeBy(700)
        val endBitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val followingAfter = top("Following task")
        val settledBounds = rule.onNodeWithText("Following task").getUnclippedBoundsInRoot()

        assertTrue("expected fading child row ink pixels on removal frame ($earlyInk)", earlyInk > 12)
        assertTrue("Following task did not move up after collapsing subtasks ($followingBefore -> $followingAfter)",
            followingAfter < followingBefore - 20f)
        // Below the settled Following task row, zero leftover ghost pixels should remain where the collapsed rows sat.
        val belowSettledInk = darkInkPixels(endBitmap, (settledBounds.bottom.value + 16f).toInt(), (followingBefore + 40f).toInt())
        assertTrue("ghost ink remained after subtask collapse ($belowSettledInk)", belowSettledInk <= 4)
    }

    private fun darkInkPixels(bitmap: Bitmap, topY: Int, bottomY: Int): Int {
        val y0 = topY.coerceIn(0, bitmap.height)
        val y1 = bottomY.coerceIn(y0, bitmap.height)
        var count = 0
        for (py in y0 until y1 step 2) {
            for (px in 40 until (bitmap.width - 40).coerceAtLeast(40) step 2) {
                val pixel = bitmap.getPixel(px, py)
                if (android.graphics.Color.red(pixel) < 120 &&
                    android.graphics.Color.green(pixel) < 120 &&
                    android.graphics.Color.blue(pixel) < 120
                ) count++
            }
        }
        return count
    }

    private lateinit var model: AgentsViewModel
    private var dark by mutableStateOf(false)
    private var width by mutableStateOf(411.dp)
    private var generation by mutableStateOf(0)

    private fun shoot(name: String, probe: String, act: () -> Unit) {
        model = newModel()
        rule.setContent {
            generation.let { }
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                    Box(Modifier.size(width, 900.dp).testTag("shot")) {
                        AgentsScreen(model, Nowhere, AgentsMetadata())
                    }
                }
            }
        }
        for (schemeDark in listOf(false, true)) for ((form, formWidth) in listOf("phone" to 411.dp, "foldable" to 673.dp)) {
            model = newModel()
            rule.mainClock.autoAdvance = true
            rule.runOnIdle {
                dark = schemeDark
                width = formWidth
                generation++
            }
            rule.waitUntil(5_000) { model.page.value.loaded }
            rule.runOnIdle { model.grouping(AgentGrouping.State) }
            rule.waitForIdle()
            rule.mainClock.autoAdvance = false
            val scheme = if (schemeDark) "dark" else "light"
            val stem = "$name-$scheme-$form"
            val start = top(probe)
            save("$stem-start")
            act()
            Snapshot.sendApplyNotifications()
            val samples = mutableListOf(start)
            var savedMid = false
            repeat(16) {
                rule.mainClock.advanceTimeByFrame()
                val y = top(probe)
                samples += y
                if (!savedMid && abs(y - start) > 4f) {
                    save("$stem-mid")
                    savedMid = true
                }
            }
            rule.mainClock.advanceTimeBy(700)
            val end = top(probe)
            save("$stem-end")
            if (!savedMid) save("$stem-mid")
            val low = minOf(start, end) - 24f
            val high = maxOf(start, end) + 24f
            assertTrue("$stem drew a frame that never moved", savedMid)
            assertTrue(
                "$stem flew or never sat between $start and $end: $samples",
                samples.any { y -> (y - start) * (y - end) < 0f && abs(y - start) > 4f && abs(y - end) > 4f } &&
                    samples.all { it in low..high },
            )
            rule.mainClock.autoAdvance = true
        }
    }

    private fun top(text: String) = rule.onNodeWithText(text).getUnclippedBoundsInRoot().top.value

    private fun newModel(sessionList: List<Session> = this@ListTapScreenShotsTest.sessions): AgentsViewModel {
        val pins = MutableStateFlow(AgentOperationPreferences())
        return AgentsViewModel(object : SessionsRepository {
                override val sessions = MutableStateFlow(sessionList)
                override val machines = MutableStateFlow(listOf(Machine("box", "Example computer", online = true)))
                override suspend fun refresh() = Unit
            }, object : InteractionCenter {
                override val pending = MutableStateFlow<List<Interaction>>(emptyList())
                override suspend fun refresh() = Unit
                override suspend fun resolve(id: String, resolution: Resolution) = Unit
            }, object : HubConnection {
                override val state = MutableStateFlow(ConnectionState.Online)
                override val events = MutableSharedFlow<ServerEvent>()
                override fun reconnect() = Unit
                override fun send(message: JsonObject) = true
            }, object : MessageSender {
                override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
                override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
            }, AgentsPreferences(object : DataStore<Preferences> {
                override val data = MutableStateFlow<Preferences>(emptyPreferences())
                override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
            }, object : HubPreferences {
                override val preferences = MutableStateFlow(HubPreferencesSnapshot())
                override suspend fun refresh() = Unit
                override suspend fun togglePin(path: String, space: Boolean) = Unit
                override suspend fun toggleMute(path: String, space: Boolean) = Unit
                override suspend fun markSeen(session: SessionKey, version: String) = Unit
            }), bindings = AgentsOperationsBindings(object : AgentOperations {
                override val scope = "pairing"
                override suspend fun pins() = pins.value
                override suspend fun mutes() = AgentOperationPreferences()
                override suspend fun savePins(value: AgentOperationPreferences) { pins.value = value }
                override suspend fun saveMutes(value: AgentOperationPreferences) = Unit
                override suspend fun renameSession(key: SessionKey, title: String?, tabName: String?) = Unit
                override suspend fun renameSpace(machineId: String, workspaceId: String, name: String) = Unit
                override suspend fun closeSession(key: SessionKey) = Unit
            }, object : AssistantConversations {
                override val assistant = MutableStateFlow(AssistantSnapshot())
                override val pinned = MutableStateFlow(emptySet<SessionKey>())
                override suspend fun startConversation(): SessionKey = error("unused")
                override suspend fun refresh() = Unit
            }))
    }

    private fun save(name: String) {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        assertTrue("$name drew nothing", bitmap.width > 50 && bitmap.height > 50)
        val first = bitmap.getPixel(bitmap.width / 2, bitmap.height / 2)
        var varied = false
        for (y in 0 until bitmap.height step 8) {
            if (bitmap.getPixel(bitmap.width / 2, y) != first) { varied = true; break }
        }
        assertTrue("$name is a flat frame", varied)
        File("build/list-tap-r1").also { it.mkdirs() }.resolve("$name.png").outputStream().use {
            bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
        }
    }

    private object Nowhere : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
}
