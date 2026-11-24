package persona.core.inbox

import java.net.URI
import java.net.URLDecoder

/** Moments share links (`…/client/moments/detail?postId=…`). */
object LarkMoments {
    fun postId(link: String?): String? {
        link ?: return null
        val uri = runCatching { URI(link.trim()) }.getOrNull() ?: return null
        val path = uri.path ?: return null
        if (!path.endsWith("/client/moments/detail") && !path.endsWith("/moments/detail")) return null
        val id = (uri.rawQuery ?: return null).split('&').firstNotNullOfOrNull { kv ->
            val i = kv.indexOf('=')
            if (i > 0 && kv.substring(0, i) == "postId") runCatching { URLDecoder.decode(kv.substring(i + 1), "UTF-8") }.getOrNull() else null
        } ?: return null
        return id.takeIf { it.isNotEmpty() && it.all { ch -> ch.isLetterOrDigit() || ch == '_' || ch == '-' } }
    }

    /**
     * A post's picture [url] as [manager] reads it (docs/assistant/managers.md "Clients"): a hub path gets `manager=`, as
     * a picture loaded by URL sends no header. As it is for none or the default, a full URL (a CDN's) and one that names
     * a manager already; the web's withManager and iOS's LarkMedia.url do the same.
     */
    fun imageUrl(url: String, manager: String?): String {
        val named = manager?.takeIf { it.isNotBlank() && it != persona.core.contracts.api.AssistantManagerChoice.current.value.defaultId }
        if (named == null || !url.startsWith("/") || url.startsWith("//") || Regex("[?&]${persona.core.hub.ManagerQuery}=").containsMatchIn(url)) return url
        val hash = url.indexOf('#').takeIf { it >= 0 } ?: url.length
        val head = url.substring(0, hash)
        return head + (if ('?' in head) "&" else "?") + persona.core.hub.ManagerQuery + "=" +
            java.net.URLEncoder.encode(named, "UTF-8") + url.substring(hash)
    }
}
