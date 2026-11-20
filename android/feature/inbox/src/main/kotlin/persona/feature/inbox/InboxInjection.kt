package persona.feature.inbox

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent

@EntryPoint
@InstallIn(SingletonComponent::class)
interface InboxEntryPoint {
    fun inboxRepository(): InboxRepository
}

@Composable
internal fun injectedInboxRepository(): InboxRepository {
    val context = LocalContext.current.applicationContext
    return remember(context) { EntryPointAccessors.fromApplication(context, InboxEntryPoint::class.java).inboxRepository() }
}

/**
 * The reader is bound to the original pairing; Coil never receives pairing credentials. Two with the same [cacheKey]
 * (pairing scope, path and drawn pixel size) are the same picture, so rebuilding a record or a message does not make it
 * a new one. [atPixelSize] keeps the encoded read but gives each drawn size its own decoded-cache entry.
 */
class AuthenticatedLarkImage(
    val cacheKey: String,
    private val sized: ((Int) -> AuthenticatedLarkImage)? = null,
    val read: suspend () -> ByteArray,
) {
    fun atPixelSize(pixels: Int): AuthenticatedLarkImage =
        sized?.invoke(pixels) ?: AuthenticatedLarkImage("$cacheKey#$pixels", read = read)

    override fun equals(other: Any?) = other is AuthenticatedLarkImage && other.cacheKey == cacheKey
    override fun hashCode() = cacheKey.hashCode()
}
