package persona.core.contracts

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.ExtensionTrust
import persona.core.contracts.api.SpaceMdError
import persona.core.contracts.api.Track
import persona.core.contracts.api.TrackChange
import persona.core.contracts.api.TrackKind
import persona.core.contracts.api.TrackKindConfig
import persona.core.contracts.api.TrackSidebar
import persona.core.contracts.api.Tracks

/** The domain spaces' rules (#13500), the same as the web's `web/src/lib/tracks.ts`. */
class TracksModelTest {
    private fun space(slug: String, title: String, order: Int = 0, group: String = Tracks.SPACES_GROUP, archived: Boolean = false) =
        Track(slug = slug, title = title, archived = archived, config = TrackKindConfig(sidebar = TrackSidebar(group = group, order = order)))

    @Test fun readsATrackWithUnknownAndMissingFields() {
        val track = JsonCodec.decodeFromString<Track>("""{"slug":"qa-plain","title":"Plain","later":{"x":1},"revision":3}""")
        assertEquals("qa-plain", track.slug)
        assertEquals(3L, track.revision)
        assertEquals("", track.config.sidebar.group)
        assertEquals(emptyList<String>(), track.defaultCategories)
        assertEquals("Plain", track.name)
        assertEquals("qa-finance", Track(slug = "qa-finance").name)
    }

    @Test fun aChangeSendsOnlyWhatItChanges() {
        val sent = JsonCodec.encodeToString(TrackChange.serializer(), TrackChange(revision = 2, archived = true))
        assertEquals("""{"revision":2,"archived":true}""", sent)
    }

    @Test fun theSidebarListsSpacesInOrderLeavingHealthArchivedAndOtherGroupsOut() {
        val list = Tracks.sidebarSpaces(listOf(
            space("qa-finance", "Money", order = 2),
            space("health", "Health"),
            space("qa-plain", "Plain", order = 1),
            space("qa-old", "Old", archived = true),
            space("qa-main", "Main", group = ""),
            space("qa-bills", "Bills", order = 2),
        ))
        assertEquals(listOf("qa-plain", "qa-bills", "qa-finance"), list.map { it.slug })
    }

    @Test fun slugsFollowTheHubsRule() {
        assertTrue(Tracks.validSlug("qa-plain"))
        assertFalse(Tracks.validSlug("q"))
        assertFalse(Tracks.validSlug("9lives"))
        assertFalse(Tracks.validSlug("Health"))
        assertFalse(Tracks.validSlug("health"))
        assertTrue(Tracks.validSlug("new"))
        assertFalse(Tracks.formSlug("new"))
    }

    @Test fun suggestsASlugFromTheName() {
        assertEquals("family-notes", Tracks.suggestSlug("Family notes"))
        assertEquals("cafe", Tracks.suggestSlug("Café"))
        assertEquals("space", Tracks.suggestSlug("家人"))
        assertEquals("space-2", Tracks.suggestSlug("お金", taken = listOf("space")))
        assertEquals("money-2", Tracks.suggestSlug("Money", taken = listOf("money")))
        assertEquals("new-2", Tracks.suggestSlug("New"))
    }

    @Test fun kindsAndTheirReconciliation() {
        val kinds = Tracks.spaceKinds(listOf(TrackKind("orchestration"), TrackKind("journal"), TrackKind("broken", disabled = "schema")))
        assertEquals(listOf("journal"), kinds.map { it.slug })
        assertEquals("journal", Tracks.reconcileKind(Tracks.DEFAULT_KIND, kinds))
        assertEquals(Tracks.DEFAULT_KIND, Tracks.reconcileKind(Tracks.DEFAULT_KIND, emptyList()))
        assertEquals(Tracks.DEFAULT_KIND, Tracks.reconcileKind(Tracks.DEFAULT_KIND, listOf(TrackKind("space"), TrackKind("journal"))))
    }

    @Test fun spaceMdErrorsByStatus() {
        assertEquals(SpaceMdError.Stale, Tracks.spaceMdError(409))
        assertEquals(SpaceMdError.Managed, Tracks.spaceMdError(400))
        assertEquals(SpaceMdError.Missing, Tracks.spaceMdError(404))
        assertEquals(SpaceMdError.Offline, Tracks.spaceMdError(503))
        assertEquals(SpaceMdError.Other, Tracks.spaceMdError(null))
    }

    @Test fun trustWithoutARowIsAThirdPartysWithNoGrant() {
        val items = listOf(ExtensionTrust("notes", ExtensionTrust.OWNER), ExtensionTrust("mail", "third_party", listOf("finance")))
        assertTrue(Tracks.trustOf(items, "notes").owner)
        assertEquals(listOf("finance"), Tracks.trustOf(items, "mail").categories)
        val none = Tracks.trustOf(items, "other")
        assertFalse(none.owner)
        assertEquals(emptyList<String>(), none.categories)
    }

    @Test fun aNotedTrackReplacesOrJoins() {
        val list = listOf(space("qa-plain", "Plain"))
        assertEquals("Renamed", Tracks.noted(list, space("qa-plain", "Renamed")).single().title)
        assertEquals(listOf("qa-plain", "qa-finance"), Tracks.noted(list, space("qa-finance", "Money")).map { it.slug })
        assertEquals(listOf("qa-finance"), Tracks.noted(null, space("qa-finance", "Money")).map { it.slug })
    }
}
