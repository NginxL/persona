package persona.feature.settings

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute

class ProviderImportLinkTest {
    @Test fun takingAnImportRequestDropsItSoItCannotReplay() {
        val link = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        ProviderImportRequests.pending.value = link
        assertEquals(link, ProviderImportRequests.take())
        assertNull("the request leaves no key-bearing link behind", ProviderImportRequests.pending.value)
        assertNull("a later providers page cannot replay it", ProviderImportRequests.take())
    }

    @Test fun anImportWaitsForItsProviderServiceInsteadOfBeingDropped() {
        val link = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        ProviderImportRequests.pending.value = link
        assertNull(ProviderImportRequests.takeIfReady(false))
        assertEquals("an unavailable service must not consume the link", link, ProviderImportRequests.pending.value)
        assertEquals(link, ProviderImportRequests.takeIfReady(true))
        assertNull(ProviderImportRequests.pending.value)
    }

    @Test fun pairingRetainsOnlyThePendingProviderImportNavigation() {
        val providers = SettingsPageRoute(SettingsPage.Providers)
        val link = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        ProviderImportRequests.pending.value = link
        assertTrue(ProviderImportRequests.retainProviderNavigationDuringPairing(providers))
        assertFalse(ProviderImportRequests.retainProviderNavigationDuringPairing(SettingsPageRoute(SettingsPage.About)))
        ProviderImportRequests.pending.value = null
        assertFalse(ProviderImportRequests.retainProviderNavigationDuringPairing(providers))
    }

    @Test fun cancellingAnImportCannotReplayItAgainstAnotherService() {
        ProviderImportRequests.pending.value = "persona://provider/import?v=1&data=FAKE-EXAMPLE-KEY"
        ProviderImportRequests.cancel()
        assertNull(ProviderImportRequests.pending.value)
    }
}
