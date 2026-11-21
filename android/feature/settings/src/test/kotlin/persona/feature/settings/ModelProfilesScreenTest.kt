package persona.feature.settings

import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2200dp-xhdpi")
class ModelProfilesScreenTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val providers = listOf(
        ModelProvider("launcher.claude", "Claude Code", kind = "launcher"),
        ModelProvider("api.fast", "Fast API", kind = "api", api = "responses"),
    )
    private val purposes = listOf(
        ModelPurpose("assistant", "Main chat", "Everyday uses", effective = ModelBinding(provider = "launcher.claude", model = "opus")),
        ModelPurpose("agent.new", "New agent", "Agents", effective = ModelBinding(provider = "launcher.claude", model = "sonnet")),
    )

    @Test fun profilesSectionSwitchesEditsAndRollsBack() {
        var currentView = ModelProfilesView(
            revision = 2,
            active = "performance",
            rollback = ModelProfileUndo(to = "economy", changes = listOf("assistant")),
            profiles = listOf(
                ModelProfile(
                    id = "performance",
                    name = "Performance",
                    description = "Strong models for complex tasks",
                    purposes = mapOf("assistant" to ModelBinding(provider = "launcher.claude", model = "opus", effort = "high")),
                    tiers = mapOf("strong" to ModelProfileTier(launcher = "claude", model = "opus", effort = "high", description = "Complex work")),
                    active = true,
                    changes = emptyList(),
                ),
                ModelProfile(
                    id = "economy",
                    name = "Economy",
                    description = "Lower cost models",
                    purposes = mapOf("assistant" to ModelBinding(provider = "launcher.claude", model = "sonnet", effort = "medium")),
                    tiers = mapOf("standard" to ModelProfileTier(launcher = "claude", model = "sonnet", description = "Routine work")),
                    active = false,
                    changes = listOf("assistant"),
                ),
            ),
        )
        val applied = mutableListOf<String>()
        var rollbacks = 0
        val savedInputs = mutableListOf<Pair<String, ModelProfileSaveInput>>()
        var catalogReads = 0

        val api = object : ModelConfiguration {
            override suspend fun catalog(): ModelCatalog {
                catalogReads++
                return ModelCatalog(purposes, providers)
            }
            override suspend fun purpose(id: String) = ModelPurposeDetail(purposes.first { it.id == id }, providers)
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = ModelPurposeSave.Conflict
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = null
            override suspend fun profiles(): ModelProfilesView = currentView
            override suspend fun applyProfile(id: String): ModelProfilesView {
                applied += id
                currentView = currentView.copy(
                    active = id,
                    profiles = currentView.profiles.map { it.copy(active = it.id == id) },
                )
                return currentView
            }
            override suspend fun rollbackProfile(): ModelProfilesView {
                rollbacks++
                currentView = currentView.copy(active = "economy", rollback = null)
                return currentView
            }
            override suspend fun saveProfile(id: String, input: ModelProfileSaveInput): ModelProfileSave {
                savedInputs += id to input
                currentView = currentView.copy(
                    revision = currentView.revision + 1,
                    profiles = currentView.profiles.map { if (it.id == id) it.copy(name = input.name, description = input.description) else it },
                )
                return ModelProfileSave.Saved(currentView)
            }
        }

        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides null) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    ModelConfigurationPage(api, onBack = {}, onPurpose = { _, _ -> })
                }
            }
        }
        rule.waitForIdle()

        // Profiles and Rollback are rendered
        rule.onNodeWithTag("model-profile-row-performance").assertIsDisplayed()
        rule.onNodeWithTag("model-profile-row-economy").assertIsDisplayed()
        rule.onNodeWithTag("model-profile-rollback").assertIsDisplayed()

        // Open Economy profile detail, inspect, then edit and save
        rule.onNodeWithTag("model-profile-row-economy").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("standard").assertIsDisplayed()
        rule.onNodeWithTag("model-profile-edit-button").performClick()
        rule.waitForIdle()

        rule.onNode(hasSetTextAction() and hasText("Economy")).performTextReplacement("Economy Plus")
        rule.waitForIdle()
        rule.onNodeWithTag("model-profile-save-button").performClick()
        rule.waitForIdle()

        assertEquals(1, savedInputs.size)
        assertEquals("economy", savedInputs.single().first)
        assertEquals("Economy Plus", savedInputs.single().second.name)
        assertEquals(2L, savedInputs.single().second.revision)

        // Apply Economy Plus profile from detail page
        rule.onNodeWithTag("model-profile-apply-button").performClick()
        rule.waitForIdle()
        assertEquals(listOf("economy"), applied)
        assertTrue("catalog should refresh after save/apply", catalogReads >= 3)

        // Trigger rollback from main page
        rule.onAllNodesWithText(rule.activity.getString(R.string.settings_model_profiles_rollback)).onLast().assertIsDisplayed().performClick()
        rule.waitForIdle()
        assertEquals(1, rollbacks)
    }

    @Test fun oldHubWithoutProfilesHidesProfilesSectionCleanly() {
        val api = object : ModelConfiguration {
            override suspend fun catalog() = ModelCatalog(purposes, providers)
            override suspend fun purpose(id: String) = ModelPurposeDetail(purposes.first { it.id == id }, providers)
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = ModelPurposeSave.Conflict
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = null
            override suspend fun profiles(): ModelProfilesView? = null
        }

        rule.setContent {
            CompositionLocalProvider(LocalManagerRoster provides null) {
                PersonaTheme(darkTheme = false, reduceMotion = true) {
                    ModelConfigurationPage(api, onBack = {}, onPurpose = { _, _ -> })
                }
            }
        }
        rule.waitForIdle()

        rule.onNodeWithText("Main chat").assertIsDisplayed()
        rule.onNodeWithText(rule.activity.getString(R.string.settings_model_profiles_title)).assertDoesNotExist()
    }
}
