package persona.feature.settings

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.ExtensionContribution
import persona.core.contracts.api.ExtensionDetail
import persona.core.contracts.api.HubExtension
import persona.core.domain.secrets.hasEnabledSecretProvider

/** The second half of the reference switch's gate: an enabled provider, not just hub wire support. */
class SecretProviderAvailabilityTest {
    private class Hub(
        private val list: List<HubExtension>,
        private val details: Map<String, ExtensionDetail>,
        private val failList: Boolean = false,
        private val failDetail: Boolean = false,
    ) : ExtensionApiStub() {
        val detailCalls = mutableListOf<String>()
        override suspend fun extensions(): List<HubExtension> {
            if (failList) throw IllegalStateException("offline")
            return list
        }
        override suspend fun extension(id: String): ExtensionDetail {
            detailCalls += id
            if (failDetail) throw IllegalStateException("unreadable")
            return details.getValue(id)
        }
    }

    private fun candidate(id: String, enabled: Boolean = true, status: String = "stopped") = HubExtension(
        id, id, "secret_provider", status, enabled = enabled, contributes = mapOf("secret_providers" to listOf("vault")))
    private fun detail(extension: HubExtension, enabled: Boolean?, canDisable: Boolean = true) = ExtensionDetail(extension,
        contributions = enabled?.let { listOf(ExtensionContribution("secret_providers", "vault", enabled = it, canDisable = canDisable)) })

    @Test fun enabledCompatibleContributionCountsEvenWhenStoppedOrReadOnly() = runBlocking {
        val extension = candidate("vault")
        val hub = Hub(listOf(extension), mapOf("vault" to detail(extension, true, canDisable = false)))
        assertTrue(hasEnabledSecretProvider(hub))
        assertEquals(listOf("vault"), hub.detailCalls)
    }

    @Test fun disabledIncompatibleOrContributionOffFailsClosed() = runBlocking {
        val off = candidate("off", enabled = false)
        val broken = candidate("broken", status = "incompatible")
        val contributionOff = candidate("contribution-off")
        val missing = candidate("missing")
        val hub = Hub(listOf(off, broken, contributionOff, missing), mapOf(
            "off" to detail(off, true), "broken" to detail(broken, true),
            "contribution-off" to detail(contributionOff, false), "missing" to detail(missing, null),
        ))
        assertFalse(hasEnabledSecretProvider(hub))
        assertEquals(listOf("contribution-off", "missing"), hub.detailCalls)
    }

    @Test fun absentProvidersAndReadFailuresAreNotAvailability() = runBlocking {
        assertFalse(hasEnabledSecretProvider(Hub(emptyList(), emptyMap())))
        val extension = candidate("vault")
        assertFalse(hasEnabledSecretProvider(Hub(listOf(extension), emptyMap(), failList = true)))
        assertFalse(hasEnabledSecretProvider(Hub(listOf(extension), emptyMap(), failDetail = true)))
    }
}
