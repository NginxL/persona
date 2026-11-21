package persona.feature.agents

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.answer.AnswerAction
import persona.core.domain.answer.AnswerDraft
import java.time.Instant

/** The full secret hand-off card in Agents; its collapsed quick path offers View, never a quick Allow. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp")
class SecretHandoffRequestCardTest {
    @get:Rule val rule = createComposeRule()
    private val now = Instant.parse("2026-11-20T08:00:00Z")
    private val card = JsonCodec.decodeFromString<Interaction>(
        """{"id":"ix_secret","machine_id":"build","session_id":"release","session_title":"Release","kind":"permission",
            "tool_name":"secret_handoff","always_label":"this launcher on this computer","tool_input":{
            "variables":[{"name":"DEPLOY_TOKEN","provider_name":"Example Vault","title":"Production deploy token",
            "reference":"op://Development/Release/token","value":"sentinel-never-show","future_sensitive_field":"sentinel-future"}],
            "agent":"Release Agent","machine_name":"Build computer","cwd":"/srv/example/release",
            "prompt":"Run the release checks, then publish the approved build.","summary":"sentinel-summary"}}""",
    )
    private val answers = mutableListOf<Pair<AnswerAction, AnswerDraft>>()
    private var opened = 0

    private fun show(request: Interaction = card, style: AppStyle = AppStyle.Glass) = rule.setContent {
        PersonaTheme(style = style, darkTheme = false, reduceMotion = true) {
            RequestCard(request, "Build computer", AgentsMetadata(), now, online = true, delivery = null, onOpen = { opened++ },
                onAnswer = { _, action, draft, _ -> answers += action to draft })
        }
    }

    @Test fun theFullCardShowsOnlyAllowlistedContextAndActions() {
        show()
        for (text in listOf("DEPLOY_TOKEN", "Example Vault", "Production deploy token", "Release Agent", "Build computer",
            "/srv/example/release", "Run the release checks, then publish the approved build.")) {
            rule.onAllNodes(hasText(text, substring = true), useUnmergedTree = true).fetchSemanticsNodes().also { assert(it.isNotEmpty()) }
        }
        for (secret in listOf("op://Development/Release/token", "sentinel-never-show", "sentinel-future", "sentinel-summary")) {
            rule.onAllNodes(hasText(secret, substring = true), useUnmergedTree = true).assertCountEquals(0)
        }
        // Only the full card offers Allow; no quick-action duplicate appears beside it.
        rule.onAllNodesWithText("Allow").assertCountEquals(1)
        rule.onNodeWithText("Allow").performClick()
        rule.onNodeWithText("Always allow").performClick()
        rule.onNodeWithText("Deny").performClick()
        assertEquals(listOf(AnswerAction.Allow, AnswerAction.Always, AnswerAction.Deny), answers.map { it.first })
        assertEquals(listOf(AnswerDraft(), AnswerDraft(), AnswerDraft()), answers.map { it.second })
    }

    @Test fun theCollapsedQuickPathOnlyOpensTheFullCard() {
        show(style = AppStyle.Board)
        rule.onAllNodesWithText("Allow").assertCountEquals(0)
        rule.onAllNodesWithText("Always allow").assertCountEquals(0)
        rule.onAllNodesWithText("Deny").assertCountEquals(0)
        rule.onNodeWithText("View request").performClick()
        assertEquals(1, opened)
        assertTrue(answers.isEmpty())
    }

    @Test fun unreadableVariablesPermitNoApproval() {
        show(JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_secret","kind":"permission","tool_name":"secret_handoff","always_label":"this launcher on this computer",
                "tool_input":{"variables":[]}}""",
        ))
        rule.onNodeWithText("Allow").assertIsNotEnabled()
        rule.onNodeWithText("Always allow").assertIsNotEnabled()
        rule.onNodeWithText("Deny").performClick()
        assertEquals(listOf(AnswerAction.Deny), answers.map { it.first })
    }
}
