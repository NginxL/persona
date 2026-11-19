package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Dp
import coil3.Image
import coil3.SingletonImageLoader
import coil3.compose.asPainter
import coil3.memory.MemoryCache
import coil3.request.ImageRequest
import coil3.request.SuccessResult
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.ensureActive
import persona.core.contracts.api.ConnectionState
import persona.core.designsystem.ContentTokens
import persona.core.designsystem.JAvatar

/** The legacy thumbnail size used for a repository-bound image before Compose resolves its drawn size. */
internal const val LarkAvatarPixels = 144

/**
 * [ref] at [pixels]: relative hub paths get a size query unless they already name one; other URLs are left unchanged.
 */
internal fun larkAvatarRef(ref: String, pixels: Int = LarkAvatarPixels): String =
    if (!ref.startsWith("/") || ref.startsWith("//") || Regex("[?&]size=").containsMatchIn(ref)) ref
    else ref + (if ('?' in ref) "&" else "?") + "size=$pixels"

/** A person's or conversation's picture at the legacy repository size, or null without a ref. */
fun larkAvatar(ref: String?, scope: String, read: suspend (String) -> ByteArray): AuthenticatedLarkImage? =
    larkAvatar(ref, scope, LarkAvatarPixels, read)

/** [ref] at [pixels], retaining the original ref and reader to make other decoded-size variants. */
fun larkAvatar(ref: String?, scope: String, pixels: Int, read: suspend (String) -> ByteArray): AuthenticatedLarkImage? =
    ref?.takeIf { it.isNotBlank() }?.let { original ->
        val sized = larkAvatarRef(original, pixels)
        val key = "$scope:$sized" + if (pixels == LarkAvatarPixels) "" else "#$pixels"
        AuthenticatedLarkImage(key, sized = { next -> larkAvatar(original, scope, next, read)!! }) { read(sized) }
    }

/**
 * One read per picture however many rows show it at once, finishing in [scope] even when the row that asked has
 * scrolled away (it is in the memory cache when the row comes back). A picture that failed is not asked for again for
 * [retryAfterMillis], so scrolling past it does not ask the hub each time; a failure from before the connection last
 * came up does not count (the read gave up waiting for a route), so reconnecting asks again at once.
 */
internal class LarkAvatarLoads<T : Any>(
    private val scope: CoroutineScope,
    private val retryAfterMillis: Long = 60_000,
    private val now: () -> Long = System::currentTimeMillis,
) {
    private val running = HashMap<String, Deferred<T?>>()
    private val failed = HashMap<String, Long>()

    /** The decoded picture, or null when there is none to have now; [onlineSince] is when the connection came up. */
    suspend fun get(image: AuthenticatedLarkImage, onlineSince: Long, decode: suspend (ByteArray) -> T?): T? {
        val key = image.cacheKey
        val load = synchronized(this) {
            failed[key]?.let { at -> if (at >= onlineSince && now() - at < retryAfterMillis) return null else failed.remove(key) }
            running[key] ?: scope.async(start = CoroutineStart.LAZY) {
                var result: T? = null
                try {
                    result = try { decode(image.read()) } catch (e: Exception) {
                        if (e is CancellationException) ensureActive()
                        null
                    }
                    result
                } finally {
                    synchronized(this@LarkAvatarLoads) {
                        running.remove(key)
                        if (result == null) failed[key] = now()
                    }
                }
            }.also { running[key] = it }
        }
        load.start()
        return load.await()
    }
}

private val avatarLoads = LarkAvatarLoads<Image>(CoroutineScope(SupervisorJob() + Dispatchers.IO))

/** When the hub connection last came up (the same clock as [LarkAvatarLoads]), or null while it is not up. */
@Composable
internal fun rememberOnlineSince(connection: ConnectionState): Long? = remember(connection == ConnectionState.Online) {
    if (connection == ConnectionState.Online) System.currentTimeMillis() else null
}

/**
 * [image] for [JAvatar]'s picture at [size]: null while it loads, when it fails and without one, so the initial stands in.
 * A picture Coil still holds at this pixel size shows on the first frame when its row comes back, without asking again.
 * Coil gets the bytes, never the pairing's credentials. Nothing is asked while the hub is not connected ([onlineSince] null);
 * once it is, the rows on screen ask again.
 */
@Composable
internal fun rememberLarkAvatar(image: AuthenticatedLarkImage?, onlineSince: Long?, size: Dp): Painter? {
    val context = LocalContext.current.applicationContext
    val pixels = with(LocalDensity.current) { size.roundToPx() }
    val sizedImage = remember(image, pixels) { image?.atPixelSize(pixels) }
    val key = sizedImage?.cacheKey
    val picture = remember(key) {
        mutableStateOf(key?.let { SingletonImageLoader.get(context).memoryCache?.get(MemoryCache.Key(it))?.image?.asPainter(context) })
    }
    LaunchedEffect(key, onlineSince) {
        if (sizedImage == null || onlineSince == null || picture.value != null) return@LaunchedEffect
        picture.value = avatarLoads.get(sizedImage, onlineSince) { bytes ->
            val request = ImageRequest.Builder(context).data(bytes).memoryCacheKey(sizedImage.cacheKey).size(pixels).build()
            (SingletonImageLoader.get(context).execute(request) as? SuccessResult)?.image
        }?.asPainter(context)
    }
    return picture.value
}

/** A person or conversation: their picture once it has loaded, the initial of [name] until then and without one. */
@Composable
internal fun LarkAvatar(name: String, image: AuthenticatedLarkImage?, onlineSince: Long?, modifier: Modifier = Modifier, size: Dp = ContentTokens.mediumAvatar) {
    JAvatar(name, modifier, size = size, image = rememberLarkAvatar(image, onlineSince, size))
}
