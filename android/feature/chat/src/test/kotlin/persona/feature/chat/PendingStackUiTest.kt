package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.SessionRenamer
import persona.core.domain.tasks.TasksStore

/**
 * The stack above the composer in the real chat page (#4210 T6, #4339 C10): the composer stays usable while a card waits
 * and a message typed then is sent as any other (as iOS's `sendText`); past half the page they scroll under "N pending";
 * each card folds on its own; Persona's conversation shows its tasks' oldest question under the task's name, and a card
 * the Needs you tray's Review opens joins the stack until it is folded.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp", application = QuestionTestApplication::class)
class PendingStackUiTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { scope.cancel() }

    private val key = SessionKey("computer-a", "importer")
    private val renamer = object : SessionRenamer {
        override fun project(source: Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = false
    }
    private val permission = Interaction("perm", machineId = key.machineId, sessionId = key.sessionId, kind = "permission", toolName = "Bash",
        summary = "npm test", createdAt = "2026-11-12T01:03:00Z")
    private val question = Interaction("ask", machineId = key.machineId, sessionId = key.sessionId, kind = "question", createdAt = "2026-11-12T01:04:00Z",
        questions = listOf(Question("Which branch should I push to?", options = listOf(QuestionOption("main"), QuestionOption("release/1.4")))))
    private val plan = Interaction("plan", machineId = key.machineId, sessionId = key.sessionId, kind = "plan", createdAt = "2026-11-12T01:05:00Z",
        summary = "Retry the import once", plan = "1. Catch the restart error\n2. Retry once")
    private val sent = mutableListOf<String>()
    private val answers = mutableListOf<Pair<String, Resolution>>()

    private fun settle() { Snapshot.sendApplyNotifications(); rule.waitForIdle() }

    private fun agentChat(requests: List<Interaction>, dark: Boolean = false) {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Computer A", online = true)))
            override val sessions = MutableStateFlow(listOf(Session(key.machineId, key.sessionId, title = "Fix the importer retry",
                agent = "claude", status = "blocked", state = "needs_you", pendingInteractions = requests.size)))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
                Item("u1", 1, role = "user", kind = "text", text = "Make the importer retry once", ts = "2026-11-12T01:01:00Z"))))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { sent += text }
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(requests)
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) { answers += id to resolution }
        }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                Box(Modifier.fillMaxSize()) {
                    ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, chrome = ChatChromeBindings(renamer = renamer))
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Make the importer retry once").fetchSemanticsNodes().isNotEmpty() }
        settle()
    }

    @Test fun theComposerStaysAndAMessageTypedWhileAPermissionWaitsIsSent() {
        agentChat(listOf(permission))
        rule.onNodeWithTag("pending-card-perm").assertExists()
        val composer = hasSetTextAction() and !hasAnyAncestor(hasTestTag("pending-card-perm"))
        rule.onNode(composer).performTextInput("Also bump the version")
        settle()
        rule.onNode(hasContentDescription("Send") and !hasAnyAncestor(hasTestTag("pending-card-perm"))).performClick()
        settle()
        assertEquals(listOf("Also bump the version"), sent)
        // the card is still there, unanswered
        rule.onNodeWithTag("pending-card-perm").assertExists()
    }

    private fun assertPreparedCardAnswers(dark: Boolean) {
        // The original window has ended; this is the replacement snapshot with the server's current lifetime.
        val retained = JsonCodec.decodeFromString<Interaction>(
            """{"id":"prepared","machine_id":"${key.machineId}","session_id":"${key.sessionId}",""" +
                """"kind":"permission","tool_name":"Bash","expires_at":"${java.time.Instant.now().plusSeconds(3_600)}",""" +
                """"can_clarify":true}""",
        )
        agentChat(listOf(retained), dark = dark)
        val inCard = hasAnyAncestor(hasTestTag("pending-card-prepared"))
        rule.onNode(hasText("Allow") and inCard).assertIsEnabled().performClick()
        settle()
        assertEquals(listOf(retained.id to Resolution(allow = true)), answers)
    }

    @Test fun aPreparedCardStillAnswersAfterItsOriginalWindow() = assertPreparedCardAnswers(dark = false)

    @Config(fontScale = 2f)
    @Test fun aPreparedCardStillAnswersWithLargeTextInDarkMode() = assertPreparedCardAnswers(dark = true)

    /** Past half the page they scroll under "3 pending": the session's requests, the number its Agents row says
     *  ("3 need your decision", one count per card of the session). */
    @Test fun pastHalfThePageTheyScrollUnderNPending() {
        agentChat(listOf(question, plan, permission))
        rule.onNodeWithText("3 pending").assertExists()
        for (id in listOf("perm", "ask", "plan")) rule.onNodeWithTag("pending-card-$id").assertExists()
    }

    @Test fun eachCardFoldsOnItsOwn() {
        agentChat(listOf(permission, question))
        val inQuestion = hasAnyAncestor(hasTestTag("pending-card-ask"))
        rule.onNode(hasContentDescription("Collapse") and hasAnyAncestor(hasTestTag("pending-card-perm"))).performClick()
        settle()
        rule.onNode(hasContentDescription("Expand") and hasAnyAncestor(hasTestTag("pending-card-perm"))).assertExists()
        // the other card is as it was, its options still there
        rule.onNode(hasText("1. main") and inQuestion).assertExists()
        rule.onNode(hasContentDescription("Collapse") and inQuestion).assertExists()
    }

    // Persona's conversation: two tasks whose Agents ask (the docs task's question is the older), and what the conversation asks itself.
    private val main = SessionKey("computer-a", "main")
    private val docs = Session("computer-a", "docs", title = "Update the docs", state = "needs_you", status = "blocked", pendingInteractions = 1)
    private val release = Session("computer-b", "release", title = "Prepare the release", state = "needs_you", status = "blocked", pendingInteractions = 1)
    private val docsAsk = Interaction("docs-ask", machineId = docs.machineId, sessionId = docs.sessionId, kind = "question", createdAt = "2026-11-12T01:04:00Z",
        questions = listOf(Question("Should the guide cover Windows too?", options = listOf(QuestionOption("Yes"), QuestionOption("Not yet")))))
    private val releaseAsk = Interaction("release-ask", machineId = release.machineId, sessionId = release.sessionId, kind = "question", createdAt = "2026-11-12T01:05:00Z",
        questions = listOf(Question("Which version number?", options = listOf(QuestionOption("1.4.0"), QuestionOption("2.0.0")))))
    private val ownAsk = Interaction("own-ask", machineId = main.machineId, sessionId = main.sessionId, kind = "question", createdAt = "2026-11-12T01:06:00Z",
        questions = listOf(Question("Which folder should I tidy first?", options = listOf(QuestionOption("Downloads"), QuestionOption("Desktop")))))
    private val ownPermission = Interaction("own-perm", machineId = main.machineId, sessionId = main.sessionId, kind = "permission", toolName = "Bash",
        summary = "ls ~/Downloads", createdAt = "2026-11-12T01:07:00Z")

    private fun personaChat(requests: List<Interaction>) {
        val todos = listOf(
            Todo("task-docs", docs.title, origin = "main", agents = listOf(TodoAgent(docs.machineId, docs.sessionId))),
            Todo("task-release", release.title, origin = "main", agents = listOf(TodoAgent(release.machineId, release.sessionId))),
        )
        val sessions = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(Session(main.machineId, main.sessionId, title = "Persona", assistant = true,
                status = "idle", state = "idle"), docs, release))
            override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true), Machine("computer-b", "Computer B", online = true)))
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(requests)
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val api = object : TasksApi {
            override suspend fun todos() = todos
            override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo = error("not used")
            override suspend fun acceptTask(todoId: String, askedAt: String) = Unit
            override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
            override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
            override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
            override suspend fun retryTask(todoId: String): Todo = error("not used")
            override suspend fun removeTask(todoId: String) = Unit
            override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
            override suspend fun today(conversation: String): FeedItem = error("not used")
            override suspend fun mainConversation() = "main"
            override suspend fun inboxReply(itemId: String, text: String, card: String?) = error("not used")
        }
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
                Item("user", 1, role = "user", kind = "text", text = "Keep the docs and the release moving.", ts = "2026-11-12T01:01:00Z"))))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.fillMaxSize()) {
                    ChatScreen(main, chats, sender, interactions, sessions, {}, chrome = ChatChromeBindings(tasks = tasks, renamer = renamer),
                        renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
                }
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Keep the docs and the release moving.").fetchSemanticsNodes().isNotEmpty() }
        rule.waitUntil(10_000) { rule.onAllNodes(hasTestTag("pending-card-docs-ask")).fetchSemanticsNodes().isNotEmpty() }
        settle()
    }

    private fun top(tag: String) = rule.onNodeWithTag(tag).getUnclippedBoundsInRoot().top
    private val composerWords = "Ask Persona…"

    /** iOS's footer order: the task question, the entries, then the conversation's own cards, then the composer. */
    @Test fun personasTaskQuestionIsAboveItsEntriesAndItsOwnCardsAloneAreBelowThem() {
        personaChat(listOf(docsAsk, releaseAsk, ownAsk))
        // the oldest question of its tasks, under its task's name, with how many more
        val inDocs = hasAnyAncestor(hasTestTag("pending-card-docs-ask"))
        rule.onNode(hasText("Update the docs") and inDocs).assertExists()
        rule.onNode(hasText("1 more") and inDocs).assertExists()
        // the other task's question is its task's row in the tray, never a card
        rule.onNodeWithTag("pending-card-release-ask").assertDoesNotExist()
        rule.onNodeWithTag("pending-card-own-ask").assertExists()
        assertTrue("the task question over the entries", top("pending-card-docs-ask") < top("assistant-tray-bar"))
        assertTrue("the entries over the conversation's own card", top("assistant-tray-bar") < top("pending-card-own-ask"))
        assertTrue("its own card over the composer",
            top("pending-card-own-ask") < rule.onNodeWithText(composerWords).getUnclippedBoundsInRoot().top)
        // one card in each place fits: no "N pending" anywhere
        rule.onAllNodesWithText("pending", substring = true).assertCountEquals(0)
    }

    /** "N pending" is the stack's: the conversation's own cards, the task question and its "N more" not among them. */
    @Test fun nPendingCountsOnlyTheConversationsOwnCards() {
        personaChat(listOf(docsAsk, releaseAsk, ownAsk, ownPermission))
        rule.onNodeWithText("2 pending").assertExists()
        rule.onAllNodesWithText("3 pending").assertCountEquals(0)
        rule.onAllNodesWithText("4 pending").assertCountEquals(0)
        rule.onAllNodesWithText("pending", substring = true).assertCountEquals(1)
        // and it sits in the stack, under the entries
        assertTrue(top("assistant-tray-bar") < rule.onNodeWithText("2 pending").getUnclippedBoundsInRoot().top)
        rule.onNode(hasText("1 more") and hasAnyAncestor(hasTestTag("pending-card-docs-ask"))).assertExists()
    }

    /** iOS's task row opens its task (`detailTask`); the question is answered above the entries or in the Agent's chat. */
    @Test fun reviewOnAnotherTasksQuestionOpensItsTaskAndNeverJoinsTheStack() {
        personaChat(listOf(docsAsk, releaseAsk))
        rule.onNodeWithTag("assistant-entry-needs").performClick()
        settle()
        rule.onNode(hasText("Review") and hasAnyAncestor(hasTestTag("assistant-needs-task-task-release"))).performClick()
        settle()
        // the tray is shut and the task's detail is up: its name, and Open chat for its Agent's page
        rule.onNodeWithTag("assistant-tray-needs").assertDoesNotExist()
        rule.onNodeWithText("Open chat").assertExists()
        rule.onAllNodesWithText("Prepare the release").onFirst().assertExists()
        // nothing came into the cards: the oldest stays above, the other has no card
        rule.onNodeWithTag("pending-card-release-ask").assertDoesNotExist()
        rule.onNodeWithTag("pending-card-docs-ask").assertExists()
    }

    /** iOS's `.ask` row: the conversation's own card is right below the entries, so the row closes the tray onto it. */
    @Test fun reviewOnTheConversationsOwnCardClosesTheTrayOntoItUnfolded() {
        personaChat(listOf(docsAsk, ownAsk))
        val inOwn = hasAnyAncestor(hasTestTag("pending-card-own-ask"))
        rule.onNode(hasContentDescription("Collapse") and inOwn).performClick()
        settle()
        rule.onNode(hasContentDescription("Expand") and inOwn).assertExists()
        rule.onNodeWithTag("assistant-entry-needs").performClick()
        settle()
        rule.onNodeWithTag("assistant-tray-needs").assertExists()
        rule.onNodeWithTag("assistant-decision-own-ask").performClick()
        settle()
        rule.onNodeWithTag("assistant-tray-needs").assertDoesNotExist()
        // folded no longer, and no sheet: it is the stack's card
        rule.onNode(hasContentDescription("Collapse") and inOwn).assertExists()
        rule.onNodeWithText("Open chat").assertDoesNotExist()
    }

    /**
     * Both places tall on a short page: each has at most half of the room the footer leaves, so the cards scroll inside
     * their places and the composer stays on the page, under the entries and the stack, with the task question on top.
     */
    @Config(qualifiers = "en-w400dp-h640dp")
    @Test fun theComposerStaysOnThePageWithTallCardsInBothPlaces() {
        personaChat(listOf(docsAsk, releaseAsk, ownAsk, ownPermission))
        val page = rule.onRoot().getUnclippedBoundsInRoot()
        val composer = rule.onNodeWithText(composerWords)
        composer.assertIsDisplayed()
        assertTrue("the composer on the page", composer.getUnclippedBoundsInRoot().bottom <= page.bottom)
        assertTrue("the task question on the page", top("pending-card-docs-ask") >= page.top)
        // scrolling, the stack puts its permission first: that card is the one at its top
        assertTrue("the task question over the entries over the stack",
            top("pending-card-docs-ask") < top("assistant-tray-bar") && top("assistant-tray-bar") < top("pending-card-own-perm"))
        assertTrue("the stack over the composer", top("pending-card-own-perm") < composer.getUnclippedBoundsInRoot().top)
        rule.onNodeWithText("2 pending").assertExists()
        // the cards are still answerable where they are: an option of the task question is there
        rule.onAllNodesWithText("1. Yes").onFirst().assertExists()
    }
}
