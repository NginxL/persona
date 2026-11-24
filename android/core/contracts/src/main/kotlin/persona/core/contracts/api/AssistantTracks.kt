package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import java.text.Collator
import java.text.Normalizer

// The assistant's tracks and domain spaces (#13500, docs/assistant/health.md "Tracks and kinds", docs/android/android.md
// "Domain spaces"): the wire types of the track API (server/internal/assistantwire/tracks.go, track_space_md.go,
// info_access.go), and the pure rules the sidebar's Spaces section and the new space form follow, the same as the web's
// (web/src/lib/tracks.ts). Every field has a default, so an answer with fields added later or left out still reads.
// Every route here is the default manager's, as the health space's `?space=` routes are.

/** What a track of a kind does (assistantwire.TrackKindConfig). */
@Serializable
data class TrackKindConfig(
    val schema: Int = 0,
    val template: String = "",
    @SerialName("user_instructions") val userInstructions: String = "",
    val context: String = "",
    @SerialName("default_categories") val defaultCategories: List<String> = emptyList(),
    @SerialName("tag_focus") val tagFocus: List<String> = emptyList(),
    val rhythms: List<TrackRhythm> = emptyList(),
    val attention: TrackAttention = TrackAttention(),
    val supervision: String = "",
    val usage: List<TrackUsageRule> = emptyList(),
    val assembly: String = "",
    @SerialName("capture_target") val captureTarget: Boolean = false,
    val channels: List<String> = emptyList(),
    val sidebar: TrackSidebar = TrackSidebar(),
)

@Serializable
data class TrackRhythm(
    val title: String = "",
    val cron: String = "",
    val tz: String = "",
    val prompt: String = "",
    @SerialName("topic_policy") val topicPolicy: String = "",
    val enabled: Boolean = false,
)

@Serializable
data class TrackAttention(
    @SerialName("push_cap") val pushCap: String = "",
    @SerialName("deep_default") val deepDefault: String = "",
    @SerialName("enter_mode") val enterMode: String = "",
)

@Serializable
data class TrackUsageRule(val scene: String = "", val category: String = "", val rule: String = "")

/** Where a kind's tracks show in the sidebar: [group] `spaces` lists them under Spaces, by [order]. */
@Serializable
data class TrackSidebar(val group: String = "", val order: Int = 0, val icon: String = "")

/** One track with its effective config (assistantwire.Track). */
@Serializable
data class Track(
    val slug: String = "",
    val kind: String = "",
    val title: String = "",
    val icon: String = "",
    val description: String = "",
    @SerialName("default_categories") val defaultCategories: List<String> = emptyList(),
    val template: String = "",
    val config: TrackKindConfig = TrackKindConfig(),
    val builtin: Boolean = false,
    val archived: Boolean = false,
    val revision: Long = 0,
) {
    /** The name people see: the title, else the slug. */
    val name: String get() = title.ifBlank { slug }
    /** The icon token it draws: its own, else its kind's. Unknown tokens are the caller's to map. */
    val iconToken: String get() = icon.ifBlank { config.sidebar.icon }
}

/** assistantwire.TrackKind. [disabled]: why its tracks are left out ("schema", "invalid"). */
@Serializable
data class TrackKind(
    val slug: String = "",
    val title: String = "",
    val config: TrackKindConfig = TrackKindConfig(),
    val builtin: Boolean = false,
    val revision: Long = 0,
    val disabled: String = "",
    val warnings: List<String> = emptyList(),
)

/** assistantwire.TrackCreate. */
@Serializable
data class TrackCreate(
    val slug: String,
    val kind: String,
    val title: String,
    val icon: String,
    val description: String,
    @SerialName("default_categories") val defaultCategories: List<String> = emptyList(),
)

/** assistantwire.TrackChange: only the fields given change; [revision] is the one the change was made against. */
@Serializable
data class TrackChange(
    val revision: Long,
    val title: String? = null,
    val icon: String? = null,
    val description: String? = null,
    @SerialName("default_categories") val defaultCategories: List<String>? = null,
    val archived: Boolean? = null,
)

/** GET / PUT …/tracks/{slug}/space-md: the whole file, its digest, and the managed state block's text. */
@Serializable
data class SpaceMd(val content: String = "", val digest: String = "", val state: String = "")

/** assistantwire.ConversationCategories: what a conversation holds, what its space sets ([floor]), what the user took away. */
@Serializable
data class ConversationCategories(
    val categories: List<String> = emptyList(),
    val floor: List<String> = emptyList(),
    val removed: List<String> = emptyList(),
)

/** assistantwire.ExtensionTrust: [OWNER] reads everything; [THIRD_PARTY] only the [categories] it was granted. */
@Serializable
data class ExtensionTrust(
    val extension: String = "",
    val trust: String = THIRD_PARTY,
    val categories: List<String> = emptyList(),
) {
    val owner: Boolean get() = trust == OWNER

    companion object {
        const val OWNER = "owner"
        const val THIRD_PARTY = "third_party"
    }
}

/** What an extension may read: GET / PUT /api/v1/assistant/extension-trust (the default manager's). */
interface ExtensionTrustSource {
    /** Every extension the user decided about; null from an assistant without grants (an older one). */
    suspend fun list(): List<ExtensionTrust>?
    /** Sets [id]'s trust; answers it as the hub kept it. */
    suspend fun set(id: String, trust: ExtensionTrust): ExtensionTrust
}

/** The track list was answered without tracks: an assistant from before them. */
class TracksUnsupported : Exception("tracks unsupported")

/** How a SPACE.md read or write failed, for the words the editor shows. */
enum class SpaceMdError { Stale, Managed, Missing, Offline, Other }

object Tracks {
    /** The sidebar group a kind lists its tracks under to appear in the Spaces section. */
    const val SPACES_GROUP = "spaces"
    /** The kind a new space defaults to. */
    const val DEFAULT_KIND = "space"
    /** The main chat's own way of working: not a place, never offered for a new space. */
    const val ORCHESTRATION_KIND = "orchestration"
    /** The information categories (server/internal/assistant/track_registry.go infoCategories), in the order shown. */
    val INFO_CATEGORIES = listOf("health", "finance", "relationships", "emotion", "legal_id")

    private val RESERVED = setOf("main", "health", "assistant", "guest", "spaces", "tracks")
    /** Slugs the hub takes but a client cannot open as a space's address (the web's `/tracks/new` is the form). */
    private val PAGE_WORDS = setOf("new")
    private val SLUG = Regex("^[a-z][a-z0-9-]{1,31}$")

    /**
     * The tracks the sidebar's Spaces section lists: not archived, in the spaces group, the health space left out (it keeps
     * its own entry). Ordered by the kind's sidebar order, then by title, then by slug.
     */
    fun sidebarSpaces(tracks: List<Track>): List<Track> {
        val collator = Collator.getInstance()
        return tracks
            .filter { !it.archived && it.slug != AssistantSpace.HEALTH && it.config.sidebar.group == SPACES_GROUP }
            .sortedWith { a, b ->
                a.config.sidebar.order.compareTo(b.config.sidebar.order).takeIf { it != 0 }
                    ?: collator.compare(a.title, b.title).takeIf { it != 0 }
                    ?: a.slug.compareTo(b.slug)
            }
    }

    /** identity.ValidTrackSlug: a lowercase letter, then 1 to 31 lowercase letters, digits or dashes; no reserved name. */
    fun validSlug(slug: String): Boolean = SLUG.matches(slug) && slug !in RESERVED

    /** Whether the new space form takes [slug]: valid for the hub, and an address of its own on every client. */
    fun formSlug(slug: String): Boolean = validSlug(slug) && slug !in PAGE_WORDS

    /**
     * A slug suggested from a title: its Latin letters and digits, dashes between words; a title with none of them
     * (Chinese, Japanese) gets "space". A reserved or [taken] one gets a number.
     */
    fun suggestSlug(title: String, taken: Collection<String> = emptyList()): String {
        var base = Normalizer.normalize(title, Normalizer.Form.NFKD)
            .replace(Regex("\\p{Mn}+"), "")
            .lowercase()
            .replace(Regex("[^a-z0-9]+"), "-")
            .replace(Regex("^[^a-z]+"), "")
            .take(28)
            .trimEnd('-')
        if (base.length < 2) base = "space"
        if (formSlug(base) && base !in taken) return base
        for (n in 2 until 1000) {
            val s = "$base-$n"
            if (formSlug(s) && s !in taken) return s
        }
        return base
    }

    /** The kinds a new space may be of: enabled ones, the main chat's own way of working left out. */
    fun spaceKinds(kinds: List<TrackKind>): List<TrackKind> =
        kinds.filter { it.disabled.isBlank() && it.slug != ORCHESTRATION_KIND }

    /** The kind the form starts on: [current] when the assistant offers it, else the first it offers. */
    fun reconcileKind(current: String, loaded: List<TrackKind>): String =
        if (loaded.isNotEmpty() && loaded.none { it.slug == current }) loaded.first().slug else current

    /** How a SPACE.md read or write failed, from the hub's status. */
    fun spaceMdError(status: Int?): SpaceMdError = when (status) {
        409 -> SpaceMdError.Stale
        400 -> SpaceMdError.Managed
        404 -> SpaceMdError.Missing
        503 -> SpaceMdError.Offline
        else -> SpaceMdError.Other
    }

    /** An extension's trust as read from the list: no row is a third party's with no grant. */
    fun trustOf(items: List<ExtensionTrust>, id: String): ExtensionTrust {
        val row = items.firstOrNull { it.extension == id }
            ?: return ExtensionTrust(id, ExtensionTrust.THIRD_PARTY, emptyList())
        return ExtensionTrust(id, if (row.owner) ExtensionTrust.OWNER else ExtensionTrust.THIRD_PARTY, row.categories)
    }

    /** [list] with [track] in place of the one of its slug, or added: a create or change shown before the next read. */
    fun noted(list: List<Track>?, track: Track): List<Track> {
        val current = list.orEmpty()
        return if (current.any { it.slug == track.slug }) current.map { if (it.slug == track.slug) track else it } else current + track
    }
}
