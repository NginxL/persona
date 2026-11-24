package persona.feature.files

import android.annotation.SuppressLint
import android.content.res.Resources
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.viewinterop.AndroidView
import persona.core.contracts.api.SessionFileContent
import persona.core.designsystem.PersonaTheme
import java.io.ByteArrayInputStream

/**
 * An HTML file previewed in the file viewer (docs/android/android.md "Files and Git in chat"; web `lib/html-preview.ts`, iOS #1297): the
 * page goes into a WebView with no address (it can't reach the app, files or the hub), scripts and the network off
 * until the user turns them on for this file from ⋯. Pure parts here; [HtmlFile] draws.
 */
internal object HtmlPreview {
    data class Policy(val scripts: Boolean = false, val network: Boolean = false)

    private val htmlExtensions = setOf("html", "htm", "xhtml")
    private val openable = setOf("http", "https", "mailto")

    fun isHtml(name: String) = FilesPaths.extension(name) in htmlExtensions

    /** The Content-Security-Policy put in front of the page: without the network only what the file itself holds loads. */
    fun csp(p: Policy): String {
        if (p.network) return ""
        val script = if (p.scripts) "'unsafe-inline' 'unsafe-eval'" else "'none'"
        return "default-src 'none'; img-src data: blob:; media-src data: blob:; font-src data:; style-src 'unsafe-inline' data:; script-src $script; form-action 'none'"
    }

    private val lead = Regex("^\\s*(?:<\\?xml[^>]*>\\s*)?(<!doctype[^>]*>)?", RegexOption.IGNORE_CASE)

    /**
     * The page with the policy put first, after the doctype (before it would put the page in quirks mode; the parser
     * moves a leading <meta> into <head> anyway, ahead of anything the page loads).
     */
    fun document(html: String, p: Policy): String {
        val csp = csp(p)
        val inject = if (csp.isEmpty()) "" else "<meta http-equiv=\"Content-Security-Policy\" content=\"$csp\">"
        val end = lead.find(html)?.range?.last?.plus(1) ?: 0
        return html.substring(0, end) + inject + html.substring(end)
    }

    // What the page loads: any src, and a <link>'s href (an <a>'s is a link to follow, not something loaded).
    private val reference = Regex("(?:\\ssrc|<link\\b[^>]*\\shref)\\s*=\\s*[\"']?([^\"'\\s>]+)", RegexOption.IGNORE_CASE)
    private val absolute = Regex("^(?:[a-z][a-z0-9+.-]*:|#|//)", RegexOption.IGNORE_CASE)

    /** How many references the page makes to files beside it (they can't load: the page has no address). */
    fun relativeRefs(html: String): Int = reference.findAll(html).count { !absolute.containsMatchIn(it.groupValues[1]) }

    /** Whether a request from the page may load: inline data always, the web only when the network is on. */
    fun allows(scheme: String, p: Policy): Boolean = when (scheme.lowercase()) {
        "data", "blob", "about" -> true
        "http", "https" -> p.network
        else -> false
    }

    /** A link the user tapped in the page that the app opens (in its link sheet): web and mail links only. */
    fun opens(scheme: String) = scheme.lowercase() in openable

    /** The line above the page: what is off, and what can't load. */
    fun notice(resources: Resources, p: Policy, refs: Int): String = buildList {
        add(when {
            p.scripts && p.network -> resources.getString(R.string.files_html_scripts_network)
            p.scripts -> resources.getString(R.string.files_html_scripts)
            p.network -> resources.getString(R.string.files_html_network)
            else -> resources.getString(R.string.files_html_off)
        })
        if (refs > 0) add(resources.getQuantityString(R.plurals.files_html_refs, refs, refs))
    }.joinToString(" · ")
}

/** The page in an isolated WebView filling the space; its own scrolling and zoom. */
@SuppressLint("SetJavaScriptEnabled")
@Composable
internal fun HtmlFile(file: SessionFileContent, policy: HtmlPreview.Policy, modifier: Modifier = Modifier) {
    val html = file.text.orEmpty()
    val document = remember(html, policy) { HtmlPreview.document(html, policy) }
    val links = LocalUriHandler.current
    val open by rememberUpdatedState<(String) -> Unit>({ val _ = runCatching { links.openUri(it) } })
    val background = PersonaTheme.palette.background.toArgb()
    Column(modifier) {
        Notice(HtmlPreview.notice(LocalResources.current, policy, remember(html) { HtmlPreview.relativeRefs(html) }))
        // The policy too: with the network on the document is the same whether scripts are on or not.
        key(document, policy) {
            AndroidView(
                modifier = Modifier.fillMaxWidth().weight(1f).testTag("html"),
                factory = { context ->
                    WebView(context).apply {
                        setBackgroundColor(background)
                        settings.javaScriptEnabled = policy.scripts
                        settings.javaScriptCanOpenWindowsAutomatically = false
                        settings.domStorageEnabled = false
                        settings.allowFileAccess = false
                        settings.allowContentAccess = false
                        settings.blockNetworkLoads = !policy.network
                        settings.blockNetworkImage = !policy.network
                        settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                        settings.cacheMode = WebSettings.LOAD_NO_CACHE
                        settings.setSupportMultipleWindows(false)
                        settings.setGeolocationEnabled(false)
                        settings.builtInZoomControls = true
                        settings.displayZoomControls = false
                        settings.useWideViewPort = true
                        settings.loadWithOverviewMode = true
                        webViewClient = object : WebViewClient() {
                            override fun shouldInterceptRequest(view: WebView?, request: WebResourceRequest?): WebResourceResponse? {
                                val scheme = request?.url?.scheme.orEmpty()
                                if (HtmlPreview.allows(scheme, policy)) return null
                                return WebResourceResponse("text/plain", "utf-8", ByteArrayInputStream(ByteArray(0)))
                            }

                            override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest): Boolean {
                                // The page never navigates itself; a tapped web link opens in the app's link sheet.
                                if (request.hasGesture() && HtmlPreview.opens(request.url.scheme.orEmpty())) open(request.url.toString())
                                return true
                            }
                        }
                        loadDataWithBaseURL(null, document, "text/html", "utf-8", null)
                    }
                },
                onRelease = { it.stopLoading(); it.destroy() },
            )
        }
    }
}
