package persona.feature.settings

import androidx.activity.ComponentActivity
import androidx.compose.runtime.Composable
import android.content.ComponentCallbacks
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme

/**
 * The welcome page leads straight into pairing (docs/android/android.md "Welcome page"): scan first, the public guide only when
 * the build has an address, the typed and command-line ways under "Other ways", and a phone that already has a hub is told
 * a scan replaces it. The guide address is a placeholder here, never a real one.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class PairingWelcomeTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class FakePairing(paired: Boolean = false) : PairingService {
        override val isPaired = MutableStateFlow(paired)
        override suspend fun pair(request: PairingRequest) = Unit
        override suspend fun unpair() = Unit
    }
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(emptyList<Machine>())
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }
    private val guide = "https://guide.example.com/start"

    private fun text(id: Int) = rule.activity.getString(id)
    private fun show(fontScale: Float = 1f, content: @Composable () -> Unit) = rule.setContent {
        PersonaTheme(reduceMotion = true) {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) { content() }
        }
    }
    private fun button(id: Int) = rule.onNode(hasText(text(id)) and hasClickAction())
    private fun tap(id: Int) { button(id).performClick(); rule.waitForIdle() }
    private fun back() { rule.runOnUiThread { rule.activity.onBackPressedDispatcher.onBackPressed() }; rule.waitForIdle() }
    private fun welcome() = rule.onNodeWithText(text(R.string.settings_pairing_welcome_title))

    @Test fun theFirstScreenOffersScanFirstAndFoldsTheOtherWays() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        welcome().assertExists()
        button(R.string.settings_pairing_scan).assertExists()
        button(R.string.settings_pairing_other_ways).assertExists()
        // None of the typed, password or command-line ways is on the first screen.
        for (id in listOf(R.string.settings_pairing_manual, R.string.settings_pairing_paste, R.string.settings_password_login_title)) {
            rule.onAllNodesWithText(text(id)).assertCountEquals(0)
        }
        rule.onNodeWithText("personad hub pair").assertDoesNotExist()
    }

    @Config(qualifiers = "en-w360dp-h480dp-mdpi")
    @Test fun scanAndAlternativeMethodsAreVisibleOnAShortPhone() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = guide) }
        button(R.string.settings_pairing_scan).assertIsDisplayed()
        button(R.string.settings_pairing_other_ways).assertIsDisplayed()
    }

    @Test fun withoutABuildAddressThereIsNoGuideLink() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        tap(R.string.settings_pairing_help)
        rule.onNodeWithText(text(R.string.settings_pairing_connect_body)).assertExists()
        rule.onNodeWithText(text(R.string.settings_pairing_welcome_guide)).assertDoesNotExist()
    }

    /** The root pairing page has no shell around it: the page's own link host opens the guide in the app's link card. */
    @Test fun theGuideOpensInTheAppsLinkCardAndNeverInTheSystemBrowser() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = guide) }
        rule.onNodeWithTag("chat_link_card").assertDoesNotExist()
        tap(R.string.settings_pairing_help)
        tap(R.string.settings_pairing_welcome_guide)
        rule.waitUntil(5_000) { rule.onAllNodesWithTag("chat_link_card").fetchSemanticsNodes().isNotEmpty() }
        assertNull(shadowOf(rule.activity).nextStartedActivity)
        // The welcome page is still under the card.
        welcome().assertExists()
    }

    /** Counts what the link host registers on the application (memory-pressure callbacks, taken with its WebView pool). */
    private class HostProbe(base: Context) : ContextWrapper(base) {
        var registered = 0
        private val app = object : ContextWrapper(base.applicationContext) {
            override fun registerComponentCallbacks(callback: ComponentCallbacks) { registered++; super.registerComponentCallbacks(callback) }
        }
        override fun getApplicationContext(): Context = app
    }

    private fun showProbed(probe: HostProbe, content: @Composable () -> Unit) = rule.setContent {
        PersonaTheme(reduceMotion = true) { CompositionLocalProvider(LocalContext provides probe) { content() } }
    }

    /** A page with a guide link is wrapped in exactly one link host, so the probe sees it (and the next tests mean something). */
    @Test fun aBuildWithAGuideAddressHostsTheWelcomePageOnce() {
        val probe = HostProbe(rule.activity)
        showProbed(probe) { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = guide) }
        rule.waitForIdle()
        assertEquals(1, probe.registered)
    }

    /** No guide address: no link host, so no WebView pool and no renderer warm-up on the first-run page. */
    @Test fun withoutABuildAddressTheWelcomePageHasNoLinkHostAndStartsNoWebView() {
        val probe = HostProbe(rule.activity)
        showProbed(probe) { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        rule.waitForIdle()
        welcome().assertExists()
        assertEquals(0, probe.registered)
    }

    @Test fun anAddressThatIsNotHttpsIsTreatedAsNoAddress() {
        val probe = HostProbe(rule.activity)
        showProbed(probe) { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = "http://guide.example.com/start") }
        rule.waitForIdle()
        rule.onNodeWithText(text(R.string.settings_pairing_welcome_guide)).assertDoesNotExist()
        assertEquals(0, probe.registered)
    }

    /** Settings › Pairing and Add hub are inside the shell, which has its own host: not wrapped again, guide or not. */
    @Test fun aPageInsideTheShellIsNotWrappedInASecondLinkHost() {
        val probe = HostProbe(rule.activity)
        showProbed(probe) { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, skipWelcome = true, gettingStartedUrl = guide) }
        rule.waitForIdle()
        assertEquals(0, probe.registered)
    }

    @Test fun otherWaysShowEveryOtherWayAndBackReturnsToTheWelcomePage() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        tap(R.string.settings_pairing_other_ways)
        welcome().assertDoesNotExist()
        for (id in listOf(R.string.settings_password_login_title, R.string.settings_pairing_scan, R.string.settings_pairing_paste, R.string.settings_pairing_manual)) {
            button(id).assertExists()
        }
        rule.onNodeWithText("personad hub pair").assertExists()
        back()
        welcome().assertExists()
        assertFalse(rule.activity.isFinishing)
    }

    @Test fun passwordSignInIsStillOneStepBehindOtherWays() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        tap(R.string.settings_pairing_other_ways)
        tap(R.string.settings_password_login_title)
        rule.onNodeWithText(text(R.string.settings_password_login_body)).assertExists()
        // Back from the password page is the connect choices, then the welcome page.
        back()
        button(R.string.settings_password_login_title).assertExists()
        back()
        welcome().assertExists()
    }

    @Test fun aPhoneWithAHubIsToldAScanReplacesIt() {
        show { PairingScreen(FakePairing(paired = true), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        rule.onNodeWithText(text(R.string.settings_pairing_replace_title)).assertExists()
        button(R.string.settings_pairing_scan).assertExists()
    }

    @Test fun aPhoneWithNoHubGetsNoReplacementNotice() {
        show { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = null) }
        rule.onNodeWithText(text(R.string.settings_pairing_replace_title)).assertDoesNotExist()
    }

    @Test fun aPairingLinkSkipsTheWelcomePageAndBackLeadsToTheConnectChoices() {
        val link = "persona://pair?url=http%3A%2F%2F127.0.0.1%3A18760&token=mock"
        show { PairingScreen(FakePairing(), sessions, initialLink = link, onReady = {}, onDiagnostics = {}, gettingStartedUrl = guide) }
        rule.waitForIdle()
        welcome().assertDoesNotExist()
        rule.onAllNodesWithText(text(R.string.settings_pairing_confirm)).assertCountAtLeast()
        back()
        button(R.string.settings_pairing_paste).assertExists()
        welcome().assertDoesNotExist()
    }

    @Test fun addingAnotherHubStartsAtTheConnectChoicesNotTheWelcomePage() {
        show { PairingScreen(FakePairing(paired = true), sessions, onReady = {}, onDiagnostics = {}, skipWelcome = true, gettingStartedUrl = guide) }
        welcome().assertDoesNotExist()
        rule.onNodeWithText(text(R.string.settings_pairing_welcome_guide)).assertDoesNotExist()
    }

    /** The three buttons of the first screen stay on one line at twice the font size, in each language. */
    private fun welcomeButtonsStayOnOneLine() {
        show(fontScale = 2f) { PairingScreen(FakePairing(), sessions, onReady = {}, onDiagnostics = {}, gettingStartedUrl = guide) }
        for (id in listOf(R.string.settings_pairing_scan, R.string.settings_pairing_help, R.string.settings_pairing_other_ways)) {
            val results = mutableListOf<TextLayoutResult>()
            val node = rule.onNodeWithText(text(id), useUnmergedTree = true).fetchSemanticsNode()
            val _ = node.config[SemanticsActions.GetTextLayoutResult].action?.invoke(results)
            assertEquals(text(id), 1, results.single().lineCount)
        }
    }

    @Config(qualifiers = "en-w400dp-h4000dp-xhdpi")
    @Test fun welcomeButtonsStayOnOneLineAtALargeFontInEnglish() = welcomeButtonsStayOnOneLine()

    @Config(qualifiers = "zh-w400dp-h4000dp-xhdpi")
    @Test fun welcomeButtonsStayOnOneLineAtALargeFontInChinese() = welcomeButtonsStayOnOneLine()

    @Config(qualifiers = "ja-w400dp-h4000dp-xhdpi")
    @Test fun welcomeButtonsStayOnOneLineAtALargeFontInJapanese() = welcomeButtonsStayOnOneLine()

    @Test fun thisBuildsAddressIsNullWithoutManifestMetaData() {
        assertNull(GettingStarted.configured(rule.activity))
    }
}

private fun androidx.compose.ui.test.SemanticsNodeInteractionCollection.assertCountAtLeast() {
    check(fetchSemanticsNodes().isNotEmpty()) { "expected at least one node" }
}
