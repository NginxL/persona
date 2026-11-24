package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
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
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.core.inbox.*

/** Geometry of the real Inbox page header, especially Board's compact title at accessibility text sizes. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class BoardLargeTextTest {
    @get:Rule val rule = createComposeRule()
    private val owner = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @After fun stop() { owner.cancel() }

    private class HeaderInbox(records: List<InboxRecord>) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = MutableStateFlow(ConnectionState.Online)
        override val preferences = MutableStateFlow(InboxPreferences(expanded = listOf("action", "fyi", "noise", "done")))
        override val conversationInbox = MutableStateFlow<InboxConversations?>(null)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private fun item(id: String, title: String) = InboxRecord(
        InboxItem(id, account = "chat", kind = "group", title = title, sender = "Example Person",
            preview = "A preview", tier = "action", updatedAt = "2026-11-13T10:00:00Z", version = "v-$id"),
        source = "lark", sourceLabel = "Chat connector", feedId = "shared-room", isGroup = true, groupName = "Planning room",
    )

    private fun show(style: AppStyle, dark: Boolean, fontScale: Float) {
        val inbox = HeaderInbox(listOf(item("first", "First event"), item("second", "Second event")))
        inbox.conversationInbox.value = InboxConversations(inbox, "synthetic-pairing", owner, ReplyComposers(inbox, owner))
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Box(Modifier.testTag(PAGE_TAG).width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                        InboxScreen(inbox, navigator)
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun textLayout(text: String): TextLayoutResult {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("one visible $text text node", 1, nodes.size)
        val layouts = mutableListOf<TextLayoutResult>()
        val _ = nodes.single().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun bounds(text: String): Rect {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("one visible $text text node", 1, nodes.size)
        return nodes.single().boundsInRoot
    }

    private fun bay(label: String): Rect = rule.onNode(
        SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Tab) and hasAnyDescendant(hasText(label)),
        useUnmergedTree = true,
    ).fetchSemanticsNode().boundsInRoot

    // The section bar (#9053): only action items here, so Needs you and All are its choices.
    private fun statisticsTop(): Float = listOf("Needs you", "All").minOf { bay(it).top }

    private fun firstTextTop(text: String): Float {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes()
        assertTrue("$text must remain present", nodes.isNotEmpty())
        return nodes.minOf { it.boundsInRoot.top }
    }

    private fun assertTitleAndSummaryHaveTheirOwnSpace(context: String) {
        val page = rule.onNodeWithTag(PAGE_TAG).fetchSemanticsNode().boundsInRoot
        val title = bounds("Inbox")
        val titleLayout = textLayout("Inbox")
        val firstGlyph = titleLayout.getBoundingBox(0)
        val lastGlyph = titleLayout.getBoundingBox("Inbox".lastIndex)
        assertTrue("$context: the title's first and last glyphs must be present, not ellipsized: " +
            "layout=${titleLayout.size} first=$firstGlyph last=$lastGlyph",
            firstGlyph.width > 0f && lastGlyph.width > 0f && firstGlyph.left >= 0f && lastGlyph.right <= titleLayout.size.width)
        assertTrue("$context: the whole title layout must start inside the page: page=$page title=$title",
            title.top >= page.top)
        assertTrue("$context: the first and last title glyphs must be inside the page: page=$page title=$title " +
            "first=$firstGlyph last=$lastGlyph",
            title.top + firstGlyph.top >= page.top && title.left + firstGlyph.left >= page.left &&
                title.left + lastGlyph.right <= page.right)

        val summary = bounds(SUMMARY)
        val summaryLayout = textLayout(SUMMARY)
        val summaryLineBottom = summaryLayout.getLineBottom(summaryLayout.lineCount - 1)
        val statsTop = statisticsTop()
        assertTrue("$context: title and summary stay in reading order: title=$title summary=$summary",
            title.bottom <= summary.top + 1f)
        assertTrue("$context: the complete summary line must end before the statistics cards: " +
            "summary=$summary layout=${summaryLayout.size} lineBottom=$summaryLineBottom statisticsTop=$statsTop",
            summary.top + summaryLineBottom <= statsTop + 1f)
    }

    @Test fun boardLightAt200PercentKeepsTitleAndSummaryClearOfStatistics() {
        show(AppStyle.Board, dark = false, fontScale = 2f)
        assertTitleAndSummaryHaveTheirOwnSpace("Board light 200%")
    }

    @Test fun boardDarkAt200PercentKeepsTitleAndSummaryClearOfStatistics() {
        show(AppStyle.Board, dark = true, fontScale = 2f)
        assertTitleAndSummaryHaveTheirOwnSpace("Board dark 200%")
    }

    @Test fun boardAt100PercentRetainsItsCompactHeaderGeometry() {
        show(AppStyle.Board, dark = false, fontScale = 1f)
        assertTitleAndSummaryHaveTheirOwnSpace("Board light 100%")

        // These are the measured pre-fix positions: natural text stays centered in the 56 dp bar at ordinary text.
        val density = ApplicationProvider.getApplicationContext<android.content.Context>().resources.displayMetrics.density
        assertEquals("Board 100% title keeps its original top", 9 * density, bounds("Inbox").top, 1f)
        assertEquals("Board 100% summary keeps its original bottom", 47.5f * density, bounds(SUMMARY).bottom, 1f)
        assertEquals("Board 100% statistics keep their original top", 60 * density, statisticsTop(), 1f)
    }

    @Test fun briefAt200PercentRetainsItsSummaryFirstGeometry() {
        show(AppStyle.Brief, dark = false, fontScale = 2f)
        // Brief's root page speaks the summary as its large headline; it does not also draw an "Inbox" title.
        assertTrue("Brief keeps its summary-first title", rule.onAllNodesWithText("Inbox", useUnmergedTree = true)
            .fetchSemanticsNodes().isEmpty())
        val page = rule.onNodeWithTag(PAGE_TAG).fetchSemanticsNode().boundsInRoot
        val summary = bounds(SUMMARY)
        assertTrue("Brief light 200%: summary starts inside the page: page=$page summary=$summary", summary.top >= page.top)
        assertTrue("Brief light 200%: quick filters remain present", firstTextTop("Needs you") >= summary.top)
    }

    @Test fun glassAt200PercentRetainsItsScrollingTitleGeometry() {
        show(AppStyle.Glass, dark = false, fontScale = 2f)
        val page = rule.onNodeWithTag(PAGE_TAG).fetchSemanticsNode().boundsInRoot
        val title = bounds("Inbox")
        val summary = bounds(SUMMARY)
        assertTrue("Glass light 200%: large title starts inside the page: page=$page title=$title", title.top >= page.top)
        assertTrue("Glass light 200%: summary follows its large title: title=$title summary=$summary",
            summary.top >= title.top)
        assertTrue("Glass light 200%: quick filters remain present", firstTextTop("Needs you") >= summary.top)
    }

    private companion object {
        const val PAGE_TAG = "board-large-text-page"
        const val SUMMARY = "2 to answer · Persona is on 0"
    }
}
