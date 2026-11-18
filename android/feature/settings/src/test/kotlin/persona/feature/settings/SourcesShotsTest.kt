package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File
import java.time.Duration
import java.time.Instant

/**
 * The data-source settings in each of the app's languages (docs/protocol/i18n.md): mail, a hub connector, Feishu, channels, model
 * configuration, a new model provider, Claude Code settings and a launcher's quota, in Glass light and dark (and the
 * other styles light). The data is made up and language-neutral, so English pages must show no Chinese at all. Written
 * as PNGs to `build/i18n-shots/` (or `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SourcesShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1600dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", mail = "邮箱", channels = "渠道", models = "模型配置", claude = "Claude Code 设置")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
    @Test fun english() = shots("en", mail = "Mail", channels = "Channels", models = "Model configuration", claude = "Claude Code settings")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1600dp-xhdpi")
    @Test fun japanese() = shots("ja", mail = "メール", channels = "チャネル", models = "モデル設定", claude = "Claude Codeの設定")

    private enum class Page { Mail, AddMail, Connector, Feishu, Channels, Channel, Models, Purpose, NewProvider, NewPreset, Claude, Quota }

    private fun shots(language: String, mail: String, channels: String, models: String, claude: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf(Page.Mail)
        var run by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1500.dp).background(PersonaTheme.palette.background)) {
                        when (page) {
                            Page.Mail -> MailAccountsPage(Connectors, onBack = {}, onAdd = {})
                            Page.AddMail -> AddMailAccountPage(Connectors, onBack = {}, onAdded = {}, start = MailAccountForm(MailAccountForm.Kind.IMAP))
                            Page.Connector -> HubConnectorPage(Connectors, "github", onBack = {})
                            Page.Feishu -> FeishuConnectorPage(Connectors, onBack = {}, onAgentModel = {})
                            Page.Channels -> ChannelsPage(Channels, onBack = {}, onChannel = {}, onMarketExtension = { _, _ -> })
                            Page.Channel -> ChannelPage(Channels, "chat", onBack = {}, onOpen = {})
                            Page.Models -> ModelConfigurationPage(Models, onBack = {}, onPurpose = { _, _ -> }, providers = Providers)
                            Page.Purpose -> ModelPurposePage(Models, "agent.new", onBack = {}, providers = Providers)
                            Page.NewProvider -> NewProviderPage(Providers, onBack = {}, onSaved = {})
                            Page.NewPreset -> NewProviderPage(Providers, onBack = {}, onSaved = {}, preferPreset = true)
                            Page.Claude -> ClaudeSettingsPage(Sessions, MachinesApi, onBack = {})
                            Page.Quota -> LazyColumn(Modifier.fillMaxWidth(), contentPadding = PaddingValues(Tokens.Space.l)) {
                                launcherQuotaItems(LauncherQuotaState(quota), { if (it == "mac") "Studio" else it }, onRefresh = {})
                                launcherQuotaItems(LauncherQuotaState(failed = ResText.of(R.string.settings_src_quota_read_failed, "timeout")), { it }, onRefresh = {}, key = "failed")
                            }
                        }
                    }
                }
            }
        }
        val looks = listOf(AppStyle.Glass to false, AppStyle.Glass to true) + AppStyle.entries.filter { it != AppStyle.Glass }.map { it to false }
        for ((s, d) in looks) {
            style = s; dark = d
            for (next in Page.entries) {
                page = next; run++
                rule.waitForIdle()
                when (next) {
                    Page.Mail -> rule.onAllNodesWithText(mail).assertAny()
                    Page.Channels -> rule.onAllNodesWithText(channels).assertAny()
                    Page.Models -> rule.onAllNodesWithText(models).assertAny()
                    Page.Claude -> rule.onAllNodesWithText(claude).assertAny()
                    else -> Unit
                }
                if (language == "en") assertNoChinese(next.name)
                capture("sources-${next.name.lowercase()}-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            }
        }
    }

    private fun androidx.compose.ui.test.SemanticsNodeInteractionCollection.assertAny() =
        assertTrue("missing on the page", fetchSemanticsNodes().isNotEmpty())

    /** Every word on an English page comes from values/ (the data here has no Chinese either). */
    private fun assertNoChinese(page: String) {
        val cjk = Regex("[　-〿぀-ヿ㐀-䶿一-鿿＀-￯]")
        val texts = mutableListOf<String>()
        fun walk(node: androidx.compose.ui.semantics.SemanticsNode) {
            node.config.getOrNull(SemanticsProperties.Text)?.forEach { texts += it.text }
            node.config.getOrNull(SemanticsProperties.EditableText)?.let { texts += it.text }
            node.config.getOrNull(SemanticsProperties.ContentDescription)?.let { texts += it }
            node.config.getOrNull(SemanticsProperties.StateDescription)?.let { texts += it }
            node.children.forEach(::walk)
        }
        walk(rule.onRoot(useUnmergedTree = true).fetchSemanticsNode())
        // The page chrome's own words (the back button) come from core/designsystem, translated in its own change.
        val chrome = setOf("返回")
        val chinese = texts.filter { cjk.containsMatchIn(it) && it !in chrome }
        assertTrue("$page shows Chinese in English: $chinese", chinese.isEmpty())
    }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    // ---- made-up data

    private val minutesAgo = { m: Long -> Instant.now().minusSeconds(m * 60) }

    private object Connectors : ConnectorsApiStub() {
        val github = HubConnector("github", "GitHub", status = "ok", account = "octo-example", enabled = true, archive = true, inbox = false,
            capabilities = listOf("archive", "inbox"))
        override suspend fun connectors(all: Boolean) = listOf(github)
        override suspend fun mailAccounts() = listOf(
            MailAccount("1", email = "me@example.com", provider = "gmail", auth = "oauth", status = "ok"),
            MailAccount("2", email = "work@example.com", provider = "imap", imapHost = "imap.example.com", status = "error", error = "auth failed"),
        )
        override suspend fun larkStatus() = LarkStatus(configured = true, connected = true, name = "Example Corp",
            lastEventAt = Instant.now().minusSeconds(180).epochSecond)
        override suspend fun larkAccounts() = LarkAccounts(listOf(
            LarkAccount("a", "Alex", tenant = "Example Corp", unread = 3), LarkAccount("b", "Sam", signedIn = false)), default = "a")
    }

    private object Channels : ChannelManagement {
        val chat = HubChannel("chat", "Example Chat", "im", "plugin", extension = "example-chat", installed = true, enabled = true, state = "connected",
            account = ChannelAccount(name = "alex"), bind = "qr", lastEventAt = Instant.now().minusSeconds(600).toString(),
            description = "Example Chat bot", target = ChannelTarget.Main,
            capabilities = ChannelCapabilities(imagesIn = true, voiceIn = true, format = "plain", maxText = 3500, asks = "text", typing = true),
            features = listOf(ChannelFeature("asks", "Questions", "Agents' questions come here", true)))
        private val list = listOf(
            HubChannel("app", "Persona", "app", "builtin", installed = true, enabled = true, state = "connected", bind = "none", readOnly = true,
                surfaces = listOf("iPhone", "Android", "Web")),
            chat,
            HubChannel("broken", "Example Team", "im", "plugin", installed = true, enabled = true, state = "error", error = "token expired", bind = "fields"),
            HubChannel("off", "Example Mention", "mention", "builtin", installed = true, enabled = false, state = "disabled", bind = "config", readOnly = true),
            HubChannel("rooms", "Example Rooms", "im", "market", extension = "example-rooms", market = "example", state = "not_installed", bind = "none"),
        )
        override val available = MutableStateFlow(true)
        override val conversations = flowOf(emptyList<Session>())
        override suspend fun channels() = list
        override suspend fun channel(id: String) = ChannelDetail(list.first { it.id == id }, ChannelWaiting(answers = 1, asks = 2),
            conversations = listOf(ChannelConversation("hub", "s1", "Weekend plans", Instant.now().minusSeconds(300).toString())))
        override suspend fun update(id: String, change: ChannelChange) = channel(id)
        override suspend fun bind(id: String, step: String?, values: Map<String, String>?) = BindPrompt("x", done = true)
        override suspend fun unbind(id: String) = channel(id)
        override suspend fun test(id: String) {}
    }

    private object Models : ModelConfiguration {
        val providers = listOf(ModelProvider("launcher.claude", "claude", model = "model-chat"), ModelProvider("launcher.old", "old-gateway", hidden = true),
            ModelProvider("api.fast", "fast-api", kind = "api", api = "messages"))
        val agent = ModelPurpose("agent.new", "New agent", "Agents", effective = ModelBinding("launcher.claude", "model-chat", "high"),
            inheritedFrom = "assistant", fields = listOf("provider", "model", "effort"), revision = 3)
        val purposes = listOf(agent, ModelPurpose("assistant", "Assistant", "Agents", effective = ModelBinding("launcher.claude", "model-chat")),
            ModelPurpose("inbox.triage", "Triage", "Inbox", kind = "api", effective = ModelBinding("api.fast"), status = "provider offline"),
            ModelPurpose("misc", "Misc"))
        override suspend fun catalog() = ModelCatalog(purposes, providers)
        override suspend fun purpose(id: String) = ModelPurposeDetail(purposes.first { it.id == id }, providers)
        override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) = ModelPurposeSave.Conflict
        override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = null
    }

    private object Providers : ProviderSetup {
        override suspend fun presets() = listOf(
            ProviderPreset("example-gateway/team", "example-gateway", "Team gateway", baseUrl = "https://gateway.example.com", model = "example-pro",
                models = listOf(PresetModel("example-pro"), PresetModel("example-lite"))),
            ProviderPreset("example-gateway/api", "example-gateway", "Gateway API", kind = "api", api = "responses", baseUrl = "https://api.example.com"),
        )
        override suspend fun add(provider: NewProvider) {}
    }

    private val studio = Machine("hub", name = "Studio", kind = "local", online = true)

    private val Sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(studio, Machine("mac", name = "Laptop", kind = "ssh", online = true)))
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() {}
    }

    private val MachinesApi = object : MachineManagement {
        override suspend fun claudeSettings(machineId: String) = listOf(
            ClaudeSetting("language", "Reply language", "The language Claude replies in", value = JsonPrimitive("English")),
            ClaudeSetting("verbose", "Verbose output", type = "bool", value = JsonPrimitive(true)),
            ClaudeSetting("model", "Model", type = "enum", options = listOf("opus", "sonnet"), default = JsonPrimitive("opus")),
        )
        override suspend fun setClaudeSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = claudeSettings(machineId)
        override suspend fun codexSettings(machineId: String) = claudeSettings(machineId)
        override suspend fun setCodexSetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = claudeSettings(machineId)
        override suspend fun agySettings(machineId: String) = claudeSettings(machineId)
        override suspend fun setAgySetting(machineId: String, key: String, value: kotlinx.serialization.json.JsonElement) = claudeSettings(machineId)
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
        override suspend fun launcherQuota(machineId: String, name: String, refresh: Boolean) = LauncherQuotaAnswer()
        override suspend fun reconnect(machineId: String) = Unit
        override suspend fun deploy(machineId: String) = Unit
        override suspend fun herdr(machineId: String): HerdrState = error("unused")
        override suspend fun startHerdr(machineId: String): HerdrState = error("unused")
        override suspend fun setEngine(machineId: String, engine: String) = Machine(machineId)
    }

    private fun later(hours: Long) = Instant.now().plus(Duration.ofHours(hours)).toString()

    private val quota = LauncherQuotaAnswer(machine = "mac", checkedAt = Instant.now().minusSeconds(180).toString(), quota = LauncherQuota(
        "example-gateway", gatewayName = "Example Gateway", keyName = "cc_main",
        budget = listOf(QuotaWindow("Monthly budget", 0.62, 496.0, 800.0, "usd", later(30))),
        pools = listOf(
            QuotaPool("Account A", "available", "available", listOf(QuotaWindow("Monthly", 0.64, 6401.0, 10000.0, "credits", later(11)))),
            QuotaPool("Account B", "cooldown", "cooling down", listOf(QuotaWindow("5 hours", 1.0, resetsAt = later(2), exhausted = true),
                QuotaWindow("Weekly", 0.77, 30.0, 100.0, "requests", later(70)))),
            QuotaPool("Provider A", "ok", windows = listOf(QuotaWindow("Weekly", 0.86, 86.0, 100.0, "percent", later(50))),
                models = listOf(QuotaModelWindows("example-flash", listOf(QuotaWindow("TPM", 0.02, 8400.0, 500000.0, "tokens"))))),
        ),
        usage = listOf(QuotaUsage("Today", 287, 210552323, 123.12), QuotaUsage("This week", 1, 1200)),
    ))
}
