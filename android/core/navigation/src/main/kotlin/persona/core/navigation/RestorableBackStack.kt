package persona.core.navigation

import androidx.compose.runtime.Composable
import androidx.compose.runtime.saveable.rememberSerializable
import androidx.navigation3.runtime.NavBackStack
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.serialization.NavBackStackSerializer
import androidx.navigation3.runtime.serialization.NavKeySerializer
import kotlinx.serialization.InternalSerializationApi
import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerializationException
import kotlinx.serialization.encoding.CompositeDecoder
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.encoding.decodeStructure
import kotlinx.serialization.serializer

/**
 * `rememberNavBackStack`, except that a saved route whose class this build lacks is dropped instead of crashing the
 * restore (#13433). Navigation 3 saves each route by its JVM class name (`Class.getName()`, not its serial name) and looks
 * it up with `Class.forName`, so a saved stack can name a class that is gone: a module left out of this build
 * (`-Ppersona.modules`), or a route that moved. The dropped routes are reported to [onDropped]; a stack left empty
 * starts again from [elements].
 */
@Composable
fun rememberRestorableNavBackStack(vararg elements: NavKey, onDropped: (String) -> Unit = {}): NavBackStack<NavKey> =
    rememberSerializable(serializer = RestorableBackStackSerializer(elements.toList(), onDropped)) { NavBackStack(*elements) }

/** Navigation 3's back-stack serializer with [RestorableNavKeySerializer], dropping what it could not restore. */
class RestorableBackStackSerializer(private val fallback: List<NavKey>, private val onDropped: (String) -> Unit = {}) :
    KSerializer<NavBackStack<NavKey>> {
    private val inner = NavBackStackSerializer(RestorableNavKeySerializer())
    override val descriptor = inner.descriptor
    override fun serialize(encoder: Encoder, value: NavBackStack<NavKey>) = inner.serialize(encoder, value)
    override fun deserialize(decoder: Decoder): NavBackStack<NavKey> {
        val restored = inner.deserialize(decoder)
        val missing = restored.filterIsInstance<MissingRoute>()
        if (missing.isEmpty()) return restored
        missing.forEach { onDropped(it.className) }
        val kept = restored.filterNot { it is MissingRoute }.ifEmpty { fallback }
        return NavBackStack(*kept.toTypedArray())
    }
}

/**
 * Navigation 3's route serializer (the same `type` and `value` elements, so the saved form is unchanged) that restores a
 * route whose class is missing as a [MissingRoute] instead of throwing.
 */
class RestorableNavKeySerializer : KSerializer<NavKey> {
    private val delegate = NavKeySerializer<NavKey>()
    override val descriptor = delegate.descriptor
    override fun serialize(encoder: Encoder, value: NavKey) = delegate.serialize(encoder, value)

    override fun deserialize(decoder: Decoder): NavKey = decoder.decodeStructure(descriptor) {
        var className: String? = null
        var key: NavKey? = null
        while (true) {
            when (val index = decodeElementIndex(descriptor)) {
                0 -> className = decodeStringElement(descriptor, 0)
                1 -> {
                    val name = className ?: throw SerializationException("A route's value came before its type")
                    val serializer = routeSerializer(name)
                    key = if (serializer == null) MissingRoute(name) else decodeSerializableElement(descriptor, 1, serializer)
                }
                CompositeDecoder.DECODE_DONE -> break
                else -> throw SerializationException("Unexpected route element $index")
            }
        }
        key ?: MissingRoute(className.orEmpty())
    }

    @OptIn(InternalSerializationApi::class)
    @Suppress("UNCHECKED_CAST")
    private fun routeSerializer(name: String): KSerializer<NavKey>? = try {
        Class.forName(name).kotlin.serializer() as KSerializer<NavKey>
    } catch (_: ClassNotFoundException) {
        null
    } catch (_: SerializationException) {
        null
    }
}

/** A saved route whose class this build does not have; [RestorableBackStackSerializer] drops it, so it is never shown. */
data class MissingRoute(val className: String) : NavKey
