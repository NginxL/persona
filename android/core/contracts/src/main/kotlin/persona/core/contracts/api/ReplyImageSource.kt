package persona.core.contracts.api

import kotlinx.coroutines.flow.StateFlow

/** Remote pictures in Markdown, bound to the pairing that owns the reply. */
interface ReplyImageSource {
    val features: StateFlow<Set<String>>
    /** Direct access on an older hub requires a user gesture. A failed hub request never falls back to the origin. */
    suspend fun load(url: String, size: Int, allowDirect: Boolean): ByteArray

    companion object {
        const val FEATURE = "remote_images"
        const val MAX_SIZE = 2048
        private val sizes = intArrayOf(256, 512, 768, 1024, 1536, MAX_SIZE)

        fun sizeFor(pixels: Float) = sizes.firstOrNull { it >= pixels } ?: MAX_SIZE
    }
}
