package persona.feature.chat

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.Item
import persona.core.contracts.PlanTask
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.chatLines
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.DispatchHint
import persona.core.domain.tasks.TasksStore

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class AssistantFeedPagingClipTest {
    @get:Rule val rule = createComposeRule()

    private class StubTasksApi : TasksApi {
        override suspend fun todos(): List<Todo> = emptyList()
        override suspend fun feed(conversation: String, since: String?): FeedResponse = FeedResponse()
        override suspend fun mainConversation(): String? = null
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = error("unused")
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = error("unused")
        override suspend fun acceptTask(todoId: String, askedAt: String) = error("unused")
        override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo = error("unused")
        override suspend fun deleteTodo(id: String) = error("unused")
        override suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) = error("unused")
        override suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) = error("unused")
    }

    private class StubConnection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = false
    }

    private fun newTasksStore() = TasksStore(StubTasksApi(), StubConnection(), CoroutineScope(Dispatchers.Unconfined))

    @Test fun loadedEarliestMatchesHasOlderAndOldestTimestampBoundaries() {
        val t1 = "2026-11-12T13:00:00Z"
        val t2 = "2026-11-12T13:05:00Z"
        val items = listOf(
            Item("u0", 1, role = "user", kind = "text", ts = null),
            Item("u1", 2, role = "user", kind = "text", ts = t1),
            Item("a1", 3, role = "assistant", kind = "text", ts = t2),
        )
        assertNull(loadedEarliest(items, hasOlder = false))
        assertNull(loadedEarliest(emptyList(), hasOlder = false))
        assertEquals(Instant.parse(t1), loadedEarliest(items, hasOlder = true))
        assertEquals(
            Instant.parse("2026-11-12T05:00:00Z"),
            loadedEarliest(listOf(Item("offset", 1, role = "user", kind = "text", ts = "2026-11-12T13:00:00+08:00")), hasOlder = true),
        )
        // Empty or untimed partial pages hold back timed history until loaded items provide a lower bound.
        assertEquals(Instant.MAX, loadedEarliest(emptyList(), hasOlder = true))
        assertEquals(Instant.MAX, loadedEarliest(listOf(Item("u0", 1, role = "user", kind = "text", ts = null)), hasOlder = true))
        assertEquals(Instant.MAX, loadedEarliest(listOf(Item("bad", 1, role = "user", kind = "text", ts = "not-a-time")), hasOlder = true))
    }

    @Test fun rememberChatPageLinesClipsOlderFeedUntilOlderPagesLoadAndKeepsBackgroundIntact() {
        val key = SessionKey("hub", "main")
        val tasks = newTasksStore()
        val day1Plan = TaskPlan("p1", "main", listOf(PlanTask("Day 1 task", todoId = "t1")), "started", "2026-11-10T12:05:00Z")
        val day2Plan = TaskPlan("p2", "main", listOf(PlanTask("Day 2 task", todoId = "t2")), "started", "2026-11-11T12:05:00Z")
        val day3Plan = TaskPlan("p3", "main", listOf(PlanTask("Day 3 task", todoId = "t3")), "started", "2026-11-12T13:05:00Z")
        val day3Open = TaskPlan("p3-open", "main", listOf(PlanTask("Pending plan")), "open", "2026-11-12T13:10:00Z")
        val plans = listOf(day1Plan, day2Plan, day3Plan, day3Open).associateBy { it.id }
        val todos = listOf(
            Todo("t1", "Day 1 task", origin = "main", planId = "p1", createdAt = "2026-11-10T12:05:00Z"),
            Todo("t2", "Day 2 task", origin = "main", planId = "p2", createdAt = "2026-11-11T12:05:00Z"),
            Todo("t3", "Day 3 task", origin = "main", planId = "p3", createdAt = "2026-11-12T13:05:00Z"),
        )
        val feed = listOf(
            FeedItem("day1-today", "main", "2026-11-10T12:00:00Z", "today"),
            FeedItem("day1-plan", "main", "2026-11-10T12:05:00Z", "plan", "p1"),
            FeedItem("day2-brief", "main", "2026-11-11T12:00:00Z", "brief"),
            FeedItem("day2-plan", "main", "2026-11-11T12:05:00Z", "plan", "p2"),
            FeedItem("day3-today", "main", "2026-11-12T12:00:00Z", "today"),
            FeedItem("day3-plan", "main", "2026-11-12T13:05:00Z", "plan", "p3"),
            FeedItem("day3-open-feed", "main", "2026-11-12T13:10:00Z", "plan", "p3-open"),
        )
        val day2Items = listOf(
            Item("u2", 20, role = "user", kind = "text", text = "Day 2 question", ts = "2026-11-11T12:02:00Z"),
            Item("a2", 21, role = "assistant", kind = "text", text = "Day 2 answer", ts = "2026-11-11T12:03:00Z"),
        )
        val day3Items = listOf(
            Item("u3", 30, role = "user", kind = "text", text = "Day 3 question", ts = "2026-11-12T13:00:00Z"),
            Item("a3", 31, role = "assistant", kind = "text", text = "Day 3 answer", ts = "2026-11-12T13:01:00Z"),
        )

        var snapshot by mutableStateOf(ChatSnapshot(items = day3Items, loading = false, hasOlder = true))
        lateinit var captured: ChatPageLines
        val search = ChatSearchState()

        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                val projection = chatLines(snapshot.items.map(::LineItem))
                captured = rememberChatPageLines(
                    key = key,
                    renderStyle = ChatRenderStyle.Assistant,
                    snapshot = snapshot,
                    session = null,
                    foldsBackground = false,
                    supervisingConversation = true,
                    tasks = tasks,
                    taskTodos = todos,
                    search = search,
                    openWork = emptySet(),
                    projection = projection,
                    feed = feed,
                    taskPlans = plans,
                    taskCardProjection = emptyMap(),
                )
            }
        }

        // Page 1 (hasOlder = true): older days and earlier same-day today card are held back; current cards stay.
        rule.waitForIdle()
        val page1 = captured
        val page1Chronological = page1.lines.asReversed()
        assertEquals(1, page1Chronological.count { it.id.startsWith("day-") })
        assertEquals(
            listOf("u3", "a3", "dispatch-p3", "feed-day3-open-feed"),
            page1Chronological.map { it.id }.filterNot { it.startsWith("day-") },
        )
        // Background records remain unclipped for the background drawer/trays.
        val backgroundFeedIds = page1.assistantPresentation.background
            .filterIsInstance<ChatLine.Extra>()
            .mapNotNull { (it.payload as? AssistantExtra.Feed)?.item?.id }
        assertEquals(listOf("day1-plan", "day2-plan", "day3-plan", "day3-open-feed"), backgroundFeedIds)

        // Page 2 (loadOlder prepends Day 2 while hasOlder stays true): Day 2 task card and Day 3 12:00 today card appear.
        rule.runOnIdle {
            snapshot = ChatSnapshot(items = day2Items + day3Items, loading = false, hasOlder = true)
        }
        rule.waitForIdle()
        val page2Chronological = captured.lines.asReversed()
        assertEquals(2, page2Chronological.count { it.id.startsWith("day-") })
        assertEquals(
            listOf("u2", "a2", "dispatch-p2", "feed-day3-today", "u3", "a3", "dispatch-p3", "feed-day3-open-feed"),
            page2Chronological.map { it.id }.filterNot { it.startsWith("day-") },
        )

        // Page 3 (hasOlder flips to false): earliest clears and Day 1 brief/task cards and day line appear.
        rule.runOnIdle {
            snapshot = ChatSnapshot(items = day2Items + day3Items, loading = false, hasOlder = false)
        }
        rule.waitForIdle()
        val page3Chronological = captured.lines.asReversed()
        assertEquals(3, page3Chronological.count { it.id.startsWith("day-") })
        val dispatchTitles = page3Chronological
            .filterIsInstance<ChatLine.Extra>()
            .mapNotNull { it.payload as? DispatchHint }
            .flatMap { hint -> hint.tasks.map { it.title } }
        assertEquals(listOf("Day 1 task", "Day 2 task", "Day 3 task"), dispatchTitles)
        assertTrue(page3Chronological.any { it.id == "feed-day1-today" })
        assertTrue(page3Chronological.any { it.id == "feed-day2-brief" })

        // Empty items with hasOlder = true holds back all timed historical cards rather than dumping them.
        rule.runOnIdle {
            snapshot = ChatSnapshot(items = emptyList(), loading = false, hasOlder = true)
        }
        rule.waitForIdle()
        assertEquals(emptyList<String>(), captured.lines.map { it.id })
    }

    @Test fun rememberChatPageLinesAlsoClipsNonCompactAssistantLinesBranch() {
        val key = SessionKey("hub", "agent")
        val tasks = newTasksStore()
        val feed = listOf(
            FeedItem("old-note", "agent", "2026-11-10T12:00:00Z", "notice", body = buildJsonObject { put("text", "Old") }),
            FeedItem("new-note", "agent", "2026-11-12T13:05:00Z", "notice", body = buildJsonObject { put("text", "New") }),
        )
        val items = listOf(
            Item("u3", 30, role = "user", kind = "text", text = "Current turn", ts = "2026-11-12T13:00:00Z"),
        )
        var hasOlder by mutableStateOf(true)
        lateinit var captured: ChatPageLines
        val search = ChatSearchState()

        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Brief) {
                val snapshot = ChatSnapshot(items = items, loading = false, hasOlder = hasOlder)
                val projection = chatLines(snapshot.items.map(::LineItem))
                captured = rememberChatPageLines(
                    key = key,
                    renderStyle = ChatRenderStyle.Agent,
                    snapshot = snapshot,
                    session = null,
                    foldsBackground = false,
                    supervisingConversation = false,
                    tasks = tasks,
                    taskTodos = emptyList(),
                    search = search,
                    openWork = emptySet(),
                    projection = projection,
                    feed = feed,
                    taskPlans = emptyMap(),
                    taskCardProjection = emptyMap(),
                )
            }
        }

        rule.waitForIdle()
        assertEquals(
            listOf("u3", "feed-new-note"),
            captured.lines.asReversed().map { it.id }.filterNot { it.startsWith("day-") },
        )

        rule.runOnIdle { hasOlder = false }
        rule.waitForIdle()
        assertEquals(
            listOf("feed-old-note", "u3", "feed-new-note"),
            captured.lines.asReversed().map { it.id }.filterNot { it.startsWith("day-") },
        )
    }
}
