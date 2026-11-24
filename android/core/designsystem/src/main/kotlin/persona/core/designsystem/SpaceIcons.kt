package persona.core.designsystem

import android.content.res.Resources
import androidx.compose.runtime.Composable
import androidx.compose.ui.platform.LocalResources
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/**
 * The icons a domain space may take (#13500): the icon tokens the hub keeps for a track (`Track.icon`, the web's
 * `SPACE_ICONS`), each drawn with its Phosphor glyph. A token this app has no glyph for (`users`, `smile`: not in the
 * generated set yet) or does not know draws [fallback], so a space made elsewhere still shows.
 */
object SpaceIcons {
    val fallback: PhIcon get() = PhIcons.Stack

    /** The tokens the new space form offers, in the order the web lists them. */
    val choices: List<String> = listOf("layers", "book", "wallet", "home", "leaf", "car", "calendar", "star", "flag",
        "music", "camera", "chart", "target", "sun")

    fun of(token: String?): PhIcon = when (token) {
        "layers" -> PhIcons.Stack
        "book" -> PhIcons.BookOpen
        "wallet" -> PhIcons.Wallet
        "home" -> PhIcons.House
        "leaf" -> PhIcons.Leaf
        "car" -> PhIcons.Car
        "calendar" -> PhIcons.CalendarBlank
        "star" -> PhIcons.Star
        "flag" -> PhIcons.Flag
        "music" -> PhIcons.MusicNote
        "camera" -> PhIcons.Camera
        "chart" -> PhIcons.ChartBar
        "target" -> PhIcons.Target
        "sun" -> PhIcons.Sun
        else -> fallback
    }
}

/** An information category's name (`health`, `finance`…); one this app does not know shows as the hub sent it. */
fun infoCategoryLabel(resources: Resources, slug: String): String = when (slug) {
    "health" -> resources.getString(R.string.designsystem_info_category_health)
    "finance" -> resources.getString(R.string.designsystem_info_category_finance)
    "relationships" -> resources.getString(R.string.designsystem_info_category_relationships)
    "emotion" -> resources.getString(R.string.designsystem_info_category_emotion)
    "legal_id" -> resources.getString(R.string.designsystem_info_category_legal_id)
    else -> slug
}

@Composable
fun infoCategoryLabel(slug: String): String = infoCategoryLabel(LocalResources.current, slug)
