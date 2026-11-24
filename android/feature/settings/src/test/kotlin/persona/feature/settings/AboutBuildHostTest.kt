package persona.feature.settings

import android.content.ClipboardManager
import android.content.Context
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.feature.settings.generated.AboutBuildRow

/**
 * Native JVM host evidence for #13076: the generated component occupies only the existing Build slot. The native
 * page, Version/Commit siblings and default handwritten row are not copied. This is not device or accessibility evidence.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi")
class AboutBuildHostTest {
    @get:Rule val rule = createComposeRule()
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    @Test fun realPackageInfoFeedsTheGeneratedSlotAndTheDefaultHostRollsBack() {
        val packages = shadowOf(context.packageManager)
        val info = packages.getInternalMutablePackageInfo(context.packageName)
        val previous = info.longVersionCode
        val build = 9_007_199_254_740_993L
        val nativeBuild = (build + 1).toString()
        val commit = BuildCommit.parse("0123456789abcdef0123456789abcdef01234567")
        val notices = mutableListOf<String>()
        val feedback = object : AppFeedback {
            override fun failure(error: Throwable, operation: Boolean) = false
            override fun notice(message: String): Boolean { notices += message; return true }
        }
        val generated = mutableStateOf(false)
        try {
            info.setLongVersionCode(build)
            val snapshot = AboutBuildSnapshot.from(context.packageManager.getPackageInfo(context.packageName, 0))
            val generatedBuild = requireNotNull(snapshot.buildNumber)
            info.setLongVersionCode(nativeBuild.toLong())
            val nativeVersion = context.packageManager.getPackageInfo(context.packageName, 0).versionName
            rule.setContent {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    PersonaTheme(darkTheme = false, reduceMotion = true) {
                        AboutSettingsPage(commit, buildRow = if (generated.value) {
                            { AboutBuildRow(generatedBuild) }
                        } else {
                            null
                        })
                    }
                }
            }

            rule.onAllNodesWithText("Build").assertCountEquals(1)
            rule.onNodeWithText(nativeBuild, useUnmergedTree = true).assertIsDisplayed()
            rule.onAllNodesWithText(generatedBuild).assertCountEquals(0)
            rule.onNodeWithText(nativeVersion ?: "Not provided", useUnmergedTree = true).assertIsDisplayed()
            rule.onNodeWithText("Commit").performClick()
            rule.waitForIdle()
            val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
            assertEquals(commit!!.full, clipboard.primaryClip?.getItemAt(0)?.text?.toString())
            assertEquals(listOf("Build commit copied"), notices)

            // The same remembered PackageInfo snapshot: handwritten default -> generated slot -> handwritten default.
            generated.value = true
            rule.waitForIdle()
            rule.onAllNodesWithText("Build").assertCountEquals(1)
            rule.onNodeWithText(generatedBuild, useUnmergedTree = true).assertIsDisplayed()
            rule.onAllNodesWithText(nativeBuild).assertCountEquals(0)
            rule.onNodeWithText(nativeVersion ?: "Not provided", useUnmergedTree = true).assertIsDisplayed()
            rule.onNodeWithText(commit.short, useUnmergedTree = true).assertIsDisplayed()

            generated.value = false
            rule.waitForIdle()
            rule.onAllNodesWithText("Build").assertCountEquals(1)
            rule.onNodeWithText(nativeBuild, useUnmergedTree = true).assertIsDisplayed()
            rule.onAllNodesWithText(generatedBuild).assertCountEquals(0)
            rule.onNodeWithText(nativeVersion ?: "Not provided", useUnmergedTree = true).assertIsDisplayed()
            rule.onNodeWithText(commit.short, useUnmergedTree = true).assertIsDisplayed()
        } finally {
            info.setLongVersionCode(previous)
        }
    }
}
