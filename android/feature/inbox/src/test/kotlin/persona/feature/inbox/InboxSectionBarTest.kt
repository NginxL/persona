package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotSelected
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.isHeading
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isSelected
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.ZoneId
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxBar
import persona.core.domain.inbox.InboxGroups
import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.Navigator
import persona.core.inbox.*

/**
 * The Inbox's section bar on Android (#9053, docs/assistant/inbox.md "Section bar"): it opens on Needs you, whatever
 * the old bays kept; a choice draws its section alone under a heading that doesn't fold, All the stacked page; the
 * counts and their tones are spoken with the names; a search shows All and clearing it brings the choice back; an item
 * opened from outside the list moves it; an empty choice says so in its own words; grouping lives in the ⋯. Shots of
 * a long inbox in every style, light and dark, and at 200% text, in build/inbox-section-bar-shots/.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
class InboxSectionBarTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(minutes: Long) = now.minus(minutes, ChronoUnit.MINUTES).toString()

    private fun mail(id: String, title: String, tier: String, at: Long, status: String? = null, doneAt: String? = null, account: String = "acc1") =
        InboxRecord(InboxItem("mail:$id", account = account, kind = "mail", title = title, sender = "Example Sender", preview = "A short note.",
            tier = tier, status = status, updatedAt = ago(at), doneAt = doneAt, version = "v-$id"), sourceLabel = "me@example.com", source = "mail")
    private fun lark(id: String, title: String, tier: String, at: Long, status: String? = null, doneAt: String? = null) =
        InboxRecord(InboxItem("lark:$id", account = "feishu", kind = "dm", title = title, sender = "Example Person", preview = "A quick question.",
            tier = tier, status = status, updatedAt = ago(at), doneAt = doneAt, version = "v-$id"), sourceLabel = "Lark", source = "lark")

    private val budget = mail("a1", "Budget sign-off", "action", 20)
    private val release = lark("a2", "Release checklist", "action", 40)
    private val drafting = lark("w1", "Reply being drafted", "action", 60, status = "working")
    private val digest = mail("f1", "Weekly digest", "fyi", 90)
    private val office = lark("f2", "Office note", "fyi", 120)
    private val newsletter = mail("n1", "Newsletter", "noise", 150)
    private val invoice = lark("d1", "Invoice paid", "fyi", 30, doneAt = ago(10))
    private val card = InboxInteraction("q1", "mac", "s1", createdAt = ago(5), sessionTitle = "Photo script", summary = "Which format?")
    private val all = listOf(budget, release, drafting, digest, office, newsletter, invoice)

    private class BarInbox(records: List<InboxRecord>, prefs: InboxPreferences = InboxPreferences(),
                           interactions: List<InboxInteraction> = emptyList(), val days: List<InboxDoneDay>? = null) : InboxRepository {
        val prefs = MutableStateFlow(prefs)
        override val preferences = this.prefs
        override suspend fun savePreferences(value: InboxPreferences) { prefs.value = value }
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records, interactions = interactions))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun doneHistory(manager: String?, cursor: InboxDoneCursor?) = InboxDonePage()
        override suspend fun doneDays(manager: String?, zone: ZoneId) = days
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var fontScale by mutableFloatStateOf(1f)

    private fun show(inbox: BarInbox, landing: InboxLanding? = null) {
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    // No ripples: Robolectric has no render thread, so a tapped choice would keep a frozen one in the shots.
                    CompositionLocalProvider(LocalRippleConfiguration provides null, LocalInboxLanding provides landing) {
                        Box(Modifier.testTag("shot").width(400.dp).height(1500.dp).background(PersonaTheme.palette.background)) {
                            InboxScreen(inbox, navigator)
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun see(vararg texts: String) = texts.forEach { assertTrue(it, rule.onAllNodesWithText(it).fetchSemanticsNodes().isNotEmpty()) }
    private fun gone(vararg texts: String) = texts.forEach { assertTrue(it, rule.onAllNodesWithText(it).fetchSemanticsNodes().isEmpty()) }
    /** A choice of the bar (a heading with the same words has no selected state). */
    private fun choice(label: String) = rule.onNode(hasText(label) and SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected))
    private fun choices(): List<String> = rule.onAllNodes(SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected)).fetchSemanticsNodes()
        .mapNotNull { it.config.getOrNull(SemanticsProperties.Text)?.firstOrNull()?.text }
    /** A section's heading that folds on a tap (a heading without one has its words on a child node). */
    private fun foldingHeader(title: String) = rule.onAllNodes(hasText(title) and isHeading() and hasClickAction()).fetchSemanticsNodes().isNotEmpty()
    private fun spoken(text: String) = rule.onAllNodes(hasContentDescription(text)).fetchSemanticsNodes().isNotEmpty()
    /** Taps a choice, scrolling the bar to it first as a person would: on a phone the last choices sit past its edge. */
    private fun pick(label: String) { choice(label).performScrollTo().performClick(); rule.waitForIdle() }

    @Test fun opensOnNeedsYouWhateverTheOldBaysKept() {
        show(BarInbox(all, InboxPreferences(section = "done"), listOf(card)))
        choice("Needs you").assertIsSelected()
        assertEquals(listOf("Needs you", "In progress", "FYI", "Done", "All"), choices())
        see("Budget sign-off", "Release checklist", "Photo script")
        gone("Weekly digest", "Reply being drafted", "Newsletter", "Invoice paid")
    }

    @Test fun theCountsAreTheRowsAndPartOfTheSpokenName() {
        show(BarInbox(all, interactions = listOf(card)))
        // Needs you: two items and the waiting Agent; In progress and FYI their rows; Done and All no number.
        assertTrue(spoken("Needs you, 3"))
        assertTrue(spoken("In progress, 1"))
        assertTrue(spoken("FYI, 2"))
        assertTrue("Done has no count", choice("Done").fetchSemanticsNode().config.getOrNull(SemanticsProperties.ContentDescription) == null)
        assertTrue("Needs you's badge", rule.onAllNodes(hasText("3"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
    }

    @Test fun aChoiceDrawsItsSectionAloneUnderAHeadingThatDoesNotFold() {
        show(BarInbox(all, interactions = listOf(card)))
        // Inside a choice the section's heading doesn't fold (In progress's never did); under All they fold as before.
        assertTrue(!foldingHeader("Needs you"))
        pick("FYI")
        choice("FYI").assertIsSelected()
        see("Weekly digest", "Office note")
        gone("Budget sign-off", "Reply being drafted")
        assertTrue(!foldingHeader("FYI"))
        pick("In progress")
        see("Reply being drafted")
        gone("Weekly digest", "Budget sign-off")
        pick("Done")
        see("Invoice paid")
        gone("Budget sign-off", "Weekly digest")
        assertTrue(!foldingHeader("Done"))
        pick("All")
        see("Budget sign-off", "Reply being drafted", "Weekly digest")
        assertTrue(foldingHeader("Needs you") && foldingHeader("FYI") && foldingHeader("Collapsed") && foldingHeader("Done"))
        // Collapsed and Done start folded under All, as the stacked page always did.
        gone("Newsletter", "Invoice paid")
    }

    @Test fun anEmptyChoiceSaysSoInItsOwnWords() {
        show(BarInbox(listOf(digest)))
        see("Nothing needs you", "The rest are in the other sections above.")
        gone("All caught up")
        assertEquals("a choice stays while chosen", listOf("Needs you", "FYI", "All"), choices())
    }

    @Test fun allCaughtUpOnlyWhenNothingIsOpenAnywhere() {
        show(BarInbox(listOf(invoice)))
        see("All caught up")
        gone("Nothing needs you")
        assertEquals(listOf("Needs you", "Done", "All"), choices())
        pick("All")
        // Under All, Done is still below it.
        see("All caught up")
        assertTrue(rule.onAllNodesWithText("Done").fetchSemanticsNodes().size >= 2)
    }

    @Test fun aSearchShowsAllAndClearingBringsTheChoiceBack() {
        val inbox = BarInbox(all, interactions = listOf(card))
        show(inbox)
        pick("FYI")
        rule.onNode(hasSetTextAction()).performTextInput("dig")
        rule.waitForIdle()
        choice("All").assertIsSelected()
        // All sits past the edge of a phone's bar: the bar scrolls it into sight.
        choice("All").assertIsDisplayed()
        // The field keeps its focus and text while the bar moves: typing goes on.
        rule.onNode(hasSetTextAction()).performTextInput("est")
        rule.waitForIdle()
        rule.onNode(hasSetTextAction() and hasText("digest")).assertExists()
        see("Weekly digest")
        gone("Office note", "Budget sign-off")
        // The counts say where the matches are.
        assertTrue(spoken("FYI, 1"))
        assertTrue(spoken("Needs you"))
        rule.onNode(hasSetTextAction()).performTextClearance()
        rule.waitForIdle()
        choice("FYI").assertIsSelected()
        choice("All").assertIsNotSelected()
    }

    @Test fun aKeptSearchOpensOnAllAndClearingGoesToNeedsYou() {
        show(BarInbox(all, InboxPreferences(query = "digest")))
        choice("All").assertIsSelected()
        rule.onNode(hasSetTextAction()).performTextClearance()
        rule.waitForIdle()
        choice("Needs you").assertIsSelected()
    }

    @Test fun anItemOpenedFromOutsideMovesTheList() {
        val inbox = BarInbox(all)
        val landing = InboxLanding().apply { land(invoice) }
        show(inbox, landing)
        // From the first frame: a done item's link lands on Done.
        choice("Done").assertIsSelected()
        see("Invoice paid")
        assertNull("taken once", landing.pending)
        // A collapsed item lands on All with Collapsed opened, kept with the other folds.
        rule.runOnIdle { landing.land(newsletter) }
        rule.waitForIdle()
        choice("All").assertIsSelected()
        see("Newsletter")
        assertTrue(InboxBar.NOISE in inbox.prefs.value.expanded)
        // A list that already shows the item does not move: All shows every section.
        rule.runOnIdle { landing.land(drafting) }
        rule.waitForIdle()
        choice("All").assertIsSelected()
        pick("FYI")
        rule.runOnIdle { landing.land(drafting) }
        rule.waitForIdle()
        choice("In progress").assertIsSelected()
        // Nor does the item's own choice.
        rule.runOnIdle { landing.land(drafting) }
        rule.waitForIdle()
        choice("In progress").assertIsSelected()
    }

    @Test fun aSidebarReturnLandsOffAllOntoTheItemsChoice() {
        val landing = InboxLanding()
        show(BarInbox(all), landing)
        pick("All")
        // enter then land, one turn: iOS's new page starts on Needs you, then land (InboxView.swift:209–212).
        rule.runOnIdle {
            landing.enter()
            landing.land(digest)
        }
        rule.waitForIdle()
        choice("FYI").assertIsSelected()
        choice("All").assertIsNotSelected()
        assertNull(landing.pending)
    }

    @Test fun theListInFrontTakesTheLandingAHiddenListLeaves() {
        val landing = InboxLanding()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalRippleConfiguration provides null, LocalInboxLanding provides landing) {
                    Column {
                        CompositionLocalProvider(LocalInboxListActive provides false) {
                            Box(Modifier.height(400.dp)) { InboxScreen(BarInbox(all), navigator) }
                        }
                        CompositionLocalProvider(LocalInboxListActive provides true) {
                            Box(Modifier.height(400.dp)) { InboxScreen(BarInbox(all), navigator) }
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        rule.runOnIdle { landing.land(invoice) }
        rule.waitForIdle()
        assertEquals(1, rule.onAllNodes(hasText("Done") and isSelected()).fetchSemanticsNodes().size)
        assertEquals(1, rule.onAllNodes(hasText("Needs you") and isSelected()).fetchSemanticsNodes().size)
        assertNull(landing.pending)
    }

    @Test fun anEntryPutsAListLeftUnderOtherPagesBackOnNeedsYou() {
        val landing = InboxLanding()
        show(BarInbox(all), landing)
        pick("FYI")
        // A widget or a link to the Inbox while this list sat under other pages: it opens as on entry.
        rule.runOnIdle { landing.enter() }
        rule.waitForIdle()
        choice("Needs you").assertIsSelected()
        assertNull("taken once", landing.pending)
    }

    @Test fun aLandingDroppedBeforeTheListShowedLeavesTheNextEntryOnNeedsYou() {
        // A link landed the list under its item, and the stack let go of that list before Back showed it.
        val landing = InboxLanding().apply { land(invoice); clear() }
        show(BarInbox(all), landing)
        choice("Needs you").assertIsSelected()
    }

    @Test fun anItemOpenedBesideTheListMovesItOnlyWhileTheListIsDrawn() {
        val landing = InboxLanding()
        val digestPage = InboxItemRoute(digest.item.id)
        val invoicePage = InboxItemRoute(invoice.item.id)
        // On a phone the list is not drawn under the item: another item's chain leaves it as the user left it.
        landing.landBeside(invoice, invoicePage)
        assertNull(landing.pending)
        show(BarInbox(all), landing)
        assertEquals("the list counts itself drawn", 1, landing.drawn)
        pick("FYI")
        rule.runOnIdle { landing.landBeside(invoice, invoicePage) }
        rule.waitForIdle()
        choice("Done").assertIsSelected()
        // Back from that item: the list goes back to the choice it had; the item it was opened from stays open.
        rule.runOnIdle { landing.closed(listOf(InboxRoute, digestPage, invoicePage)) }
        rule.waitForIdle()
        choice("Done").assertIsSelected()
        rule.runOnIdle { landing.closed(listOf(InboxRoute, digestPage)) }
        rule.waitForIdle()
        choice("FYI").assertIsSelected()
        assertNull("put back once", landing.restore)
        // A choice made after the move is the user's: closing the page later leaves it.
        rule.runOnIdle { landing.landBeside(invoice, invoicePage) }
        rule.waitForIdle()
        choice("Done").assertIsSelected()
        pick("All")
        rule.runOnIdle { landing.closed(listOf(InboxRoute, digestPage)) }
        rule.waitForIdle()
        choice("All").assertIsSelected()
    }

    @Test fun needsYouStaysInSightFromEveryChoice() {
        show(BarInbox(many(), interactions = listOf(card)))
        pick("All")
        for (s in AppStyle.entries) {
            style = s
            Snapshot.sendApplyNotifications(); rule.waitForIdle()
            choice("All").assertIsSelected().assertIsDisplayed()
            // The bar scrolled to All; Needs you, pinned at its start, is whole in sight with its count.
            val needs = choice("Needs you").fetchSemanticsNode()
            assertEquals("${s.key}: Needs you whole", needs.size.width.toFloat(), needs.boundsInRoot.width, 1f)
            assertTrue("${s.key}: its count spoken", spoken("Needs you, 31"))
        }
        style = AppStyle.Glass
    }

    @Test fun groupingIsInTheMoreSheetWhereItApplies() {
        val inbox = BarInbox(all)
        show(inbox)
        gone("By source", "By time")
        rule.onAllNodesWithContentDescription("More").onFirst().performClick()
        rule.waitForIdle()
        see("Group by", "By source", "By time")
        // Clean up all keeps its scope: every open item, not only the choice's.
        see("Clean up all · 6 items")
        rule.onNodeWithText("By time").performClick()
        rule.waitForIdle()
        assertEquals(InboxGroups.TIME, inbox.prefs.value.grouping)
        pick("Done")
        rule.onAllNodesWithContentDescription("More").onFirst().performClick()
        rule.waitForIdle()
        gone("Group by", "By time")
    }

    @Test fun groupingIsNotOfferedInProgress() {
        // In progress is grouped by neither source nor time (docs/assistant/inbox.md "Section bar"), as Done.
        show(BarInbox(all))
        pick("In progress")
        rule.onAllNodesWithContentDescription("More").onFirst().performClick()
        rule.waitForIdle()
        see("Clean up all · 6 items")
        gone("Group by", "By source", "By time")
    }

    @Test fun doneOnTheHubsCountsAlone() {
        val today = now.atZone(ZoneId.systemDefault()).toLocalDate()
        show(BarInbox(listOf(budget), days = listOf(InboxDoneDay(today, 40))))
        assertEquals(listOf("Needs you", "Done", "All"), choices())
        pick("Done")
        assertTrue("Done's heading counts what the hub counted", rule.onAllNodes(hasText("40"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        gone("Nothing done yet")
        // All stays the stacked page: no Done section without a done row, only the choice.
        pick("All")
        assertEquals(1, rule.onAllNodesWithText("Done").fetchSemanticsNodes().size)
    }

    private fun shots(name: String, styles: List<AppStyle> = AppStyle.entries, themes: List<Boolean> = listOf(false, true)) {
        val dir = File(System.getProperty("persona.shots") ?: "build/inbox-section-bar-shots").also { it.mkdirs() }
        for (s in styles) for (d in themes) {
            style = s; dark = d
            Snapshot.sendApplyNotifications(); rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(dir, "$name-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        style = AppStyle.Glass; dark = false
        Snapshot.sendApplyNotifications(); rule.waitForIdle()
    }

    /** A long inbox, as the mock's MOCK_INBOX_MANY: dozens of rows in every section. */
    private fun many(): List<InboxRecord> = List(30) { i -> if (i % 2 == 0) mail("ma$i", "Request $i", "action", 10L + i * 30) else lark("la$i", "Question $i", "action", 10L + i * 30) } +
        List(6) { i -> lark("w$i", "Being handled $i", "action", 15L + i * 40, status = "working") } +
        List(40) { i -> mail("mf$i", "Digest $i", "fyi", 20L + i * 50, account = if (i % 3 == 0) "acc2" else "acc1") } +
        List(20) { i -> lark("n$i", "Notice $i", "noise", 30L + i * 60) } +
        List(25) { i -> mail("md$i", "Handled $i", "fyi", 40L + i * 70, doneAt = ago(5L + i * 70)) }

    @Test fun drawsTheBarInEveryStyle() {
        show(BarInbox(many(), interactions = listOf(card)))
        shots("needs-you")
        pick("FYI")
        shots("fyi")
        pick("All")
        shots("all")
    }

    @Test fun drawsAnEmptyChoiceInEveryStyle() {
        show(BarInbox(listOf(digest, office)))
        shots("nothing-needs-you")
    }

    @Test fun drawsTheBarAtLargeText() {
        fontScale = 2f
        show(BarInbox(many(), interactions = listOf(card)))
        shots("needs-you-200", themes = listOf(false))
    }
}
