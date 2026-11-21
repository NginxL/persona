package persona.platform.phonetools

import android.Manifest
import android.app.Application
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.PhoneToolCall
import persona.core.contracts.api.PhoneToolResult
import persona.core.contracts.api.PhoneToolSpec
import persona.core.contracts.api.PhoneToolsTransport
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.lang.reflect.Proxy

/**
 * Settings › Phone data and call / calendar confirmations in each language (docs/protocol/i18n.md), Glass light, dark
 * and large text. PNGs go to `build/i18n-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class PhoneToolsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    @After fun stop() = scope.cancel()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1800dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", "手机数据", "已授权", "确认手机操作", "允许", "电话")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
    @Test fun english() = shots("en", "Phone data", "Allowed", "Confirm phone action", "Allow", "Phone calls")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1800dp-xhdpi")
    @Test fun japanese() = shots("ja", "スマートフォンのデータ", "許可済み", "スマートフォンの操作を確認", "許可", "電話")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1800dp-xhdpi")
    @Test fun simplifiedChineseLargeCall() = largeCall("zh", "手机数据", "已授权", "确认手机操作", "允许", "电话")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
    @Test fun englishLargeCall() = largeCall("en", "Phone data", "Allowed", "Confirm phone action", "Allow", "Phone calls")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1800dp-xhdpi")
    @Test fun japaneseLargeCall() = largeCall("ja", "スマートフォンのデータ", "許可済み", "スマートフォンの操作を確認", "許可", "電話")

    private fun largeCall(language: String, title: String, granted: String, confirmTitle: String, allow: String, calls: String) {
        // A dialog has its own window: set the configuration before mounting, not only the composition's density.
        RuntimeEnvironment.setFontScale(2f)
        shots(language, title, granted, confirmTitle, allow, calls, largeCallOnly = true)
    }

    private fun shots(language: String, title: String, granted: String, confirmTitle: String, allow: String, calls: String,
        largeCallOnly: Boolean = false) {
        val app = ApplicationProvider.getApplicationContext<Application>()
        // A mix of states: contacts allowed, location only approximate, calendar read only, the rest not asked yet.
        Shadows.shadowOf(app).grantPermissions(Manifest.permission.READ_CONTACTS, Manifest.permission.WRITE_CONTACTS, Manifest.permission.ACCESS_COARSE_LOCATION,
            Manifest.permission.READ_CALENDAR)
        val connection = Proxy.newProxyInstance(HubConnection::class.java.classLoader, arrayOf(HubConnection::class.java)) { _, method, _ ->
            when (method.name) {
                "getState" -> MutableStateFlow(ConnectionState.WaitingForNetwork)
                "getEvents" -> emptyFlow<Any>()
                else -> error("not used: ${method.name}")
            }
        } as HubConnection
        val transport = object : PhoneToolsTransport {
            override val calls = emptyFlow<PhoneToolCall>()
            override fun discardPending() = Unit
            override fun register(deviceId: String, deviceName: String, simulator: Boolean, tools: List<PhoneToolSpec>) = true
            override fun result(callId: String, result: PhoneToolResult) = true
        }
        val runtime = PhoneToolsRuntime(app, HubScope("hub-$language", "one"), "device", connection, transport, scope)
        runBlocking { listOf(PhoneSource.Contacts, PhoneSource.Calendar, PhoneSource.Location).forEach { runtime.settings.setEnabled(it, true) } }
        val navigator = object : Navigator {
            override fun navigate(key: NavKey) = Unit
            override fun back() = Unit
            override fun selectTab(tab: AppTab) = Unit
        }
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1760.dp).background(PersonaTheme.palette.background)) {
                        PhoneToolsScreen(runtime, navigator)
                    }
                    PhoneConfirmationHost(runtime.confirmations)
                }
            }
        }
        if (!largeCallOnly) {
            val appearances = listOf(Triple("light", false, 1f), Triple("dark", true, 1f), Triple("light-xl", false, 2f))
            for ((suffix, isDark, scale) in appearances) {
                dark = isDark; fontScale = scale
                rule.waitForIdle()
                rule.onNodeWithText(title).assertExists()
                rule.onNodeWithText(granted).assertExists()
                capture(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "phone-tools-$language-$suffix")
                rule.onNode(hasScrollAction()).performScrollToNode(hasText(calls))
                rule.onNodeWithText(calls).assertExists()
                capture(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap(), "phone-tools-calls-$language-$suffix")
            }
            val event = CalendarArguments.parse(kotlinx.serialization.json.buildJsonObject {
                put("title", kotlinx.serialization.json.JsonPrimitive("Weekly sync"))
                put("start", kotlinx.serialization.json.JsonPrimitive("2026-11-06T15:00:00+08:00"))
                put("end", kotlinx.serialization.json.JsonPrimitive("2026-11-06T15:30:00+08:00"))
                put("location", kotlinx.serialization.json.JsonPrimitive("3F"))
            })
            runtime.setForeground(true)
            val asking = scope.launch { runtime.confirmations.confirm { res -> event.summary(res) } }
            for (d in listOf(false, true)) {
                dark = d; fontScale = 1f
                rule.waitForIdle()
                rule.onNodeWithText(confirmTitle).assertExists()
                rule.onNode(hasText(allow) and hasAnyAncestor(isDialog())).assertExists()
                capture(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "phone-confirm-$language-${if (d) "dark" else "light"}")
            }
            asking.cancel()
            rule.waitForIdle()
        }
        val call = CallTool.confirmation(kotlinx.serialization.json.buildJsonObject {
            put("number", kotlinx.serialization.json.JsonPrimitive("+1 (555) 010-0199"))
            put("name", kotlinx.serialization.json.JsonPrimitive("Sam"))
        })
        val callAsking = scope.launch { runtime.confirmations.confirm(call) }
        val callAppearances = if (largeCallOnly) listOf(Triple("light-xl", false, 1f))
            else listOf(Triple("light", false, 1f), Triple("dark", true, 1f))
        for ((suffix, isDark, scale) in callAppearances) {
            dark = isDark; fontScale = scale
            rule.waitForIdle()
            rule.onNodeWithText(confirmTitle).assertExists()
            rule.onNode(hasText(allow) and hasAnyAncestor(isDialog())).assertExists()
            capture(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "phone-call-confirm-$language-$suffix")
        }
        callAsking.cancel()
        rule.waitForIdle()
        runtime.close()
    }

    private fun capture(bitmap: Bitmap, name: String) =
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
}
