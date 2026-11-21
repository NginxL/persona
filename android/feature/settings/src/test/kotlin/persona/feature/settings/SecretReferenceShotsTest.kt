package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasAnySibling
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.api.ExtensionContribution
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.ExtensionSecret
import persona.core.contracts.api.ExtensionSecretUser
import persona.core.contracts.api.HandoffRule
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.SecretReference
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

/**
 * Extension and model-provider secret references, using the production page, sheet and shared key field. Captures cover
 * every style, light/dark and default/200% text, and are written outside the repository with `-Dpersona.shots=<dir>`.
 * These are Robolectric native JVM images, not emulator or physical-device verification.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
class SecretReferenceShotsTest {
    @get:Rule val rule = createComposeRule()

    private val reference = SecretReference("example-vault", "Example Vault", "op://Development/Release/token", "Release token")
    private val provider = HubExtension(
        "example-vault", "Example Vault", "secret_provider", "stopped", source = "installed", enabled = true,
        contributes = mapOf("secret_providers" to listOf("vault")),
    )
    private val savedSecret = ExtensionSecret("deploy_token", title = "Deployment token", set = true, reference = reference)
    private val detail = ExtensionDetail(
        provider,
        secrets = listOf(savedSecret),
        contributions = listOf(ExtensionContribution("secret_providers", "vault", title = "Example Vault", enabled = true)),
        secretUsers = listOf(
            ExtensionSecretUser(kind = "extension", extension = "release-notes", extensionName = "Release Notes",
                secret = "deploy_token", secretTitle = "Deployment token", reference = reference.reference),
            ExtensionSecretUser(kind = "launcher", launcher = "release_gateway", launcherName = "Release Gateway",
                reference = reference.reference),
            ExtensionSecretUser(kind = "launcher", launcher = "release_gateway", launcherName = "Release Gateway",
                machine = "build", reference = reference.reference),
        ),
        handoffRules = listOf(HandoffRule("rule-release", reference.reference, "Release Agent", "build")),
    )

    private class Hub(
        var current: ExtensionDetail,
        feature: Boolean,
        private val providers: List<HubExtension>,
    ) : ExtensionApiStub() {
        val supported = MutableStateFlow(feature)
        /** False refuses only the fresh write check, leaving the already-open sheet drawn as available. */
        var writeAllowed = true
        var detailGate: CompletableDeferred<Unit>? = null
        val values = mutableListOf<Pair<String, String>>()
        val references = mutableListOf<Pair<String, String>>()
        val removed = mutableListOf<String>()
        override val secretReferencesAvailable: Flow<Boolean> get() = if (writeAllowed) supported else flowOf(false)
        override suspend fun extensions(): List<HubExtension> = providers
        override suspend fun extension(id: String): ExtensionDetail {
            detailGate?.await()
            return current
        }
        override suspend fun autoUpgrades(): Boolean = true
        override suspend fun setSecret(id: String, name: String, value: String): ExtensionDetail {
            values += name to value
            current = current.copy(secrets = current.secrets.map { if (it.name == name) it.copy(set = true, reference = null) else it })
            return current
        }
        override suspend fun setSecretReference(id: String, name: String, reference: String): ExtensionDetail {
            references += name to reference
            current = current.copy(secrets = current.secrets.map {
                if (it.name == name) it.copy(set = true, reference = SecretReference(provider = "example-vault", reference = reference)) else it
            })
            return current
        }
        override suspend fun removeHandoffRule(id: String, rule: String): ExtensionDetail {
            removed += rule
            current = current.copy(handoffRules = current.handoffRules.filterNot { it.id == rule })
            return current
        }
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/secret-reference-shots").also { it.mkdirs() }

    private fun show(hub: Hub, current: ExtensionDetail = hub.current) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                    ExtensionPage(hub, current.extension.id, onBack = {}, machines = flowOf(listOf(Machine("build", "Build computer", online = true))))
                }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText(if (current.readOnly) "Example Vault" else "Deployment token").fetchSemanticsNodes().isNotEmpty() }
    }

    private fun openSecret() {
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Deployment token"))
        rule.onNodeWithText("Deployment token").performClick()
        rule.waitForIdle()
    }

    @Test fun featureOffHidesTheReferenceSwitch() = assertReferenceSwitchAbsent(feature = false, providers = listOf(provider))

    @Test fun noEnabledProviderHidesTheReferenceSwitch() = assertReferenceSwitchAbsent(feature = true, providers = emptyList())

    private fun assertReferenceSwitchAbsent(feature: Boolean, providers: List<HubExtension>) {
        val hub = Hub(detail.copy(secrets = listOf(savedSecret.copy(reference = null, set = false))), feature, providers)
        show(hub)
        openSecret()
        rule.onAllNodesWithText("Use a reference").assertCountEquals(0)
        rule.onNode(hasSetTextAction()).assertExists()
    }

    @Test fun anOpaquePrefixIsAValueUntilTheUserExplicitlyChoosesReferenceMode() {
        val hub = Hub(detail.copy(secrets = listOf(savedSecret.copy(reference = null, set = false))), true, listOf(provider))
        show(hub)
        openSecret()
        rule.onNodeWithText("Use a reference").assertIsOff()
        rule.onNode(hasSetTextAction()).performTextReplacement("op://Development/Release/literal")
        rule.onNodeWithText("Save").performClick()
        rule.waitUntil { hub.values.isNotEmpty() }
        assertEquals(listOf("deploy_token" to "op://Development/Release/literal"), hub.values)
        assertTrue(hub.references.isEmpty())

        openSecret()
        rule.onNodeWithText("Use a reference").performClick()
        rule.onNodeWithText("Use a reference").assertIsOn()
        rule.onNode(hasSetTextAction()).performTextReplacement("op://Development/Release/reference")
        rule.onNodeWithText("Save").performClick()
        rule.waitUntil { hub.references.isNotEmpty() }
        assertEquals(listOf("deploy_token" to "op://Development/Release/reference"), hub.references)
        assertEquals(1, hub.values.size)
    }

    @Test fun aGateChangeClearsAnOpenReferenceDraftAndRecovers() {
        val hub = Hub(detail.copy(secrets = listOf(savedSecret.copy(reference = null, set = false))), true, listOf(provider))
        show(hub)
        openSecret()
        rule.onNodeWithText("Use a reference").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("op://Development/Release/transient")
        hub.supported.value = false
        rule.waitUntil { rule.onAllNodesWithText("Use a reference").fetchSemanticsNodes().isEmpty() }
        rule.onNodeWithText("Save").assertIsNotEnabled()
        assertTrue(hub.references.isEmpty())

        hub.supported.value = true
        rule.waitUntil { rule.onAllNodesWithText("Use a reference").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Use a reference").assertIsOff().performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("op://Development/Release/recovered")
        rule.onNodeWithText("Save").performClick()
        rule.waitUntil { hub.references.isNotEmpty() }
        assertEquals(listOf("deploy_token" to "op://Development/Release/recovered"), hub.references)
    }

    @Test fun anOpenSheetRechecksTheProviderGateAtTheWrite() {
        val hub = Hub(detail.copy(secrets = listOf(savedSecret.copy(reference = null, set = false))), true, listOf(provider))
        show(hub)
        openSecret()
        rule.onNodeWithText("Use a reference").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("op://Development/Release/stale")
        // No UI invalidation arrives before the tap: only the write's fresh capability read sees the closed gate.
        hub.writeAllowed = false
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertTrue(hub.references.isEmpty())
        assertTrue(hub.values.isEmpty())
    }

    @Test fun aSlowWriteCheckLocksTheSubmittedReference() {
        val hub = Hub(detail.copy(secrets = listOf(savedSecret.copy(reference = null, set = false))), true, listOf(provider))
        show(hub)
        openSecret()
        rule.onNodeWithText("Use a reference").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("op://Development/Release/first")
        val gate = CompletableDeferred<Unit>()
        hub.detailGate = gate
        rule.onNodeWithText("Save").performClick()
        try {
            rule.onAllNodes(hasSetTextAction()).assertCountEquals(0)
            rule.onNodeWithText("Save").assertIsNotEnabled()
        } finally {
            gate.complete(Unit)
            hub.detailGate = null
        }
        rule.waitUntil { hub.references.isNotEmpty() }
        assertEquals(listOf("deploy_token" to "op://Development/Release/first"), hub.references)
    }

    @Test fun aSavedReferenceStaysReadableWhenCreationIsUnavailable() {
        val hub = Hub(detail, false, emptyList())
        show(hub)
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Deployment token"))
        rule.onNodeWithText("From Example Vault: op://Development/Release/token", substring = true).assertExists()
        rule.onNodeWithText("Release token", substring = true).assertExists()
        openSecret()
        rule.onAllNodesWithText("Use a reference").assertCountEquals(0)
    }

    @Test fun removingAnAlwaysRuleRequiresConfirmation() {
        val hub = Hub(detail, true, listOf(provider))
        show(hub)
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Always for Agents"))
        rule.onAllNodesWithText("Remove").onFirst().performScrollTo().performClick()
        rule.onNodeWithText("Remove this rule?").assertExists()
        assertTrue(hub.removed.isEmpty())
        rule.onNodeWithText("Cancel").performClick()
        assertTrue(hub.removed.isEmpty())
        rule.onAllNodesWithText("Remove").onFirst().performClick()
        rule.onAllNodesWithText("Remove").onLast().performClick()
        rule.waitUntil { hub.removed.isNotEmpty() }
        assertEquals(listOf("rule-release"), hub.removed)
    }

    @Test fun readOnlyRulesHaveNoRemoval() {
        val readOnly = Hub(detail.copy(readOnly = true), true, listOf(provider))
        show(readOnly)
        rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Always for Agents"))
        rule.onNodeWithText("Release Agent", substring = true).assertExists()
        rule.onAllNodes(hasText("Remove") and hasAnySibling(hasText("Release Agent", substring = true)), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun sharedProviderKeyFieldGatesTheSwitchAndKeepsStoredReferenceMetadataReadable() {
        var value by mutableStateOf("op://Development/Release/literal")
        var byReference by mutableStateOf(false)
        var available by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ProviderKeyField(value, { value = it }, byReference,
                    onByReferenceChange = if (available) ({ on -> byReference = on; value = "" }) else null,
                    stored = reference, label = "API key")
            }
        }
        rule.onNodeWithText("Use a reference").assertIsOff().performClick()
        assertEquals("", value)
        rule.onNodeWithText("Use a reference").assertIsOn()
        rule.onNodeWithText("From Example Vault · Release token", substring = true).assertExists()
        rule.runOnIdle { available = false }
        rule.onAllNodesWithText("Use a reference").assertCountEquals(0)
        rule.onNodeWithText("From Example Vault · Release token", substring = true).assertExists()
    }

    @Test fun capturesEveryStyleAndAppearance() = captureMatrix(large = false)

    /** A dialog has its own window density, so the largest-text pass also changes Robolectric's configuration. */
    @Test @Config(fontScale = 2f) fun capturesEveryStyleAndAppearanceAtLargestText() = captureMatrix(large = true)

    private fun captureMatrix(large: Boolean) {
        val hub = Hub(detail, true, listOf(provider))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scene by mutableStateOf("page")
        var run by mutableStateOf(0)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, if (large) 2f else 1f)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        key(run) {
                            if (scene == "page") ExtensionPage(hub, provider.id, onBack = {},
                                machines = flowOf(listOf(Machine("build", "Build computer", online = true))))
                            else Column(Modifier.width(400.dp).padding(Tokens.Space.l)) {
                                ProviderKeyField(reference.reference, {}, byReference = true, onByReferenceChange = {},
                                    stored = reference, label = "API key")
                            }
                        }
                    }
                }
            }
        }
        fun capture(name: String, dialog: Boolean = false) {
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            val node = if (dialog) rule.onAllNodes(isDialog()).onLast() else rule.onNodeWithTag("shot")
            val bitmap = node.captureToImage().asAndroidBitmap()
            assertTrue("$name drew an image", bitmap.width > 0 && bitmap.height > 0)
            File(out(), "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; scene = "page"; run++
            rule.waitUntil { rule.onAllNodesWithText("Deployment token").fetchSemanticsNodes().isNotEmpty() }
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}-${if (large) "text-200" else "text-100"}"
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Deployment token"))
            rule.onNodeWithText("From Example Vault: op://Development/Release/token", substring = true).assertExists()
            rule.onNodeWithText("Release token", substring = true).assertExists()
            capture("extension-saved-reference-$suffix")
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Always for Agents"))
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Release Agent on Build computer", substring = true))
            rule.onNodeWithText("Release Agent on Build computer", substring = true).assertExists()
            capture("extension-provider-details-$suffix")
            openSecret()
            rule.onNodeWithText("Use a reference").assertIsOn()
            capture("extension-reference-switch-$suffix", dialog = true)

            scene = "key"; run++
            rule.onNodeWithText("Use a reference").assertIsOn()
            rule.onNodeWithText("From Example Vault · Release token", substring = true).assertExists()
            capture("provider-key-reference-switch-$suffix")
        }
    }
}
