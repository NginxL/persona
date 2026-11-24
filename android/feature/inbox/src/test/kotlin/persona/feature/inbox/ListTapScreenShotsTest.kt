package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import kotlin.math.abs
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
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
import java.io.File
import persona.core.inbox.*

/**
 * The real Inbox (#9600), Glass, phone and foldable. One bulk Mark as done, start / mid / end.
 * PNGs in `build/list-tap-r1/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w800dp-h1400dp-mdpi")
class ListTapScreenShotsTest {
    @get:Rule val rule = createComposeRule()

    private var dark by mutableStateOf(false)
    private var width by mutableStateOf(411.dp)
    private var inbox by mutableStateOf(Sample())

    @Test fun bulkDone() {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.size(width, 900.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                    InboxScreen(inbox, Nowhere)
                }
            }
        }
        for (schemeDark in listOf(false, true)) for ((form, formWidth) in listOf("phone" to 411.dp, "foldable" to 673.dp)) {
            rule.mainClock.autoAdvance = true
            inbox = Sample()
            rule.runOnIdle {
                dark = schemeDark
                width = formWidth
            }
            rule.waitForIdle()
            // The section capsule uses Tab; the source/kind strip and any filter sheet do not (#13243).
            sectionChip("All").performClick()
            rule.waitForIdle()
            rule.onNodeWithText("Weekly report").performTouchInput { longClick() }
            rule.waitForIdle()
            rule.mainClock.autoAdvance = false
            val scheme = if (schemeDark) "dark" else "light"
            val stem = "done-$scheme-$form"
            val start = top("Office hours")
            save("$stem-start")
            rule.onNodeWithText("Mark as done").performClick()
            Snapshot.sendApplyNotifications()
            val samples = mutableListOf(start)
            // The section bar and the search field are one lazy item. They snap with the title.
            val chips = mutableListOf(top(FILTERS))
            var savedMid = false
            var afterTitle: Float? = null
            repeat(16) {
                rule.mainClock.advanceTimeByFrame()
                // Needs you and FYI have rows, so they are in the bar; Done is not until a row is filed.
                // The section header also says "Needs you".
                assertChip("Needs you")
                assertChip("FYI")
                assertChip("All")
                val y = top("Office hours")
                samples += y
                chips += top(FILTERS)
                // The title appears first and shifts the list at once. The removal spring is the move after that.
                if (afterTitle == null && abs(y - start) > 4f) afterTitle = y
                val removal = afterTitle?.let { abs(y - it) > 4f } == true
                // Idle rows hold no GraphicsLayer until armed; mirror Choreographer's per-frame draw on the armed
                // frame before removal so Robolectric populates the newly allocated fadeOut GraphicsLayer.
                if (afterTitle != null && !removal) rule.onNodeWithTag("shot").captureToImage()
                if (!savedMid && removal) {
                    save("$stem-mid")
                    savedMid = true
                    // The fading row is drawn by the item animator, so it is not in the semantics tree.
                    // Its title is a dark line of its own, below Release notes (InboxView.swift:632-634).
                    // On the old frame that line was missing: the ghost sat on Release notes.
                    val notes = rule.onAllNodesWithText("Release notes", useUnmergedTree = true)
                        .fetchSemanticsNodes().map { it.boundsInRoot }
                    val notesBottom = notes.maxOf { it.bottom }.toInt()
                    val image = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                    var ownSlot = -1
                    for (py in notesBottom + 8 until minOf(image.height, notesBottom + 120)) {
                        var dark = 0
                        for (px in 60 until image.width - 40 step 2) {
                            val pixel = image.getPixel(px, py)
                            if (android.graphics.Color.red(pixel) < 80 &&
                                android.graphics.Color.green(pixel) < 80 &&
                                android.graphics.Color.blue(pixel) < 80) dark++
                        }
                        if (dark > 8) { ownSlot = py; break }
                    }
                    assertTrue("$stem fading row overlaps Release notes (no title below $notesBottom)", ownSlot > notesBottom)
                }
            }
            rule.mainClock.advanceTimeBy(700)
            val end = top("Office hours")
            val endChip = top(FILTERS)
            save("$stem-end")
            if (!savedMid) save("$stem-mid")
            assertChip("Needs you")
            assertChip("FYI")
            assertChip("All")
            assertChip("Done")
            rule.onAllNodesWithText("Weekly report").assertCountEquals(0)
            // The large title appears at once (InboxView.swift:152-153). The search field snaps with that shift.
            // Office hours springs only the distance the removal added on top of that shared shift.
            val chipStart = chips.first()
            assertTrue(
                "$stem chips traveled $chips -> $endChip",
                (chips + endChip).all { abs(it - chipStart) < 2f || abs(it - endChip) < 2f },
            )
            val moved = samples.filter { abs(it - start) > 2f }
            val origin = moved.firstOrNull() ?: end
            val low = minOf(origin, end) - 16f
            val high = maxOf(origin, end) + 16f
            assertTrue(
                "$stem flew ($start -> $end via $origin): $samples",
                abs(origin - end) > 8f &&
                    moved.any { y -> (y - origin) * (y - end) < 0f && abs(y - origin) > 4f && abs(y - end) > 4f } &&
                    moved.all { it in low..high },
            )
            rule.mainClock.autoAdvance = true
        }
    }

    @Test fun twoMarksInOneFrameSendOnce() {
        val inbox = armed()
        rule.onNodeWithText("Mark as done").performClick()
        rule.onNodeWithText("Mark as done").performClick()
        rule.mainClock.advanceTimeBy(400)
        assertEquals(1, inbox.bulkCalls)
    }

    @Test fun leavingBeforeTheFrameStillSends() {
        var show by mutableStateOf(true)
        val inbox = Sample()
        rule.mainClock.autoAdvance = false
        rule.setContent {
            if (show) {
                PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                    Box(Modifier.size(411.dp, 900.dp).background(PersonaTheme.palette.background)) { InboxScreen(inbox, Nowhere) }
                }
            }
        }
        rule.mainClock.autoAdvance = true
        rule.waitForIdle()
        rule.onNodeWithText("Weekly report").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.mainClock.autoAdvance = false
        rule.onNodeWithText("Mark as done").performClick()
        show = false
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(400)
        assertEquals(1, inbox.bulkCalls)
    }

    /** Select one row and stop the clock on the frame of Mark as done. */
    private fun armed(): Sample {
        val inbox = Sample()
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.size(411.dp, 900.dp).background(PersonaTheme.palette.background)) { InboxScreen(inbox, Nowhere) }
            }
        }
        rule.mainClock.autoAdvance = true
        rule.waitForIdle()
        rule.onNodeWithText("Weekly report").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.mainClock.autoAdvance = false
        return inbox
    }

    /**
     * The Glass section capsule is a Tab; the source/kind strip and dialog choices are not (AgentFilterCapsule).
     */
    private fun sectionChips(label: String) = rule.onAllNodes(
        SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Tab) and hasAnyDescendant(hasText(label)) and !hasAnyAncestor(isDialog()),
        useUnmergedTree = true,
    ).fetchSemanticsNodes()

    private fun sectionChip(label: String) = rule.onNode(
        SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Tab) and hasAnyDescendant(hasText(label)) and !hasAnyAncestor(isDialog()),
        useUnmergedTree = true,
    )

    private fun assertChip(label: String) {
        val nodes = sectionChips(label)
        val onScreen = nodes.filter { node ->
            val bounds = node.boundsInRoot
            bounds.width > 0f && bounds.height > 0f && bounds.bottom > 0f && bounds.top < 900f
        }
        assertTrue("$label chip not on screen: ${nodes.map { it.boundsInRoot }}", onScreen.isNotEmpty())
    }

    private fun top(text: String) = rule.onNodeWithText(text).getUnclippedBoundsInRoot().top.value

    /** The search field, in the same lazy item as the section bar. */
    private companion object {
        const val FILTERS = "Search titles, senders and text"
    }

    private class Sample : InboxRepository {
        override val subscriptionsShown = MutableStateFlow(false)
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(
            InboxRecord(InboxItem("mail:a:1", kind = "mail", title = "Weekly report", sender = "A colleague", preview = "What moved this week.",
                tier = "action", updatedAt = "2026-11-04T09:00:00Z", version = "v1"), sourceLabel = "Mail", source = "mail"),
            InboxRecord(InboxItem("mail:a:2", kind = "mail", title = "Release notes", sender = "A colleague", preview = "The build is ready.",
                tier = "action", updatedAt = "2026-11-04T09:05:00Z", version = "v2"), sourceLabel = "Mail", source = "mail"),
            InboxRecord(InboxItem("mail:a:3", kind = "mail", title = "Office hours", sender = "A colleague", preview = "Thursday afternoon.",
                tier = "fyi", updatedAt = "2026-11-04T09:10:00Z", version = "v3"), sourceLabel = "Mail", source = "mail"),
        )))
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        var bulkCalls = 0
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            bulkCalls++
            val ids = items.map { it.id }.toSet()
            val done = action == InboxBulkAction.Done || action == InboxBulkAction.Dismiss
            snapshot.value = snapshot.value.copy(records = snapshot.value.records.map { record ->
                if (record.item.id !in ids) record
                else record.copy(item = record.item.copy(doneAt = if (done) "2026-11-17T02:00:00Z" else null))
            })
            return InboxBulkResult(changed = items.map { it.id })
        }
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
    }

    private fun save(name: String) {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        assertTrue("$name drew nothing", bitmap.width > 50)
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
