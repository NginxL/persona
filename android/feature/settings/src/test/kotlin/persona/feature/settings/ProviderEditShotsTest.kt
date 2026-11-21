package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.flowOf
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

/**
 * Editing a model provider and its device scope, drawn on the JVM with Robolectric's native graphics in every style, light
 * and dark: the provider's page with its "Computers" summary, the edit form with "Advanced" open, "Computers" with the
 * per-computer keys open (one stored, one removed), a legacy provider's "Computers", the proxy page, and the list grouped
 * by harness. Written as PNGs to `build/provider-edit-shots/` (or `-Dpersona.shots=<dir>`). Made-up gateways and computers.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class ProviderEditShotsTest {
    @get:Rule val rule = createComposeRule()

    private val machines = listOf(
        Machine("mac", "Work Mac", online = true), Machine("box", "Build box", online = true),
        Machine("office", "Office PC", trust = "work"),
    )

    private val gateway = HubLauncher(
        "jv_gw", source = "persona", baseUrl = "https://gateway.example.com", model = "gw-pro", hasKey = true, hasHeaders = true, auth = "bearer",
        models = listOf(PresetModel("gw-pro", "GW Pro", contextWindow = 200000), PresetModel("gw-max", contextWindow = 1000000, efforts = listOf("high", "max"))),
        opusModel = "gw-max", effort = "high", timeoutMs = 600000, share = "selected", scopeMachines = listOf("mac", "box"), delivery = "push",
        ownService = "skip", keyMachines = listOf("mac"),
    )
    private val legacy = gateway.copy(name = "jv_old", share = "all", scopeMachines = emptyList(), ownService = "use", keyMachines = emptyList(), legacyScope = true)
    private val list = listOf(
        gateway, HubLauncher("cx_team", source = "persona", agent = "codex", baseUrl = "https://codex.example.com", model = "cx-1", hasKey = true, share = "all"),
        HubLauncher("triage_api", source = "persona", kind = "api", api = "responses", baseUrl = "https://api.example.com/v1", model = "api-mini", hasKey = true, share = "none"),
        HubLauncher("cc_local", source = "zshrc", model = "claude-sonnet", machines = listOf("mac")),
        HubLauncher("traecli", agent = "paseo", machines = listOf("mac")),
        HubLauncher("trae-cli", agent = "paseo", displayName = "Team CLI", machines = listOf("box")),
    )

    private val providers = object : ProviderSetup {
        override val launcherScope = flowOf(true)
        override suspend fun presets(): List<ProviderPreset> = emptyList()
        override suspend fun add(provider: NewProvider) {}
        override suspend fun launchers() = list
        override suspend fun launcher(name: String) = list.first { it.name == name }
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/provider-edit-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        Snapshot.sendApplyNotifications()
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun absentProviderUsesTheCliDisplayName() {
        rule.setContent {
            PersonaTheme { ProviderDetailPage(null, "trae-cli", machines, onBack = {}) }
        }
        rule.onNodeWithText("TRAE CLI").assertExists()
    }

    @Test fun drawsTheEditorTheComputersPageAndTheGroupedListInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("detail")
        // "Computers" as it is mid-edit: the stored key of one computer removed, a key typed for another.
        var scope by mutableStateOf(ScopeForm(Share.Selected, machines = listOf("mac", "box"), delivery = Delivery.Push, ownService = OwnService.Skip,
            keys = mapOf("box" to "sk-example"), remove = setOf("mac")))
        var legacyScope by mutableStateOf(scopeFrom(legacy)!!)
        var proxy by mutableStateOf(providerDraftOf(gateway).copy(proxyMode = ProxyMode.Manual, proxy = "http://127.0.0.1:7890", noProxy = "localhost, .example.com"))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1600.dp).background(PersonaTheme.palette.background)) {
                    when (page) {
                        "detail" -> ProviderDetailPage(providers, "jv_gw", machines, onBack = {})
                        "edit" -> NewProviderPage(providers, onBack = {}, onSaved = {}, existing = gateway, machines = machines)
                        "computers" -> ProviderComputersPage(scope, { scope = it }, machines, gateway, "launcher", null, defaultUse = false, onBack = {},
                            save = {}, canSave = true)
                        "legacy" -> ProviderComputersPage(legacyScope, { legacyScope = it }, machines, legacy, "launcher", null, defaultUse = false, onBack = {})
                        "proxy" -> ProviderProxyPage(proxy, true, { proxy = it }, onBack = {})
                        else -> ProvidersPage(providers, machines, onBack = {}, onOpen = {}, onAdd = {}, onImport = {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            page = "detail"; capture("provider-detail-$suffix")
            page = "edit"; rule.waitForIdle()
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Advanced"))
            rule.onNodeWithText("Advanced").performClick()
            // Bring what "Advanced" opened into view, not only its header.
            rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("Proxy"))
            capture("provider-edit-$suffix")
            page = "computers"; rule.waitForIdle()
            rule.onNodeWithText("Per-computer keys").performClick()
            capture("provider-computers-$suffix")
            page = "legacy"; capture("provider-computers-legacy-$suffix")
            page = "proxy"; capture("provider-proxy-$suffix")
            page = "list"; capture("providers-grouped-$suffix")
            rule.onNodeWithText("TRAE CLI").assertExists()
            rule.onNodeWithText("Team CLI").assertExists()
        }
    }
}
