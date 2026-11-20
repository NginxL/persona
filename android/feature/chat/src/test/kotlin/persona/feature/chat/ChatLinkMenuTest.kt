package persona.feature.chat

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.click
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.SessionKey
import persona.core.designsystem.LocalSelectionAbove
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w400dp-h640dp-xxhdpi")
class ChatLinkMenuTest {
    @get:Rule val rule = createComposeRule()

    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @Test fun eligibleLinkUrlsExcludePathsAndRefusedSchemes() {
        assertTrue(isMenuLinkUrl("https://example.com/docs/guide"))
        assertTrue(isMenuLinkUrl("http://localhost:3000/app"))
        assertTrue(isMenuLinkUrl("persona://session/mac/s1"))
        assertTrue(isMenuLinkUrl("mailto:team@example.com"))
        assertFalse(isMenuLinkUrl(""))
        assertFalse(isMenuLinkUrl("   "))
        assertFalse(isMenuLinkUrl(PathLinks.uri("src/Main.kt", 12)))
        assertFalse(isMenuLinkUrl("javascript:alert(1)"))
        assertFalse(isMenuLinkUrl("file:///etc/hosts"))
        assertFalse(isMenuLinkUrl("data:text/plain,hi"))
    }

    @Test fun sectionHeaderStripsWebSchemeOnly() {
        assertEquals("example.com/docs/start", linkMenuSection("https://example.com/docs/start"))
        assertEquals("example.com/path", linkMenuSection("http://example.com/path"))
        assertEquals("mailto:team@example.com", linkMenuSection("mailto:team@example.com"))
    }

    @Test fun actionsAdaptToWebForwardedPortAndExternalLinks() {
        val res = context.resources
        val web = chatLinkMenuActions("https://example.com/docs", res, isForwardedPort = false, {}, {}, {}, {})
        assertEquals(
            listOf(
                res.getString(R.string.chat_link_open_in_app),
                res.getString(R.string.chat_link_open_browser),
                res.getString(R.string.chat_link_copy),
                res.getString(R.string.chat_link_share),
            ),
            web.map { it.label },
        )
        assertEquals("example.com/docs", web.first().section)

        val forwarded = chatLinkMenuActions("http://localhost:3000", res, isForwardedPort = true, {}, {}, {}, {})
        assertEquals(
            listOf(
                res.getString(R.string.chat_link_open_in_app),
                res.getString(R.string.chat_link_copy),
            ),
            forwarded.map { it.label },
        )

        val mailto = chatLinkMenuActions("mailto:team@example.com", res, isForwardedPort = false, {}, {}, {}, {})
        assertEquals(
            listOf(
                res.getString(R.string.chat_link_open),
                res.getString(R.string.chat_link_copy),
            ),
            mailto.map { it.label },
        )
    }

    @Test fun longPressOnMarkdownLinkOpensMenuCopiesUrlAndDoesNotTriggerMessageMenu() {
        val url = "https://example.com/persona/docs"
        val markdown = "[Read the documentation]($url)"
        val messageMenu = MessageMenuState()
        val rowActions = ChatRowActions(
            SessionKey("mac", "s1"),
            fullText = { it.text.orEmpty() },
            openPath = null,
            photos = null,
            pictures = { emptyList() },
            openImages = { _, _ -> },
            menu = messageMenu,
            retryLineId = null,
            onRetry = {},
        )
        val target = MessageTarget("m1", markdown, user = false)
        var clipboardManager: ClipboardManager? = null

        rule.setContent {
            clipboardManager = LocalContext.current.getSystemService(ClipboardManager::class.java)
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                ChatLinkHost(Modifier.width(360.dp)) {
                    CompositionLocalProvider(LocalChatRowActions provides rowActions) {
                        Column(
                            Modifier
                                .fillMaxWidth()
                                .padding(Tokens.Space.m)
                                .testTag("bubble")
                                .messageMenu(target),
                        ) {
                            ChatMarkdown(markdown, id = "m1", selectable = false)
                        }
                    }
                }
            }
        }
        // Compose idleness does not wait for ChatMarkdownCache's background parser.
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Read the documentation").fetchSemanticsNodes().isNotEmpty() }

        rule.onNodeWithText("Read the documentation").performTouchInput {
            longClick(Offset(24f, centerY))
        }
        rule.waitForIdle()

        rule.onNodeWithTag(CHAT_LINK_MENU_TAG).assertIsDisplayed()
        assertNull("message menu must not open on link long press", messageMenu.target)
        assertEquals(0, rule.onAllNodesWithTag("chat_link_card").fetchSemanticsNodes().size)

        rule.onNodeWithText(context.getString(R.string.chat_link_open_in_app)).assertIsDisplayed()
        rule.onNodeWithText(context.getString(R.string.chat_link_open_browser)).assertIsDisplayed()
        rule.onNodeWithText(context.getString(R.string.chat_link_share)).assertIsDisplayed()
        rule.onNodeWithText(context.getString(R.string.chat_link_copy)).performClick()
        rule.waitForIdle()

        val clip: ClipData? = clipboardManager?.primaryClip
        assertNotNull(clip)
        assertEquals(url, clip!!.getItemAt(0).text.toString())
        assertEquals(0, rule.onAllNodesWithTag(CHAT_LINK_MENU_TAG).fetchSemanticsNodes().size)
    }

    @Test fun shortTapStillOpensLinkAndNonLinkLongPressStillOpensMessageMenu() {
        val url = "https://example.com/guide"
        val opened = mutableListOf<String>()
        val messageMenu = MessageMenuState()
        val rowActions = ChatRowActions(
            SessionKey("mac", "s1"),
            fullText = { it.text.orEmpty() },
            openPath = null,
            photos = null,
            pictures = { emptyList() },
            openImages = { _, _ -> },
            menu = messageMenu,
            retryLineId = null,
            onRetry = {},
        )
        val markdown = "Plain intro words.\n\n[Open guide]($url)"
        val target = MessageTarget("m2", markdown, user = false)

        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalChatHubLinks provides { link -> opened += link; true }) {
                    ChatLinkHost(Modifier.width(360.dp)) {
                        CompositionLocalProvider(LocalChatRowActions provides rowActions) {
                            Column(
                                Modifier
                                    .fillMaxWidth()
                                    .padding(Tokens.Space.m)
                                    .messageMenu(target),
                            ) {
                                ChatMarkdown(markdown, id = "m2", selectable = false)
                            }
                        }
                    }
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Open guide").fetchSemanticsNodes().isNotEmpty() }

        // Short tap opens the link without showing the link menu.
        rule.onNodeWithText("Open guide").performTouchInput {
            click(Offset(24f, centerY))
        }
        rule.waitForIdle()
        assertEquals(listOf(url), opened)
        assertEquals(0, rule.onAllNodesWithTag(CHAT_LINK_MENU_TAG).fetchSemanticsNodes().size)

        // Long press on plain text opens the message menu, not the link menu.
        rule.onNodeWithText("Plain intro words.", substring = true).performTouchInput {
            longClick(Offset(24f, centerY))
        }
        rule.waitForIdle()
        assertEquals("m2", messageMenu.target?.id)
        assertEquals(0, rule.onAllNodesWithTag(CHAT_LINK_MENU_TAG).fetchSemanticsNodes().size)
    }

    @Test fun selectionModeDisablesLinkLongPressMenu() {
        val url = "https://example.com/selected"
        val markdown = "[Selectable link]($url)"
        val linkMenu = ChatLinkMenuState()

        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(
                    LocalChatLinkMenu provides linkMenu,
                    LocalSelectionAbove provides true,
                ) {
                    ChatMarkdown(markdown, id = "m3", selectable = false)
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Selectable link").fetchSemanticsNodes().isNotEmpty() }

        rule.onNodeWithText("Selectable link").performTouchInput {
            longClick(Offset(24f, centerY))
        }
        rule.waitForIdle()
        assertNull(linkMenu.target)
    }

    @Test fun longPressWorksInMarkdownListsAndHeadings() {
        val listUrl = "https://example.com/list-item"
        val headingUrl = "https://example.com/heading-link"
        val markdown = "## [Heading link]($headingUrl)\n\n- [List item link]($listUrl)"
        val linkMenu = ChatLinkMenuState()

        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalChatLinkMenu provides linkMenu) {
                    Column(Modifier.width(360.dp).padding(Tokens.Space.m)) {
                        ChatMarkdown(markdown, id = "m4", selectable = false)
                    }
                }
            }
        }
        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("Heading link").fetchSemanticsNodes().isNotEmpty() &&
                rule.onAllNodesWithText("List item link").fetchSemanticsNodes().isNotEmpty()
        }
        rule.waitForIdle()

        rule.onNodeWithText("Heading link").performTouchInput { longClick(Offset(24f, centerY)) }
        rule.waitForIdle()
        assertEquals(headingUrl, linkMenu.target?.url)
        linkMenu.close()

        rule.onNodeWithText("List item link").performTouchInput { longClick(Offset(24f, centerY)) }
        rule.waitForIdle()
        assertEquals(listUrl, linkMenu.target?.url)
    }
}
