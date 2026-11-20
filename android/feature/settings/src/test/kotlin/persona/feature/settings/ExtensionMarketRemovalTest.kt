package persona.feature.settings

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ExtensionMarket
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme

/** Removing a market (#1895): its installed extensions stay, and the confirmation says how many stop upgrading. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp")
class ExtensionMarketRemovalTest {
    @get:Rule val rule = createComposeRule()

    private val first = ExtensionMarket("m1", "https://plugins.example.com/one/", "Market one")
    private val second = ExtensionMarket("m2", "https://plugins.example.com/two/", "Market two")
    private val roster = ManagerRoster(listOf(
        HubManager("assistant", "Personal", online = true, isDefault = true),
        HubManager("other-manager", "Other", online = true),
    ), "assistant")
    private val wire = Json { ignoreUnknownKeys = true }

    @Before fun chooseAssistant() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun resetAssistant() = AssistantManagerChoice.reset()

    private class RemovalApi(
        private val markets: List<ExtensionMarket>,
        private val answer: suspend (call: Int) -> List<HubExtension>,
    ) : ExtensionApiStub() {
        var reads = 0
            private set
        val removed = mutableListOf<String>()

        override suspend fun markets() = markets
        override suspend fun extensions(): List<HubExtension> = answer(++reads)
        override suspend fun removeMarket(id: String) { removed += id }
    }

    /**
     * `market` is additive hub provenance. Decode it as wire data rather than requiring a newer Android model field:
     * the old model ignores it, while the fixed page must use it for an honest count.
     */
    private fun installed(id: String, market: String?, manager: String? = null, autoUpgrade: Boolean? = true): HubExtension =
        wire.decodeFromJsonElement(HubExtension.serializer(), buildJsonObject {
            put("id", id)
            put("name", id)
            put("kind", "ui")
            put("status", "running")
            put("source", "installed")
            autoUpgrade?.let { put("auto_upgrade", it) }
            market?.let { put("market", it) }
            manager?.let { put("manager", it) }
        })

    private fun show(api: RemovalApi, managers: ManagerRoster? = roster) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides managers) { ExtensionMarketsPage(api, onBack = {}) }
            }
        }
        rule.waitUntil { shown(first.name) }
    }

    private fun shown(text: String) = rule.onAllNodesWithText(text).fetchSemanticsNodes().isNotEmpty()

    private fun askRemoval(market: ExtensionMarket = first) {
        rule.onNodeWithText(market.name).performClick()
        rule.onNodeWithText("Remove market").performClick()
        rule.waitForIdle()
    }

    private fun awaitWords(words: String) {
        rule.waitUntil(5_000) { shown(words) }
        rule.onNodeWithText(words).assertExists()
    }

    private val uncounted = "The hub stops reading this market. Extensions installed from it stay, but stop upgrading until the market is added again with the same address."

    @Test fun zeroAffectedExtensionsSaysNoneCameFromTheMarket() {
        val api = RemovalApi(listOf(first)) { listOf(
            installed("other-market", "m2"),
            installed("not-from-a-market", null, autoUpgrade = null),
        ) }
        show(api)
        askRemoval()
        awaitWords("The hub stops reading this market. No installed extension came from it.")
        assertEquals(1, api.reads)
        rule.onNode(hasText("0 extensions", substring = true) and hasAnyAncestor(isDialog())).assertDoesNotExist()
    }

    @Test fun oneAffectedExtensionUsesSingularAndCancelRemovesNothing() {
        val api = RemovalApi(listOf(first)) { listOf(installed("notes", "m1")) }
        show(api)
        askRemoval()
        awaitWords("The hub stops reading this market. 1 extension installed from it stays, but stops upgrading until the market is added again with the same address.")
        assertEquals(1, api.reads)
        rule.onNodeWithText("1 extensions", substring = true).assertDoesNotExist()
        rule.onNodeWithText("Cancel").performClick()
        rule.onNode(isDialog()).assertDoesNotExist()
        assertTrue(api.removed.isEmpty())
    }

    @Test fun severalAffectedExtensionsUsePluralExcludeOtherManagersAndStillRemove() {
        val api = RemovalApi(listOf(first)) { listOf(
            installed("on", "m1", manager = "assistant", autoUpgrade = true),
            installed("off", "m1", manager = "assistant", autoUpgrade = false),
            installed("another-market", "m2", manager = "assistant"),
            installed("another-assistant", "m1", manager = "other-manager"),
            installed("not-from-a-market", null, manager = "assistant", autoUpgrade = null),
        ) }
        show(api)
        askRemoval()
        awaitWords("The hub stops reading this market. 2 extensions installed from it stay, but stop upgrading until the market is added again with the same address.")
        assertEquals(1, api.reads)
        rule.onNode(hasText("Remove") and hasAnyAncestor(isDialog()) and hasClickAction()).performClick()
        rule.waitUntil { api.removed.isNotEmpty() }
        assertEquals(listOf("m1"), api.removed)
        rule.onNode(isDialog()).assertDoesNotExist()
    }

    @Test fun oldHubWithoutMarketProvenanceShowsAnUncountedWarning() {
        val api = RemovalApi(listOf(first)) { listOf(installed("old-hub", market = null)) }
        show(api)
        askRemoval()
        awaitWords(uncounted)
        assertEquals(1, api.reads)
        rule.onNode(hasText("0 extensions", substring = true) and hasAnyAncestor(isDialog())).assertDoesNotExist()
    }

    @Test fun failedExtensionListShowsAnUncountedWarning() {
        val api = RemovalApi(listOf(first)) { throw IllegalStateException("offline") }
        show(api)
        askRemoval()
        awaitWords(uncounted)
        assertEquals(1, api.reads)
        rule.onNodeWithText("No installed extension came from it", substring = true).assertDoesNotExist()
    }

    @Test fun ambiguousOwnershipShowsAnUncountedWarningInsteadOfGuessing() {
        val api = RemovalApi(listOf(first)) { listOf(
            installed("possibly-default", "m1", manager = "assistant"),
            installed("explicitly-other", "m1", manager = "other-manager"),
        ) }
        show(api, managers = null)
        askRemoval()
        awaitWords(uncounted)
        assertEquals(1, api.reads)
        rule.onNodeWithText("2 extensions installed", substring = true).assertDoesNotExist()
        rule.onNode(hasText("0 extensions", substring = true) and hasAnyAncestor(isDialog())).assertDoesNotExist()
    }

    @Test fun nonMarketItemsCarryingMarketProvenanceDoNotCount() {
        val api = RemovalApi(listOf(first)) { listOf(
            installed("configured", "m1").copy(source = "hub.json"),
            installed("attached", "m1").copy(attached = buildJsonObject { put("online", true) }),
        ) }
        show(api)
        askRemoval()
        awaitWords("The hub stops reading this market. No installed extension came from it.")
        assertEquals(1, api.reads)
    }

    @Test fun selectedAssistantDoesNotCountUnstampedDefaultExtensions() {
        AssistantManagerChoice.choose("hub", "other-manager", "assistant")
        val api = RemovalApi(listOf(first)) { listOf(installed("default-only", "m1", manager = null)) }
        show(api)
        askRemoval()
        awaitWords("The hub stops reading this market. No installed extension came from it.")
        assertEquals(1, api.reads)
    }

    @Test fun aLateCountCannotReplaceANewerMarketConfirmation() {
        val releaseFirst = CompletableDeferred<Unit>()
        val api = RemovalApi(listOf(first, second)) { call ->
            if (call == 1) releaseFirst.await()
            listOf(installed("extension-$call", if (call == 1) "m1" else "m2", manager = "assistant"))
        }
        show(api)
        askRemoval(first)
        // Both a delayed confirmation and one already showing an uncounted warning can be superseded.
        if (rule.onAllNodes(isDialog()).fetchSemanticsNodes().isNotEmpty()) {
            rule.onNodeWithText("Cancel").performClick()
            rule.waitForIdle()
        }
        askRemoval(second)
        rule.waitUntil(5_000) { api.reads >= 2 }
        rule.waitUntil(5_000) { shown("Remove Market two?") }
        releaseFirst.complete(Unit)
        rule.waitForIdle()
        rule.onNodeWithText("Remove Market one?").assertDoesNotExist()
        rule.onNodeWithText("Remove Market two?").assertExists()
        assertTrue(api.removed.isEmpty())
    }
}
