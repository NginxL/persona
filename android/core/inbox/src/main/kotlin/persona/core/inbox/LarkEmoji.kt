package persona.core.inbox

import android.content.res.Resources

object LarkEmojiHelper {
    /** Each key's symbol and its name for accessibility. */
    private val EMOJI_MAP: Map<String, Pair<String, Int>> = mapOf(
        "thumbsup" to ("👍" to R.string.inbox_lark_emoji_thumbsup),
        "ok" to ("👌" to R.string.inbox_lark_emoji_ok),
        "done" to ("✅" to R.string.inbox_lark_emoji_done),
        "heart" to ("❤️" to R.string.inbox_lark_emoji_heart),
        "smile" to ("😄" to R.string.inbox_lark_emoji_smile),
        "laugh" to ("😂" to R.string.inbox_lark_emoji_laugh),
        "surprised" to ("😮" to R.string.inbox_lark_emoji_surprised),
        "cry" to ("😭" to R.string.inbox_lark_emoji_cry),
        "clap" to ("👏" to R.string.inbox_lark_emoji_clap),
        "fire" to ("🔥" to R.string.inbox_lark_emoji_fire),
        "party" to ("🎉" to R.string.inbox_lark_emoji_party),
        "thinking" to ("🤔" to R.string.inbox_lark_emoji_thinking),
        "muscle" to ("💪" to R.string.inbox_lark_emoji_muscle),
        "sob" to ("😭" to R.string.inbox_lark_emoji_cry),
        "wave" to ("👋" to R.string.inbox_lark_emoji_wave),
        "jiayi" to ("+1" to R.string.inbox_lark_emoji_jiayi),
        "fist" to ("👊" to R.string.inbox_lark_emoji_fist),
        "shake" to ("🤝" to R.string.inbox_lark_emoji_shake),
        "rose" to ("🌹" to R.string.inbox_lark_emoji_rose),
        "beer" to ("🍺" to R.string.inbox_lark_emoji_beer),
        "cake" to ("🎂" to R.string.inbox_lark_emoji_cake),
        "coffee" to ("☕" to R.string.inbox_lark_emoji_coffee),
    )

    fun symbol(key: String): String = EMOJI_MAP[key.lowercase()]?.first ?: "[$key]"
    fun label(res: Resources, key: String): String {
        val entry = EMOJI_MAP[key.lowercase()] ?: return key
        return res.getString(entry.second)
    }
}
