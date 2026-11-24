package persona.feature.assistant

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * The main conversation's title menu with every item it can have, as iOS's `AssistantTopBar.menuItems` order them (#4339 C18):
 * the heading names the conversation and what it runs on, and on Glass each item has iOS's icon, the new ones for Compact
 * context and New main chat among them. The items are the real `assistantMenu` list with their own words and icons; the page
 * around it is `AssistantDetailsShotsTest`'s. Light and dark; PNGs in `build/assistant-menu-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class AssistantMenuShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")

    @Test fun theMainConversationsMenuWithEveryItemOnGlass() {
        var dark by mutableStateOf(false)
        val row = Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true, launcher = "claude", model = "anthropic/claude-opus-4-5")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                val res = LocalResources.current
                val copy = AssistantCopyText(row, main, main = true, config = null, assistantMachine = "", machines = emptyList())
                // A module's action where the voice module puts "Voice chat" (Modules.kt), so the picture keeps every item.
                val voice = persona.core.moduleapi.ModuleMenuAction("example.chat", "Voice chat", persona.core.designsystem.icons.PersonaIcons.voiceChannel) {}
                val sections = assistantMenu(offline = true, modules = listOf(voice), main = main, active = main, compact = true, newMain = true, working = true)
                JActionSheet(sections.flatMapIndexed { index, items ->
                    items.mapIndexed { i, item -> JAction(item.label(res), item.icon, separatorBefore = index > 0 && i == 0) {} }
                }, onDismiss = {}, title = copy.menuTitle(res, "Persona"))
            }
        }
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            rule.onNodeWithText("Main chat · claude · Opus 4.5").assertExists()
            rule.onNodeWithText("Compact context").assertExists()
            rule.onNodeWithText("New main chat").assertExists()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            val out = File(System.getProperty("persona.shots") ?: "build/assistant-menu-shots").also { it.mkdirs() }
            File(out, "main-menu-glass-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
