package persona.feature.chat

import android.content.ClipboardManager
import android.content.Context
import androidx.activity.ComponentActivity
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.contextmenu.data.TextContextMenuData
import androidx.compose.foundation.text.contextmenu.data.TextContextMenuItem
import androidx.compose.foundation.text.contextmenu.data.TextContextMenuSession
import androidx.compose.foundation.text.contextmenu.provider.LocalTextContextMenuToolbarProvider
import androidx.compose.foundation.text.contextmenu.provider.TextContextMenuDataProvider
import androidx.compose.foundation.text.contextmenu.provider.TextContextMenuProvider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.Clipboard
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isRoot
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performKeyInput
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.test.click
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.awaitCancellation
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.designsystem.PersonaTheme

/**
 * "Select" selects a message's words where the message is (#4210 T5, iOS `LiftSelection.swift`): that message alone, with
 * the long press, the swipe to quote and the inline-code copy off while it lasts and back afterwards, Copy and Quote of what
 * is picked, and the ways it ends. Robolectric draws no toolbar: [toolbar] stands in for the system's and shows what it would.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
@OptIn(ExperimentalTestApi::class)
class MessageSelectionTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val menu = MessageMenuState()
    private val swiped = mutableListOf<String>()
    private val quotedWords = mutableListOf<Pair<String, String>>()
    private val copied = mutableListOf<String>()
    private val toolbars = mutableListOf<TextContextMenuData>()
    private val actions = ChatRowActions(SessionKey("box", "s1"), { "" }, null, photos = null, pictures = { emptyList() },
        openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})

    private val clipboard = object : Clipboard {
        override suspend fun getClipEntry(): ClipEntry? = null
        override suspend fun setClipEntry(clipEntry: ClipEntry?) { copied += clipEntry?.clipData?.getItemAt(0)?.text.toString() }
        override val nativeClipboard get() = error("unused")
    }
    private val toolbar = object : TextContextMenuProvider {
        override suspend fun showTextContextMenu(dataProvider: TextContextMenuDataProvider) {
            toolbars += dataProvider.data()
            awaitCancellation()
        }
    }

    private fun show(writes: Boolean = true, content: @Composable () -> Unit) {
        menu.quote = if (writes) { target -> swiped += target.text } else null
        menu.quoteWords = if (writes) { target, words -> quotedWords += target.id to words } else null
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalChatRowActions provides actions, LocalClipboard provides clipboard,
                    LocalTextContextMenuToolbarProvider provides toolbar) {
                    Column(Modifier.width(400.dp).endSelectionOnTapOutside(menu)) { content() }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun agent(id: String, text: String) = Item(id, role = "assistant", kind = "text", text = text)

    @Composable private fun Reply(id: String, text: String) {
        Box(Modifier.testTag(id)) { MessageContent(agent(id, text), user = false) }
    }

    private fun select(id: String, text: String, user: Boolean = false, pending: DeliveryState? = null) {
        rule.runOnIdle { menu.select(MessageTarget(id, text, user, pending = pending)) }
        rule.waitForIdle()
        rule.waitForIdle()
    }

    private fun copy() {
        rule.onAllNodes(isRoot()).onFirst().performKeyInput { keyDown(Key.CtrlLeft); keyDown(Key.C); keyUp(Key.C); keyUp(Key.CtrlLeft) }
        rule.waitForIdle()
    }

    /** The selection takes a few frames to be taken for over. */
    private fun settle() {
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()
    }

    private fun items() = toolbars.lastOrNull()?.components.orEmpty().filterIsInstance<TextContextMenuItem>()

    @Test fun selectSelectsThatMessagesWordsAndNoOther() {
        show {
            Reply("a", "第一条回复，字很多。")
            Reply("b", "第二条回复，不选它。")
        }
        select("a", "第一条回复，字很多。")
        assertEquals("a", menu.selecting?.id)
        // Everything of A is selected, as iOS's Select Text does; the menu comes up with Copy and Quote.
        copy()
        assertTrue("copied: $copied", copied.isNotEmpty() && copied.all { it == "第一条回复，字很多。" })
        assertEquals(listOf("复制", "引用"), items().map { it.label })
        // B is as it was: its long press opens its own menu, and that is a press outside A, which ends A's selection.
        rule.onNodeWithTag("b").performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals("b", menu.target?.id)
        assertNull(menu.selecting)
    }

    @Test fun theLongPressTheSwipeAndTheInlineCodeCopyAreOffWhileSelectingAndBackAfter() {
        show { Reply("a", "先运行 `make test`，再看结果。") }
        val code = hasClickAction() and hasAnyAncestor(hasText("make test", substring = true))
        fun swipeLeft() {
            rule.onNodeWithTag("a").performTouchInput { down(center); moveBy(Offset(-60f, 0f)); moveBy(Offset(-200f, 0f)); up() }
            rule.waitForIdle()
        }
        // At rest: a long press opens the menu, a swipe quotes, a tap on the code copies it.
        rule.onNodeWithTag("a").performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals("a", menu.target?.id)
        rule.runOnIdle { menu.close() }
        swipeLeft()
        assertEquals(1, swiped.size)
        rule.onAllNodes(code, useUnmergedTree = true).assertCountEquals(1)
        rule.onAllNodes(code, useUnmergedTree = true).onFirst().performTouchInput { click() }
        rule.waitForIdle()
        val system = ApplicationProvider.getApplicationContext<Context>().getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        assertEquals("make test", system.primaryClip?.getItemAt(0)?.text?.toString())

        // Selecting: none of the three, and no "Quote" action for a screen reader either.
        select("a", "先运行 `make test`，再看结果。")
        swipeLeft()
        assertEquals("a swipe quotes nothing", 1, swiped.size)
        assertEquals("a", menu.selecting?.id)
        rule.onAllNodes(code, useUnmergedTree = true).assertCountEquals(0)
        rule.onAllNodes(SemanticsMatcher("has 引用") { node ->
            node.config.getOrNull(SemanticsActions.CustomActions)?.any { it.label == "引用" } == true
        }).assertCountEquals(0)
        rule.onAllNodes(SemanticsMatcher("has a long click") { it.config.getOrNull(SemanticsActions.OnLongClick) != null }).assertCountEquals(0)
        // A long press is the selection's, not the menu's (on blank space it lets go of the words, as it does anywhere).
        rule.onNodeWithTag("a").performTouchInput { longClick() }
        settle()
        assertNull("a long press opens no menu", menu.target)

        // …and back afterwards (the long press above let go of the words).
        assertNull(menu.selecting)
        swipeLeft()
        assertEquals(2, swiped.size)
        rule.onAllNodes(code, useUnmergedTree = true).assertCountEquals(1)
        rule.onNodeWithTag("a").performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals("a", menu.target?.id)
    }

    @Test fun copyTakesTheWordsAsTheChatShowsThem() {
        show {
            Reply("a", "先看 `foo()` 的结果。\n\n- 要点一\n- 要点二\n\n1. 第一步\n2. 第二步\n\n```kotlin\nfun a() = 1\nfun b() = 2\n```\n\n结尾 [链接](https://example.com/x)。")
        }
        select("a", "…")
        copy()
        // No thin spaces round the code, a list item's marker on its line, the code block's lines among the words and
        // none of its title bar (the language and Copy), the link as its words.
        assertEquals("先看 foo() 的结果。\n• 要点一\n• 要点二\n1. 第一步\n2. 第二步\nfun a() = 1\nfun b() = 2\n结尾 链接。", copied.last())
    }

    @Test fun quoteTakesThePickedWordsAndEndsTheSelection() {
        show { Reply("a", "改好了：先备份，再迁移。") }
        select("a", "改好了：先备份，再迁移。")
        val quote = items().single { it.label == "引用" }
        rule.runOnIdle { quote.onClick(object : TextContextMenuSession { override fun close() {} }) }
        rule.waitForIdle()
        assertEquals(listOf("a" to "改好了：先备份，再迁移。"), quotedWords)
        assertNull("the selection is over", menu.selecting)
    }

    @Test fun noQuoteWhereTheChatDoesNotQuote() {
        show(writes = false) { Reply("a", "只读的对话。") }
        select("a", "只读的对话。")
        assertEquals(listOf("复制"), items().map { it.label })
    }

    @Test fun aMessageOnlyOnThisPhoneCanBeCopiedNotQuoted() {
        show { MergedUserBubble(listOf(Item("c1", role = "user", kind = "text", text = "还没发出去的话。")), pending = DeliveryState.Queued) }
        select("c1", "还没发出去的话。", user = true, pending = DeliveryState.Queued)
        copy()
        assertEquals("还没发出去的话。", copied.last())
        assertEquals(listOf("复制"), items().map { it.label })
    }

    @Test fun aTapOnTheWordsClearsTheSelectionAndThatEndsIt() {
        show { Reply("a", "点一下就放开。") }
        select("a", "点一下就放开。")
        assertEquals("a", menu.selecting?.id)
        rule.onNodeWithTag("a").performTouchInput { click() }
        settle()
        assertNull(menu.selecting)
    }

    @Test fun aTapOutsideEndsItButADragDoesNot() {
        show {
            Reply("a", "被选中的话。")
            Box(Modifier.testTag("outside").width(400.dp).height(120.dp))
        }
        select("a", "被选中的话。")
        rule.onNodeWithTag("outside").performTouchInput { down(center); moveBy(Offset(0f, -120f)); up() }
        rule.waitForIdle()
        assertEquals("a drag is a scroll, not a tap", "a", menu.selecting?.id)
        rule.onNodeWithTag("outside").performTouchInput { click() }
        rule.waitForIdle()
        assertNull(menu.selecting)
    }

    @Test fun backEndsIt() {
        show { Reply("a", "按返回键就放开。") }
        select("a", "按返回键就放开。")
        rule.runOnUiThread { rule.activity.onBackPressedDispatcher.onBackPressed() }
        rule.waitForIdle()
        assertNull(menu.selecting)
        assertFalse(rule.activity.onBackPressedDispatcher.hasEnabledCallbacks())
    }

    @Test fun scrollingTheMessageOutOfTheScreenEndsIt() {
        show {
            Box(Modifier.width(400.dp).height(400.dp)) {
                LazyColumn(Modifier.fillMaxSize().testTag("list")) {
                    items(30, key = { it }) { n -> if (n == 0) Reply("a", "会被滚走的话。") else Text("下面的第 $n 行", Modifier.height(80.dp)) }
                }
            }
        }
        select("a", "会被滚走的话。")
        assertEquals("a", menu.selecting?.id)
        rule.onNodeWithTag("list").performScrollToIndex(20)
        rule.waitForIdle()
        assertNull("nothing of it shows any more", menu.selecting)
    }

    @Test fun aMessageStillComposedButScrolledClearOfTheScreenEndsItToo() {
        show {
            val scroll = androidx.compose.foundation.rememberScrollState()
            Column(Modifier.width(400.dp).height(400.dp).verticalScroll(scroll).testTag("column")) {
                Reply("a", "还在组合里但看不见的话。")
                repeat(20) { n -> Text("下面的第 $n 行", Modifier.height(80.dp)) }
            }
        }
        select("a", "还在组合里但看不见的话。")
        assertEquals("a", menu.selecting?.id)
        rule.onNodeWithTag("column").performTouchInput { swipeUp(startY = bottom - 1f, endY = top + 1f) }
        rule.waitForIdle()
        assertNull("it is still there, but nothing of it shows", menu.selecting)
    }

    @Test fun whatWasUnfoldedStaysUnfoldedWhenSelectingStarts() {
        val lines = (1..60).joinToString("\n") { "line $it" }
        show { Reply("a", "```\n$lines\n```") }
        rule.onNodeWithText("展开全部").performTouchInput { click() }
        rule.waitForIdle()
        select("a", "…")
        copy()
        assertTrue("the whole block was open, so the whole block is selected", copied.last().endsWith("line 60"))
    }

    @Test fun yourOwnBubbleSelectsToo() {
        show { MergedUserBubble(listOf(Item("u", role = "user", kind = "text", text = "帮我看看这个报错"))) }
        select("u", "帮我看看这个报错", user = true)
        copy()
        assertEquals("帮我看看这个报错", copied.last())
        assertEquals(listOf("复制", "引用"), items().map { it.label })
    }

    @Test fun aMessageWithNothingToSelectIsNotLeftSelecting() {
        // a message of nothing but a context block has no words at all
        show { Reply("a", "<persona-context kind=\"context\" title=\"t\" url=\"\">\nbody\n</persona-context>") }
        select("a", "…")
        rule.mainClock.advanceTimeBy(2_000)
        rule.waitForIdle()
        assertNull(menu.selecting)
    }

    @Test fun selectInTheMenuSelectsInPlaceAndOpensNoReader() {
        show {
            Reply("a", "长按它，再点选择。")
            MessageMenuSheet(menu, writes = true, onQuote = {}, onEdit = {}, onContext = {}, onHandToPersona = null, onNotice = {})
        }
        rule.onNodeWithTag("a").performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals("a", menu.target?.id)
        rule.onNodeWithText("选择").performClick()
        rule.waitForIdle()
        rule.waitForIdle()
        assertNull("the menu has gone", menu.target)
        assertEquals("the words are selected where they are", "a", menu.selecting?.id)
        // The full-screen reader, with its "copy all", is not what Select opens now.
        rule.onNodeWithContentDescription("复制全文").assertDoesNotExist()
        copy()
        assertEquals("长按它，再点选择。", copied.last())
    }

    @Test fun aQuoteOfPickedWordsIsAPartOfTheMessage() {
        val words = ChatLinkContext.quote("改好了", "Claude", "msg-1", ChatLinkContext.PART_SELECTION)
        assertEquals("<persona-context kind=\"quote\" title=\"Claude\" url=\"\" msg=\"msg-1\" part=\"selection\">\n改好了\n</persona-context>", words.xml())
        assertEquals("selection", ChatLinkContext.parse(words.xml() + "\n\n好的").first.single().part)
        // As iOS's `ContextRef.adding`: a message goes over the box once whole and once per part picked; the same words once.
        val whole = ChatLinkContext.quote("全文", "Claude", "msg-1")
        val both = withQuote(listOf(whole), words)
        assertEquals(2, both.size)
        assertEquals(both, withQuote(both, ChatLinkContext.quote("改好了", "Claude", "msg-1", ChatLinkContext.PART_SELECTION)))
        assertEquals(3, withQuote(both, ChatLinkContext.quote("另一句", "Claude", "msg-1", ChatLinkContext.PART_SELECTION)).size)
        assertEquals(both, withQuote(both, whole))
    }

    @Test fun theContextsKeepTheirPartAcrossAConfigurationChange() {
        val kept = listOf(ChatLinkContext.quote("改好了", "Claude", "msg-1", ChatLinkContext.PART_SELECTION), ChatLinkContext.quote("全文", "我", "msg-2"))
        val saver = ChatLinkContextsSaver
        val saved = with(saver) { androidx.compose.runtime.saveable.SaverScope { true }.save(kept) }!!
        assertEquals(kept, saver.restore(saved))
        // What an older build saved: six values each after "contexts-v2" (no part), five each before that.
        val v2 = listOf("contexts-v2", "quote", "Claude", "", "改好了", "id-1", "msg-1")
        assertEquals(listOf(ChatLinkContext("quote", "Claude", "", "改好了", "id-1", "msg-1")), saver.restore(v2))
        assertEquals(listOf(ChatLinkContext("context", "t", "u", "x", "id-2")), saver.restore(listOf("context", "t", "u", "x", "id-2")))
    }

    @Test fun theQuoteCardSaysItQuotesSelectedText() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                Column(Modifier.width(400.dp)) {
                    ChatContextCard(ChatLinkContext.quote("改好了", "Claude", "m1", ChatLinkContext.PART_SELECTION))
                    ChatContextCard(ChatLinkContext.quote("我的话", ChatLinkContext.QUOTE_ME, "m2", ChatLinkContext.PART_SELECTION))
                    ChatContextCard(ChatLinkContext.quote("全文", "Claude", "m3"))
                }
            }
        }
        rule.onNodeWithText("引用选中的文字 · Claude").assertExists()
        rule.onNodeWithText("引用选中的文字 · 我").assertExists()
        rule.onNodeWithText("引用 Claude").assertExists()
    }

    @Test fun selectingTwoMessagesInTurnLeavesOneSelecting() {
        show {
            Reply("a", "先选这一条。")
            Reply("b", "再选这一条。")
        }
        select("a", "先选这一条。")
        select("b", "再选这一条。")
        assertEquals("b", menu.selecting?.id)
        copy()
        assertEquals("再选这一条。", copied.last())
        assertNotNull(menu.selecting)
    }
}
