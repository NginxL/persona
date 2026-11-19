package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File

/** #5220's two additional provider setups, in every style and light/dark with entirely synthetic data. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
class ProviderPasteShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val machines = listOf(Machine("mac", "Work Mac", online = false), Machine("linux", "Build box", online = true))
    private val existing = HubLauncher("work", displayName = "Work", source = "persona", baseUrl = "https://work.example.com",
        model = "old-model", models = listOf(PresetModel("old-model")), hasKey = true, auth = "bearer", sonnetModel = "old-mid")

    private class Providers(private val supported: Boolean = true, private val parsedModels: List<PresetModel> = listOf(PresetModel("new-mid"))) : ProviderSetup {
        override val builtinPresets = flowOf(true)
        override val launcherImportParse = flowOf(supported)
        val previews = mutableListOf<Pair<String, String?>>()
        val imports = mutableListOf<Triple<String, List<String>, String?>>()
        val added = mutableListOf<NewProvider>()
        val parsed = mutableListOf<String>()
        var parseGate: CompletableDeferred<Unit>? = null
        override suspend fun presets() = emptyList<ProviderPreset>()
        override suspend fun add(provider: NewProvider) { added += provider }
        override suspend fun launchers() = listOf(
            HubLauncher("work", displayName = "Work", source = "persona", baseUrl = "https://work.example.com", model = "old-model",
                models = listOf(PresetModel("old-model")), hasKey = true, auth = "bearer", sonnetModel = "old-mid"),
            HubLauncher("cc_work", source = "zshrc", baseUrl = "https://work.example.com", machines = listOf("mac", "linux")),
            HubLauncher("cc_hidden", source = "zshrc", baseUrl = "https://hidden.example.com", machines = listOf("mac"), hidden = true),
            HubLauncher("cc_imported", source = "zshrc", baseUrl = "https://imported.example.com", machines = listOf("mac")),
            HubLauncher("old", source = "persona", origin = "zshrc:cc_imported@mac"),
        )
        override suspend fun launcher(name: String) = launchers().first { it.name == name }
        override suspend fun parse(text: String): ProviderPasteResult {
            parsed += text
            parseGate?.await()
            return ProviderPasteResult(format = if (text.startsWith("persona://provider/import")) "link" else "shell", drafts = listOf(ProviderPasteDraft(
                label = "cc_work",
                provider = PastedProvider(name = "cc_work", displayName = "Work gateway", baseUrl = "https://work.example.com", apiKey = "eg-secret", auth = "bearer",
                    model = "new-model", models = parsedModels, env = "MAX_OUTPUT=8000", headers = "X-Team: synthetic"),
                envNames = listOf("MAX_OUTPUT"), skippedEnv = listOf(ProviderPasteSkipped("OTHER_TOKEN", "credential")),
                skippedHeaders = listOf(ProviderPasteSkipped("X-Api-Key", "credential")),
                matches = listOf(ProviderPasteMatch("work", "Work", sameKey = true)),
                notes = listOf(ProviderPasteNote("args_left_out", line = 2)),
            )), notes = listOf(ProviderPasteNote("not_claude", name = "other")))
        }
        override suspend fun importPreview(machine: String, alias: String?): List<ImportedProvider> {
            previews += machine to alias
            return listOf(ImportedProvider("cc-work", "https://work.example.com", "new-model", hasKey = true, aliases = listOf("cc_work")))
        }
        override suspend fun import(machine: String, providers: List<String>, alias: String?): LauncherImportResult {
            imports += Triple(machine, providers, alias)
            return LauncherImportResult(created = listOf("cc-work"))
        }
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/provider-paste-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun anOlderHubHasNeitherAdditionalEntry() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                NewProviderPage(Providers(false), onBack = {}, onSaved = {}, machines = machines)
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Custom compatible gateway").assertExists()
        rule.onNodeWithText("Paste a configuration").assertDoesNotExist()
        rule.onNodeWithText("Found on your computers").assertDoesNotExist()
    }

    @Test fun oneModelUsesSingular() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                NewProviderPage(Providers(parsedModels = emptyList()), onBack = {}, onSaved = {}, machines = machines)
            }
        }
        rule.onNodeWithText("Paste a configuration").performClick()
        rule.onAllNodes(hasSetTextAction()).onFirst().performTextInput("export ANTHROPIC_BASE_URL=https://work.example.com")
        rule.mainClock.advanceTimeBy(1_000)
        rule.waitForIdle()
        rule.onNodeWithText("1 model", substring = true).assertExists()
        rule.onNodeWithText("1 models", substring = true).assertDoesNotExist()
    }

    @Test fun anImportLinkLightOpensReviewAndPrefillWithoutSaving() = importLinkOpensReviewAndPrefill(false)

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-night-xhdpi")
    @Test fun anImportLinkDarkOpensReviewAndPrefillWithoutSaving() = importLinkOpensReviewAndPrefill(true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi", fontScale = 2.0f)
    @Test fun anImportLinkLargestTextLightOpensReviewAndPrefill() = importLinkOpensReviewAndPrefill(false, largest = true)

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-night-xhdpi", fontScale = 2.0f)
    @Test fun anImportLinkLargestTextDarkOpensReviewAndPrefill() = importLinkOpensReviewAndPrefill(true, largest = true)

    private fun importLinkOpensReviewAndPrefill(dark: Boolean, largest: Boolean = false) {
        val api = Providers()
        val firstLink = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        var link by mutableStateOf(firstLink)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").width(400.dp).height(1450.dp).background(PersonaTheme.palette.background)) {
                    NewProviderPage(api, onBack = {}, onSaved = {}, machines = machines, importLink = link)
                }
            }
        }
        rule.waitForIdle()
        val suffix = (if (dark) "dark" else "light") + (if (largest) "-ax" else "")
        rule.onNodeWithText("Opened from a link", substring = true).assertExists()
        rule.onNodeWithText("A provider import link").assertExists()
        rule.onNodeWithText("Work gateway").assertExists()
        rule.onNodeWithText(link, substring = true).assertDoesNotExist()
        assertTrue("nothing is added before Save", api.added.isEmpty())
        capture("provider-import-link-review-$suffix")

        val secondLink = "persona://provider/import?v=1&data=ANOTHER-FAKE-KEY"
        link = secondLink
        rule.waitForIdle()
        assertEquals("a newer external link replaces the old review", listOf(firstLink, secondLink), api.parsed)
        rule.onNodeWithText(secondLink, substring = true).assertDoesNotExist()

        rule.onNodeWithText("Add as a new provider").performClick()
        rule.onNodeWithText("Continue").performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("cc_work").onFirst().assertExists()
        rule.onNodeWithText("https://work.example.com").assertExists()
        rule.onNodeWithText("Save").assertExists()
        assertTrue("the prefilled form still has not saved", api.added.isEmpty())
        capture("provider-import-link-prefill-$suffix")
    }

    @Test fun anImportLinkParsesOnlyOnceWhileItsAnswerIsPending() {
        val api = Providers().apply { parseGate = CompletableDeferred() }
        val link = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                NewProviderPage(api, onBack = {}, onSaved = {}, machines = machines, importLink = link)
            }
        }
        rule.waitUntil(5_000) { api.parsed.isNotEmpty() }
        rule.mainClock.advanceTimeBy(1_000)
        assertEquals("the delayed answer must not trigger a second request", listOf(link), api.parsed)
        api.parseGate?.complete(Unit)
        rule.waitForIdle()
        assertEquals(listOf(link), api.parsed)
        rule.onNodeWithText("Work gateway").assertExists()
    }

    @Test fun reopeningTheSameImportLinkWhileWizardIsActiveParsesAgain() {
        val api = Providers()
        val link = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        val services = demoSettingsServices(rule.activity.resources).copy(demo = false, providers = api)
        ProviderImportRequests.pending.value = link
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                SettingsScreen(services, initialPage = "providers")
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Opened from a link", substring = true).assertExists()
        assertEquals(listOf(link), api.parsed)

        rule.onNodeWithText("Paste something else").performClick()
        rule.waitForIdle()
        ProviderImportRequests.pending.value = link
        rule.waitForIdle()
        assertEquals("the second external open must restart the active wizard's import", listOf(link, link), api.parsed)
        rule.onNodeWithText("Opened from a link", substring = true).assertExists()
    }

    @Test fun systemBackCancelsAnExternalImportReview() = externalImportBackCancels(toolbar = false)

    @Test fun toolbarBackCancelsAnExternalImportReview() = externalImportBackCancels(toolbar = true)

    private fun externalImportBackCancels(toolbar: Boolean) {
        val api = Providers()
        var closed = false
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                NewProviderPage(api, onBack = { closed = true }, onSaved = {}, machines = machines,
                    importLink = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY")
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Opened from a link", substring = true).assertExists()
        if (toolbar) rule.onNodeWithContentDescription("Back").performClick()
        else rule.runOnIdle { rule.activity.onBackPressedDispatcher.onBackPressed() }
        rule.waitForIdle()
        assertTrue("the first Back leaves the import-owned wizard", closed)
        assertTrue("Back never saves the imported draft", api.added.isEmpty())
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-night-xhdpi")
    @Test fun darkDrawsBothSetups() = drawsBothSetups(true)

    @Test fun lightDrawsBothSetups() = drawsBothSetups(false)

    private fun drawsBothSetups(dark: Boolean) {
        val api = Providers()
        var style by mutableStateOf(AppStyle.Glass)
        var run by mutableIntStateOf(0)
        var saved by mutableStateOf("")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1450.dp).background(PersonaTheme.palette.background)) {
                        NewProviderPage(api, onBack = {}, onSaved = { saved = it }, machines = machines)
                    }
                }
            }
        }
        for (s in AppStyle.entries) {
            style = s
            run++
            rule.waitForIdle()
            rule.onNodeWithText("Paste a configuration").assertExists()
            rule.onNodeWithText("cc_work").assertExists()
            rule.onNodeWithText("cc_hidden").assertDoesNotExist()
            rule.onNodeWithText("cc_imported").assertDoesNotExist()
            rule.onNodeWithText("Build box\nOnline", substring = true).assertExists()
            rule.onNodeWithText("Build box · Online", substring = true).assertDoesNotExist()
            val suffix = "${s.name.lowercase()}-${if (dark) "dark" else "light"}"
            capture("provider-paste-entry-$suffix")

            rule.onNodeWithText("Paste a configuration").performClick()
            rule.onAllNodes(hasSetTextAction()).onFirst().performTextInput("export ANTHROPIC_BASE_URL=https://work.example.com")
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            rule.onNodeWithText("Work gateway").assertExists()
            rule.onNodeWithText("Shell aliases and environment").assertExists()
            rule.onNodeWithText("This command does not run Claude Code (other)").assertExists()
            rule.onNodeWithText("Arguments that could contain sensitive values were left out (line 2)").assertExists()
            rule.onNodeWithText("Environment variables left out (credential): OTHER_TOKEN").assertExists()
            rule.onNodeWithText("Headers left out (credential): X-Api-Key").assertExists()
            rule.onNodeWithText("2 models\nBearer", substring = true).assertExists()
            rule.onNodeWithText("2 models · Bearer", substring = true).assertDoesNotExist()
            capture("provider-paste-review-$suffix")

            rule.onNodeWithText("Continue").performClick()
            rule.waitForIdle()
            rule.onNodeWithText("Add models to Work").assertDoesNotExist()
            rule.onAllNodesWithText("old-model").onFirst().assertExists()
            rule.onNodeWithText("Save").assertExists()
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("new-mid"))
            rule.onNodeWithText("new-mid").assertExists()
            capture("provider-paste-merge-$suffix")

            run++
            rule.waitForIdle()
            rule.onNodeWithText("cc_work").performClick()
            rule.waitForIdle()
            rule.onNodeWithText("cc-work").assertExists()
            assertEquals("linux" to "cc_work", api.previews.last())
            capture("provider-alias-import-$suffix")
            rule.onNodeWithText("Import 1 provider").performClick()
            rule.waitForIdle()
            rule.onNodeWithText("Added cc-work").assertExists()
            rule.onNodeWithText("Done").performClick()
            rule.waitForIdle()
            assertEquals("launcher.cc-work", saved)
            assertEquals(Triple("linux", listOf("cc-work"), "cc_work"), api.imports.last())
        }
    }
}
