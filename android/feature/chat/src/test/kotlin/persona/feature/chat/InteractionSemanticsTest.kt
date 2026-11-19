package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.answer.AnswerAction
import persona.core.domain.answer.AnswerDraft

/** The conversation's question card read by TalkBack (#4326): multi-select ticks, "n of total", the page, the status. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class InteractionSemanticsTest {
    @get:Rule val rule = createComposeRule()

    private val machine = Machine("box", name = "box")
    private val request = Interaction("q", machineId = "box", sessionId = "s1", kind = "question", questions = listOf(
        Question("要跑哪些测试？", multiSelect = true, options = listOf(QuestionOption("单元测试"), QuestionOption("集成测试"))),
        Question("用哪个框架？", options = listOf(QuestionOption("JUnit"), QuestionOption("Kotest"), QuestionOption("Spek"))),
    ))

    private fun show(
        busy: Boolean = false,
        interaction: Interaction = request,
        onAnswer: (AnswerAction, AnswerDraft, Boolean) -> Unit = { _, _, _ -> },
    ) = rule.setContent {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            var draft by remember { mutableStateOf(AnswerDraft(picks = mapOf("要跑哪些测试？" to setOf("单元测试")))) }
            Column {
                InteractionForm(interaction, machine, draft, { draft = it }, busy = busy, uncertain = false, full = false,
                    onAnswer = onAnswer, onTerminal = null, onExpand = {})
            }
        }
    }

    private fun recordedPlan(mode: String): Interaction = JsonCodec.decodeFromString(
        """{"id":"plan-$mode","machine_id":"box","session_id":"s1","kind":"plan","plan":"1. Ship it","plan_then":"$mode"}""",
    )

    private fun description(text: String) = SemanticsMatcher.expectValue(SemanticsProperties.ContentDescription, listOf(text))

    @Test fun multiSelectOptionsAreTickedCheckboxesWithTheirPosition() {
        show()
        rule.onNode(description("1. 单元测试，第 1 个，共 2 个"))
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Checkbox))
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.ToggleableState, ToggleableState.On))
        rule.onNode(description("2. 集成测试，第 2 个，共 2 个"))
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.ToggleableState, ToggleableState.Off))
        rule.onNode(description("第 1 题，共 2 题"), useUnmergedTree = true).assertExists()
    }

    @Test fun statusLineIsALiveRegion() {
        show(busy = true)
        rule.onNodeWithTag("request-status")
            .assert(SemanticsMatcher.expectValue(SemanticsProperties.LiveRegion, LiveRegionMode.Polite))
    }

    @Test fun recordedPlanModeIsShownWithoutConflictingApprovalChoices() {
        var approval: AnswerAction? = null
        show(interaction = recordedPlan("default"), onAnswer = { action, _, _ -> approval = action })
        rule.onNodeWithText("手动确认", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("批", useUnmergedTree = true).assertExists().performClick()
        rule.onNodeWithText("批准编辑", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("批准，手动确认", useUnmergedTree = true).assertDoesNotExist()
        assertEquals(AnswerAction.ApproveStepwise, approval)
        rule.onNodeWithText("打回", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("批准并自动执行", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("批准，每步确认", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun recordedAutoModeUsesTheGeneralModeNameAndApprovalAction() {
        var approval: AnswerAction? = null
        show(interaction = recordedPlan("auto"), onAnswer = { action, _, _ -> approval = action })
        rule.onNodeWithText("自动", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("批", useUnmergedTree = true).assertExists().performClick()
        assertEquals(AnswerAction.ApproveEdits, approval)
        rule.onNodeWithText("批准编辑", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("批准并自动执行", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("批准，每步确认", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun recordedBypassShowsOnlyRecordedApproval() {
        show(interaction = recordedPlan("bypassPermissions"))
        rule.onNodeWithText("全部放行", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("批", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("批准编辑", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("批准，每步确认", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun unknownRecordedModeKeepsExistingApprovalChoices() {
        show(interaction = recordedPlan("futureMode"))
        for (choice in listOf("批准编辑", "打回", "批准并自动执行", "批准，每步确认")) {
            rule.onNodeWithText(choice, useUnmergedTree = true).assertExists()
        }
    }

    @Test fun planWithoutRecordedModeKeepsExistingApprovalChoices() {
        show(interaction = Interaction("plan-legacy", machineId = "box", sessionId = "s1", kind = "plan", plan = "1. Ship it"))
        for (choice in listOf("批准编辑", "打回", "批准并自动执行", "批准，每步确认")) {
            rule.onNodeWithText(choice, useUnmergedTree = true).assertExists()
        }
    }
}
