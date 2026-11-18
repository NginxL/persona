package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.*
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2600dp-xhdpi")
class ReorganizedSettingsPagesTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private fun back() = rule.runOnIdle { rule.activity.onBackPressedDispatcher.onBackPressed() }
    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/settings-reorganized-shots").also { it.mkdirs() }
    private fun image(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("page-shot").captureToImage().asAndroidBitmap()
        val out = out()
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
    @Test fun searchAndGroupParentsSurviveBack() {
        val services = demoSettingsServices(rule.activity.resources)
        rule.setContent { PersonaTheme(reduceMotion = true) { SettingsScreen(services) } }
        rule.onNode(hasSetTextAction()).performTextInput("Codex")
        rule.onNodeWithText("Agent").performClick()
        rule.onNodeWithText("Codex 设置").performClick()
        back()
        rule.onNodeWithText("Claude Code 设置").assertExists()
        back()
        rule.onNode(hasSetTextAction()).assertTextEquals("Codex")
        rule.onNode(hasSetTextAction()).performTextClearance()
        rule.onNodeWithText("模型", useUnmergedTree = true).performClick()
        // An older hub without the model catalog can still reach its provider settings.
        rule.onNodeWithText("模型供应商").performClick()
        back()
        rule.onNodeWithText("高级").assertExists()
    }
    @Test fun notificationAliasesHaveOneChecklistAndDoNotFinishOnboarding() {
        val services = demoSettingsServices(rule.activity.resources)
        ReadinessRecords(rule.activity).pending(true)
        var initial by mutableStateOf("notify")
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("page-shot").fillMaxSize()) { SettingsScreen(services, initialPage = initial) }
        } }
        for (alias in listOf("notify", "ready", "notification-background")) {
            rule.runOnIdle { initial = alias }
            rule.waitForIdle()
            rule.onNodeWithText("通知与后台").assertExists()
            rule.onNodeWithText("跳过未完成项").assertDoesNotExist()
            rule.onNodeWithText("主对话").assertDoesNotExist()
            rule.onNodeWithText("准备清单").assertDoesNotExist()
            assertTrue(needsOnboardingReadiness(rule.activity))
            assertNull(ReadinessRecords(rule.activity).decision("machines"))
        }
        for (d in listOf(false, true)) {
            rule.runOnIdle { dark = d }
            image("notification-background-${if (d) "dark" else "light"}")
        }
    }
    @Test fun agentAboutAndConnectionPagesInBothSchemes() {
        val services = demoSettingsServices(rule.activity.resources)
        var initial by mutableStateOf("agent-settings")
        var dark by mutableStateOf(false)
        rule.setContent { PersonaTheme(darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("page-shot").fillMaxSize()) { SettingsScreen(services, initialPage = initial) }
        } }
        for (page in listOf("agent-settings", "about", "diag")) for (d in listOf(false, true)) {
            rule.runOnIdle { initial = page; dark = d }
            rule.waitForIdle()
            if (page == "agent-settings") {
                rule.onNodeWithText("Antigravity 设置").assertExists()
                rule.onNodeWithText("会话分诊").assertExists()
            }
            if (page == "about") rule.onNodeWithText("检查更新").assertExists()
            if (page == "diag") rule.onNodeWithText("配对").assertExists()
            image("$page-${if (d) "dark" else "light"}")
        }
    }
}
