package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.io.IOException

@OptIn(ExperimentalCoroutinesApi::class)
class LarkAvatarTest {
    @Test fun hubPicturesAreAskedForAtTheThumbnailSize() {
        assertEquals("/api/v1/lark/media/k?size=144", larkAvatarRef("/api/v1/lark/media/k"))
        assertEquals("/api/v1/lark/media/k?v=2&size=144", larkAvatarRef("/api/v1/lark/media/k?v=2"))
        // One that names a size already, or is not on the hub, is left as it is.
        assertEquals("/api/v1/lark/media/k?size=72", larkAvatarRef("/api/v1/lark/media/k?size=72"))
        assertEquals("https://example.com/a.png", larkAvatarRef("https://example.com/a.png"))
        assertEquals("//example.com/a.png", larkAvatarRef("//example.com/a.png"))
    }

    @Test fun noRefIsNoPictureAndTheKeyNamesThePairing() {
        assertNull(larkAvatar(null, "hub/a") { error("unused") })
        assertNull(larkAvatar(" ", "hub/a") { error("unused") })
        val a = larkAvatar("/api/v1/lark/media/k", "hub/a") { byteArrayOf() }!!
        assertEquals("hub/a:/api/v1/lark/media/k?size=144", a.cacheKey)
        // The same picture rebuilt is equal (rows stay equal across refreshes); another pairing's is not.
        assertEquals(a, larkAvatar("/api/v1/lark/media/k", "hub/a") { byteArrayOf() })
        assertNotEquals(a, larkAvatar("/api/v1/lark/media/k", "hub/b") { byteArrayOf() })
        val sized = a.atPixelSize(168)
        assertEquals("hub/a:/api/v1/lark/media/k?size=168#168", sized.cacheKey)
        assertEquals(sized, larkAvatar("/api/v1/lark/media/k", "hub/a", 168) { byteArrayOf() })
        assertNotEquals(a, sized)
        assertNotEquals(sized, a.atPixelSize(192))
        val cdn = larkAvatar("https://example.com/a.png", "hub/a") { byteArrayOf() }!!
        assertEquals("hub/a:https://example.com/a.png#192", cdn.atPixelSize(192).cacheKey)
    }

    @Test fun aFailureAtOneDrawnSizeDoesNotHoldBackAnother() = runTest {
        val refs = mutableListOf<String>()
        val image = larkAvatar("/api/v1/lark/media/k", "hub/a") { sized ->
            refs += sized
            if ("size=168" in sized) byteArrayOf(1) else throw IOException("offline")
        }!!
        val loads = LarkAvatarLoads<String>(backgroundScope, now = { 0 })
        assertNull(loads.get(image.atPixelSize(120), 0) { null })
        assertNull(loads.get(image.atPixelSize(120), 0) { null })
        assertEquals("decoded", loads.get(image.atPixelSize(168), 0) { "decoded" })
        assertEquals(listOf("/api/v1/lark/media/k?size=120", "/api/v1/lark/media/k?size=168"), refs)
    }

    @Test fun rowsShowingTheSamePictureShareOneRead() = runTest {
        val loads = LarkAvatarLoads<String>(backgroundScope)
        val gate = CompletableDeferred<ByteArray>()
        var reads = 0
        val image = AuthenticatedLarkImage("hub/a:/p") { reads++; gate.await() }
        val first = async { loads.get(image, 0) { "decoded" } }
        val second = async { loads.get(image, 0) { "decoded" } }
        runCurrent()
        gate.complete(byteArrayOf(1))
        assertEquals("decoded", first.await())
        assertEquals("decoded", second.await())
        assertEquals(1, reads)
    }

    @Test fun aRowScrolledAwayDoesNotStopTheLoadTheOthersWaitFor() = runTest {
        val loads = LarkAvatarLoads<String>(backgroundScope)
        val gate = CompletableDeferred<ByteArray>()
        val image = AuthenticatedLarkImage("hub/a:/p") { gate.await() }
        val gone = async { loads.get(image, 0) { "decoded" } }
        val staying = async { loads.get(image, 0) { "decoded" } }
        runCurrent()
        gone.cancelAndJoin()
        gate.complete(byteArrayOf(1))
        assertEquals("decoded", staying.await())
    }

    @Test fun aFailedPictureIsNotAskedForAgainUntilTheRetryTime() = runTest {
        var clock = 0L
        val loads = LarkAvatarLoads<String>(backgroundScope, retryAfterMillis = 60_000, now = { clock })
        var reads = 0
        val image = AuthenticatedLarkImage("hub/a:/p") { reads++; throw IOException("offline") }
        assertNull(loads.get(image, 0) { "decoded" })
        clock = 59_999
        assertNull(loads.get(image, 0) { "decoded" })
        assertEquals(1, reads)
        clock = 60_000
        assertNull(loads.get(image, 0) { "decoded" })
        assertEquals(2, reads)
    }

    @Test fun aFailureFromBeforeTheConnectionCameUpDoesNotHoldBackTheRetry() = runTest {
        var clock = 1_000L
        val loads = LarkAvatarLoads<String>(backgroundScope, retryAfterMillis = 60_000, now = { clock })
        var online = false
        var reads = 0
        val image = AuthenticatedLarkImage("hub/a:/p") { reads++; if (online) byteArrayOf(1) else throw IOException("no route") }
        // Read while the connection was still coming up: it gave up waiting for a route.
        assertNull(loads.get(image, 0) { "decoded" })
        clock = 5_000
        online = true
        assertEquals("decoded", loads.get(image, onlineSince = 5_000) { "decoded" })
        assertEquals(2, reads)
    }

    @Test fun aFailureAfterTheConnectionCameUpStillWaits() = runTest {
        var clock = 5_000L
        val loads = LarkAvatarLoads<String>(backgroundScope, retryAfterMillis = 60_000, now = { clock })
        var reads = 0
        val image = AuthenticatedLarkImage("hub/a:/p") { reads++; throw IOException("not found") }
        assertNull(loads.get(image, onlineSince = 5_000) { "decoded" })
        clock = 6_000
        assertNull(loads.get(image, onlineSince = 5_000) { "decoded" })
        assertEquals(1, reads)
    }

    @Test fun aPictureThatDoesNotDecodeCountsAsFailed() = runTest {
        val loads = LarkAvatarLoads<String>(backgroundScope, now = { 0 })
        var reads = 0
        val image = AuthenticatedLarkImage("hub/a:/p") { reads++; byteArrayOf(1) }
        assertNull(loads.get(image, 0) { null })
        assertNull(loads.get(image, 0) { "decoded" })
        assertEquals(1, reads)
    }

    @Test fun aLoadedPictureIsReadAgainOnlyWhenAskedAgain() = runTest {
        // Coil's memory cache holds what loaded; a row asks again only after Coil let it go.
        val loads = LarkAvatarLoads<String>(backgroundScope)
        var reads = 0
        val image = AuthenticatedLarkImage("hub/a:/p") { reads++; byteArrayOf(1) }
        assertEquals("decoded", loads.get(image, 0) { "decoded" })
        assertEquals("decoded", loads.get(image, 0) { "decoded" })
        assertEquals(2, reads)
    }
}
