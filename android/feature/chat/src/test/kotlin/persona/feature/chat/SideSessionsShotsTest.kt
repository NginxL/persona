package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FoldedAs
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionChildren
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.sideRollup
import persona.core.domain.sort.sideSessions
import java.io.File

/**
 * Side sessions in their parent's chat (#7944): the header's Side sessions control (its count, the needs-you dot), the
 * ⋯ menu's "Side sessions (N)" and the sheet listing them, needs you first; a tap opens the side. Every style, light and
 * dark. PNGs in `build/side-sessions-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
class SideSessionsShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/side-sessions-shots").also { it.mkdirs() }

    private val parent = Session("macbook", "parent", title = "Plan the release", status = "idle", state = "idle", agent = "claude",
        children = SessionChildren(total = 3, working = 1, needsYou = 1, sides = 3, sidesWorking = 1, sidesNeedsYou = 1))
    private val rows = listOf(
        parent,
        side("old", "Count the open issues", "idle", "offline", "2026-11-15T11:00:00Z"),
        side("work", "Check the screenshots", "working", "working", "2026-11-15T09:00:00Z"),
        side("ask", "Which changelog format?", "needs_you", "blocked", "2026-11-15T08:00:00Z"),
    )
    private val machine = Machine(id = "macbook", name = "MacBook", online = true)

    private fun side(id: String, title: String, state: String, status: String, updated: String) = Session("macbook", id, title = title,
        state = state, status = status, agent = "claude", foldedUnder = "parent", foldedAs = FoldedAs.SIDE, parentAgentId = "parent",
        updatedAt = updated)

    private fun save(bitmap: Bitmap, name: String) {
        File(out(), "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    @Test fun theControlTheMenuRowAndTheSheetInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var round by mutableIntStateOf(0)
        val opened = mutableListOf<SessionKey>()
        val sides = sideSessions(rows, parent.key)
        assertEquals(listOf("ask", "work", "old"), sides.map { it.sessionId })
        val rollup = sideRollup(parent, sides, rows)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val scope = rememberCoroutineScope()
                val resources = androidx.compose.ui.platform.LocalResources.current
                val chrome = remember { ChatChromeState(resources, null, scope, {}, {}) }
                key(round) {
                    Box(Modifier.testTag("shot").width(400.dp).height(860.dp).background(PersonaTheme.palette.background)) {
                        Box(Modifier.testTag("header")) {
                            ChatHeader(parent, machine, rows, chrome, null, false, onBack = {}, onOpenSession = {},
                                onConnectionDetails = {}, onOverlay = {}, sides = sides, sideRollup = rollup, onOpenSide = { opened += it })
                        }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d; round++ }
            rule.waitForIdle()
            val name = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            save(rule.onNodeWithTag("header").captureToImage().asAndroidBitmap(), "header-$name")
            rule.onNode(hasContentDescription("Side sessions (3)", substring = true)).performClick()
            rule.waitForIdle()
            repeat(4) { rule.mainClock.advanceTimeByFrame(); rule.waitForIdle() }
            rule.onNodeWithText("Which changelog format?").assertExists()
            rule.onNodeWithText("Count the open issues").assertExists()
            save(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "sheet-$name")
            rule.onNodeWithText("Check the screenshots").performClick()
            rule.waitForIdle()
            rule.onNodeWithContentDescription("More actions").performClick()
            rule.waitForIdle()
            repeat(4) { rule.mainClock.advanceTimeByFrame(); rule.waitForIdle() }
            rule.onNodeWithText("Side sessions (3)").assertExists()
            rule.onNodeWithText("A side session needs you").assertExists()
            save(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "menu-$name")
        }
        assertEquals(List(AppStyle.entries.size * 2) { SessionKey("macbook", "work") }, opened)
    }

    @Test fun reorderingSidesMidPressDoesNotOpenTheNewlyPromotedFirstRow() {
        val first = side("s1", "First branch session", "working", "working", "2026-11-15T10:00:00Z")
        val second = side("s2", "Second branch session", "working", "working", "2026-11-15T09:59:00Z")
        var currentSides by mutableStateOf(listOf(first, second))
        val opened = mutableListOf<SessionKey>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                ChatSideSessionsSheet(
                    sides = currentSides,
                    rows = listOf(parent) + currentSides,
                    rollup = sideRollup(parent, currentSides, listOf(parent) + currentSides),
                    onDismiss = {},
                    onOpen = { opened += it },
                )
            }
        }
        rule.waitForIdle()
        // Touch down on Row #1 ("First branch session"), then reorder [s2, s1] before lifting the finger
        val node = rule.onNodeWithText("First branch session")
        node.performTouchInput { down(center) }
        rule.runOnIdle { currentSides = listOf(second, first) }
        rule.waitForIdle()
        node.performTouchInput { up() }
        rule.waitForIdle()
        assertEquals("tapping #1 must never misroute to s2 when s2 jumps to #1", emptyList<SessionKey>(), opened.filter { it == second.key })
    }
}
