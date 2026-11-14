package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxSuggestion
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * The detail page's state line: the suggestion card speaks for itself (iOS `.suggestion` is an `EmptyView`, "the card
 * says it"), so the state must not repeat the suggestion above it; and every state has words — a hub without `now`,
 * without a mark and without a suggestion still shows the page's own tier or done words.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w500dp-h1200dp-xhdpi")
class InboxDetailStateReviewTest {
    @get:Rule val rule = createComposeRule()
    private val repository = FakeInboxRepository()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun record(
        now: String? = null,
        suggestion: InboxSuggestion? = null,
        tier: String = "action",
        status: String? = null,
        done: Boolean = false,
        doneBy: String? = null,
    ): InboxRecord {
        val event = InboxEventFields(status = status, now = now, suggestion = suggestion, doneBy = doneBy)
        return InboxRecord(
            InboxItem("event", kind = "group", title = "Event title", sender = "Alex", preview = "Can we?",
                tier = tier, status = status, doneAt = if (done) "2026-01-01T12:00:00Z" else null),
            source = "chat", sourceLabel = "Team chat", event = event,
        )
    }

    private fun show(record: InboxRecord) {
        repository.snapshot.value = InboxSnapshot(records = listOf(record))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Box { InboxEventScreen(repository, InboxEventRoute(record.item.id), navigator) }
            }
        }
    }

    @Test fun aSuggestionIsSaidOnceByItsCardNotAgainAsAStateLine() {
        val reply = InboxSuggestion(version = 1, kind = "reply", text = "Friday works")
        show(record(now = "confirm_reply", suggestion = reply))
        // The card: its kind and its body, once each.
        assertEquals(1, rule.onAllNodesWithText("Suggested reply", useUnmergedTree = true).fetchSemanticsNodes().size)
        assertEquals(1, rule.onAllNodesWithText("Friday works", useUnmergedTree = true).fetchSemanticsNodes().size)
        // And not the state line's own "Suggested reply: Friday works".
        assertEquals(0, rule.onAllNodesWithText("Suggested reply: Friday works", useUnmergedTree = true).fetchSemanticsNodes().size)
        // The shared state word for the key is not drawn beside it either: the card is the state.
        assertEquals(0, rule.onAllNodesWithText("Waiting for you to confirm a reply", substring = true, useUnmergedTree = true).fetchSemanticsNodes().size)
    }

    @Test fun everyStateHasWords() {
        // An older hub's suggestion, without now: the kind and the first line.
        val reply = InboxSuggestion(version = 1, kind = "reply", text = "Friday works")
        show(record(suggestion = reply))
        assertTrue("suggestion card", rule.onAllNodesWithText("Friday works", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

        // A tier-only item without now and without a mark: the page's own tier words.
        repository.snapshot.value = InboxSnapshot(records = listOf(record(tier = "fyi", status = "fyi")))
        assertTrue("tier words", rule.onAllNodesWithText("FYI", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

        // A done item: its done words.
        repository.snapshot.value = InboxSnapshot(records = listOf(record(tier = "action", done = true, doneBy = "user")))
        assertTrue("done words", rule.onAllNodesWithText("Done", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

        // A now key the table knows: its words.
        repository.snapshot.value = InboxSnapshot(records = listOf(record(now = "check_due")))
        assertTrue("now words", rule.onAllNodesWithText("Time to look again", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
    }

    @Test fun theTimelineShowsTheThreeNewestAndAllWhenExpanded() {
        val chain = (1..5).map { i ->
            InboxChainEntry("c$i", at = "2026-01-01T11:0$i:00Z", kind = if (i == 5) "reported" else "received",
                text = "Step $i", via = if (i == 5) "agent" else "chat")
        }
        val withChain = record().let { it.copy(event = it.event.copy(chain = chain)) }
        show(withChain)
        // Folded: the three newest, the oldest of them first.
        val shown = listOf("Step 3", "Step 4", "Step 5").map {
            rule.onAllNodesWithText(it, useUnmergedTree = true).fetchSemanticsNodes().single().boundsInRoot.top
        }
        assertTrue("three newest, oldest first: $shown", shown[0] < shown[1] && shown[1] < shown[2])
        for (older in listOf("Step 1", "Step 2")) {
            assertEquals(older, 0, rule.onAllNodesWithText(older, useUnmergedTree = true).fetchSemanticsNodes().size)
        }
        // Only a reported step says so.
        rule.onNodeWithText("Assistant says", useUnmergedTree = true).assertExists()
    }

    @Test fun aReportedStepIsDrawnApartFromTheProgramsOwnSteps() {
        val chain = listOf(
            InboxChainEntry("c1", at = "2026-01-01T11:01:00Z", kind = "received", text = "Received", via = "chat"),
            InboxChainEntry("c2", at = "2026-01-01T11:02:00Z", kind = "reported", text = "Sent the email", via = "agent"),
        )
        val withChain = record().let { it.copy(event = it.event.copy(chain = chain)) }
        show(withChain)
        assertEquals("reported", chainIconMeaning(chain[1]))
        assertEquals("the reported step's tag", 1,
            rule.onAllNodesWithText("Assistant says", useUnmergedTree = true).fetchSemanticsNodes().size)
    }
}
