package persona.core.inbox

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
