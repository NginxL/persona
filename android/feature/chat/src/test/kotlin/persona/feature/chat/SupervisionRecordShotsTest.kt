package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.tasks.*
import java.io.File

/**
 * #4339 C13: on Glass a quiet supervision round is one "Handled in the background" line in Persona's conversation that opens to
 * the round (iOS `SupervisionRecordLine`), and Persona's work line is the Agent chat's ("Tools 2"); Brief and Board keep
 * the round in the background record and the "Checked N places" line. Native JVM captures with synthetic data.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SupervisionRecordShotsTest {
    @get:Rule val rule = createComposeRule()
    private val session = SessionKey("computer", "main")
    private fun at(n: Int) = "2026-11-12T10:00:${n.toString().padStart(2, '0')}Z"
    private val transcript = listOf(
        Item("ask", 1, ts = at(1), role = "user", kind = "text", text = "Is the login fix merged?"),
        Item("read", 2, ts = at(2), role = "assistant", kind = "tool_use", toolUseId = "read", toolName = "Read",
            toolInput = buildJsonObject { put("file_path", "/sample/notes.md") }),
        Item("read-result", 3, ts = at(3), role = "user", kind = "tool_result", toolUseId = "read", text = "Notes"),
        Item("search", 4, ts = at(4), role = "assistant", kind = "tool_use", toolUseId = "search", toolName = "Grep",
            toolInput = buildJsonObject { put("pattern", "login") }),
        Item("search-result", 5, ts = at(5), role = "user", kind = "tool_result", toolUseId = "search", text = "One match"),
        Item("answer", 6, ts = at(6), role = "assistant", kind = "text", text = "Yes, it was merged this morning."),
        Item("digest", 7, ts = at(7), role = "user", kind = "text", text = "Supervision: 2 tasks running, nothing needs the owner.",
            injected = Injected("system", "supervise", "Supervision")),
        Item("quiet", 8, ts = at(8), role = "assistant", kind = "text", text = "Nothing needs the owner right now.", narration = true),
        Item("next", 9, ts = at(9), role = "user", kind = "text", text = "Thanks"),
        Item("welcome", 10, ts = at(10), role = "assistant", kind = "text", text = "You're welcome."),
    )

    private fun show(style: AppStyle, dark: MutableState<Boolean>) {
        val open = mutableStateOf(emptySet<String>())
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark.value, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                DisposableEffect(loader) { onDispose { loader.shutdown() } }
                val glass = style == AppStyle.Glass
                val original = ChatTimeline().lines(transcript, false, open.value).lines
                // as the chat page does: Glass folds a supervision round with the chat's open-line set (rounds: the main
                // conversation's switch, off here)
                val lines = AssistantConversation.project(original, transcript,
                    if (glass) BackgroundFolding(open.value, rounds = false) else null, inline = glass).foreground
                // no ripples: Robolectric has no render thread, so a tapped line would keep a frozen one in the shots
                CompositionLocalProvider(androidx.compose.material3.LocalRippleConfiguration provides null, LocalHomeChrome provides HomeChrome(
                    listOf(NavItem("assistant", "Assistant", PhIcons.House)), "assistant", onSelect = {}, leading = {})) {
                    Column(Modifier.fillMaxSize().testTag("supervision-shot").background(PersonaTheme.palette.background)
                        .padding(Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        lines.forEach { line -> key(line.contentKey) {
                            ChatLineContent(line, session, loader, 8, onToggle = { id ->
                                open.value = if (id in open.value) open.value - id else open.value + id
                            }, onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {}, style = ChatRenderStyle.Assistant)
                        } }
                    }
                }
            }
        }
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(500)
    }

    private fun capture(name: String): Int {
        val directory = File(System.getProperty("persona.shots") ?: "build/supervision-record-shots").also { it.mkdirs() }
        val bitmap = rule.onNodeWithTag("supervision-shot").captureToImage().asAndroidBitmap()
        File(directory, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        return bitmap.getPixel(2, 2)
    }

    @Test fun glassFoldsSupervisionIntoOneLineAndTheWorkLineCountsLightAndDark() {
        val dark = mutableStateOf(false)
        show(AppStyle.Glass, dark)
        val colors = mutableListOf<Int>()
        for (night in listOf(false, true)) {
            dark.value = night; settle()
            val mode = if (night) "dark" else "light"
            rule.onNodeWithText("Handled in the background").assertIsDisplayed()
            rule.onNodeWithText("Nothing needs the owner right now.").assertDoesNotExist()
            rule.onNodeWithText("Checked", substring = true).assertDoesNotExist()
            rule.onNodeWithText("Tools 2", substring = true).assertIsDisplayed()
            colors += capture("supervision-glass-$mode")
            rule.onNodeWithText("Handled in the background").performClick(); settle()
            rule.onNodeWithText("Nothing needs the owner right now.").assertIsDisplayed()
            capture("supervision-glass-open-$mode")
            rule.onNodeWithText("Handled in the background").performClick(); settle()
            rule.onNodeWithText("Nothing needs the owner right now.").assertDoesNotExist()
        }
        assertNotEquals("dark capture must differ from light", colors[0], colors[1])
    }

    private fun keepsTheRoundBehind(style: AppStyle) {
        show(style, mutableStateOf(false)); settle()
        rule.onNodeWithText("Handled in the background").assertDoesNotExist()
        rule.onNodeWithText("Nothing needs the owner right now.").assertDoesNotExist()
        rule.onNodeWithText("Checked 2 places").assertIsDisplayed()
        capture("supervision-${style.name.lowercase()}-light")
    }

    @Test fun briefKeepsTheRoundInTheBackgroundRecord() = keepsTheRoundBehind(AppStyle.Brief)
    @Test fun boardKeepsTheRoundInTheBackgroundRecord() = keepsTheRoundBehind(AppStyle.Board)
}
