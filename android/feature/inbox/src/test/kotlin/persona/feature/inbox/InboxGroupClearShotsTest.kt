package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
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
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isHeading
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlin.math.roundToInt
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * Robolectric native-graphics coverage for #10425: both changed group titles and their two-choice menus in Glass, Brief
 * and Board, light and dark, at ordinary and 200% text. Separate Robolectric configurations are essential: a Compose
 * LocalDensity override on the page does not reach a popup window. PNGs go to build/inbox-group-clear-shots/.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InboxGroupClearShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun lark(id: String, title: String, minutes: Long) = InboxRecord(
        InboxItem(id, account = "feishu", kind = "dm", title = title, sender = "Example Person", preview = "A short update.",
            tier = "fyi", updatedAt = "2026-11-19T10:${minutes.toString().padStart(2, '0')}:00Z", version = "v-$id"),
        sourceLabel = "Lark", source = "lark")

    private fun mail(id: String, title: String, minutes: Long) = InboxRecord(
        InboxItem(id, account = "acc1", kind = "mail", title = title, sender = "Example Sender", preview = "A short note.",
            tier = "fyi", updatedAt = "2026-11-19T10:${minutes.toString().padStart(2, '0')}:00Z", version = "v-$id"),
        sourceLabel = "me@example.com", source = "mail")

    private val records = listOf(
        lark("f1", "Release notes", 50),
        lark("f2", "Office update", 40),
        mail("f3", "Weekly digest", 30),
        mail("f4", "Receipt available", 20),
    )
    private val collapsedRecords = records.map { it.copy(item = it.item.copy(tier = "noise")) }

    private class ShotInbox(records: List<InboxRecord>, preferences: InboxPreferences = InboxPreferences()) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records, refreshes = 1))
        override val connectionState = flowOf(ConnectionState.Online)
        override val preferences = MutableStateFlow(preferences)
        val asked = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            asked += items to action
            // Keep the fixture on screen for the next appearance, as the other screenshot fakes do.
            return InboxBulkResult(changed = items.map { it.id })
        }
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

    private fun out() = File(System.getProperty("persona.shots") ?: "build/inbox-group-clear-shots").also { it.mkdirs() }
    private fun metrics() = File(out(), "metrics").also { it.mkdirs() }
    private fun configuredFontScale() = ApplicationProvider.getApplicationContext<android.content.Context>()
        .resources.configuration.fontScale

    private fun save(name: String, bitmap: Bitmap) {
        File(out(), "$name.png").outputStream().use {
            assertTrue("$name could not be encoded", bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }

    private fun openSourceGroup(label: String) {
        val group = hasText("Lark") and isHeading()
        val direct = rule.onAllNodes(hasText(label) and hasClickAction() and hasAnyAncestor(group))
        if (direct.fetchSemanticsNodes().isNotEmpty()) {
            direct[0].performScrollTo().performClick()
        } else {
            // At 200% text JSectionHeader may move the same action into the group's overflow rather than clip it.
            val overflow = rule.onAllNodes(hasTestTag("inbox.section.overflow") and hasAnyAncestor(group), useUnmergedTree = true)
            assertTrue("The Lark group action must remain reachable at large text", overflow.fetchSemanticsNodes().isNotEmpty())
            overflow[0].performScrollTo().performClick()
            rule.onNode(hasText(label) and hasClickAction()).performClick()
        }
        rule.waitForIdle()
    }

    private fun renderedLayout(text: String): TextLayoutResult {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    /** The actual glyph height in the popup, not the page's density or a screenshot's filename. */
    private fun renderedTextHeight(text: String): Int = renderedLayout(text).getBoundingBox(0).height.roundToInt()

    private fun assertWholeChoice(text: String) {
        val layout = renderedLayout(text)
        val visible = layout.getLineEnd(layout.lineCount - 1, visibleEnd = true)
        assertTrue("$text shows only $visible of ${text.length} characters at ${configuredFontScale()}x", visible == text.length)
    }

    private fun checkLargeTextEvidence(key: String, bitmap: Bitmap, label: String) {
        val largeText = renderedTextHeight(label)
        val metric = File(metrics(), "$key.txt")
        if (configuredFontScale() == 1f) {
            metric.writeText("${bitmap.width},${bitmap.height},$largeText")
            return
        }
        assertTrue("Normal-text metric $metric is missing; run the complete screenshot class", metric.isFile)
        val (width, height, normalText) = metric.readText().split(',').map { it.toInt() }
        assertTrue(
            "$key: 200% menu must grow in capture geometry or rendered text; " +
                "normal=${width}x$height/text=$normalText, large=${bitmap.width}x${bitmap.height}/text=$largeText",
            bitmap.width > width || bitmap.height > height || largeText > normalText,
        )
    }

    private fun verifyEveryAppearance(
        collapsed: Boolean,
        section: String = if (collapsed) "All" else "FYI",
        action: String = if (collapsed) "Clear all" else "Mark all seen",
        groupChoice: String = if (collapsed) "Clear all 2 in “Lark”" else "Mark all 2 in “Lark” seen",
        sectionChoice: String = if (collapsed) "Clear all 4 in “Collapsed”" else "Mark all 4 in “FYI” seen",
        prefix: String = if (collapsed) "collapsed" else "fyi",
        appearances: List<AppStyle> = AppStyle.entries,
        compareTextGrowth: Boolean = true,
    ) {
        val inbox = if (collapsed) ShotInbox(collapsedRecords, InboxPreferences(expanded = listOf("action", "fyi", "noise")))
        else ShotInbox(records)
        rule.setContent {
            // Do not override LocalDensity: Robolectric's configuration creates both the Activity and popup at this scale.
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalRippleConfiguration provides null) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1200.dp).background(PersonaTheme.palette.background)) {
                        InboxScreen(inbox, navigator)
                    }
                }
            }
        }
        rule.waitForIdle()
        rule.onNode(hasText(section) and androidx.compose.ui.test.SemanticsMatcher.keyIsDefined(
            androidx.compose.ui.semantics.SemanticsProperties.Selected)).performClick()
        rule.waitForIdle()

        val scale = (configuredFontScale() * 100).roundToInt()
        for (appearance in appearances) for (night in listOf(false, true)) {
            rule.runOnIdle {
                style = appearance
                dark = night
                Snapshot.sendApplyNotifications()
            }
            rule.waitForIdle()
            val mode = if (night) "dark" else "light"
            val suffix = "${appearance.key}-$mode-$scale"
            save("$prefix-title-$suffix", rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap())

            openSourceGroup(action)
            rule.onNodeWithText(groupChoice).assertExists()
            rule.onNodeWithText(sectionChoice).assertExists()
            assertWholeChoice(groupChoice)
            assertWholeChoice(sectionChoice)
            rule.onNode(isPopup()).assertExists()
            assertTrue("The choices must stay in an anchored pull-down, not a sheet", rule.onAllNodes(isDialog()).fetchSemanticsNodes().isEmpty())
            val menu = rule.onAllNodes(isPopup()).onLast().captureToImage().asAndroidBitmap()
            save("$prefix-menu-$suffix", menu)
            if (compareTextGrowth) checkLargeTextEvidence("$prefix-menu-${appearance.key}-$mode", menu, groupChoice)

            // Dismiss through the group choice. The fake leaves its source snapshot unchanged for the next appearance.
            val expectedCalls = inbox.asked.size + 1
            rule.onNodeWithText(groupChoice).performClick()
            rule.waitUntil(timeoutMillis = 5_000) { inbox.asked.size == expectedCalls }
            rule.waitForIdle()
        }
    }

    @Test fun aFyiNormalText() = verifyEveryAppearance(collapsed = false)

    @Config(fontScale = 2f)
    @Test fun bFyiLargeText() = verifyEveryAppearance(collapsed = false)

    @Test fun cCollapsedNormalText() = verifyEveryAppearance(collapsed = true)

    @Config(fontScale = 2f)
    @Test fun dCollapsedLargeText() = verifyEveryAppearance(collapsed = true)

    @Config(qualifiers = "ja-w400dp-h1200dp-xhdpi", fontScale = 2f)
    @Test fun eJapaneseLargeTextKeepsTheWholeLongChoice() = verifyEveryAppearance(
        collapsed = false,
        section = "参考",
        action = "すべて確認",
        groupChoice = "「Lark」の2件をすべて確認済みにする",
        sectionChoice = "「参考」の4件をすべて確認済みにする",
        prefix = "fyi-japanese",
        appearances = listOf(AppStyle.Glass),
        compareTextGrowth = false,
    )
}
