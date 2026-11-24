package persona.feature.chat

import android.content.ClipboardManager
import android.content.Context
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performMouseInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.rightClick
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.RewindRestore
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalLargeWindowInput
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
@OptIn(ExperimentalTestApi::class)
class MessagePointerMenuTest {
    @get:Rule val rule = createComposeRule()
    private val context = ApplicationProvider.getApplicationContext<Context>()
    private val resources = context.resources
    private val ordinary = MessageTarget("user#1", "mine", user = true, editText = "edit mine")

    @Test fun ordinaryOpenClearsThePointerAnchorAndCloseClearsBoth() {
        val menu = MessageMenuState()
        menu.openAt(ordinary, Offset(20f, 40f))
        assertEquals(ordinary, menu.target)
        assertEquals(Offset(20f, 40f), menu.pointerAnchor)
        menu.open(ordinary)
        assertNull(menu.pointerAnchor)
        menu.openAt(ordinary, Offset(60f, 80f))
        menu.openAt(ordinary, Offset(100f, 120f))
        assertEquals(Offset(100f, 120f), menu.pointerAnchor)
        menu.close()
        assertNull(menu.target)
        assertNull(menu.pointerAnchor)
    }

    @Test fun onlyLargeWindowPointerInputUsesTheAnchoredPresentation() {
        assertEquals(MessageMenuPresentation.Pointer, messageMenuPresentation(true, Offset.Zero))
        assertEquals(MessageMenuPresentation.ActionSheet, messageMenuPresentation(false, Offset.Zero))
        assertEquals(MessageMenuPresentation.ActionSheet, messageMenuPresentation(true, null))
        assertEquals(MessageMenuPresentation.ActionSheet, messageMenuPresentation(false, null))
    }

    @Test fun rightClickRecordsAnAnchorOnlyInALargeWindowAndTouchStillUsesOrdinaryOpen() {
        val menu = MessageMenuState()
        var large by mutableStateOf(true)
        val rowActions = ChatRowActions(SessionKey("machine", "session"), fullText = { it.text.orEmpty() }, openPath = null,
            photos = null, pictures = { emptyList() }, openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalLargeWindowInput provides large, LocalChatRowActions provides rowActions) {
                    Box(Modifier.size(120.dp).messageMenu(ordinary).testTag("message"))
                }
            }
        }
        val message = rule.onNodeWithTag("message")
        message.performMouseInput { rightClick() }
        rule.waitForIdle()
        assertNotNull(menu.pointerAnchor)
        rule.runOnUiThread { menu.close(); large = false }
        rule.waitForIdle()
        message.performMouseInput { rightClick() }
        rule.waitForIdle()
        assertEquals(ordinary, menu.target)
        assertNull(menu.pointerAnchor)
        rule.runOnUiThread { menu.close() }
        message.performTouchInput { longClick() }
        rule.waitForIdle()
        assertEquals(ordinary, menu.target)
        assertNull(menu.pointerAnchor)
    }

    private data class Render(val pointer: Boolean, val target: MessageTarget, val rewindBlock: String?, val generation: Int)

    @Test fun bothPresentationsKeepActionOrderEnabledStateResultsAndFollowUps() {
        val menu = MessageMenuState()
        val events = mutableListOf<String>()
        var render by mutableStateOf(Render(true, ordinary, null, 0))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalLargeWindowInput provides render.pointer) {
                    key(render) {
                        MessageMenuSheet(menu, writes = true,
                            onQuote = { events += "quote:${it.id}" }, onEdit = { events += "edit:$it" },
                            onContext = { events += "context:$it" }, onForward = { events += "forward:${it.id}" },
                            onNotice = { events += "notice:$it" }, onFork = { events += "fork:$it" },
                            rewind = RewindMenu(render.rewindBlock) { target, restore -> events += "rewind:${target.id}:$restore" },
                            onDelete = { events += "delete:${it.id}" },
                            share = ShareCardSource("Claude", null, "space › tab"))
                    }
                }
            }
        }
        fun open(pointer: Boolean, target: MessageTarget, block: String? = null) {
            rule.runOnUiThread {
                render = Render(pointer, target, block, render.generation + 1)
                if (pointer) menu.openAt(target, Offset(240f, 240f)) else menu.open(target)
            }
            rule.waitForIdle()
        }
        val reply = MessageTarget("00000000-0000-0000-0000-000000000001#0", "reply", user = false)
        val pending = ordinary.copy(pending = DeliveryState.WaitingForNetwork)
        val extras = MessageExtras(fork = true, rewind = true, delete = true)
        for (pointer in listOf(false, true)) for (target in listOf(ordinary, reply, pending)) {
            val expected = messageActions(target, writes = true, forward = false, extras = extras)
            open(pointer, target)
            assertActionOrder(expected, target)
            for (action in expected) {
                open(pointer, target)
                rule.onNodeWithText(label(action, target)).performClick()
                rule.waitForIdle()
                when (action) {
                    MessageAction.Copy -> {
                        val clipboard = context.getSystemService(ClipboardManager::class.java)
                        assertEquals(target.text, clipboard.primaryClip?.getItemAt(0)?.text?.toString())
                    }
                    MessageAction.Select -> {
                        assertEquals(target, menu.selecting)
                        assertNull(menu.target)
                        assertNull(menu.pointerAnchor)
                        rule.runOnUiThread { menu.endSelection(target.id) }
                        assertNull(menu.selecting)
                    }
                    MessageAction.Quote -> assertEquals("quote:${target.id}", events.last())
                    MessageAction.Share -> assertReaderOpensAndReturns(R.string.chat_share_card_title)
                    MessageAction.Star, MessageAction.Unstar -> Unit
                    MessageAction.Forward -> assertEquals("forward:${target.id}", events.last())
                    MessageAction.Context -> assertEquals("context:${target.text}", events.last())
                    MessageAction.Edit -> assertEquals("edit:${target.editText}", events.last())
                    MessageAction.Fork -> assertEquals("fork:${target.id}", events.last())
                    MessageAction.Rewind -> {
                        rule.onNodeWithText(resources.getString(R.string.chat_rewind_title)).assertExists()
                        val code = resources.getString(R.string.chat_rewind_code_and_conversation)
                        rule.onNodeWithText(code).performClick()
                        rule.onNodeWithText(resources.getString(R.string.chat_rewind_confirm_code)).assertExists()
                        rule.onNodeWithText(code).performClick()
                        rule.waitForIdle()
                        assertEquals("rewind:${target.id}:${RewindRestore.CodeAndConversation}", events.last())
                    }
                    MessageAction.Delete -> assertEquals("delete:${target.id}", events.last())
                }
            }
        }
        for (pointer in listOf(false, true)) {
            open(pointer, ordinary, "Working")
            rule.onNodeWithText(label(MessageAction.Rewind, ordinary, "Working")).assertIsNotEnabled()
        }
    }

    private fun label(action: MessageAction, target: MessageTarget, rewindBlock: String? = null): String = when {
        action == MessageAction.Rewind && rewindBlock != null -> resources.getString(R.string.chat_menu_rewind_blocked, rewindBlock)
        action == MessageAction.Delete && target.pending?.allows(DeliveryAction.Cancel) == true ->
            resources.getString(R.string.chat_outbox_dont_send)
        else -> resources.getString(action.label)
    }

    private fun assertActionOrder(actions: List<MessageAction>, target: MessageTarget) {
        var previous = -1f
        for (action in actions) {
            val top = rule.onNodeWithText(label(action, target)).getUnclippedBoundsInRoot().top.value
            assertTrue("${action.name} is out of order", top >= previous)
            previous = top
        }
    }

    private fun assertReaderOpensAndReturns(titleResource: Int) {
        val title = resources.getString(titleResource)
        rule.onNodeWithText(title).assertExists()
        rule.onNodeWithContentDescription(resources.getString(persona.core.designsystem.R.string.designsystem_back)).performClick()
        rule.waitForIdle()
        rule.onNodeWithText(title).assertDoesNotExist()
    }
}
