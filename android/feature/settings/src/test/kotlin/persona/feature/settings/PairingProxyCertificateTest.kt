package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.PairingAttemptStep
import persona.core.contracts.api.PairingException
import persona.core.contracts.api.PairingFailure
import persona.core.contracts.api.PairingRequest
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.io.IOException

/**
 * A pairing whose only certificate failure was the link's proxied address (#2622): the page names that address and says
 * it is the proxy's certificate, not a reinstalled hub, and offers to try again rather than to scan a new code. Every
 * style, light and dark (`pairing-proxy-certificate-*`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class PairingProxyCertificateTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val address = "https://hub.example.com"
    private val link = "persona://pair?url=https%3A%2F%2Fhub.example.com&token=example-token&hub=example-hub" +
        "&fp=${"A".repeat(43)}&tls=19443"

    private val pairing = object : PairingService {
        override val isPaired = MutableStateFlow(false)
        override suspend fun pair(request: PairingRequest) = pair(request) {}
        // as the app's: a pinned link goes through the progress overload, which probes and fails at the proxy
        override suspend fun pair(request: PairingRequest, progress: (persona.core.contracts.api.PairingProgress) -> Unit) {
            throw PairingException(PairingFailure.ProxyCertificate, address).also { it.initCause(IOException("handshake")) }
        }
        override suspend fun unpair() = Unit
    }
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(emptyList<Machine>())
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }

    private fun text(id: Int, vararg args: Any) = rule.activity.getString(id, *args)

    @Test fun theFailureNamesTheProxiedAddressInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    key(style, dark) { PairingScreen(pairing, sessions, initialLink = link, onReady = {}, onDiagnostics = {}) }
                }
            }
        }
        val named = text(R.string.settings_pairing_failure_proxy_certificate, address)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNode(hasText(text(R.string.settings_pairing_confirm)) and hasClickAction()).performClick()
            rule.waitUntil(5_000) { rule.onAllNodesWithText(text(R.string.settings_pairing_failed_title)).fetchSemanticsNodes().isNotEmpty() }
            assertTrue("$s: the address isn't named", rule.onAllNodesWithText(named).fetchSemanticsNodes().isNotEmpty())
            // not the reinstalled-hub advice, and Try again rather than Scan
            assertTrue(rule.onAllNodesWithText(text(R.string.settings_pairing_failure_certificate)).fetchSemanticsNodes().isEmpty())
            assertTrue(rule.onAllNodesWithText(text(R.string.settings_pairing_recovery_retry)).fetchSemanticsNodes().isNotEmpty())
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "pairing-proxy-certificate-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Test fun theAddressIsReadFromTheErrorChain() {
        val failure = PairingException(PairingFailure.ProxyCertificate, address)
        assertEquals(address, pairingFailureAddress(IOException("wrapped", failure)))
        assertNull(pairingFailureAddress(PairingException(PairingFailure.Certificate)))
        assertEquals(PairingFailure.ProxyCertificate, pairingFailure(failure))
        assertEquals(PairingRecovery.Retry, PairingFailure.ProxyCertificate.recovery())
        assertEquals(PairingAttemptStep.Tls, pairingStep(failure))
        assertFalse(PairingFailure.ProxyCertificate.recovery() == PairingRecovery.Scan)
    }

    @Test
    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
    fun cutShortTlsHandshakeShowsReachAddressStepAndTryAgainInLightDarkAndLargestText() {
        val cutShortPairing = object : PairingService {
            override val isPaired = MutableStateFlow(false)
            override suspend fun pair(request: PairingRequest) = pair(request) {}
            override suspend fun pair(request: PairingRequest, progress: (persona.core.contracts.api.PairingProgress) -> Unit) {
                progress(persona.core.contracts.api.PairingProgress(
                    persona.core.contracts.api.PairingStage.Probe,
                    candidates = listOf(persona.core.contracts.api.ConnectionCandidate(
                        address, "lan", persona.core.contracts.CoreText.plain("Connection failed")
                    )),
                ))
                val eof = javax.net.ssl.SSLHandshakeException("Remote host terminated the handshake").apply {
                    initCause(java.io.EOFException("SSL peer shut down incorrectly"))
                }
                val hubFailure = persona.core.hub.HubFailure.from(eof)
                throw PairingException(pairingFailure(hubFailure)).apply { initCause(hubFailure) }
            }
            override suspend fun unpair() = Unit
        }
        var dark by mutableStateOf(false)
        var large by mutableStateOf(false)
        rule.setContent {
            val scale = if (large) 2f else 1f
            val configuration = android.content.res.Configuration(androidx.compose.ui.platform.LocalConfiguration.current).apply {
                fontScale = scale
                uiMode = (uiMode and android.content.res.Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) android.content.res.Configuration.UI_MODE_NIGHT_YES else android.content.res.Configuration.UI_MODE_NIGHT_NO
            }
            val density = androidx.compose.ui.unit.Density(androidx.compose.ui.platform.LocalDensity.current.density, scale)
            androidx.compose.runtime.CompositionLocalProvider(
                androidx.compose.ui.platform.LocalConfiguration provides configuration,
                androidx.compose.ui.platform.LocalDensity provides density,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    Box(Modifier.testTag("shot").width(400.dp).height(960.dp).background(PersonaTheme.palette.background)) {
                        key(dark, large) { PairingScreen(cutShortPairing, sessions, initialLink = link, onReady = {}, onDiagnostics = {}) }
                    }
                }
            }
        }
        val expectedStep = text(R.string.settings_pairing_stopped_at, text(R.string.settings_pairing_step_network))
        val wrongStep = text(R.string.settings_pairing_stopped_at, text(R.string.settings_pairing_step_tls))
        for (big in listOf(false, true)) for (d in listOf(false, true)) {
            dark = d; large = big
            rule.waitForIdle()
            rule.onNode(hasText(text(R.string.settings_pairing_confirm)) and hasClickAction()).performClick()
            rule.waitUntil(5_000) { rule.onAllNodesWithText(text(R.string.settings_pairing_failed_title)).fetchSemanticsNodes().isNotEmpty() }
            assertTrue(rule.onAllNodesWithText(expectedStep).fetchSemanticsNodes().isNotEmpty())
            assertTrue(rule.onAllNodesWithText(wrongStep).fetchSemanticsNodes().isEmpty())
            assertTrue(rule.onAllNodesWithText(text(R.string.settings_pairing_recovery_retry)).fetchSemanticsNodes().isNotEmpty())
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "pairing-cut-short-tls-${if (d) "dark" else "light"}-${if (big) "large" else "default"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
