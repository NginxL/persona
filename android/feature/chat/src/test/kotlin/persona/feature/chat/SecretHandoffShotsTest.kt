package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/**
 * The reserved secret hand-off card (#3962), drawn by the real conversation editor in every style, appearance and at
 * Android's largest text size. The input deliberately carries reference/value sentinels: none may reach the UI. Native
 * JVM PNGs go to `-Dpersona.shots=<dir>` (not emulator or device evidence).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi", application = QuestionTestApplication::class)
class SecretHandoffShotsTest {
    @get:Rule val rule = createComposeRule()

    private val input = buildJsonObject {
        putJsonArray("variables") {
            add(buildJsonObject {
                put("name", "DEPLOY_TOKEN")
                put("provider", "example-vault")
                put("provider_name", "Example Vault")
                put("title", "Production deploy token")
                put("reference", "op://sentinel/never/show")
                put("value", "sentinel-secret-value")
            })
        }
        put("agent", "Release Agent")
        put("machine_name", "Build computer")
        put("cwd", "/srv/example/release")
        put("prompt", "Run the release checks, then publish the approved build.")
        put("reference", "op://sentinel/root/never/show")
        put("value", "sentinel-root-secret-value")
        put("future_sensitive_field", "sentinel-future-secret")
    }
    private val card = Interaction(
        "handoff", machineId = "build", sessionId = "release", sessionTitle = "Release checks", kind = "permission",
        toolName = "secret_handoff", toolInput = input, summary = "sentinel-summary-never-show",
        alwaysLabel = "this reference for this Agent on this computer",
    )
    private val sent = mutableListOf<Resolution>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/secret-handoff-shots").also { it.mkdirs() }

    private fun show(busy: Boolean = false, request: Interaction = card) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                    InteractionPanel(listOf(request), null, 1400.dp, busyId = busyId(busy), uncertainIds = emptySet(),
                        onResolve = { _, resolution -> sent += resolution }, onTerminal = { error("No terminal action") }, onOverlay = {})
                }
            }
        }
    }

    private fun busyId(busy: Boolean) = if (busy) card.id else null

    private fun assertSafeCompleteCard() {
        rule.onNodeWithText("Release Agent", substring = true).assertExists()
        rule.onNodeWithText("Build computer", substring = true).assertExists()
        rule.onNodeWithText("DEPLOY_TOKEN").assertExists()
        rule.onNodeWithText("Example Vault", substring = true).assertExists()
        rule.onNodeWithText("Production deploy token", substring = true).assertExists()
        rule.onNodeWithText("/srv/example/release").assertExists()
        rule.onNodeWithText("Run the release checks, then publish the approved build.").assertExists()
        rule.onAllNodesWithTag("secret-handoff-variable").assertCountEquals(1)
        for (sentinel in listOf("sentinel", "op://", "future_sensitive_field", "secret_handoff")) {
            rule.onAllNodes(hasText(sentinel, substring = true), useUnmergedTree = true).assertCountEquals(0)
        }
        rule.onAllNodesWithText("The draft is kept on this page only").assertCountEquals(0)
        rule.onAllNodesWithText("Show full input").assertCountEquals(0)
        rule.onAllNodesWithText("Reason for denying (optional)").assertCountEquals(0)
        rule.onAllNodesWithText("Leave to computer").assertCountEquals(0)
        rule.onAllNodesWithText("Handle in terminal", substring = true).assertCountEquals(0)
        rule.onAllNodes(hasSetTextAction(), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun completeCardShowsOnlyAllowlistedContextAndExactActions() {
        show()
        assertSafeCompleteCard()
        rule.onNodeWithText("Allow").assertIsEnabled().performClick()
        rule.onNodeWithText("Always allow").assertIsEnabled().performClick()
        rule.onNodeWithText("Deny").assertIsEnabled().performClick()
        assertEquals(listOf(Resolution(allow = true), Resolution(allow = true, always = true), Resolution(allow = false)), sent)
    }

    @Test fun incompleteOrBusyCardsCannotApprove() {
        show(request = card.copy(toolInput = buildJsonObject { putJsonArray("variables") {} }))
        rule.onNodeWithText("Allow").assertIsNotEnabled()
        rule.onNodeWithText("Always allow").assertIsNotEnabled()
        rule.onNodeWithText("Deny").assertIsEnabled()
        assertTrue(sent.isEmpty())
    }

    @Test fun aBusyCardCannotSubmitAgain() {
        show(busy = true)
        rule.onNodeWithText("Allow").assertIsNotEnabled()
        rule.onNodeWithText("Always allow").assertIsNotEnabled()
        rule.onNodeWithText("Deny").assertIsNotEnabled()
        assertTrue(sent.isEmpty())
    }

    @Test fun capturesEveryStyleAppearanceAndTextSize() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var large by mutableStateOf(false)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, if (large) 2f else 1f)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("shot")) {
                        InteractionPanel(listOf(card), null, 1500.dp, null, emptySet(), { _, resolution -> sent += resolution }, null, {})
                    }
                }
            }
        }
        for (text in listOf(false, true)) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            large = text; style = s; dark = d
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            assertSafeCompleteCard()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            assertTrue("The card drew pixels", bitmap.width > 0 && bitmap.height > 0)
            val name = "secret-handoff-${s.name.lowercase()}-${if (d) "dark" else "light"}-${if (text) "text-200" else "text-100"}.png"
            File(out(), name).outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
        assertTrue(sent.isEmpty())
    }
}
