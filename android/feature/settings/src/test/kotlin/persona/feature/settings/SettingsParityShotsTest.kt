package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubDesktop
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File
import java.time.Instant

/**
 * The settings pages of the and-settings parity batch (#3048), drawn on the JVM with Robolectric's native graphics in
 * every style, light and dark: the settings home ("Model providers", "Assistant", "Storage", the signed-in Mac), "Interface style" with "Theme color" and
 * "Appearance", "Notifications" with "Mute phone", the signed-in Macs, "Model providers" and one provider, "Import from ~/.zshrc", "Storage", and "Claude Code settings" with
 * "Blocked config directories". Written to `build/parity-shots/` (or `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2100dp-xhdpi")
class SettingsParityShotsTest {
    @get:Rule val rule = createComposeRule()

    private val studio = Machine("hub", name = "Studio", kind = "local", online = true)
    private val laptop = Machine("mac", name = "Laptop", kind = "ssh", online = true)

    private val desktops = MutableStateFlow(listOf(HubDesktop("d1", "MacBook Pro", Instant.now().minusSeconds(3600).toString())))

    private class Devices : DeviceSettings {
        var prefs = DeviceNotificationPreferences(longTurnSeconds = 120, doneDelaySeconds = 1, muteWhileDesktop = true)
        override suspend fun devices() = PairedDevices(emptyList(), null)
        override suspend fun revoke(credentialId: String) = Unit
        override suspend fun preferences() = prefs
        override suspend fun savePreferences(preferences: DeviceNotificationPreferences) = preferences.also { prefs = it }
    }

    private object Providers : ProviderSetup {
        val list = listOf(
            HubLauncher("team", source = "persona", baseUrl = "https://gateway.example.com/v1", model = "example-pro", hasKey = true, auth = "bearer",
                models = listOf(PresetModel("example-pro"), PresetModel("example-lite"))),
            HubLauncher("api-direct", source = "persona", kind = "api", api = "responses", baseUrl = "https://api.example.com", model = "example-mini", hasKey = true),
            HubLauncher("cc_work", source = "zshrc", baseUrl = "https://proxy.example.com", model = "example-max", machines = listOf("hub", "mac"),
                definitions = mapOf("hub" to "cc_work() {\n  ANTHROPIC_BASE_URL=https://proxy.example.com \\\n  ANTHROPIC_AUTH_TOKEN=••• claude \"$@\"\n}")),
            HubLauncher("cc_old", source = "zshrc", model = "example-legacy", machines = listOf("mac"), hidden = true),
        )
        override suspend fun presets() = emptyList<ProviderPreset>()
        override suspend fun add(provider: NewProvider) {}
        override suspend fun launchers() = list
        override suspend fun launcher(name: String) = list.first { it.name == name }
        override suspend fun check(name: String, machine: String?) =
            LauncherCheckAnswer(machine, LauncherCheck(ok = machine != "mac", model = "example-max", answer = "OK", millis = 840,
                error = "gateway answered 401".takeIf { machine == "mac" }))
        override suspend fun importPreview(machine: String, alias: String?) = listOf(
            ImportedProvider("proxy", "https://proxy.example.com", "example-max", hasKey = true, aliases = listOf("cc_work", "cc_fast")),
            ImportedProvider("team", "https://gateway.example.com/v1", "example-pro", hasKey = true, aliases = listOf("cc_team"), existing = "team"),
        )
    }

    private val machinesApi = object : MachineManagement {
        override suspend fun claudeSettings(machineId: String) = listOf(
            ClaudeSetting("verbose", "详细输出", type = "bool", value = JsonPrimitive(true)),
        )
        override suspend fun setClaudeSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = claudeSettings(machineId)
        override suspend fun codexSettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setCodexSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = emptyList<ClaudeSetting>()
        override suspend fun agySettings(machineId: String) = emptyList<ClaudeSetting>()
        override suspend fun setAgySetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = emptyList<ClaudeSetting>()
        override suspend fun claudeDirs(machineId: String) = ClaudeDirsState(
            dirs = listOf(
                ClaudeDir("/home/me/.claude", isDefault = true, source = "default", running = 2, sessions = 48),
                ClaudeDir("/home/me/.claude-work", source = "config", sessions = 12),
                ClaudeDir("/home/me/.automation/nightly", blocked = true, blockedBy = "~/.automation/*", sessions = 230),
            ),
            blocked = listOf("~/.automation/*", "~/.old-bot"), home = "/home/me", live = true)
        override suspend fun setBlockedClaudeDirs(machineId: String, blocked: List<String>) = claudeDirs(machineId)
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) = LauncherQuotaAnswer()
        override suspend fun reconnect(machineId: String) = Unit
        override suspend fun deploy(machineId: String) = Unit
        override suspend fun herdr(machineId: String): HerdrState = error("unused")
        override suspend fun startHerdr(machineId: String): HerdrState = error("unused")
        override suspend fun setEngine(machineId: String, engine: String) = Machine(machineId)
    }

    private val services = SettingsServices(
        pairing = object : PairingService {
            override val isPaired = MutableStateFlow(true)
            override suspend fun pair(request: PairingRequest) = error("not used")
            override suspend fun unpair() = error("not used")
        },
        connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = error("not used")
            override fun send(message: JsonObject) = false
        },
        sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(studio, laptop))
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        },
        updates = object : AppUpdates { override suspend fun check(): AppUpdate? = null },
        deviceSettings = Devices(),
        machines = machinesApi,
        providers = Providers,
        desktops = desktops,
    )

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/parity-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private enum class Shot(val height: Int) { Home(2300), Appearance(2100), Notify(1500), Desktops(900), Providers(1300), Provider(1500), Import(900), Storage(1100), Claude(1100) }

    @Test fun drawsTheParityPagesInEveryStyle() {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val store = AppStyleStore(context)
        // Something to measure on "Storage".
        fun seed(path: String, bytes: Int) = File(context.cacheDir, path).apply { parentFile!!.mkdirs(); writeBytes(ByteArray(bytes)) }
        seed("hub-data/h/1/images/a.png", 3_400_000); seed("hub-data/h/1/inbox-media/b.jpg", 1_200_000)
        seed("hub-data/h/1/items.db", 2_100_000); seed("markdown-render/c.png", 400_000)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shot by mutableStateOf(Shot.Home)
        var run by mutableIntStateOf(0)
        rule.setContent {
            val color by store.color.collectAsState()
            CompositionLocalProvider(LocalAppStyleStore provides store, LocalThemeAccent provides ThemeColors.accent(color, null)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    key(run) {
                        Box(Modifier.testTag("shot").width(400.dp).height(shot.height.dp).background(PersonaTheme.palette.background)) {
                            when (shot) {
                                Shot.Home -> SettingsScreen(services, onOpen = {}, onNavigate = {})
                                Shot.Appearance -> SettingsScreen(services, initialPage = "style", onBack = {})
                                Shot.Notify -> SettingsScreen(services, initialPage = "notify", onBack = {})
                                Shot.Desktops -> SettingsScreen(services, initialPage = "desktops", onBack = {})
                                Shot.Providers -> SettingsScreen(services, initialPage = "providers", onBack = {})
                                Shot.Provider -> ProviderDetailPage(Providers, "cc_work", listOf(studio, laptop), onBack = {})
                                Shot.Import -> ProviderImportPage(Providers, listOf(studio, laptop), onBack = {}, onDone = {})
                                Shot.Storage -> SettingsScreen(services, initialPage = "storage", onBack = {})
                                Shot.Claude -> SettingsScreen(services, initialPage = "claude", onBack = {})
                            }
                        }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            for (next in Shot.entries) {
                shot = next; run++
                rule.waitForIdle()
                when (next) {
                    Shot.Provider -> rule.onNode(androidx.compose.ui.test.hasText("测试") and androidx.compose.ui.test.hasClickAction()).performClick()
                    Shot.Import -> rule.onNode(androidx.compose.ui.test.hasText("读取 ~/.zshrc") and androidx.compose.ui.test.hasClickAction()).performClick()
                    else -> Unit
                }
                capture("parity-${next.name.lowercase()}-$suffix")
            }
        }
        // A fixed accent changes what is drawn, and goes back to the style's own.
        store.setColor(ThemeColor.Coral); store.setScheme(ColorSchemeChoice.Dark)
        style = AppStyle.Glass; dark = false; shot = Shot.Appearance; run++
        capture("parity-appearance-coral")
        store.setColor(ThemeColor.Style); store.setScheme(ColorSchemeChoice.System)
    }
}
