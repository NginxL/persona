package persona.feature.assistant

import android.content.res.Resources
import android.graphics.BitmapFactory
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalDensity
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.contracts.Session
import persona.core.contracts.api.ExtensionIconPath
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.Brand
import persona.core.designsystem.Tokens
import persona.core.designsystem.connectorBrand
import persona.core.designsystem.currentAssistant

// A conversation an extension opened, marked with that extension's own icon (#10358; the web's OpenerTile and
// lib/opener-icons.ts, iOS ConversationOpener.swift; docs/protocol/protocol.md "Origin of assistant chats"). The hub's
// origin says only how it was opened: any extension that opens one with the lark.agent purpose gets `lark_agent`, which
// was drawn as Lark's. The extension itself is the conversation's `opened_by`, else its `origin_ref`, and its icon is the
// one its package brings, which the hub serves from the extension's own manager. It is drawn in the box of the origin's
// mark ([persona.core.designsystem.JSourceMark]'s `picture`). Until it is drawn, and when the extension has no such
// icon, is not installed (any more) for the conversation's manager, is the one the origin's mark already stands for (the
// Lark extension for Lark /agent) or its icon does not load, the origin's own mark stays. The mark's TalkBack name says
// "From <the extension's name>" while its icon is drawn, and only then, so the words never disagree with it.

/** The extension that opened a conversation from outside (Lark /agent's or another extension's), by id, with the manager the conversation belongs to. */
@Immutable
internal data class ConversationOpener(val id: String, val manager: String? = null) {
    companion object {
        /**
         * `opened_by`, else `origin_ref`, which for these two origins is that extension too (the hub notes both,
         * server/internal/assistant/start.go requestOrigin); null for any other origin, and when neither is there (a
         * conversation the hub's backfill judged, or one from before `opened_by`).
         */
        fun of(origin: String?, openedBy: String?, originRef: String?, manager: String? = null): ConversationOpener? {
            val source = automaticConversationOrigin(origin)
            if (source != AssistantConversationOrigin.LarkAgent && source != AssistantConversationOrigin.Extension) return null
            val id = listOf(openedBy, originRef).firstNotNullOfOrNull { it?.trim()?.takeIf(String::isNotEmpty) } ?: return null
            return ConversationOpener(id, manager)
        }

        /** [session]'s, under [origin] (the row's: the built-in background supervisor is drawn as a task's). */
        fun of(session: Session, origin: String? = session.origin): ConversationOpener? =
            of(origin, session.openedBy, session.originRef, session.manager)
    }
}

/** The brand an origin's own mark draws: Lark's for Lark /agent; none for the others. */
internal val AssistantConversationOrigin.brand: Brand? get() = if (this == AssistantConversationOrigin.LarkAgent) Brand.Lark else null

/** The icon a mark draws for the extension that opened a conversation: its route on the hub, the manager to ask it of, and the extension's name, if it has one. */
@Immutable
internal data class OpenerIcon(val path: String, val manager: String?, val name: String?)

/**
 * The icon to draw for [opener] from the installed extensions ([list], every manager's): the one of its id that is the
 * conversation's manager's ([sameManager], told the extension's manager), when its `icon_url` is one of the hub's own
 * icon routes ([iconPath]: an installed or a catalog icon; public artwork is left out, as on the web and iOS) and it is
 * not the extension the origin's mark ([brand]) stands for. Null while the list is not read and in every other case: the
 * origin's mark stays.
 */
internal fun openerIcon(
    list: List<HubExtension>?, opener: ConversationOpener, brand: Brand?, sameManager: (String?) -> Boolean,
    iconPath: (String) -> String? = { url -> url.takeIf(ExtensionIconPath::matches) },
): OpenerIcon? {
    val extension = list?.firstOrNull { it.id == opener.id && sameManager(it.manager) } ?: return null
    if (extension.standsFor(brand)) return null
    val path = extension.iconUrl?.let(iconPath) ?: return null
    return OpenerIcon(path, extension.manager, extension.name.trim().takeIf(String::isNotEmpty))
}

/**
 * Whether it is the extension the origin's mark ([brand]) stands for: it brings a connector or a messaging source of that
 * brand, as the Lark extension brings `lark` (the web's `connectorBrand`). Read from its manifest, not its id.
 */
internal fun HubExtension.standsFor(brand: Brand?): Boolean = brand != null &&
    listOf("connectors", "messaging").any { point -> contributes?.get(point).orEmpty().any { connectorBrand(it) == brand } }

/**
 * Whether an extension stamped [extensionManager] is the manager's of a conversation stamped [conversationManager]
 * (null: the one the Persona page shows, [shown]), as iOS's `ConversationOpener.sameManager` and the web tell it: with
 * one manager (or no roster yet) every one is.
 */
internal fun openerSameManager(extensionManager: String?, conversationManager: String?, shown: String, roster: ManagerRoster?): Boolean {
    if (roster == null || !roster.isMulti) return true
    return roster.ownerOf(extensionManager) == roster.ownerOf(conversationManager ?: shown)
}

/** The mark's TalkBack name: "From <name>" while the opener's icon is drawn ([drawnName]), else the origin's words. */
internal fun originMarkLabel(source: AssistantConversationOrigin, drawnName: String?, resources: Resources): String =
    drawnName?.let { resources.getString(R.string.assistant_origin_from, it) } ?: source.accessibilityLabel(resources)

/**
 * The installed extensions and their icons as those marks need them (the web's lib/opener-icons.ts, iOS `OpenerIcons`).
 * Nothing is read at launch: the list (`GET /api/v1/extensions`, every manager's) is read once a mark needs it ([ensure]),
 * again when a mark appears a minute or more after the last read, and after [stale] (an extension installed, upgraded or
 * removed; the hub's features changed); a [stale] that comes while the list is being read has it read again once that
 * read answers, whose answer is kept but not taken as fresh. [reset] forgets it all for another hub. The last list stays
 * meanwhile. Each icon is read once from its extension's manager, decoded off the main thread at about the size drawn
 * and kept, at most [KEPT] of them; one that did not load is asked again only after the list is read again, by the
 * marks on screen too ([revision]). Used on the main thread.
 */
@Stable
class OpenerIcons(
    private val scope: CoroutineScope,
    private val source: Source,
    private val io: CoroutineDispatcher = Dispatchers.IO,
    private val now: () -> Long = System::currentTimeMillis,
    private val decode: (ByteArray, Int) -> ImageBitmap? = ::decodeOpenerIcon,
) {
    /** The hub on screen, as the marks read it. */
    interface Source {
        /** Whether it lists extensions (`extensions` or `plugins` in its hello's features). */
        val listsExtensions: Boolean
        /** `GET /api/v1/extensions` asked of no manager: every manager's, each stamped with its own. */
        suspend fun extensions(): List<HubExtension>
        /** The hub's own icon route an `icon_url` names; null for anything else (public artwork, another host, an odd path). */
        fun iconPath(url: String): String?
        /** The PNG on the icon route [path], asked of [manager] (null: the default). */
        suspend fun icon(path: String, manager: String?): ByteArray
    }

    /** One icon as the marks draw it, observed on its own: a picture that comes in redraws only the marks of it. */
    internal class Drawn {
        var image by mutableStateOf<ImageBitmap?>(null)
        var pixels = 0
        var loading = false
        var failed = false
    }

    /** The installed extensions of the hub on screen; null until read, and on a hub without extensions. */
    internal var extensions by mutableStateOf<List<HubExtension>?>(null)
        private set

    /** Bumped by [reset]: the marks on screen ask for the new hub's list. */
    internal var generation by mutableIntStateOf(0)
        private set

    /** Bumped by every list read that answers, the same list too: the marks on screen ask again for an icon that did not load. */
    internal var revision by mutableIntStateOf(0)
        private set

    private var wanted = false
    private var readAt: Long? = null
    private var reading: Job? = null
    /** [stale] came while [reading]: its answer may predate the change. */
    private var staleWhileReading = false
    private val drawn = HashMap<String, Drawn>()

    /** What the mark of a conversation [opener] opened draws in place of [origin]'s own: null for the origin's. */
    internal fun icon(opener: ConversationOpener, origin: AssistantConversationOrigin, sameManager: (String?) -> Boolean): OpenerIcon? =
        openerIcon(extensions, opener, origin.brand, sameManager, source::iconPath)

    /** The entry of [icon] on the hub on screen, made when first asked for. */
    internal fun drawn(icon: OpenerIcon): Drawn {
        val key = icon.manager.orEmpty() + "\u0000" + icon.path
        drawn[key]?.let { return it }
        if (drawn.size >= KEPT) drawn.clear()
        return Drawn().also { drawn[key] = it }
    }

    /** Reads the list when there is none yet, or the last read is a minute old. A hub without extensions is not asked. */
    fun ensure() {
        wanted = true
        if (reading != null || !source.listsExtensions) return
        readAt?.let { if (now() - it < FRESH_MS) return }
        val at = generation
        val job = scope.launch(start = CoroutineStart.LAZY) {
            val next = try {
                withContext(io) { source.extensions() }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                null
            }
            if (at != generation) return@launch
            reading = null
            val again = staleWhileReading
            staleWhileReading = false
            if (!again) readAt = now() // a failed read is asked again a minute later; the last list stays
            if (next != null) {
                drawn.values.forEach { it.failed = false }
                if (next != extensions) extensions = next
                revision++
            }
            if (again) ensure()
        }
        reading = job
        job.start()
    }

    /** The list may have changed: read it again now if a mark has asked for it, else when one does; once the read on its way answers, if there is one. */
    fun stale() {
        readAt = null
        if (reading != null) staleWhileReading = true
        else if (wanted) ensure()
    }

    /** Another hub: what this one read is not its. */
    fun reset() {
        generation++
        reading?.cancel()
        reading = null
        staleWhileReading = false
        readAt = null
        wanted = false
        drawn.clear()
        extensions = null
    }

    /**
     * Reads [icon]'s picture for a mark of [pixels] (on its longer side), unless it is there at that size, on its way, or
     * did not load since the list was read. The read is the store's, not the mark's: it goes on when the mark goes.
     */
    internal fun load(icon: OpenerIcon, pixels: Int) {
        val entry = drawn(icon)
        if (entry.pixels >= pixels || entry.loading || entry.failed) return
        entry.loading = true
        val at = generation
        scope.launch {
            val picture = try {
                withContext(io) { decode(source.icon(icon.path, icon.manager), pixels) }
            } catch (cancelled: CancellationException) {
                entry.loading = false
                throw cancelled
            } catch (_: Exception) {
                null
            }
            entry.loading = false
            if (at != generation) return@launch
            if (picture != null) {
                entry.pixels = pixels
                entry.image = picture
            } else if (entry.image == null) {
                entry.failed = true
            }
        }
    }

    companion object {
        /** A list this old is read again when a mark appears. */
        const val FRESH_MS = 60_000L
        /** At most this many icons are kept (a few each, about the size of a row's mark). */
        const val KEPT = 64
    }
}

/** The installed extensions' icons for the marks of conversations an extension opened; null (demo, previews): the origin's marks. */
val LocalOpenerIcons = staticCompositionLocalOf<OpenerIcons?> { null }

/** The opener's icon once it is drawn, with the extension's name for the words (none: the origin's). */
@Immutable
internal data class DrawnOpener(val picture: ImageBitmap, val name: String?)

/**
 * What the mark of a conversation [opener] opened (origin [origin]) draws instead of the origin's: null until the
 * extension's icon is drawn, and for every fallback. Asks the store for the list when it appears and for the icon once
 * the list names one.
 */
@Composable
internal fun rememberDrawnOpener(opener: ConversationOpener?, origin: AssistantConversationOrigin): DrawnOpener? {
    val openers = LocalOpenerIcons.current
    if (opener == null || openers == null) return null
    val assistant = currentAssistant()
    LaunchedEffect(openers, opener, openers.generation) { openers.ensure() }
    val icon = openers.icon(opener, origin) { openerSameManager(it, opener.manager, assistant.id, assistant.roster) }
    val pixels = with(LocalDensity.current) { Tokens.Size.markS.roundToPx() }
    // Again after every list read (revision): an icon that did not load is asked again while its mark stays on screen.
    LaunchedEffect(openers, icon, pixels, openers.revision) { icon?.let { openers.load(it, pixels) } }
    val picture = icon?.let { openers.drawn(it).image } ?: return null
    return DrawnOpener(picture, icon.name)
}

/** The largest icon read: 2 MiB of PNG, as the hub serves at most (docs/extensions/extensions.md "Bundled catalog artwork"). */
internal const val MAX_OPENER_ICON_BYTES = 2 shl 20

private val PngSignature = byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)

/** [bytes] decoded at about [pixels] on its longer side, called off the main thread: a PNG of at most [MAX_OPENER_ICON_BYTES], else none. */
internal fun decodeOpenerIcon(bytes: ByteArray, pixels: Int): ImageBitmap? {
    if (bytes.size > MAX_OPENER_ICON_BYTES || bytes.size < PngSignature.size || PngSignature.indices.any { bytes[it] != PngSignature[it] }) return null
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    val longer = maxOf(bounds.outWidth, bounds.outHeight)
    if (longer <= 0) return null
    var sample = 1
    while (longer / (sample * 2) >= pixels.coerceAtLeast(1)) sample *= 2
    return BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })?.asImageBitmap()
}
