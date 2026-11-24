package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.isRoot
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ServerEvent
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File

/**
 * The settings pages this lane translated (#2453) in each of the app's languages, Glass light and dark (the list also
 * in the other styles): the list (demo mode and paired), "Interface style", "Avatar", "Connection diagnostics", "Devices", "Notifications", "Checklist", "Floating capsule", "Translation",
 * "Session triage", "About" and "Phone data". Written as PNGs to `build/i18n-shots/` (or `-Dpersona.shots=<dir>`) to look at; text
 * still in Chinese on an English or Japanese page is printed.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SettingsCoreShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val longHubAddress = "https://hub.example.com:65535/this/is/a/very/long/hub/address/that/wraps/on/a/phone"

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h2600dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", settings = "设置", diagnostics = "连接", quiet = "免打扰时段")

    @Config(sdk = [35], qualifiers = "en-w400dp-h2600dp-xhdpi")
    @Test fun english() = shots("en", settings = "Settings", diagnostics = "Connection", quiet = "Quiet hours")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h2600dp-xhdpi")
    @Test fun japanese() = shots("ja", settings = "設定", diagnostics = "接続", quiet = "おやすみ時間")

    private class Live(sample: ConnectionDiagnostics) : HubConnection, ConnectionDiagnosticsSource {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val diagnostics = MutableStateFlow(sample)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = false
        override suspend fun readDiagnostics() = diagnostics.value
    }

    private class Devices : DeviceSettings {
        override suspend fun devices() = PairedDevices(
            listOf(
                PairedDevice("phone", "Pixel", "phone", "full", lastSeen = "2026-11-05T01:02:03Z", platform = "android"),
                PairedDevice("watch", "Watch", "watch", "answer", createdAt = "2026-11-01T08:00:00Z", platform = "watchos"),
            ),
            currentId = "phone",
            revoked = listOf(PairedDevice("old", "iPhone", "iphone", "full", platform = "ios", revokedAt = "2026-10-20T10:00:00Z")),
        )
        override suspend fun revoke(credentialId: String) = Unit
        override suspend fun preferences() = DeviceNotificationPreferences(longTurnSeconds = 120, doneDelaySeconds = 1,
            quiet = QuietHours(enabled = true, tz = "Pacific/Auckland"))
        override suspend fun savePreferences(preferences: DeviceNotificationPreferences) = preferences
    }

    private class Capsule : FloatingCapsuleSettings {
        override val mode = MutableStateFlow("active")
        override val permissionGranted = MutableStateFlow(false)
        override fun setMode(mode: String) { this.mode.value = mode }
        override fun refreshPermission() = Unit
        override fun requestPermission() = Unit
    }

    private class Translation : TranslationSettings {
        override val prefs = MutableStateFlow(TranslationPrefs(enabled = true, chats = listOf(TranslateChat("c1", "Design review"))))
        override fun update(transform: (TranslationPrefs) -> TranslationPrefs) { prefs.value = transform(prefs.value) }
    }

    private class Triage : AgentTriage {
        override suspend fun switches() = TriageSwitches(enabled = true, decide = true, closeout = true)
        override suspend fun put(value: TriageSwitches) = value
    }

    private fun shots(language: String, settings: String, diagnostics: String, quiet: String) {
        val resources = rule.activity.resources
        val demo = demoSettingsServices(resources)
        val sample = kotlinx.coroutines.runBlocking { demo.diagnostics!!.read() }.copy(
            fingerprint = "3f2a9c41d7e0b6a85c13f4e2d9b07a61", heartbeatSeconds = 20, foregroundHeartbeatSeconds = 20,
            backgroundHeartbeatSeconds = 240, pongTimeoutSeconds = 10, lastReceivedAt = java.time.Instant.now().toString(),
            candidates = demo.diagnostics!!.let { kotlinx.coroutines.runBlocking { it.read() } }.candidates.mapIndexed { i, c ->
                c.copy(probeDurationMillis = 40L + i, sampleCount = 3, checkedAt = java.time.Instant.now().toString())
            },
        )
        val history = object : SettingsDiagnostics by demo.diagnostics!! {
            override suspend fun lockScreenTest() = LockScreenTest("t1", "received", java.time.Instant.now().toString(),
                sentAt = java.time.Instant.now().toString(), receivedAt = java.time.Instant.now().toString(), notificationShown = true)
            override suspend fun disconnectSummary() = DeviceDisconnectSummary(3, 42.4)
        }
        val live = Live(sample)
        val paired = demo.copy(
            pairing = object : PairingService by demo.pairing { override val isPaired = MutableStateFlow(true) },
            connection = live, liveDiagnostics = live, endpoints = null, demo = false, diagnostics = history,
            deviceSettings = Devices(), hubAddress = longHubAddress, capsule = Capsule(),
            translation = Translation(), triage = Triage(),
        )
        // (name, services, page)
        val pages = listOf(
            Triple("list-demo", demo, "settings"), Triple("list", paired, "settings"), Triple("style", paired, "style"),
            Triple("avatar", demo, "persona"), Triple("diagnostics", paired, "diag"), Triple("devices", paired, "devices"),
            Triple("notify", paired, "notify"), Triple("ready", paired, "ready"), Triple("capsule", paired, "capsule"),
            Triple("translate", paired, "translate"), Triple("triage", paired, "triage"), Triple("about", paired, "about"),
            Triple("phone", paired, "phone"),
        )
        var page by mutableStateOf(pages.first())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            val context = LocalContext.current
            val store = remember { AppStyleStore(context) }
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalLanguageHost provides rule.activity, LocalAppStyleStore provides store) {
                    Box(Modifier.testTag("shot").width(400.dp).height(2500.dp).background(PersonaTheme.palette.background)) {
                        SettingsScreen(page.second, initialPage = page.third)
                    }
                }
            }
        }
        val cjk = Regex("[\\u3040-\\u30ff\\u3400-\\u9fff]")
        for (p in pages) {
            val variants = if (p.first == "list" || p.first == "triage") AppStyle.entries.flatMap { s -> listOf(s to false, s to true) }
                else listOf(AppStyle.Glass to false, AppStyle.Glass to true)
            for ((s, d) in variants) {
                page = p; style = s; dark = d
                rule.mainClock.advanceTimeBy(2_000)
                rule.waitForIdle()
                if (p.first.startsWith("list")) rule.onNodeWithText(settings).assertExists()
                if (p.first == "diagnostics") {
                    rule.onNodeWithTag("settings-hub-address-selection").assertExists()
                    val address = rule.onNodeWithText(longHubAddress, useUnmergedTree = true).assertExists()
                    val layouts = mutableListOf<TextLayoutResult>()
                    val _ = address.fetchSemanticsNode().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
                    val layout = layouts.single()
                    assertTrue("hub address must wrap in $s, dark=$d", layout.lineCount > 1)
                    assertFalse("hub address clipped in $s, dark=$d", layout.hasVisualOverflow)
                }
                if (p.first == "diagnostics") rule.onNodeWithText(diagnostics).assertExists()
                if (p.first == "notify") rule.onNodeWithText(quiet).assertExists()
                val texts = rule.onAllNodes(isRoot()).fetchSemanticsNodes().flatMap { root ->
                    fun walk(node: androidx.compose.ui.semantics.SemanticsNode): List<String> =
                        node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } + node.children.flatMap(::walk)
                    walk(root)
                }
                assertTrue("raw key on ${p.first}", texts.none { it.startsWith("settings_") })
                if (language != "zh" && s == AppStyle.Glass && !d) {
                    texts.filter { cjk.containsMatchIn(it) && (language != "ja") }.distinct()
                        .forEach { println("i18n-leftover[$language/${p.first}]: $it") }
                }
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                val name = "settings-core-$language-${p.first}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
                File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }
}
