package persona.feature.knowledge

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.MeetingHandoff
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Native JVM review captures, not device evidence: the hand-off sheet and rows in every style and color mode. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class MeetingHandoffShotsTest {
    @get:Rule val rule = createComposeRule()
    private val out = File(System.getProperty("persona.shots") ?: "build/meeting-handoff-shots").also { it.mkdirs() }
    private val targets = listOf(
        Session(machineId = "mac", sessionId = "site", title = "Website redesign", status = "idle", updatedAt = "2026-11-19T11:00:00Z"),
        Session(machineId = "mac", sessionId = "board", title = "Data board", status = "idle", updatedAt = "2026-11-19T10:00:00Z"),
    )
    private val machines = listOf(Machine("mac", name = "Work Mac", online = true))
    private val sent = MeetingHandoff("h1", "2026-11-19T12:00:00Z", "mac", "site", title = "Website redesign", state = "sent")
    private val failed = MeetingHandoff("h2", "2026-11-19T12:01:00Z", "mac", "board", title = "Data board",
        transcript = true, state = "failed", problem = "unavailable")

    @Test fun sheetInEveryStyleAndColorMode() = sheet("meeting-handoff-sheet")

    @Test fun rowsInEveryStyleAndColorMode() = rows("meeting-handoff-rows", 500)

    @Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi", fontScale = 2f)
    @Test fun sheetAtLargestTextInEveryStyleAndColorMode() = sheet("meeting-handoff-sheet-large")

    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi", fontScale = 2f)
    @Test fun rowsAtLargestTextInEveryStyleAndColorMode() = rows("meeting-handoff-rows-large", 800)

    private fun sheet(name: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                    MeetingHandoffSheet(targets, machines, transcriptOffered = true, again = null,
                        work = KnowledgeMeetingHandoffWork(), onSend = { _, _, _ -> }, onDismiss = {})
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            val window = listOf(isDialog(), isPopup()).first { rule.onAllNodes(it).fetchSemanticsNodes().isNotEmpty() }
            val bitmap = rule.onAllNodes(window).onLast().captureToImage().asAndroidBitmap()
            write("$name-${s.key}-${if (d) "dark" else "light"}.png", bitmap)
        }
    }

    private fun rows(name: String, height: Int) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(height.dp).background(PersonaTheme.palette.background)) {
                    Column {
                        MeetingHandoffRow(sent, NoMeetingNavigator)
                        MeetingHandoffRow(failed, NoMeetingNavigator, onRetry = {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            write("$name-${s.key}-${if (d) "dark" else "light"}.png", bitmap)
        }
    }

    private fun write(name: String, bitmap: Bitmap) {
        File(out, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}

private object NoMeetingNavigator : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}
