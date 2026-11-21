package persona.feature.assistant

import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.performTouchInput
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.ConversationTagFilters
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-xhdpi")
class ConversationTagsScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val notices = mutableListOf<String>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun chat(id: String, title: String, tags: List<String>? = null, minutes: Long = 1) = Session(
        machineId = "hub", sessionId = id, title = title, assistant = true, status = "idle", tags = tags,
        updatedAt = java.time.Instant.parse("2026-11-17T12:00:00Z").minusSeconds(minutes * 60).toString(),
    )

    private fun show(hub: HistoryHub): HistoryHub {
        val requested = hub.assistant.value.mainSession
        rule.setContent {
            CompositionLocalProvider(LocalAppFeedback provides object : AppFeedback {
                override fun failure(failure: Throwable, operation: Boolean): Boolean {
                    notices += failure.message.orEmpty()
                    return true
                }
                override fun notice(message: String): Boolean {
                    notices += message
                    return true
                }
                override fun notice(message: String, style: FeedbackStyle): Boolean {
                    notices += message
                    return true
                }
            }) {
                AssistantScreen(navigator, hub, hub, startConversation = { error("Unexpected creation") },
                    requestedSession = requested, management = hub, composerService = null,
                    chatContent = { Text("chat:${it.sessionId}") })
            }
        }
        rule.onNodeWithContentDescription("Chats").performClick()
        rule.waitForIdle()
        return hub
    }

    private fun menuOf(row: Int) = rule.onAllNodesWithContentDescription("Chat actions")[row].performClick()
    private fun scrollTo(text: String) = rule.onAllNodes(hasScrollToNodeAction())[0].performScrollToNode(hasText(text))
    private fun openAllAndLongPress(title: String) {
        scrollTo("All chats (1)")
        rule.onNodeWithText("All chats (1)").performClick()
        scrollTo(title)
        rule.onAllNodesWithText(title)[0].performTouchInput { longClick() }
    }
    private fun tagDraft() = rule.onNode(hasSetTextAction() and hasAnyAncestor(hasTestTag("conversation-tag-draft")))
    private fun filterDraft() = rule.onNode(hasSetTextAction() and
        hasAnyAncestor(hasTestTag("conversation-tag-filter-draft")))
    private fun tagChip(label: String) = rule.onNode(
        hasText(label) and hasAnyAncestor(hasTestTag("conversation-tag-filter")) and hasClickAction())

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun noTaggedConversationsKeepTheExistingListAndHaveNoFilterStrip() {
        show(HistoryHub(chat("main", "Main"), chat("plain", "Plain chat")))

        rule.onAllNodesWithTag("conversation-tag-filter").assertCountEquals(0)
        rule.onNodeWithText("Recent").assertExists()
        rule.onNodeWithText("All chats (1)").assertExists()
        rule.onNodeWithText("Plain chat").assertExists()
    }

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun rowsShowTagsAndTheChipCombinesWithTextSearchAndTogglesOff() {
        val main = chat("main", "Main", listOf("Planning"), 0)
        val trip = chat("trip", "Trip itinerary", listOf("Travel"), 1)
        val work = chat("work", "Release plan", listOf("Planning", "Research"), 2)
        val plain = chat("plain", "Release personal", null, 3)
        show(HistoryHub(main, trip, work, plain))

        rule.onNodeWithTag("conversation-tag-filter").assertExists()
        rule.onNodeWithTag("conversation-row-tags-hub/main").assertExists()
        rule.onNodeWithTag("conversation-row-tags-hub/work").assertExists()
        tagChip("Travel").performClick()
        rule.onNodeWithText("Trip itinerary").assertExists()
        rule.onAllNodesWithText("Main conversation").assertCountEquals(0)
        rule.onAllNodesWithText("Release plan").assertCountEquals(0)

        tagChip("Planning").performClick()
        rule.onNodeWithText("Main conversation").assertExists()
        rule.onNodeWithText("Search chats").performTextInput("Release")
        rule.onNodeWithText("Release plan").assertExists()
        rule.onAllNodesWithText("Release personal").assertCountEquals(0)
        rule.onAllNodesWithText("Trip itinerary").assertCountEquals(0)

        tagChip("Planning").performClick()
        rule.onNodeWithText("Release plan").assertExists()
        rule.onNodeWithText("Release personal").assertExists()
    }

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun aTagRemovedEverywhereClearsTheFilterInsteadOfLeavingAnEmptyList() {
        val work = chat("work", "Work chat", listOf("Planning"), 1)
        val plain = chat("plain", "Plain chat", null, 2)
        val hub = show(HistoryHub(chat("main", "Main"), work, plain))
        tagChip("Planning").performClick()
        rule.onNodeWithText("Work chat").assertExists()
        rule.onAllNodesWithText("Plain chat").assertCountEquals(0)

        hub.assistant.value = hub.assistant.value.copy(conversations = hub.assistant.value.conversations.map {
            if (it.key == work.key) it.copy(tags = null) else it
        })
        rule.waitForIdle()
        rule.onAllNodesWithTag("conversation-tag-filter").assertCountEquals(0)
        rule.onNodeWithText("Work chat").assertExists()
        rule.onNodeWithText("Plain chat").assertExists()
    }

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun customEmptySetDoesNotFallBackToAutomaticChipsAndCanBeAddedTo() {
        val hub = HistoryHub(chat("main", "Main", listOf("Planning")), chat("work", "Work chat", listOf("Planning"))).apply {
            assistant.value = assistant.value.copy(conversationTagFilters = ConversationTagFilters(emptyList()))
        }
        show(hub)

        rule.onNodeWithText("No tag filters").assertExists()
        tagChip("Planning").assertDoesNotExist()
        rule.onNodeWithContentDescription("Customize tag filters").performClick()
        filterDraft().performTextInput("Unused")
        rule.onNodeWithText("Add").performClick()
        rule.waitForIdle()

        assertEquals(listOf("Unused"), hub.filterWrites.single().entries)
        tagChip("Unused").assertExists()
        rule.onNodeWithText("No tag filters").assertDoesNotExist()
        // A filter entry need not match any loaded conversation; it is not silently cleared.
        tagChip("Unused").performSemanticsAction(SemanticsActions.OnClick)
        rule.waitForIdle()
        assertEquals(1, hub.filterWrites.size)
        tagChip("Unused").assertIsSelected()
        rule.onAllNodesWithText("Main conversation").assertCountEquals(0)
        rule.onAllNodesWithText("Work chat").assertCountEquals(0)
    }

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun resetWritesNullAndRestoresAutomaticChipsAndRows() {
        val hub = HistoryHub(chat("main", "Main", listOf("Planning")), chat("work", "Work chat", listOf("Planning"))).apply {
            assistant.value = assistant.value.copy(
                conversationTagFilters = ConversationTagFilters(listOf("Unused", "Planning")))
        }
        show(hub)
        tagChip("Unused").performClick()
        rule.onAllNodesWithText("Main conversation").assertCountEquals(0)

        rule.onNodeWithContentDescription("Customize tag filters").performClick()
        rule.onNodeWithText("Reset to automatic").performClick()
        rule.waitForIdle()

        assertEquals(null, hub.filterWrites.single().entries)
        tagChip("Planning").assertExists()
        rule.onAllNodesWithText("Main conversation").assertCountEquals(1)
        rule.onNodeWithText("Done").performClick()
        rule.onNodeWithTag("conversation-tag-filter-draft").assertDoesNotExist()
    }

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun refusedCustomRemovalRollsBackTheChipAndReportsTheFailure() {
        val hub = HistoryHub(chat("main", "Main", listOf("Planning"))).apply {
            assistant.value = assistant.value.copy(
                conversationTagFilters = ConversationTagFilters(listOf("Planning", "Unused")))
            refuseFilterWrites = true
        }
        show(hub)
        rule.onNodeWithContentDescription("Customize tag filters").performClick()
        rule.onNodeWithContentDescription("Remove Unused").performClick()
        rule.waitForIdle()

        assertEquals(listOf("Planning"), hub.filterWrites.single().entries)
        tagChip("Unused").assertExists()
        assertTrue(notices.toString(), notices.any { "Tag filters unavailable" in it })
    }

    @Test fun theMainChatCanClearItsTags() {
        val main = chat("main", "Main", listOf("Planning"), 0)
        val hub = show(HistoryHub(main))
        menuOf(0)
        rule.onNodeWithText("Edit tags…").performClick()
        rule.onNodeWithContentDescription("Remove Planning").performClick()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertEquals(listOf(main.key to emptyList<String>()), hub.tagWrites)
        rule.onAllNodesWithText("Edit tags").assertCountEquals(0)
    }

    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun theBuiltInSupervisorHasNoTagEditor() {
        val background = chat("background", "Internal runtime title", listOf("BuiltIn"))
        val supervised = HistoryHub(chat("main", "Main"), background)
        supervised.assistant.value = supervised.assistant.value.copy(backgroundSupervisor = background.sessionId)
        show(supervised)
        rule.onNodeWithText("All chats (1)").performClick()
        menuOf(1)
        rule.onAllNodesWithText("Edit tags…").assertCountEquals(0)
        rule.onNodeWithText("Copy session ID").assertExists()
    }

    @Test fun archivedRowsShowFilterAndEditTheSameTags() {
        val planning = chat("archived-planning", "Archived planning", listOf("Planning"), 1).copy(archived = true)
        val travel = chat("archived-travel", "Archived travel", listOf("Travel"), 2).copy(archived = true)
        val hub = HistoryHub(chat("main", "Main")).apply { archivedList = listOf(planning, travel) }
        show(hub)
        scrollTo("Planning")
        tagChip("Planning").performClick()
        scrollTo("Archived (2)")
        rule.onNodeWithText("Archived (2)").performClick()

        rule.onNodeWithText("Archived planning").assertExists()
        rule.onAllNodesWithText("Archived travel").assertCountEquals(0)
        rule.onNode(hasText("Planning") and hasAnyAncestor(hasTestTag("archived-conversations"))).assertExists()
        rule.onNodeWithText("Archived planning").performTouchInput { longClick() }
        rule.onNodeWithText("Edit tags…").performClick()
        tagDraft().performTextInput("New")
        rule.onNodeWithText("Add").performClick()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertEquals(listOf(planning.key to listOf("Planning", "New")), hub.tagWrites)
    }

    @Test fun anOldHubRefusalRollsBackTheRowAndKeepsTheEditorOpen() {
        val plain = chat("plain", "Plain chat")
        val hub = HistoryHub(chat("main", "Main"), plain).apply { refuseTags = true }
        show(hub)
        openAllAndLongPress("Plain chat")
        rule.onNodeWithText("Edit tags…").performClick()
        tagDraft().performTextInput("New")
        rule.onNodeWithText("Add").performClick()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()

        rule.onNodeWithText("Edit tags").assertExists()
        rule.onNodeWithTag("conversation-row-tags-hub/plain").assertDoesNotExist()
        assertTrue(notices.toString(), notices.any { "Tags unavailable" in it })
        assertEquals(listOf(plain.key to listOf("New")), hub.tagWrites)
    }

    @Test fun theEditorRefusesCodePointOverflowAndCollapsesTrimmedDuplicates() {
        val plain = chat("plain", "Plain chat")
        val hub = show(HistoryHub(chat("main", "Main"), plain))
        openAllAndLongPress("Plain chat")
        rule.onNodeWithText("Edit tags…").performClick()
        tagDraft().performTextInput("📚".repeat(33))
        rule.onNodeWithText("Add").performClick()
        rule.onNodeWithText("Each tag can be at most 32 characters.").assertExists()
        assertTrue(hub.tagWrites.isEmpty())

        tagDraft().performTextReplacement(" One ")
        rule.onNodeWithText("Add").performClick()
        tagDraft().performTextInput("One")
        rule.onNodeWithText("Add").performClick()
        rule.onAllNodesWithContentDescription("Remove One").assertCountEquals(1)
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertEquals(listOf(plain.key to listOf("One")), hub.tagWrites)
    }
}
