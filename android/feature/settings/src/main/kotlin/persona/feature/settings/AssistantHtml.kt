package persona.feature.settings

import android.annotation.SuppressLint
import android.os.Message
import android.util.Base64
import android.webkit.CookieManager
import android.webkit.GeolocationPermissions
import android.webkit.JsPromptResult
import android.webkit.JsResult
import android.webkit.PermissionRequest
import android.webkit.RenderProcessGoneDetail
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionViewItem
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JDialog
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.rowPadding
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.views.AssistantCard
import persona.core.domain.views.AssistantCards
import java.util.concurrent.atomic.AtomicInteger

// An assistant's `html` component (docs/assistant/assistant-views.md "Free-form HTML" "Security boundaries every client must verify").
//
// Android has not passed the security acceptance yet: the cases the spec lists (fetch, XHR, images, CSS, iframes,
// WebSocket, beacons, redirects, meta refresh, popups, forms, cookies / storage, forged bridge messages, confirm floods,
// endless loops) have not been observed on a real WebView with a request counter, and this machine has no emulator.
// Until they are, [AssistantHtmlSandbox.Accepted] is false and every page is drawn as its card's fallback. The sandbox
// below is what is to be accepted; it is never claimed as a hard CPU or memory limit (a loop in the page holds the
// app's shared renderer until the page is destroyed).

/** What a card gives its HTML pages: their bridge's two calls, each after the user confirmed it in the app. */
internal class HtmlBridgeHost(
    /** Null: the card cannot answer now (answered, not registered, read-only); a page's reply is refused. */
    val reply: (suspend (componentKey: String, text: String) -> Unit)?,
    val open: suspend (componentKey: String, url: String) -> Unit,
)

internal object AssistantHtmlSandbox {
    /** Whether Android's HTML sandbox passed the security acceptance. Not yet: pages show their card's fallback. */
    const val Accepted = false

    /** At most this many pages run at once; the others wait for "Run". */
    const val MostRunning = 2

    /**
     * The policy put before anything of the page's: inline script and style only, pictures and fonts from data: only,
     * no connection, frame, worker, object, form target or base. A policy the page adds can only narrow it.
     */
    const val Policy = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; " +
        "media-src 'none'; connect-src 'none'; frame-src 'none'; child-src 'none'; worker-src 'none'; object-src 'none'; " +
        "manifest-src 'none'; form-action 'none'; base-uri 'none'"

    /** The listener's object; the page sees only the frozen `persona.reply(text)` / `persona.open(url)` around it. */
    const val BridgeObject = "personaBridge"

    private const val Bridge = "(function(){var b=window.$BridgeObject;if(!b)return;" +
        "function s(m){try{b.postMessage(JSON.stringify(m))}catch(e){}}" +
        "Object.defineProperty(window,'persona',{value:Object.freeze({" +
        "reply:function(t){s({kind:'reply',text:String(t)})},open:function(u){s({kind:'open',url:String(u)})}})," +
        "writable:false,configurable:false,enumerable:false});})();"

    /** The page as loaded: the policy and the bridge first, then the assistant's HTML as it wrote it. */
    fun document(html: String, bridge: Boolean): String = buildString {
        append("<!doctype html><html><head><meta charset=\"utf-8\">")
        append("<meta http-equiv=\"Content-Security-Policy\" content=\"").append(Policy).append("\">")
        append("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
        if (bridge) append("<script>").append(Bridge).append("</script>")
        append("</head><body>").append(html).append("</body></html>")
    }

    /** Whether a page may run here: accepted, and this WebView can take bridge messages with their frame. */
    fun runnable(): Boolean = Accepted && runCatching { WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER) }.getOrDefault(false)

    /**
     * A WebView that keeps nothing, reaches nothing and goes nowhere: no file or content access, no storage, every
     * request answered empty here (the policy stops the ones that never ask, WebSocket), every navigation refused,
     * no windows, dialogs or permissions. Cookies are refused for this view only (never the app's cookie store).
     */
    @SuppressLint("SetJavaScriptEnabled")
    fun configure(view: WebView, blocked: () -> Unit, gone: () -> Unit) {
        if (runCatching { WebViewFeature.isFeatureSupported(WebViewFeature.MULTI_PROFILE) }.getOrDefault(false)) {
            val _ = runCatching { WebViewCompat.setProfile(view, "persona-assistant-html") }
        }
        view.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = false
            @Suppress("DEPRECATION") databaseEnabled = false
            allowFileAccess = false
            allowContentAccess = false
            @Suppress("DEPRECATION") allowFileAccessFromFileURLs = false
            @Suppress("DEPRECATION") allowUniversalAccessFromFileURLs = false
            blockNetworkLoads = true
            blockNetworkImage = true
            loadsImagesAutomatically = true
            cacheMode = WebSettings.LOAD_NO_CACHE
            setSupportMultipleWindows(false)
            javaScriptCanOpenWindowsAutomatically = false
            mediaPlaybackRequiresUserGesture = true
            setGeolocationEnabled(false)
            setSupportZoom(false)
        }
        CookieManager.getInstance().setAcceptThirdPartyCookies(view, false)
        view.isLongClickable = false
        view.webViewClient = SandboxClient(blocked, gone)
        view.webChromeClient = SandboxChrome
    }

    /** The page's HTML as one data: document (an opaque origin: no cookies, no storage of the app's). */
    fun load(view: WebView, html: String, bridge: Boolean) {
        val encoded = Base64.encodeToString(document(html, bridge).toByteArray(Charsets.UTF_8), Base64.NO_PADDING or Base64.NO_WRAP)
        view.loadData(encoded, "text/html; charset=utf-8", "base64")
    }

    internal class SandboxClient(private val blocked: () -> Unit, private val gone: () -> Unit) : WebViewClient() {
        override fun shouldInterceptRequest(view: WebView?, request: WebResourceRequest?): WebResourceResponse {
            blocked()
            return WebResourceResponse("text/plain", "utf-8", 403, "Blocked", emptyMap(), java.io.ByteArrayInputStream(ByteArray(0)))
        }

        override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean { blocked(); return true }

        @Deprecated("Deprecated in Java")
        override fun shouldOverrideUrlLoading(view: WebView?, url: String?): Boolean { blocked(); return true }

        /** The renderer died (or was killed for memory): this page stops; the app goes on. */
        override fun onRenderProcessGone(view: WebView?, detail: RenderProcessGoneDetail?): Boolean { gone(); return true }
    }

    internal object SandboxChrome : WebChromeClient() {
        override fun onCreateWindow(view: WebView?, isDialog: Boolean, isUserGesture: Boolean, resultMsg: Message?) = false
        override fun onJsAlert(view: WebView?, url: String?, message: String?, result: JsResult?): Boolean { result?.cancel(); return true }
        override fun onJsConfirm(view: WebView?, url: String?, message: String?, result: JsResult?): Boolean { result?.cancel(); return true }
        override fun onJsPrompt(view: WebView?, url: String?, message: String?, defaultValue: String?, result: JsPromptResult?): Boolean {
            result?.cancel(); return true
        }
        override fun onJsBeforeUnload(view: WebView?, url: String?, message: String?, result: JsResult?): Boolean { result?.cancel(); return true }
        override fun onPermissionRequest(request: PermissionRequest?) { request?.deny() }
        override fun onGeolocationPermissionsShowPrompt(origin: String?, callback: GeolocationPermissions.Callback?) {
            callback?.invoke(origin, false, false)
        }
    }

    internal val running = AtomicInteger(0)
}

/** A request a page made through its bridge, once read and checked. */
internal sealed interface HtmlBridgeRequest {
    data class Reply(val text: String) : HtmlBridgeRequest
    data class Open(val url: String) : HtmlBridgeRequest
}

/**
 * Which bridge messages a page's instance takes: only from its main frame, only from the instance on screen now (a
 * page reloaded or replaced never speaks for the new one), one at a time while a confirm is open, and none after the
 * user turned down [MostDeclines] of them (a page asking again and again is not shown again).
 */
internal class HtmlBridgePolicy {
    companion object { const val MostDeclines = 2 }

    var declines = 0
        private set
    var asking = false
        private set
    val stopped: Boolean get() = declines >= MostDeclines

    fun read(message: String?, mainFrame: Boolean, instance: Int, current: Int): HtmlBridgeRequest? {
        if (!mainFrame || instance != current || asking || stopped || message == null || message.length > 16_384) return null
        val o = runCatching { JsonCodec.parseToJsonElement(message) }.getOrNull() as? JsonObject ?: return null
        fun string(name: String) = (o[name] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull
        val request = when (string("kind")) {
            "reply" -> string("text")?.takeIf { it.isNotBlank() && it.codePointCount(0, it.length) <= AssistantCards.ReplyMost }?.let { HtmlBridgeRequest.Reply(it) }
            "open" -> string("url")?.takeIf { it.length <= 2048 }?.let { HtmlBridgeRequest.Open(it) }
            else -> null
        } ?: return null
        asking = true
        return request
    }

    fun answered(confirmed: Boolean) {
        asking = false
        if (!confirmed) declines++
    }
}

/**
 * An `html` item of a card: its fallback until the sandbox is accepted; then the page in its own WebView, run while on
 * screen (at most [AssistantHtmlSandbox.MostRunning] at once), destroyed when it leaves, with "Stop" and "Retry" outside it.
 */
@Composable
internal fun AssistantHtmlItem(item: ExtensionViewItem, card: AssistantCard, modifier: Modifier, bridge: HtmlBridgeHost?) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val title = item.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_card_html_title)
    JGroupItem(GroupPosition.Single, modifier) {
        Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                JIcon(PhIcons.Code, null, size = Tokens.Space.l, tint = palette.ink3)
                Spacer(Modifier.width(Tokens.Space.xs))
                Text(title, Modifier.weight(1f), style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (!AssistantHtmlSandbox.runnable()) {
                Text(stringResource(R.string.settings_card_html_fallback), style = type.caption, color = palette.ink3)
                Text(card.fallback, style = type.body, color = palette.ink)
            } else HtmlPage(item, (item.height ?: 320).coerceIn(160, 640), bridge.takeIf { !item.key.isNullOrBlank() })
        }
    }
}

@SuppressLint("ClickableViewAccessibility")
@Composable
private fun HtmlPage(item: ExtensionViewItem, height: Int, bridge: HtmlBridgeHost?) {
    val scope = rememberCoroutineScope()
    var run by remember(item) { mutableIntStateOf(0) }
    var stopped by remember(item) { mutableStateOf(false) }
    var slot by remember(item) { mutableStateOf(false) }
    var asking by remember(item) { mutableStateOf<HtmlBridgeRequest?>(null) }
    val policy = remember(item, run) { HtmlBridgePolicy() }
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    DisposableEffect(item, run, stopped) {
        slot = !stopped && AssistantHtmlSandbox.running.incrementAndGet() <= AssistantHtmlSandbox.MostRunning
        if (!slot && !stopped) AssistantHtmlSandbox.running.decrementAndGet()
        onDispose { if (slot) { AssistantHtmlSandbox.running.decrementAndGet(); slot = false } }
    }
    if (!stopped && slot) {
        val instance = run
        AndroidView(factory = { context ->
            WebView(context).also { view ->
                AssistantHtmlSandbox.configure(view, blocked = {}, gone = { stopped = true })
                val key = item.key
                if (bridge != null && key != null) {
                    WebViewCompat.addWebMessageListener(view, AssistantHtmlSandbox.BridgeObject, setOf("*")) { _, message, _, isMainFrame, _ ->
                        policy.read(message.data, isMainFrame, instance, run)?.let { asking = it }
                    }
                }
                AssistantHtmlSandbox.load(view, item.html.orEmpty(), bridge != null)
            }
        }, modifier = Modifier.fillMaxWidth().height(height.dp), onRelease = { view ->
            view.stopLoading()
            view.loadUrl("about:blank")
            view.destroy()
        })
    } else Text(stringResource(if (stopped) R.string.settings_card_html_stopped else R.string.settings_card_html_too_many), style = type.caption, color = palette.ink3)
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (!stopped) JButton(stringResource(R.string.settings_card_html_stop), { stopped = true }, kind = JButtonKind.Quiet, size = JButtonSize.Small, icon = PhIcons.Stop)
        JButton(stringResource(R.string.settings_card_html_retry), { stopped = false; run++ }, kind = JButtonKind.Quiet, size = JButtonSize.Small, icon = PhIcons.ArrowClockwise)
    }
    asking?.let { request ->
        val key = item.key.orEmpty()
        fun done(confirmed: Boolean) { policy.answered(confirmed); asking = null }
        when (request) {
            is HtmlBridgeRequest.Reply -> JDialog({ done(false) }, title = stringResource(R.string.settings_card_html_reply_title),
                text = stringResource(R.string.settings_card_html_reply_text, request.text),
                confirm = if (bridge?.reply != null) stringResource(R.string.settings_card_html_send) to { done(true); scope.launch { bridge.reply.invoke(key, request.text) } } else null)
            is HtmlBridgeRequest.Open -> JDialog({ done(false) }, title = stringResource(R.string.settings_card_html_open_title),
                text = stringResource(R.string.settings_card_html_open_text, request.url),
                confirm = stringResource(R.string.settings_card_html_open) to { done(true); scope.launch { bridge?.open?.invoke(key, request.url) } })
        }
    }
}
