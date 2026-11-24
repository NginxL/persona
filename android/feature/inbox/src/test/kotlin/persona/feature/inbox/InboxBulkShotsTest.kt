package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
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
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import persona.core.inbox.*

/**
 * The inbox's More sheet with its one bulk entry Clean up all (#5114), and the confirmation it asks for over
 * [BULK_CONFIRM_AT] items, on the JVM with Robolectric's native graphics, light and dark. Confirming files every open
 * item as dismissed in one request. The PNGs go to `build/inbox-bulk-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxBulkShotsTest {
    @get:Rule val rule = createComposeRule()

    private val records = (1..12).map { n ->
        InboxRecord(InboxItem("mail:a1:t:$n", kind = "mail", title = "周报 $n", sender = "示例同事", preview = "这周的进展和下周的计划。",
            tier = if (n % 2 == 0) "action" else "fyi", updatedAt = "2026-11-04T09:${10 + n}:00Z", version = "v$n"),
            sourceLabel = "邮件", source = "mail")
    }

    private class OneInbox(snapshot: InboxSnapshot, subscriptions: Boolean = false) : InboxRepository {
        override val subscriptionsShown = MutableStateFlow(subscriptions)
        val asked = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            asked += items to action
            return InboxBulkResult(changed = items.map { it.id })
        }
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigated = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { navigated += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-bulk-shots").also { it.mkdirs() }

    private fun save(name: String, bitmap: Bitmap) {
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Suppress("UNCHECKED_CAST")
    private fun screen(): Bitmap {
        val global = Class.forName("android.view.WindowManagerGlobal")
        val instance = global.getMethod("getInstance").invoke(null)
        val views = global.getDeclaredField("mViews").apply { isAccessible = true }.get(instance) as List<android.view.View>
        val first = views.first()
        val bitmap = Bitmap.createBitmap(first.width, first.height, Bitmap.Config.ARGB_8888)
        val canvas = android.graphics.Canvas(bitmap)
        views.forEach { view ->
            val at = IntArray(2).also { view.getLocationOnScreen(it) }
            canvas.save(); canvas.translate(at[0].toFloat(), at[1].toFloat()); view.draw(canvas); canvas.restore()
        }
        return bitmap
    }

    private fun shoot(dark: Boolean) {
        val inbox = OneInbox(InboxSnapshot(records = records))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxScreen(inbox, navigator)
                }
            }
        }
        val mode = if (dark) "dark" else "light"
        rule.waitForIdle()
        rule.onNodeWithContentDescription("更多").performClick()
        rule.waitForIdle()
        // The sheet is its own window over the page; captureToImage of that window draws the page under it on the JVM,
        // so every window is drawn in order, as the screen shows them.
        save("inbox-bulk-sheet-$mode", screen())
        // One bulk entry for all 12 open items, then the confirmation.
        val entries = rule.onAllNodes(hasText(" · 12 件", substring = true)).fetchSemanticsNodes()
        assertEquals("one bulk entry", 1, entries.size)
        rule.onNode(hasText("全部清理 · 12 件")).performClick()
        rule.waitForIdle()
        save("inbox-bulk-confirm-$mode", rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
        rule.onNode(hasText("清理 12 件")).performClick()
        rule.waitForIdle()
        assertEquals(1, inbox.asked.size)
        assertEquals(InboxBulkAction.Dismiss, inbox.asked.single().second)
        assertEquals(records.map { InboxDoneTarget(it.item.id, it.item.version) }.toSet(), inbox.asked.single().first.toSet())
    }

    @Test fun light() = shoot(dark = false)
    @Test fun dark() = shoot(dark = true)

    /** Source subscriptions (#2041): once the hub shows them, their entry joins the Inbox's other settings and opens their pages. */
    private fun subscriptions(dark: Boolean) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxScreen(OneInbox(InboxSnapshot(records = records), subscriptions = true), navigator)
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithContentDescription("更多").performClick()
        rule.waitForIdle()
        save("inbox-subscriptions-sheet-${if (dark) "dark" else "light"}", screen())
        rule.onNode(hasText("订阅来源")).performClick()
        rule.waitForIdle()
        val route = navigated.single() as persona.core.navigation.ExtensionUiRoute
        assertEquals(persona.core.contracts.api.SUBSCRIPTIONS_UI, route.extensionId)
    }

    @Test fun subscriptionsLight() = subscriptions(dark = false)
    @Test fun subscriptionsDark() = subscriptions(dark = true)

    @Test fun noSubscriptionsEntryUntilTheHubShowsThem() {
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxScreen(OneInbox(InboxSnapshot(records = records)), navigator) } }
        rule.waitForIdle()
        rule.onNodeWithContentDescription("更多").performClick()
        rule.waitForIdle()
        rule.onNode(hasText("订阅来源")).assertDoesNotExist()
    }
}
