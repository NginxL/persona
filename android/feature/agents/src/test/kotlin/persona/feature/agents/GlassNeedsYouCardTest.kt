package persona.feature.agents

import androidx.compose.foundation.layout.Column
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.PersonaTheme
import persona.core.domain.answer.AnswerAction
import persona.core.domain.answer.AnswerDraft
import persona.core.domain.sort.NeedsYouRow
import java.time.Instant

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class GlassNeedsYouCardTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-22T08:00:00Z")
    private val planCard = Interaction(
        id = "ix_plan",
        machineId = "mac",
        sessionId = "s1",
        sessionTitle = "Photo sync HEIC",
        kind = "plan",
        plan = "Support HEIC\n1. Convert with sips",
        expiresAt = now.plusSeconds(600).toString(),
    )
    private val questionCard = Interaction(
        id = "ix_q",
        machineId = "mac",
        sessionId = "s1",
        sessionTitle = "Photo sync HEIC",
        kind = "question",
        questions = listOf(
            Question(
                question = "Which output format?",
                options = listOf(
                    QuestionOption("JPEG", "Compatible"),
                    QuestionOption("PNG", "Lossless"),
                ),
            ),
        ),
        expiresAt = now.plusSeconds(600).toString(),
    )
    private val bashCard = Interaction(
        id = "ix_bash",
        machineId = "mac",
        sessionId = "s1",
        sessionTitle = "Photo sync HEIC",
        kind = "permission",
        toolName = "Bash",
        toolInput = buildJsonObject { put("command", "python3 -m unittest discover") },
        expiresAt = now.plusSeconds(600).toString(),
    )

    @Test fun flattenShowsPlaceOnlyOnFirstCardOfEachSession() {
        val session = Session("mac", "s1", title = "Photo sync HEIC", state = "needs_input")
        val row = NeedsYouRow(SessionKey("mac", "s1"), listOf(planCard, questionCard, bashCard), now)
        val flattened = flattenGlassNeedsYou(listOf(row), { session }, { "Mac Studio" }, includeOwnerlessWaiting = false)
        assertEquals(3, flattened.size)
        val cards = flattened.filterIsInstance<GlassNeedsYouItem.Card>()
        assertTrue(cards[0].showsPlace)
        assertFalse(cards[1].showsPlace)
        assertFalse(cards[2].showsPlace)
    }

    @Test fun groupedGlassCardRendersInlinePillsAndDispatchesQuickActions() {
        val answers = mutableListOf<Triple<String, AnswerAction, AnswerDraft>>()
        val opened = mutableListOf<String>()
        val items = listOf(
            GlassNeedsYouItem.Card(planCard, null, "Mac Studio", showsPlace = true),
            GlassNeedsYouItem.Card(questionCard, null, "Mac Studio", showsPlace = false),
            GlassNeedsYouItem.Card(bashCard, null, "Mac Studio", showsPlace = false),
        )
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Column {
                    items.forEachIndexed { index, entry ->
                        JGroupItem(GroupPosition.of(index, items.size), grouped = true) {
                            GlassNeedsYouCardRow(
                                request = entry.request,
                                machine = entry.machine,
                                now = now,
                                online = true,
                                delivery = null,
                                showsPlace = entry.showsPlace,
                                onOpen = { opened += entry.request.id },
                                onAnswer = { req, action, draft, _ -> answers += Triple(req.id, action, draft) },
                            )
                        }
                    }
                }
            }
        }
        // Place line appears once (on the first card only), matching iOS InteractionPreviewCard.showsPlace.
        assertEquals(1, rule.onAllNodesWithText("Photo sync HEIC · Mac Studio").fetchSemanticsNodes().size)
        rule.onNodeWithText("Support HEIC").assertIsDisplayed()
        rule.onNodeWithText("Which output format?").assertIsDisplayed()
        rule.onNodeWithText("Bash: python3 -m unittest discover").assertIsDisplayed()
        // Plan row has no giant Review plan button; tapping opens it.
        assertEquals(0, rule.onAllNodesWithText("Review plan").fetchSemanticsNodes().size)
        rule.onNodeWithText("Support HEIC").performClick()
        assertEquals(listOf("ix_plan"), opened)
        // Question inline option pill dispatches PickOption.
        rule.onNodeWithText("PNG").performClick()
        assertEquals(Triple("ix_q", AnswerAction.PickOption, AnswerDraft(optionIndex = 1)), answers.last())
        // Permission inline Allow pill dispatches Allow.
        rule.onNodeWithText("Allow").performClick()
        assertEquals(Triple("ix_bash", AnswerAction.Allow, AnswerDraft()), answers.last())
    }

    @Test fun offlineGlassCardDisablesPillsAndShowsReason() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                GlassNeedsYouCardRow(
                    request = bashCard,
                    machine = "Mac Studio",
                    now = now,
                    online = false,
                    delivery = null,
                    showsPlace = true,
                    onOpen = {},
                    onAnswer = { _, _, _, _ -> },
                )
            }
        }
        rule.onNodeWithText("Connect to the hub to send your choice").assertIsDisplayed()
        assertTrue(SemanticsProperties.Disabled in rule.onNodeWithText("Allow").fetchSemanticsNode().config)
        assertTrue(SemanticsProperties.Disabled in rule.onNodeWithText("Deny").fetchSemanticsNode().config)
    }
}
