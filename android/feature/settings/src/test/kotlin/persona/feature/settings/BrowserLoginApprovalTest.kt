package persona.feature.settings

import androidx.activity.ComponentActivity
import androidx.compose.runtime.key
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.IOException
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.PersonaTheme
import persona.core.hub.HubError
import persona.core.hub.HubFailure

/** Browser sign-in's number step, cancellation and polling at the real Compose boundary (#10319). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class BrowserLoginApprovalTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val request = BrowserLoginRequest("a".repeat(40) + "b-_", "hub-1")
    private var dismissed = 0

    private fun show(hub: ScriptedBrowserLoginHub) {
        dismissed = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                key(hub) { BrowserLoginApproval(hub, request, rememberCoroutineScope()) { dismissed++ } }
            }
        }
    }

    private fun text(id: Int) = rule.activity.getString(id)
    private fun allow() = text(R.string.settings_browser_login_allow)
    private fun cancel() = text(R.string.settings_browser_login_cancel)

    private fun waitForRequest() {
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Chrome · Mac", substring = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(hasText(allow()) and hasClickAction()).assertIsEnabled()
    }

    private fun tapAllow() {
        rule.onNode(hasText(allow()) and hasClickAction()).performClick()
    }

    private fun tapCancel() {
        rule.onNode(hasText(cancel()) and hasClickAction()).assertIsEnabled().performClick()
        rule.waitUntil(5_000) { dismissed == 1 }
    }

    @Test fun allowShowsTheNumberWithoutDismissingAndCancelStillDenies() {
        var allowed = false
        val hub = ScriptedBrowserLoginHub { _, allow ->
            when (allow) {
                true -> {
                    allowed = true
                    browserLoginDetails(state = "confirm", number = "47")
                }
                false -> browserLoginDetails(state = "denied")
                null -> browserLoginDetails(state = if (allowed) "confirm" else "scanned", number = if (allowed) "47" else null)
            }
        }
        show(hub)
        waitForRequest()
        tapAllow()

        rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }
        assertEquals("Allow must move to the number, not close the dialog", 0, dismissed)
        rule.onNodeWithText("Type this number in the browser.", substring = true).assertExists()
        rule.onNodeWithText("Don't tell anyone this number.", substring = true).assertExists()
        rule.onNodeWithText("47").assertExists()
        assertEquals(listOf(null, true), hub.calls.take(2).map { it.allow })
        assertEquals(listOf(request.id, request.hub), hub.calls.first().let { listOf(it.id, it.hub) })

        tapCancel()
        rule.waitUntil(5_000) { hub.calls.any { it.allow == false } }
        assertEquals("only the explicit Allow approves", 1, hub.calls.count { it.allow == true })
        assertEquals(1, dismissed)
    }

    @Test fun olderHubWithoutNumberMatchKeepsSuccessfulAllowDismissal() {
        val hub = ScriptedBrowserLoginHub { _, _ -> browserLoginDetails(state = null, network = null) }
        show(hub)
        waitForRequest()
        tapAllow()

        rule.waitUntil(5_000) { dismissed == 1 }
        assertEquals(listOf(null, true), hub.calls.map { it.allow })
        rule.onAllNodesWithText("47").assertCountEquals(0)
    }

    @Test fun cancellationIsAvailableBeforeInspectionAnswers() {
        val inspectionStarted = CompletableDeferred<Unit>()
        val hub = ScriptedBrowserLoginHub { _, allow ->
            if (allow == null) {
                inspectionStarted.complete(Unit)
                kotlinx.coroutines.awaitCancellation()
            }
            browserLoginDetails(state = "denied")
        }
        show(hub)
        rule.waitUntil(5_000) { inspectionStarted.isCompleted }
        rule.onNode(hasText(allow()) and hasClickAction()).assertExists()
        tapCancel()

        assertTrue(hub.calls.any { it.allow == false })
        assertFalse(hub.calls.any { it.allow == true })
    }

    @Test fun cancellationIsAvailableWhileAllowWaitsForTheNumber() {
        val allowStarted = CompletableDeferred<Unit>()
        val finishAllow = CompletableDeferred<Unit>()
        val hub = ScriptedBrowserLoginHub { _, allow ->
            when (allow) {
                true -> {
                    allowStarted.complete(Unit)
                    finishAllow.await()
                    browserLoginDetails(state = "confirm", number = "47")
                }
                false -> browserLoginDetails(state = "denied")
                null -> browserLoginDetails()
            }
        }
        show(hub)
        waitForRequest()
        tapAllow()
        rule.waitUntil(5_000) { allowStarted.isCompleted }
        try {
            tapCancel()
            assertTrue(hub.calls.any { it.allow == false })
            assertEquals(1, dismissed)
        } finally {
            finishAllow.complete(Unit)
        }
    }

    @Test fun inspectingConfirmRestoresTheNumberWithoutApprovingAgain() {
        val hub = ScriptedBrowserLoginHub { _, _ -> browserLoginDetails(state = "confirm", number = "47") }
        show(hub)

        rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }
        assertEquals(0, dismissed)
        assertEquals(null, hub.calls.first().allow)
        assertFalse("recovering a number must not send another Allow", hub.calls.any { it.allow == true })
        rule.onNode(hasText(cancel()) and hasClickAction()).assertIsEnabled()
    }

    @Test fun pollingKeepsConfirmRetriesALostReplyAndThenShowsApproved() {
        var allowed = false
        var polls = 0
        val hub = ScriptedBrowserLoginHub { _, allow ->
            when (allow) {
                true -> {
                    allowed = true
                    browserLoginDetails(state = "confirm", number = "47")
                }
                false -> browserLoginDetails(state = "denied")
                null -> if (!allowed) browserLoginDetails() else {
                    polls++
                    when (polls) {
                        1 -> browserLoginDetails(state = "confirm", number = "47")
                        2 -> throw IOException("one lost polling reply")
                        else -> browserLoginDetails(state = "approved", number = null)
                    }
                }
            }
        }
        show(hub)
        waitForRequest()
        tapAllow()
        rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }

        rule.waitUntil(10_000) { rule.onAllNodesWithText("Browser signed in", ignoreCase = true).fetchSemanticsNodes().isNotEmpty() }
        assertTrue("confirm and a lost reply must both be polled through", polls >= 3)
        assertEquals(0, dismissed)
        rule.onAllNodesWithText("47").assertCountEquals(0)
    }

    @Test fun pollingShowsVoidedWhenTheBrowserTypesTheWrongNumber() {
        show(hubEndingIn("voided"))
        waitForRequest()
        tapAllow()
        rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }

        rule.waitUntil(10_000) {
            rule.onAllNodesWithText("The number didn't match, so this sign-in was cancelled", ignoreCase = true).fetchSemanticsNodes().isNotEmpty()
        }
        rule.onAllNodesWithText("47").assertCountEquals(0)
    }

    @Test fun pollingShowsDeniedWhenAnotherDeviceDeclines() {
        show(hubEndingIn("denied"))
        waitForRequest()
        tapAllow()
        rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }

        rule.waitUntil(10_000) { rule.onAllNodesWithText("This sign-in was declined", ignoreCase = true).fetchSemanticsNodes().isNotEmpty() }
        rule.onAllNodesWithText("47").assertCountEquals(0)
    }

    @Test fun pollingPreservesExpiredRequestHandling() {
        var allowed = false
        val hub = ScriptedBrowserLoginHub { _, allow ->
            when (allow) {
                true -> {
                    allowed = true
                    browserLoginDetails(state = "confirm", number = "47")
                }
                false -> browserLoginDetails(state = "denied")
                null -> if (!allowed) browserLoginDetails() else throw HubFailure(HubError.Server, status = 410)
            }
        }
        show(hub)
        waitForRequest()
        tapAllow()
        rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }

        rule.waitUntil(10_000) { rule.onAllNodesWithText("This code has expired", ignoreCase = true).fetchSemanticsNodes().isNotEmpty() }
    }

    private fun hubEndingIn(state: String): ScriptedBrowserLoginHub {
        var allowed = false
        return ScriptedBrowserLoginHub { _, allow ->
            when (allow) {
                true -> {
                    allowed = true
                    browserLoginDetails(state = "confirm", number = "47")
                }
                false -> browserLoginDetails(state = "denied")
                null -> browserLoginDetails(state = if (allowed) state else "scanned", number = null)
            }
        }
    }
}
