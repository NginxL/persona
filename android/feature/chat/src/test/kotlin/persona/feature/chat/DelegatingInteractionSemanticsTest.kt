package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsNotSelected
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Resolution
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import java.time.Instant
import java.util.Locale

/** A question the assistant is looking at, its handover context, and its recommended options (#544). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp", application = QuestionTestApplication::class)
class DelegatingInteractionSemanticsTest {
    @get:Rule val rule = createComposeRule()

    private val oneQuestion = """[{"question":"Which driver?","options":[{"label":"pgx"},{"label":"sqlc"}]}]"""
    private val fullContext = """{"what":"Choose the database driver","why_now":"Queries start today",""" +
        """"recommend":"Wait for benchmarks","cost":"A later migration rewrites the queries",""" +
        """"reason":"This changes the release","deadline":"2026-11-08T10:00:00Z"}"""

    private fun card(questions: String = oneQuestion, delegating: Boolean = false, delegate: String? = null) =
        JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_delegate","machine_id":"box","session_id":"s1","kind":"question","delegating":$delegating,""" +
                """"questions":$questions""" + (delegate?.let { ""","delegate":$it""" } ?: "") + "}",
        )

    private fun show(interaction: Interaction, style: AppStyle = AppStyle.Glass): MutableList<Resolution> {
        val sent = mutableListOf<Resolution>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                Column(Modifier.width(400.dp)) {
                    InteractionPanel(listOf(interaction), null, 1200.dp, null, emptySet(), { _, answer -> sent += answer }, null, {})
                }
            }
        }
        rule.waitForIdle()
        return sent
    }

    @Test fun lookingCardSaysTheAssistantIsLookingAndRemainsAnswerable() {
        val sent = show(card(delegating = true))
        rule.onNodeWithText("Assistant is looking").assertExists()
        rule.onNodeWithText("The assistant is deciding whether it can answer this for you and hasn't notified you yet. " +
            "You can still answer; whichever answer comes first counts.").assertExists()

        rule.onNodeWithText("2. sqlc").performClick()
        assertEquals(listOf(Resolution(answers = mapOf("Which driver?" to "sqlc"))), sent)
    }

    private data class Words(
        val looking: String,
        val lookingHint: String,
        val contextTitle: String,
        val labels: List<String>,
        val locale: Locale,
    )

    private fun localizedLookingAndContext(words: Words) {
        show(card(delegating = true, delegate = fullContext))
        rule.onNodeWithText(words.looking).assertExists()
        rule.onNodeWithText(words.lookingHint).assertExists()
        rule.onNodeWithText(words.contextTitle).assertExists()
        for (value in listOf("Choose the database driver", "Queries start today", "Wait for benchmarks",
            "A later migration rewrites the queries", "This changes the release")) {
            rule.onNodeWithText(value, useUnmergedTree = true).assertExists()
        }
        val deadline = LocalizedFormats.format(Instant.parse("2026-11-08T10:00:00Z"), LocalizedFormats.DATE_TIME, words.locale)
        rule.onNodeWithText(deadline, useUnmergedTree = true).assertExists()

        // What, why now, recommendation, cost, reason, deadline: the settled order on web and Apple.
        val tops = words.labels.map { label ->
            rule.onNodeWithText(label, useUnmergedTree = true).assertExists().getUnclippedBoundsInRoot().top
        }
        assertEquals(words.labels.joinToString(), tops.sorted(), tops)
        rule.onAllNodesWithText("Assistant's pick", useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun englishLookingAndContextUseTheSettledOrder() = localizedLookingAndContext(Words(
        "Assistant is looking",
        "The assistant is deciding whether it can answer this for you and hasn't notified you yet. You can still answer; whichever answer comes first counts.",
        "What the assistant found",
        listOf("What", "Why now", "Recommended", "If not", "Why you decide", "Deadline"),
        Locale.ENGLISH,
    ))

    @Config(qualifiers = "zh-w400dp-h1400dp")
    @Test fun simplifiedChineseLookingAndContextUseTheSettledOrder() = localizedLookingAndContext(Words(
        "助手在看",
        "助手正在判断能不能替你答，还没通知你。你也可以直接答，先到的算。",
        "助手补充的情况",
        listOf("这件事", "为什么现在问", "推荐", "不选的代价", "为什么要你定", "截止"),
        Locale.SIMPLIFIED_CHINESE,
    ))

    @Config(qualifiers = "ja-w400dp-h1400dp")
    @Test fun japaneseLookingAndContextUseTheSettledOrder() = localizedLookingAndContext(Words(
        "アシスタントが確認中",
        "アシスタントが代わりに答えられるか判断しているところで、まだ通知していません。ここで答えることもでき、先に届いた回答が使われます。",
        "アシスタントの補足",
        listOf("内容", "今聞く理由", "おすすめ", "選ばない場合", "あなたが決める理由", "期限"),
        Locale.JAPANESE,
    ))

    @Test fun exactOptionRecommendationIsTaggedWithoutBeingPreselected() {
        val sent = show(card(delegate = """{"what":"Choose the database driver","recommend":"pgx"}"""))
        rule.onAllNodesWithText("Assistant's pick", useUnmergedTree = true).assertCountEquals(1)
        rule.onNode(hasText("1. pgx") and hasClickAction()).assertIsNotSelected()
        rule.onNodeWithText("Recommended", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("What", useUnmergedTree = true).assertExists()
        assertTrue(sent.isEmpty())
    }

    @Test fun nonOptionRecommendationStaysInContextWithoutTaggingAnOption() {
        show(card(delegate = """{"recommend":"Wait for benchmarks"}"""))
        rule.onNodeWithText("Recommended", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Wait for benchmarks", useUnmergedTree = true).assertExists()
        rule.onAllNodesWithText("Assistant's pick", useUnmergedTree = true).assertCountEquals(0)
        rule.onNode(hasText("1. pgx") and hasClickAction()).assertIsNotSelected()
        rule.onNode(hasText("2. sqlc") and hasClickAction()).assertIsNotSelected()
    }

    @Test fun multiSelectRecommendationTagsEveryCommaJoinedPickWithoutTickingIt() {
        val questions = """[{"question":"Which checks?","multi_select":true,""" +
            """"options":[{"label":"A"},{"label":"B"},{"label":"C"}]}]"""
        show(card(questions, delegate = """{"recommend":"A, C"}"""))
        rule.onAllNodesWithText("Assistant's pick", useUnmergedTree = true).assertCountEquals(2)
        for (label in listOf("1. A", "2. B", "3. C")) {
            rule.onNode(hasText(label) and hasClickAction())
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.ToggleableState, ToggleableState.Off))
        }
    }

    @Test fun fullWidthSemicolonMapsRecommendationsToTheirOwnQuestions() {
        val questions = """[{"question":"Which driver?","options":[{"label":"pgx"},{"label":"sqlc"}]},""" +
            """{"question":"Run tests first?","options":[{"label":"Yes"},{"label":"No"}]}]"""
        val sent = show(card(questions, delegate = """{"recommend":"pgx；No"}"""))
        rule.onAllNodesWithText("Assistant's pick", useUnmergedTree = true).assertCountEquals(1)
        rule.onNode(hasText("1. pgx") and hasClickAction()).assertIsNotSelected()

        // Android advances after a single-choice pick; the second recommendation is not picked for us.
        rule.onNodeWithText("1. pgx").performClick()
        rule.onNodeWithText("Run tests first?").assertExists()
        rule.onAllNodesWithText("Assistant's pick", useUnmergedTree = true).assertCountEquals(1)
        rule.onNode(hasText("2. No") and hasClickAction()).assertIsNotSelected()
        assertTrue(sent.isEmpty())
    }
}
