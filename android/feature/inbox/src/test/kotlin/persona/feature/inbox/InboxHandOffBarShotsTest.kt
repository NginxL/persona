package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.io.IOException
import kotlinx.coroutines.CompletableDeferred
import org.junit.Rule
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.inbox.*

/** Native JVM component evidence only; IME, real windows and device glass still need the adaptive scene and integration shots. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxHandOffBarShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out() = File(System.getProperty("persona.shots") ?: "build/inbox-handoff-bar-shots").also { it.mkdirs() }
    private fun save(name: String, bitmap: Bitmap) {
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun capture(name: String) {
        rule.waitForIdle()
        save(name, rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap())
    }

    @Test fun everyStateInEveryStyleAndColorMode() {
        rule.mainClock.autoAdvance = false
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scenario by mutableStateOf("default")
        var run by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    val repository = remember {
                        HandOffBarTestRepository(agentCount = 5).apply {
                            when (scenario) {
                                "sending" -> handOffGate = CompletableDeferred()
                                "failure" -> failure = IOException("Example transport failure")
                                "last-gone" -> handOffMemory.value = listOf(
                                    HandOffMemoryEntry("mail|sender:sender@example.com", "agent:machine/gone"),
                                )
                            }
                        }
                    }
                    val scope = rememberCoroutineScope()
                    val resources = LocalResources.current
                    val model = remember { InboxHandOffBarModel(repository.record.item.id, repository, scope,
                        resources, undoWindowMillis = 60_000) }
                    LaunchedEffect(model) {
                        when (scenario) {
                            "input" -> model.updateInput("A typed request")
                            "queued", "sending", "sent", "failure" -> {
                                model.queue("Investigate this event", typed = false)
                                if (scenario != "queued") model.viewQueued()
                            }
                        }
                    }
                    Box(
                        Modifier.testTag("shot").width(400.dp).height(360.dp).background(PersonaTheme.palette.background),
                        contentAlignment = Alignment.BottomCenter,
                    ) {
                        InboxHandOffBar(repository, model, enabled = true, onDraftReply = {}, onOpen = {})
                    }
                }
            }
        }
        val scenarios = listOf("default", "input", "queued", "sending", "sent", "failure", "last-gone")
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (scene in scenarios) {
            rule.runOnIdle {
                style = s
                dark = d
                scenario = scene
                run++
                Snapshot.sendApplyNotifications()
            }
            rule.mainClock.advanceTimeBy(100)
            rule.waitForIdle()
            if (scene == "queued") {
                rule.onNodeWithText("Undo").assertHasClickAction()
                rule.onNodeWithText("View").assertHasClickAction()
            }
            capture("inbox-handoff-$scene-${s.key}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun targetMenuAndDraftSheet() {
        val repository = HandOffBarTestRepository(agentCount = 12)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.testTag("shot").width(400.dp).height(1000.dp).background(PersonaTheme.palette.background)) {
                    InboxEventScreen(repository, InboxEventRoute(repository.record.item.id), HandOffBarTestNavigator())
                }
            }
        }
        rule.onNodeWithTag("inbox-handoff-target").performClick()
        save("inbox-handoff-target-menu-glass-light", rule.onAllNodes(isPopup()).onLast().captureToImage().asAndroidBitmap())
        rule.onNodeWithText("More…").performClick()
        rule.onNode(hasText("Main chat") and hasAnyAncestor(isPopup())).assertExists()
        rule.onNodeWithText("Also mark as done").assertExists()
        for (index in 1..12) rule.onNodeWithText("Agent $index").assertExists()
        rule.onNodeWithText("Agent 4").performClick()
        rule.onNodeWithTag("inbox-handoff-chip-draft").performClick()
        rule.onNodeWithText("Existing AI reply").assertExists()
        lateinit var draftBitmap: Bitmap
        rule.runOnIdle {
            // Compose's dialog-root capture can crop the Activity bitmap; draw the actual sheet window instead.
            val decor = checkNotNull(org.robolectric.shadows.ShadowDialog.getLatestDialog().window).decorView
            draftBitmap = Bitmap.createBitmap(decor.width, decor.height, Bitmap.Config.ARGB_8888)
            decor.draw(android.graphics.Canvas(draftBitmap))
        }
        save("inbox-handoff-draft-reply-glass-light", draftBitmap)
    }

    @Config(qualifiers = "en-w320dp-h800dp-xhdpi")
    @Test fun narrowAndLargestTextKeepQueuedActionsReachable() = largeText("en")

    @Config(qualifiers = "zh-w320dp-h800dp-xhdpi")
    @Test fun simplifiedChineseKeepsActionsOnOneLine() = largeText("zh")

    @Config(qualifiers = "ja-w320dp-h800dp-xhdpi")
    @Test fun japaneseKeepsActionsOnOneLine() = largeText("ja")

    private fun largeText(language: String) {
        val longTarget = "An agent with a deliberately long target name"
        var style by mutableStateOf(AppStyle.Glass)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale = 2f)) {
                PersonaTheme(reduceMotion = true, style = style) {
                    val repository = remember { HandOffBarTestRepository() }
                    val scope = rememberCoroutineScope()
                    val resources = LocalResources.current
                    val model = remember {
                        InboxHandOffBarModel(repository.record.item.id, repository, scope,
                            resources, undoWindowMillis = 60_000).also {
                            it.selectTarget(it.liveTargets().single().copy(title = longTarget))
                            it.queue("Investigate", typed = false)
                        }
                    }
                    Box(
                        Modifier.testTag("shot").width(320.dp).height(500.dp).background(PersonaTheme.palette.background),
                        contentAlignment = Alignment.BottomCenter,
                    ) {
                        InboxHandOffBar(repository, model, enabled = true, onDraftReply = {}, onOpen = {})
                    }
                }
            }
        }
        for (nextStyle in AppStyle.entries) {
            rule.runOnIdle {
                style = nextStyle
                Snapshot.sendApplyNotifications()
            }
            rule.mainClock.advanceTimeByFrame()
            rule.waitForIdle()
            rule.onNodeWithText(when (language) {
                "zh" -> "撤销"
                "ja" -> "元に戻す"
                else -> "Undo"
            }).assertHasClickAction()
            rule.onNodeWithText(when (language) {
                "zh" -> "查看"
                "ja" -> "表示"
                else -> "View"
            }).assertHasClickAction()
            val layouts = mutableListOf<TextLayoutResult>()
            val _ = rule.onNodeWithText(longTarget, useUnmergedTree = true).fetchSemanticsNode()
                .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
            assertTrue("$nextStyle: the target label must ellipsize in its bounded chip",
                layouts.single().isLineEllipsized(0))
            val targetBounds = rule.onNodeWithTag("inbox-handoff-target").getUnclippedBoundsInRoot()
            val fieldBounds = rule.onNodeWithTag("inbox-handoff-field").getUnclippedBoundsInRoot()
            val sendBounds = rule.onNodeWithTag("inbox-handoff-send").getUnclippedBoundsInRoot()
            assertTrue("$nextStyle: the target must not cover the input", targetBounds.right <= fieldBounds.left)
            assertTrue("$nextStyle: the input must not cover Send", fieldBounds.right <= sendBounds.left)
            capture("inbox-handoff-queued-$language-large-text-${nextStyle.key}")
        }
    }
}
