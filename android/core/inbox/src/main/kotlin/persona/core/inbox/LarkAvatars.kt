package persona.core.inbox

/** The legacy thumbnail size used for a repository-bound image before Compose resolves its drawn size. */
const val LarkAvatarPixels = 144

/**
 * [ref] at [pixels]: relative hub paths get a size query unless they already name one; other URLs are left unchanged.
 */
fun larkAvatarRef(ref: String, pixels: Int = LarkAvatarPixels): String =
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
