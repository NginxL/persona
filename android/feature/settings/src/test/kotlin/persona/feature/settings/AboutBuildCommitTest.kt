package persona.feature.settings

import android.content.ClipboardManager
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.assertHasClickAction
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import java.util.Locale

/**
 * About's commit row (the build's commit, shown short and copied in full): which resource values count as a commit, what the
 * row says and does with and without one, and that a long value never costs the other two rows or a second line.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w320dp-h800dp-xhdpi")
class AboutBuildCommitTest {
    @get:Rule val rule = createComposeRule()

    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val full40 = "0123456789abcdef0123456789abcdef01234567"
    private val full64 = "0123456789abcdef".repeat(4)

    /** A resource table that answers [BUILD_COMMIT_RESOURCE] with [value] (the id is 0 when [value] is absent). */
    private fun resourcesWith(value: String?): Resources {
        val base = context.resources
        return object : Resources(base.assets, base.displayMetrics, base.configuration) {
            override fun getIdentifier(name: String?, defType: String?, defPackage: String?) =
                if (name == BUILD_COMMIT_RESOURCE && defType == "string" && value != null) 4242 else 0
            override fun getString(id: Int): String = if (id == 4242 && value != null) value else throw Resources.NotFoundException("$id")
        }
    }

    @Test fun sevenFortyAndSixtyFourHexDigitsAreCommits() {
        for (value in listOf("abcdef0", full40, full64)) assertEquals(value, BuildCommit.parse(value)?.full)
        assertEquals("abcdef0123", BuildCommit.parse("ABCDEF0123")?.full)
    }

    @Test fun anythingElseIsNoCommit() {
        val rejected = listOf(
            null, "", "unknown", "UNKNOWN", "main", "feature/about-commit", "origin/main", "/Users/someone/work/persona",
            "abcdef", "a".repeat(65), "abcdefg", " abcdef0", "abcdef0 ", "abcd ef0", "abcdef0\n", "abcdef0+", "0x1234567",
        )
        for (value in rejected) assertNull("accepted: $value", BuildCommit.parse(value))
    }

    @Test fun theShortFormIsAPrefixAndTheFullValueIsKept() {
        assertEquals("0123456789ab", BuildCommit.parse(full40)?.short)
        assertEquals(full40, BuildCommit.parse(full40)?.full)
        assertEquals("abcdef0", BuildCommit.parse("abcdef0")?.short)
        assertEquals(64, BuildCommit.parse(full64)?.full?.length)
    }

    @Test fun theResourceReaderReturnsNoCommitForAMissingEmptyOrUnknownValue() {
        assertNull("no such resource", resourcesWith(null).buildCommit("com.example.persona"))
        assertNull("empty", resourcesWith("").buildCommit("com.example.persona"))
        assertNull("unknown", resourcesWith("unknown").buildCommit("com.example.persona"))
        assertNull("not a commit", resourcesWith("main").buildCommit("com.example.persona"))
        assertNull("this module's own test app carries none", context.resources.buildCommit(context.packageName))
    }

    @Test fun theResourceReaderKeepsTheWholeValue() {
        assertEquals(full40, resourcesWith(full40).buildCommit("com.example.persona")?.full)
        assertEquals(full64, resourcesWith(full64).buildCommit("com.example.persona")?.full)
    }

    @Test fun everyLanguageHasTheCommitWords() {
        val names = listOf(R.string.settings_about_commit, R.string.settings_about_commit_unknown,
            R.string.settings_about_commit_copy, R.string.settings_about_commit_copied)
        val byLanguage = listOf("en", "zh", "ja").associateWith { tag ->
            val configuration = Configuration(context.resources.configuration).apply { setLocale(Locale.forLanguageTag(tag)) }
            names.map { context.createConfigurationContext(configuration).getString(it) }
        }
        for ((tag, words) in byLanguage) assertTrue("$tag has an empty string", words.all { it.isNotBlank() })
        assertNotEquals(byLanguage.getValue("en"), byLanguage.getValue("zh"))
        assertNotEquals(byLanguage.getValue("en"), byLanguage.getValue("ja"))
        assertNotEquals(byLanguage.getValue("zh"), byLanguage.getValue("ja"))
    }

    private class Notices : AppFeedback {
        val shown = mutableListOf<String>()
        override fun failure(error: Throwable, operation: Boolean) = false
        override fun notice(message: String): Boolean { shown += message; return true }
    }

    private fun clipboardText(): String? =
        (context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager).primaryClip?.getItemAt(0)?.text?.toString()

    private fun clearClipboard() = (context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager).clearPrimaryClip()

    private fun lines(node: SemanticsNodeInteraction): Int {
        val results = mutableListOf<TextLayoutResult>()
        val semantics: SemanticsNode = node.fetchSemanticsNode()
        val _ = semantics.config[SemanticsActions.GetTextLayoutResult].action?.invoke(results)
        return results.single().lineCount
    }

    @Test fun tappingACommitCopiesTheFullValueOnceWithOneNotice() {
        clearClipboard()
        val notices = Notices()
        rule.setContent {
            CompositionLocalProvider(LocalAppFeedback provides notices) {
                PersonaTheme(darkTheme = false, reduceMotion = true) { AboutBuildCommitRow(BuildCommit.parse(full40)) }
            }
        }
        rule.onNodeWithText("Commit").assertHasClickAction()
        rule.onNodeWithText("0123456789ab", useUnmergedTree = true).assertIsDisplayed()
        assertEquals("only the short form is drawn", 0, rule.onAllNodesWithText(full40, useUnmergedTree = true).fetchSemanticsNodes().size)
        rule.onNodeWithText("Commit").performClick()
        rule.waitForIdle()
        assertEquals(full40, clipboardText())
        assertEquals(listOf("Build commit copied"), notices.shown)
    }

    @Test fun aSixtyFourDigitCommitCopiesAllSixtyFourDigits() {
        clearClipboard()
        rule.setContent { PersonaTheme(darkTheme = false, reduceMotion = true) { AboutBuildCommitRow(BuildCommit.parse(full64)) } }
        rule.onNodeWithText("Commit").performClick()
        rule.waitForIdle()
        assertEquals(full64, clipboardText())
    }

    @Test fun withoutACommitTheRowSaysUnknownAndHasNothingToTap() {
        clearClipboard()
        val notices = Notices()
        rule.setContent {
            CompositionLocalProvider(LocalAppFeedback provides notices) {
                PersonaTheme(darkTheme = false, reduceMotion = true) { AboutBuildCommitRow(null) }
            }
        }
        rule.onNodeWithText("Commit").assertHasNoClickAction()
        rule.onNodeWithText("Unknown", useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText("Commit").performClick()
        rule.waitForIdle()
        assertNull(clipboardText())
        assertTrue(notices.shown.isEmpty())
    }

    @Test fun theThreeRowsKeepTheirOrderAndTheVersionRowsStayAsTheyWere() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) { Column { AboutSettingsPage(BuildCommit.parse(full40)) } }
        }
        val info = context.packageManager.getPackageInfo(context.packageName, 0)
        val top = { text: String -> rule.onNodeWithText(text).fetchSemanticsNode().boundsInRoot.top }
        assertTrue(top("Version") < top("Build"))
        assertTrue(top("Build") < top("Commit"))
        rule.onNodeWithText("${info.longVersionCode}", useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText(info.versionName ?: "Not provided", useUnmergedTree = true).assertIsDisplayed()
    }

    @Test fun aLongCommitOnANarrowLargeFontScreenKeepsEveryRowOnOneLine() {
        rule.setContent {
            val base = androidx.compose.ui.platform.LocalDensity.current
            CompositionLocalProvider(androidx.compose.ui.platform.LocalDensity provides Density(base.density, fontScale = 2f)) {
                PersonaTheme(darkTheme = false, reduceMotion = true) { Column { AboutSettingsPage(BuildCommit.parse(full64)) } }
            }
        }
        for (title in listOf("Version", "Build", "Commit")) {
            rule.onNodeWithText(title, useUnmergedTree = true).assertIsDisplayed()
            assertEquals("$title wrapped", 1, lines(rule.onNodeWithText(title, useUnmergedTree = true)))
        }
        rule.onNodeWithText("0123456789ab", useUnmergedTree = true).assertIsDisplayed()
        assertEquals("the commit wrapped", 1, lines(rule.onNodeWithText("0123456789ab", useUnmergedTree = true)))
    }
}
