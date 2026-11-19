package persona.feature.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.*
import persona.core.hub.HubFailure
import persona.core.hub.HubError

class BrowserLoginDecisionTest {
    private class Hub : DeviceSettings {
        var failure: Exception? = null
        var hold: CompletableDeferred<Unit>? = null
        val decisions = mutableListOf<Boolean?>()
        override suspend fun browserLogin(id: String, hub: String, allow: Boolean?): BrowserLoginDetails {
            decisions.add(allow)
            hold?.await()
            failure?.let { throw it }
            return BrowserLoginDetails("Browser", "127.0.0.1", 1)
        }
        override suspend fun devices() = PairedDevices(emptyList(), null)
        override suspend fun revoke(credentialId: String) {}
        override suspend fun preferences() = DeviceNotificationPreferences()
        override suspend fun savePreferences(preferences: DeviceNotificationPreferences) = preferences
    }
    private val request = BrowserLoginRequest("A".repeat(43), "test-hub")

    @Test fun cancellationClosesLocallyAndDeniesEvenBeforeInspectionFinishes() = runTest {
        val hub = Hub(); hub.hold = CompletableDeferred()
        var dismissed = false
        val task = async { decideBrowserLogin(hub, request, false) { dismissed = true } }
        testScheduler.runCurrent()
        assertTrue(dismissed)
        assertEquals(listOf(false), hub.decisions)
        hub.hold!!.complete(Unit); task.await()
    }

    @Test fun expiredAndAlreadyDecidedRequestsCanBeClosed() = runTest {
        for (status in listOf(409, 410)) {
            val hub = Hub(); hub.failure = HubFailure(HubError.Server, status = status)
            var dismissed = false
            decideBrowserLogin(hub, request, false) { dismissed = true }
            assertTrue(dismissed)
        }
    }

    @Test fun expiredAndAlreadyDecidedAllowAnswersRemainTerminal() = runTest {
        for (status in listOf(409, 410)) {
            val hub = Hub(); hub.failure = HubFailure(HubError.Server, status = status)
            var dismissed = false
            decideBrowserLogin(hub, request, true) { dismissed = true }
            assertTrue("a stale $status decision keeps its existing terminal handling", dismissed)
        }
    }

    @Test fun aTransportFailureNeverTrapsCancellationOrPretendsApprovalSucceeded() = runTest {
        val hub = Hub(); hub.failure = java.io.IOException("fixture network failure")
        var dismissed = false
        try { decideBrowserLogin(hub, request, false) { dismissed = true }; fail() }
        catch (_: java.io.IOException) { assertTrue(dismissed) }
        dismissed = false
        try { decideBrowserLogin(hub, request, true) { dismissed = true }; fail() }
        catch (_: java.io.IOException) { assertFalse(dismissed) }
    }
}
