package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * A Persona conversation's own Bash approval (#4883), as its chat's panel above the composer draws it: before, the
 * assistant style showed no panel until a tray row was tapped; now the card is there, says 在等你批准, Allow sends the
 * answer and the panel is gone once the card is. Every style, light and dark; PNGs in `build/assistant-own-card-shots/`
 * (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class AssistantOwnCardShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/assistant-own-card-shots").also { it.mkdirs() }

    private val chat = SessionKey("hub", "c1")
    private val bash = Interaction("ix_bash", machineId = "hub", sessionId = "c1", kind = "permission", toolName = "Bash",
        toolInput = buildJsonObject {
            put("command", JsonPrimitive("ssh vps 'cat /etc/example/config.json'"))
            put("description", JsonPrimitive("Read the server config on the VPS"))
        })

    @Test fun theOwnCardIsInTheChatAndLeavesOnceAllowed() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var pending by mutableStateOf(listOf(bash))
        val sent = mutableListOf<Resolution>()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val panel = pending.filter { it.sessionKey == chat }
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                    InteractionPanel(panel, null, 600.dp, null, emptySet(), { _, r -> sent += r; pending = emptyList() }, null, {})
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("在等你批准", useUnmergedTree = true).assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "after-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
        style = AppStyle.Glass; dark = false
        rule.waitForIdle()
        rule.onNode(hasText("允许") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(1, sent.size)
        assertEquals(true, sent.single().allow)
        rule.onNodeWithText("在等你批准", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun hubTitleNamesThePermissionCardAtEveryStyleAndFontSize() {
        val titled = bash.copy(toolName = "mcp__persona__report_to_supervisor", toolInput = null,
            toolTitle = "Report to supervisor · Done", toolNameTitle = "Report to supervisor")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var large by mutableStateOf(false)
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply { fontScale = if (large) 1.6f else 1f }
            val density = Density(LocalDensity.current.density, if (large) 1.6f else 1f)
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides density) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val panel = listOf(titled).filter { it.sessionKey == chat }
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                        InteractionPanel(panel, null, 600.dp, null, emptySet(), { _, _ -> }, null, {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (big in listOf(false, true)) {
            style = s; dark = d; large = big
            rule.waitForIdle()
            rule.onNodeWithText("请求使用 Report to supervisor · Done", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("总是同意会放行：这段对话里的所有「Report to supervisor」调用", useUnmergedTree = true).assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val font = if (big) "large" else "default"
            File(out(), "title-$font-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }

    @Test fun ownerProposalExactReplyShots() {
        val proposal = bash.copy(toolName = "mcp__persona__mail_send", toolTitle = "Send mail",
            toolInput = buildJsonObject {
                put("to", JsonPrimitive("me@example.com"))
                put("subject", JsonPrimitive("Weekly report"))
                put("body", JsonPrimitive("The draft is ready for review."))
            }, ownerProposal = true, approvalBatchId = "batch-demo", approvalItemId = "item-mail-1",
            ownerApprovalPhrases = listOf("发", "同意"), noExpiry = true, alwaysHonored = false)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var large by mutableStateOf(false)
        var allow = ""
        var deny = ""
        var reply = ""
        var always = ""
        rule.setContent {
            allow = stringResource(R.string.chat_request_allow)
            deny = stringResource(R.string.chat_request_deny)
            reply = stringResource(R.string.chat_request_owner_reply, "发")
            always = stringResource(persona.core.designsystem.R.string.designsystem_core_always_allow_session)
            val configuration = Configuration(LocalConfiguration.current).apply { fontScale = if (large) 2f else 1f }
            val density = Density(LocalDensity.current.density, if (large) 2f else 1f)
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides density) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                        InteractionPanel(listOf(proposal), null, 700.dp, null, emptySet(), { _, _ -> }, null, {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (big in listOf(false, true)) {
            style = s; dark = d; large = big
            rule.waitForIdle()
            rule.onNodeWithText(reply, useUnmergedTree = true).assertExists()
            rule.onNodeWithText(allow, useUnmergedTree = true).assertExists()
            rule.onNodeWithText(deny, useUnmergedTree = true).assertExists()
            rule.onNodeWithText(always, useUnmergedTree = true).assertDoesNotExist()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val font = if (big) "large" else "default"
            File(out(), "owner-proposal-$font-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
}
