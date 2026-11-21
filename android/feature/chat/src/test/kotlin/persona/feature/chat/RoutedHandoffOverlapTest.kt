package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.TasksStore

/**
 * A message that starts a new Agent (`routed` with `new_agent`) and that Agent's hand-off card share one transcript
 * row. The row is a Box (`ChatSearchHighlight`), so the two must be stacked, message then card, and must not overlap.
 * iOS `FeedLine` case `.routed` draws only the message (TaskViews.swift); the card is the later Agent that
 * `DispatchHandoffCards` leaves off the task's own cards. Glass, Brief and Board, light and dark.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class RoutedHandoffOverlapTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    @After fun close() = scope.cancel()

    private val now = Instant.parse("2026-11-21T12:00:00Z")
    private val words = "Continue from the last notes. Keep the examples."
    private val item = FeedItem("routed-1", "main", now.toString(), "routed", "task1", buildJsonObject {
        put("text", JsonPrimitive(words))
        put("new_agent", JsonPrimitive("successor"))
        put("new_agent_machine", JsonPrimitive("desk"))
    })

    private class Api : TasksApi {
        override suspend fun todos() = listOf(Todo("task1", title = "Release notes", createdAt = "2026-11-21T11:00:00Z", origin = "main"))
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private fun host(): AssistantTaskHost {
        val store = TasksStore(Api(), Connection(), scope)
        store.reloadTodos()
        val session = Session("desk", "successor", title = "Release notes", state = "working", activityAt = now.minusSeconds(30).toString())
        return AssistantTaskHost(store, "main", listOf(session), { it }, {}, { throw it }, {})
    }

    @Test fun routedMessageAndNewAgentCardDoNotOverlap() {
        val host = host()
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalRippleConfiguration provides null) {
                    Box(Modifier.testTag("shot").width(360.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        // The transcript's search highlight is the Box the row is drawn in.
                        ChatSearchHighlight(ChatSearchState(), "routed-1") {
                            AssistantExtraLine(AssistantExtra.Feed(item), host)
                        }
                    }
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/routed-handoff-shots").also { it.mkdirs() }
        val light = mutableMapOf<AppStyle, Bitmap>()
        for (s in listOf(AppStyle.Glass, AppStyle.Brief, AppStyle.Board)) for (d in listOf(false, true)) {
            setLook(s, d)
            rule.waitForIdle()
            val message = rule.onNode(hasTestTag("exchange-user-routed") or hasTestTag("routed-owner"), useUnmergedTree = true)
                .assertIsDisplayed().getUnclippedBoundsInRoot()
            val card = rule.onNodeWithTag("new-agent-handoff", useUnmergedTree = true).assertIsDisplayed().getUnclippedBoundsInRoot()
            val gap = card.top - message.bottom
            assertTrue("$s dark=$d: card $card overlaps message $message", gap >= 0.dp)
            // iOS hand-off cards use Theme.Space.s (8). A shared edge or a doubled pad is the wrong gap.
            assertEquals("$s dark=$d gap", 8f, gap.value, 1f)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            if (!d) light[s] = bitmap.copy(Bitmap.Config.ARGB_8888, false)
            else assertFalse("Light and dark must differ for $s", light.getValue(s).sameAs(bitmap))
            File(out, "routed-handoff-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    /** A row that emits one composable stays flush with the Box: the column adds no gap around a single child. */
    @Test fun aSingleFeedLineStaysFlushWithItsBox() {
        val host = host()
        val notice = FeedItem("n", "main", kind = "notice", body = buildJsonObject { put("text", JsonPrimitive("The computer is ready.")) })
        rule.setContent {
            PersonaTheme(style = AppStyle.Brief, darkTheme = false, reduceMotion = true) {
                Box(Modifier.testTag("edge").width(360.dp)) {
                    ChatSearchHighlight(ChatSearchState(), "n") { AssistantExtraLine(AssistantExtra.Feed(notice), host) }
                }
            }
        }
        rule.waitForIdle()
        val box = rule.onNodeWithTag("edge").getUnclippedBoundsInRoot()
        val line = rule.onNodeWithTag("feed-notice", useUnmergedTree = true).assertIsDisplayed().getUnclippedBoundsInRoot()
        assertEquals(box.top.value, line.top.value, 0.5f)
        assertEquals(box.bottom.value, line.bottom.value, 0.5f)
        rule.onNodeWithTag("new-agent-handoff").assertDoesNotExist()
    }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private fun setLook(style: AppStyle, dark: Boolean) {
        this.style = style
        this.dark = dark
        Snapshot.sendApplyNotifications()
        rule.waitForIdle()
    }
}
