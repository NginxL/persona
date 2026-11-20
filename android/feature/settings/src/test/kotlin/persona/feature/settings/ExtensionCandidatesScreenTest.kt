package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.unit.Density
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h850dp-xhdpi")
class ExtensionCandidatesScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val fixture = """
        {
          "purpose":{"id":"extension.sample","name":"Research notes","kind":"api","fields":["provider","model"],
            "revision":12,"provider_ids":["launcher.alpha","launcher.beta","launcher.cli"],
            "candidates":{"mode":"extension","modes":["extension","off","custom"],"most":3,
              "direct_provider_ids":["launcher.alpha","launcher.beta"],"custom":[],
              "requested":[{"models":[{"model":"notes-fast","provider":"launcher.alpha","provider_name":"Aster","available":true},
                  {"model":"notes-archive","available":false,"problem":"No configured provider offers this model."}],
                "last_used":"2026-06-02T12:30:00Z"},{"models":[{"model":"notes-long","provider":"launcher.beta","available":true}],
                "last_used":"2026-05-29T03:14:00Z"}]}},
          "providers":[{"id":"launcher.alpha","name":"Aster","kind":"api","has_key":true},
            {"id":"launcher.beta","name":"Birch","kind":"api","has_key":true},
            {"id":"launcher.cli","name":"Local terminal","kind":"launcher"}]
        }
    """.trimIndent()

    private var response = fixture
    private val saves = mutableListOf<Pair<String?, JsonObject>>()
    private val reads = mutableListOf<String?>()
    private val service = object : ModelConfiguration {
        override suspend fun catalog() = error("Opening a purpose reads its own details")
        override suspend fun purpose(id: String): ModelPurposeDetail {
            reads += currentCoroutineContext()[RequestManager]?.id
            return JsonCodec.decodeFromString(response)
        }
        override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft): ModelPurposeSave {
            saves += currentCoroutineContext()[RequestManager]?.id to draft.patch(purpose)
            return ModelPurposeSave.Conflict
        }
        override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = null
    }
    private fun capture(name: String) {
        val out = File(System.getenv("PERSONA_CANDIDATE_SHOTS") ?: "build/reports/tests/extension-candidate-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use { assertTrue(rule.onNodeWithTag("candidate-page").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    private val roster = ManagerRoster(listOf(HubManager("assistant", "Personal", isDefault = true),
        HubManager("work", "Work")), "assistant")
    @Composable private fun Page(look: AppStyle = AppStyle.Glass, dark: Boolean = false, scale: Float = 1f,
        id: String = "extension.sample") {
        CompositionLocalProvider(LocalManagerRoster provides roster, LocalDensity provides Density(LocalDensity.current.density, scale)) {
            PersonaTheme(darkTheme = dark, style = look, reduceMotion = true) {
                Box(Modifier.testTag("candidate-page").fillMaxSize().background(PersonaTheme.palette.background)) {
                    ModelPurposePage(service, id, onBack = {}, manager = "work")
                }
            }
        }
    }
    private fun list() = rule.onNode(hasScrollToIndexAction())
    private fun show(matcher: SemanticsMatcher): SemanticsNodeInteraction {
        list().performScrollToNode(matcher)
        return rule.onNode(matcher)
    }
    private fun mode(label: String) {
        show(hasTestTag("candidate-mode")).performClick()
        rule.onNodeWithText(label).performClick()
    }
    private fun provider(position: Int, name: String) {
        show(hasTestTag("candidate-provider-$position")).performClick()
        rule.onNodeWithText("Local terminal").assertDoesNotExist()
        rule.onNode(hasText(name) and !hasText("Model provider") and hasClickAction()).performClick()
    }
    private fun field(position: Int): SemanticsNodeInteraction {
        show(hasTestTag("candidate-model-$position"))
        return rule.onNode(hasSetTextAction() and hasAnyAncestor(hasTestTag("candidate-model-$position")))
    }

    @Test fun extensionPurposeOffersItsCandidateSettings() {
        rule.setContent { Page() }
        rule.onNodeWithText("Research notes").assertIsDisplayed()
        capture("purpose-overview")
        rule.onNode(hasScrollToIndexAction()).performScrollToNode(hasText("Candidate models"))
        rule.onNodeWithText("Candidate models").assertIsDisplayed()
    }

    @Test fun requestsExplainResolutionAndLastUseInEitherPolicy() {
        rule.setContent { Page() }
        show(hasTestTag("candidate-requests")).performClick()
        show(hasText("notes-fast")).assertIsDisplayed()
        show(hasText("Aster")).assertIsDisplayed()
        show(hasText("No configured provider offers this model.", substring = true)).assertIsDisplayed()
        show(hasText("Last used ", substring = true) and hasText("Jun", substring = true)).assertIsDisplayed()
        show(hasText("notes-long")).assertIsDisplayed()
        show(hasText("launcher.beta")).assertIsDisplayed()
        mode("Use only the purpose’s model")
        show(hasText("notes-archive")).assertIsDisplayed()
        assertTrue(saves.isEmpty())
        assertEquals(listOf("work"), reads)
    }

    @Test fun customEditorEnforcesLimitsAndPreservesOrderedRowsOnAConflict() {
        rule.setContent { Page() }
        mode("Use my list")
        rule.onNodeWithText("Save").assertIsNotEnabled()
        provider(1, "Aster")
        field(1).performTextReplacement("notes-fast")
        show(hasText("Add model")).performClick()
        provider(2, "Aster")
        field(2).performTextReplacement("notes-fast")
        show(hasText("Each provider and model pair can appear only once.")).assertIsDisplayed()
        rule.onNodeWithText("Save").assertIsNotEnabled()
        provider(2, "Birch")
        field(2).assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, AnnotatedString("")))
        show(hasContentDescription("Move model 2 up")).performClick()
        show(hasTestTag("candidate-provider-1")).assertTextContains("Birch")
        show(hasText("Add model")).performClick()
        show(hasText("Add model")).assertIsNotEnabled()
        show(hasContentDescription("Remove model 3")).performClick()
        field(2).assertTextEquals("notes-fast")
        rule.onNodeWithText("Save").assertIsEnabled().performClick()
        assertEquals(listOf("work" to JsonCodec.parseToJsonElement("""{"revision":12,"candidates":{"mode":"custom","custom":[{"provider":"launcher.beta","model":""},{"provider":"launcher.alpha","model":"notes-fast"}]}}""").jsonObject), saves)
        show(hasText("The configuration was changed elsewhere.", substring = true)).assertIsDisplayed()
        show(hasTestTag("candidate-provider-1")).assertTextContains("Birch")
        field(2).assertTextEquals("notes-fast")
        rule.onNodeWithText("Save").assertIsNotEnabled()
        assertEquals(listOf("work"), reads)
    }

    @Test fun nonExtensionAndOlderHubPurposesKeepTheirExistingEditor() {
        var id by mutableStateOf("assistant")
        response = fixture.replace("extension.sample", "assistant")
        rule.setContent { Page(id = id) }
        show(hasText("Settings for this use")).assertIsDisplayed()
        rule.onNodeWithText("Candidate models").assertDoesNotExist()
        rule.runOnIdle {
            response = """{"purpose":{"id":"extension.old","name":"Legacy use","revision":7},"providers":[]}"""
            id = "extension.old"
        }
        show(hasText("Settings for this use")).assertIsDisplayed()
        rule.onNodeWithText("Candidate models").assertDoesNotExist()
        rule.onNodeWithText("Save").assertIsEnabled()
        assertTrue(saves.isEmpty())
    }

    @Test fun screenshotsCoverRequestedAndCustomListsInEveryAppearance() {
        response = fixture.replace("\"mode\":\"extension\"", "\"mode\":\"custom\"")
            .replace("\"custom\":[]", """ "custom":[{"provider":"launcher.alpha","model":"notes-fast","available":true},{"provider":"launcher.beta","model":"effective-default","configured_model":"","available":true}]""")
        var look by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        rule.setContent { Page(look, dark, scale) }
        for (style in AppStyle.entries) for (font in listOf(1f, 2f)) for (night in listOf(false, true)) {
            look = style; dark = night; scale = font
            Snapshot.sendApplyNotifications(); rule.waitForIdle()
            val suffix = "${style.name.lowercase()}-${if (night) "dark" else "light"}-${if (font == 2f) "large" else "default"}"
            list().performScrollToKey("candidate-title")
            capture("custom-$suffix")
            val requests = hasTestTag("candidate-requests")
            list().performScrollToKey("candidate-requests")
            rule.onNode(requests).performSemanticsAction(SemanticsActions.OnClick)
            rule.onNode(requests).assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Expanded"))
            list().performScrollToKey("candidate-requests")
            capture("requests-$suffix")
            rule.onNode(requests).performSemanticsAction(SemanticsActions.OnClick)
            rule.onNode(requests).assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Collapsed"))
        }
        assertTrue(saves.isEmpty())
    }
}
