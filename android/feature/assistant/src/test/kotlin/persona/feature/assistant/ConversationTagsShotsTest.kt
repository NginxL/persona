package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.compose.ui.unit.width
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.time.Instant

/** Synthetic tag rows and chips, in every style, light/dark and with default/large text; no hub or user data. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class ConversationTagsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun chat(id: String, title: String, tags: List<String>, minutes: Long) = Session(
        machineId = "hub", sessionId = id, title = title, assistant = true, status = "idle", tags = tags,
        updatedAt = Instant.parse("2026-11-17T12:00:00Z").minusSeconds(minutes * 60).toString(),
    )

    @Test fun tagsInEveryStyleAppearanceAndTextSize() {
        val main = chat("main", "Main", listOf("Planning"), 0)
        val recent = listOf(
            chat("release", "Release plan", listOf("Planning", "Research"), 2),
            chat("trip", "Trip itinerary", listOf("Travel"), 8),
            chat("notes", "Research notes", listOf("Research"), 20),
        )
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableFloatStateOf(1f)
        rule.setContent {
            val density = LocalDensity.current
            val configuration = Configuration(LocalConfiguration.current).apply {
                this.fontScale = fontScale
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale),
                LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.testTag("conversation-tags-shot").width(400.dp).height(1100.dp)
                        .background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, ConversationGroups(main, emptyList(), recent), active = null,
                            hasMain = true, onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {},
                            onSettings = {}, tagChoices = listOf("Planning", "Research", "Travel"))
                    }
                }
            }
        }
        rule.onNodeWithText("All chats (3)").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Release plan").assertExists()
        val standard = mutableMapOf<Pair<AppStyle, Boolean>, Bitmap>()
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            style = s
            dark = d
            fontScale = scale
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            rule.onNodeWithTag("conversation-tag-filter").assertExists()
            val suffix = if (scale > 1f) "-large" else ""
            val name = "android-conversation-tags-${s.name.lowercase()}-${if (d) "dark" else "light"}$suffix"
            val bitmap = rule.onNodeWithTag("conversation-tags-shot").captureToImage().asAndroidBitmap()
            if (scale == 1f) standard[s to d] = bitmap
            else assertFalse("large text must change the rendered pixels", standard.getValue(s to d).sameAs(bitmap))
            historyShot(bitmap, name)
        }
    }

    @Test fun filterEditorInEveryStyleAppearanceAndTextSize() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableFloatStateOf(1f)
        rule.setContent {
            val density = LocalDensity.current
            val configuration = Configuration(LocalConfiguration.current).apply {
                this.fontScale = fontScale
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale),
                LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.testTag("conversation-tag-filters-shot").width(400.dp).height(320.dp)
                        .background(PersonaTheme.palette.background)) {
                        ConversationTagFilterBar(entries = listOf("Planning", "Research", "Unused"),
                            activeTag = "Planning", customized = true, editing = true, busy = false,
                            onSelect = {}, onChange = { true }, onEdit = {}, onDone = {})
                    }
                }
            }
        }
        val standard = mutableMapOf<Pair<AppStyle, Boolean>, Bitmap>()
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (scale in listOf(1f, 2f)) {
            style = s
            dark = d
            fontScale = scale
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            rule.onNodeWithText("Reset to automatic").assertExists()
            for (tag in listOf("Planning", "Research", "Unused")) {
                val bounds = rule.onNodeWithText(tag, useUnmergedTree = true).getUnclippedBoundsInRoot()
                assertTrue("$tag label collapsed for $s dark=$d scale=$scale", bounds.width > 0.dp && bounds.height > 0.dp)
            }
            val suffix = if (scale > 1f) "-large" else ""
            val name = "android-conversation-tag-filters-${s.name.lowercase()}-${if (d) "dark" else "light"}$suffix"
            val bitmap = rule.onNodeWithTag("conversation-tag-filters-shot").captureToImage().asAndroidBitmap()
            if (scale == 1f) standard[s to d] = bitmap
            else assertFalse("large text must change the rendered pixels", standard.getValue(s to d).sameAs(bitmap))
            historyShot(bitmap, name)
        }
    }

    @Test fun archivedTagRowsInLightDarkAndLargeText() {
        val archived = listOf(
            chat("archived-release", "Archived release", listOf("Planning", "Research"), 4).copy(archived = true),
            chat("archived-trip", "Archived trip", listOf("Travel"), 12).copy(archived = true),
        )
        var dark by mutableStateOf(false)
        val standard = mutableMapOf<Boolean, Bitmap>()
        fun mount(fontScale: Float) {
            // A dialog window takes its density from the recreated Activity, not the parent's CompositionLocal alone.
            RuntimeEnvironment.setFontScale(fontScale)
            rule.setContent {
                val density = LocalDensity.current
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale),
                    LocalConfiguration provides configuration) {
                    PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                        Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background))
                        ArchivedConversationsSheet(archived, onDismiss = {}, onOpen = {}, onUnarchive = {}, onEditTags = {})
                    }
                }
            }
        }
        for (scale in listOf(1f, 2f)) {
            if (scale > 1f) rule.activityRule.scenario.recreate()
            mount(scale)
            for (d in listOf(false, true)) {
                dark = d
                Snapshot.sendApplyNotifications()
                rule.waitForIdle()
                rule.onNodeWithText("Archived release").assertExists()
                val suffix = if (scale > 1f) "-large" else ""
                val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
                if (scale == 1f) standard[d] = bitmap
                else assertFalse("large text must change archived tag pixels", standard.getValue(d).sameAs(bitmap))
                historyShot(bitmap, "android-conversation-tags-archived-glass-${if (d) "dark" else "light"}$suffix")
            }
        }
    }
}

/** The new editor on the repository's default Robolectric screen, where a dialog text field can become idle. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-xhdpi")
class ConversationTagsEditorShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Test fun editorInLightDarkAndLargeText() {
        val session = Session(machineId = "hub", sessionId = "editor", title = "Release plan",
            tags = listOf("Planning", "Research"))
        var dark by mutableStateOf(false)
        val standard = mutableMapOf<Boolean, Bitmap>()
        fun mount(fontScale: Float) {
            RuntimeEnvironment.setFontScale(fontScale)
            rule.setContent {
                val density = LocalDensity.current
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale),
                    LocalConfiguration provides configuration) {
                    PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                        Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background))
                        ConversationTagsDialog(session, busy = false, onDismiss = {}, onSave = {})
                    }
                }
            }
        }
        for (scale in listOf(1f, 2f)) {
            if (scale > 1f) rule.activityRule.scenario.recreate()
            mount(scale)
            for (d in listOf(false, true)) {
                dark = d
                Snapshot.sendApplyNotifications()
                rule.waitForIdle()
                rule.onNodeWithTag("conversation-tag-draft").assertExists()
                val suffix = if (scale > 1f) "-large" else ""
                val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
                if (scale == 1f) standard[d] = bitmap
                else assertFalse("large text must change editor pixels", standard.getValue(d).sameAs(bitmap))
                historyShot(bitmap, "android-conversation-tags-editor-glass-${if (d) "dark" else "light"}$suffix")
            }
        }
    }
}
