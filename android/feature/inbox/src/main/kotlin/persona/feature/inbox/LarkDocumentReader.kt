package persona.feature.inbox

import androidx.compose.ui.res.stringResource

import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.gestures.*
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.contracts.api.ConnectionState
import persona.core.navigation.ConnectionSettingsRoute
import androidx.compose.ui.draw.clip
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.inbox.*

internal val LocalLarkLinkOpener = staticCompositionLocalOf<((String, LarkContentSource?) -> Unit)?> { null }
private data class LarkLink(val url: String, val source: LarkContentSource?)
private enum class ReaderDetent { Full, Peek, Hidden }

/**
 * In-app links open full height; the drag handle leaves the conversation interactive at the peek detent. [manager] is the
 * inbox item's whose page this is: the documents and posts its links open are read through it (null: none named).
 */
@Composable
internal fun LarkLinkHost(repository: InboxRepository, manager: String? = null, content: @Composable () -> Unit) {
    var target by remember { mutableStateOf<LarkLink?>(null) }
    var momentPostId by remember { mutableStateOf<String?>(null) }
    var momentLink by remember { mutableStateOf<String?>(null) }
    val parentOccluded = LocalLarkReadOccluded.current
    val scope = rememberCoroutineScope()
    val lark = LocalLarkFeatures.current
    // A web page over the page: no glass may sample its WebView, here or in the page around it (LocalWebSheets).
    WebSheetShown(target != null || momentPostId != null)
    CompositionLocalProvider(LocalLarkLinkOpener provides { url, source ->
        // Native views only while the hub has them (the Lark extension's features, #1530); else a web page.
        when (val native = larkNativeLink(url, lark)) {
            is LarkNativeLink.MomentsPost -> { momentPostId = native.postId; momentLink = url }
            is LarkNativeLink.Document -> larkWebUrl(url)?.let { target = LarkLink(it, source?.forManager(manager)) }
            null -> larkWebUrl(url)?.let { target = LarkLink(it, null) }
        }
    }, LocalLarkReadOccluded provides (parentOccluded || target != null || momentPostId != null)) {
        BoxWithConstraints(Modifier.fillMaxSize()) {
            content()
            val pixels = with(LocalDensity.current) { maxHeight.toPx() }
            val drag = remember { AnchoredDraggableState(ReaderDetent.Hidden) }
            SideEffect { drag.updateAnchors(DraggableAnchors {
                ReaderDetent.Full at 0f; ReaderDetent.Peek at pixels * .4f; ReaderDetent.Hidden at pixels
            }) }
            LaunchedEffect(target) { if (target != null) drag.animateTo(ReaderDetent.Full) }
            LaunchedEffect(drag.settledValue) {
                if (drag.settledValue == ReaderDetent.Hidden && !drag.isAnimationRunning) target = null
            }
            target?.let { link ->
                BackHandler { scope.launch { drag.animateTo(ReaderDetent.Hidden); target = null } }
                val height = with(LocalDensity.current) {
                    (pixels - (drag.offset.takeIf { it.isFinite() } ?: pixels)).coerceIn(0f, pixels).toDp()
                }
                CompositionLocalProvider(LocalGlassLevel provides GlassLevel.Solid) {
                Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(height)
                    .clip(PersonaTheme.shapes.sheetShape).background(PersonaTheme.palette.surfaceOpaque)) {
                    Column {
                        Column(Modifier.fillMaxWidth().anchoredDraggable(drag, Orientation.Vertical)) {
                            Box(Modifier.align(Alignment.CenterHorizontally).padding(Tokens.Space.s).size(40.dp, 4.dp)
                                .background(PersonaTheme.palette.ink3, PersonaTheme.shapes.controlShape))
                            Row(Modifier.fillMaxWidth().padding(end = Tokens.Space.m), verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                                InboxConnectionTitle(repository, stringResource(if (link.source != null && isLarkDocumentUrl(link.url)) R.string.inbox_lark_doc else R.string.inbox_lark_webpage),
                                    Modifier.weight(1f).padding(start = Tokens.Space.m))
                                JIconButton(if (drag.currentValue == ReaderDetent.Full) PhIcons.CaretDown else PhIcons.CaretUp,
                                    stringResource(if (drag.currentValue == ReaderDetent.Full) R.string.inbox_collapse else R.string.inbox_expand), onClick = {
                                    scope.launch { drag.animateTo(if (drag.currentValue == ReaderDetent.Full) ReaderDetent.Peek else ReaderDetent.Full) }
                                })
                                JIconButton(PhIcons.X, stringResource(R.string.inbox_close), onClick = { scope.launch { drag.animateTo(ReaderDetent.Hidden); target = null } })
                            }
                        }
                        LarkDocumentReader(link.url, link.source, repository, Modifier.weight(1f), showConnectionTitle = false)
                    }
                }
                }
            }
            momentPostId?.let { pid ->
                LarkMomentsSheet(
                    postId = pid,
                    link = momentLink,
                    repository = repository,
                    manager = manager,
                    onDismiss = {
                        momentPostId = null
                        momentLink = null
                    }
                )
            }
        }
    }
}

/**
 * The shared conversation link host can use the current inbox pairing without changing the app shell. [picks] puts
 * the card's "AI edit" / "Use as context" in the selection menu and hears the title; [refresh] counts the card's "Refresh";
 * [manager] is the conversation's, which the document is read through (null: none named).
 */
@Composable
fun LarkDocumentLink(url: String, modifier: Modifier = Modifier, picks: LarkLinkPicks? = null, refresh: Int = 0,
    manager: String? = null, webpageContent: @Composable () -> Unit) {
    val repository = LocalInboxRepository.current ?: injectedInboxRepository()
    val snapshot by repository.snapshot.collectAsStateWithLifecycle()
    LarkDocumentReader(url, snapshot.contentSource?.forManager(manager), repository, modifier, webpageContent, picks = picks, refresh = refresh)
}

/**
 * Starts reading the Feishu documents among [urls] that the card would open natively, so a tap shows them at once
 * (web `setLinkPrefetcher`); they go to [LarkDocumentCache.shared] as [manager]'s, as the card would read them, never
 * into the card's WebView pool.
 */
@Composable
fun LarkDocumentPrefetch(urls: List<String>, manager: String? = null) {
    val repository = injectedInboxRepository()
    val source = repository.snapshot.collectAsStateWithLifecycle().value.contentSource?.forManager(manager) ?: return
    LaunchedEffect(urls, source.scope) { urls.take(3).forEach { LarkDocumentCache.shared.prefetch(source, it) } }
}

/** Reusable native reader for a pairing-bound source; every read failure falls back to the original webpage. */
@Composable
fun LarkDocumentReader(url: String, source: LarkContentSource?, repository: InboxRepository, modifier: Modifier = Modifier,
    webpageContent: (@Composable () -> Unit)? = null,
    showConnectionTitle: Boolean = true,
    picks: LarkLinkPicks? = null,
    refresh: Int = 0,
    cache: LarkDocumentCache = LarkDocumentCache.shared,
) {
    var document by remember(url, source?.scope) { mutableStateOf<LarkDocument?>(null) }
    var details by remember(url, source?.scope) { mutableStateOf(LarkDocumentDetails()) }
    var webpage by remember(url, source?.scope) { mutableStateOf(!isLarkDocumentUrl(url) || source == null) }
    // The first read may come from the cache; a "Refresh" after it reads again.
    val firstRefresh = remember(url, source?.scope) { refresh }
    LaunchedEffect(url, source?.scope, refresh) {
        if (!webpage && source != null) {
            val read = cache.load(source, url, fresh = refresh != firstRefresh)
            try { document = read.await() }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { webpage = true }
        }
    }
    // What is shown around the text is read on every open and "Refresh" (never cached: its counts move), as on web and iOS.
    LaunchedEffect(url, source?.scope, refresh, webpage) {
        if (!webpage && source != null) details = try { source.details(url) } catch (e: CancellationException) { throw e } catch (_: Exception) { LarkDocumentDetails() }
    }
    val onWebpage by rememberUpdatedState(picks?.onWebpage ?: {})
    LaunchedEffect(webpage) { onWebpage(webpage) }
    val onLoaded by rememberUpdatedState(picks?.onLoaded ?: { _, _ -> })
    val loadedTitle = document?.title.orEmpty()
    LaunchedEffect(loadedTitle) { if (loadedTitle.isNotBlank()) onLoaded(loadedTitle, "") }
    val navigator = LocalInboxNavigator.current
    val connection by repository.connectionState.collectAsStateWithLifecycle(ConnectionState.Connecting)
    val list = rememberLazyListState()
    val scope = rememberCoroutineScope()
    var outlineShown by remember(url) { mutableStateOf(false) }
    val outline = remember(document) { document?.outline().orEmpty() }
    // The body's first item is the title block, so block i is item i + 1.
    val jump: (Int) -> Unit = { index -> outlineShown = false; scope.launch { list.animateScrollToItem(index + 1) } }
    // Comments (#2964): read where the hub has Feishu, written where its plugin says `lark_doc_comments`.
    val lark = LocalLarkFeatures.current
    val unnamed = stringResource(R.string.inbox_lark_doc_comments_unnamed)
    val comments = remember(url, source?.scope) { source?.let { LarkDocCommentsModel(url, LarkDocCommentsTransport(it), unnamed = unnamed) } }
    var commentsShown by remember(url, source?.scope) { mutableStateOf(false) }
    // "Comment on this passage" (#2964, iOS #1729): the comments open with the box on that passage (its text, the block it is in when known).
    val commentOn: ((String, String?) -> Unit)? = if (comments == null || !lark.docComments) null else { quote, block ->
        comments.target = LarkDocCommentTarget.Passage(quote, block)
        commentsShown = true
    }
    var pressed by remember(url) { mutableStateOf<LarkDocumentBlock?>(null) }
    val connectionDetails = rememberConnectionDetails(connection, navigator?.let { nav -> { nav.navigate(ConnectionSettingsRoute) } })
    JPage(title = if (connection == ConnectionState.Online) document?.title?.ifBlank { null } ?: stringResource(R.string.inbox_lark_doc) else inboxTitle(connection), modifier = modifier,
        titleBusy = connection in setOf(ConnectionState.Connecting, ConnectionState.Updating),
        onTitleClick = if (connection == ConnectionState.Online) null else connectionDetails,
        onBack = navigator?.let { { it.back() } },
        topBar = if (showConnectionTitle) null else ({}),
        actions = {
            if (!webpage && outline.isNotEmpty()) JIconButton(PhIcons.ListChecks, stringResource(R.string.inbox_lark_doc_outline), { outlineShown = true })
            if (!webpage && comments != null && document != null) JIconButton(PhIcons.ChatCircle, stringResource(R.string.inbox_lark_doc_comments_title),
                { commentsShown = true }, Modifier.testTag("docComments.open"))
            JTextButton(stringResource(R.string.inbox_lark_switch_web), { webpage = true })
        }) { padding ->
        if (webpage) {
            Box(Modifier.fillMaxSize().padding(padding).padding(horizontal = pagePadding)) {
                if (webpageContent != null) webpageContent() else LarkDocumentWebView(url, Modifier.fillMaxSize())
            }
        } else if (document == null) {
            LazyColumn(contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
                top = padding.calculateTopPadding(), bottom = padding.calculateBottomPadding())) {
                item { JLoading(text = stringResource(R.string.inbox_lark_reading_doc)) }
                item { JButton(stringResource(R.string.inbox_lark_switch_web), onClick = { webpage = true }) }
            }
        } else {
            val loaded = requireNotNull(document)
            val body: @Composable () -> Unit = {
                LazyColumn(state = list, contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
                    top = padding.calculateTopPadding() + Tokens.Space.m, bottom = padding.calculateBottomPadding() + Tokens.Space.m),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    item(key = "title") {
                        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                            Text(loaded.title.ifBlank { stringResource(R.string.inbox_lark_doc) }, style = PersonaTheme.type.title, color = PersonaTheme.palette.ink)
                            LarkDocumentMeta(details)
                            if (!showConnectionTitle) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                                if (outline.isNotEmpty()) JTextButton(stringResource(R.string.inbox_lark_doc_outline), { outlineShown = true }, icon = PhIcons.ListChecks)
                                if (comments != null) JTextButton(stringResource(R.string.inbox_lark_doc_comments_title), { commentsShown = true }, icon = PhIcons.ChatCircle)
                                JTextButton(stringResource(R.string.inbox_lark_switch_web), { webpage = true })
                            }
                            if (details.summary.isNotBlank()) LarkDocumentSummary(details.summary)
                        }
                    }
                    items(loaded.blocks) { block ->
                        when (block.kind) {
                            "heading" -> Text(block.text, Modifier.commentable(block, commentOn != null && !loaded.copyAllowed) { pressed = block },
                                style = if (block.level <= 2) PersonaTheme.type.title else PersonaTheme.type.headline,
                                color = PersonaTheme.palette.ink)
                            "codeBlock" -> JCard(padding = Tokens.Space.m) {
                                if (block.language.isNotBlank()) Text(block.language, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
                                Text(block.text, Modifier.horizontalScroll(rememberScrollState()), style = PersonaTheme.type.code, color = PersonaTheme.palette.ink)
                            }
                            "image" -> if (block.image.isBlank()) Text(block.text.ifBlank { stringResource(R.string.inbox_lark_doc_image_unavailable) },
                                style = PersonaTheme.type.body, color = PersonaTheme.palette.ink2)
                                else LarkPartContent(requireNotNull(source).image(block.image, document = true), repository) { }
                            "divider" -> JDivider()
                            else -> Row(Modifier.commentable(block, commentOn != null && !loaded.copyAllowed) { pressed = block },
                                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                                if (block.marker.isNotEmpty()) Text(block.marker, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink3)
                                LarkLinkedText(block.text.ifBlank { if (block.kind == "unsupported") stringResource(R.string.inbox_lark_doc_block_unsupported) else "" },
                                    source, Modifier.weight(1f))
                            }
                        }
                    }
                    if (loaded.partial) item { JNotice(stringResource(R.string.inbox_lark_doc_partial)) }
                    item(key = "footer") { LarkDocumentFooter(url) }
                }
            }
            // Outside a conversation the app shell's "Hand to Persona", as on the inbox's web pages (LocalInboxWebSelection).
            val hand = LocalInboxWebSelection.current
            val handLabel = stringResource(R.string.inbox_hand_to_persona)
            val title = loaded.title.ifBlank { stringResource(R.string.inbox_lark_doc) }
            val commentLabel = stringResource(R.string.inbox_lark_doc_comment_selection)
            // Text the document lets be selected: "Comment" in the selection menu comments on the words picked (the long
            // press selects them; the plugin finds their block). Text it does not: a long press on a paragraph.
            val selectionPicks = (picks?.picks ?: hand?.let { listOf(LarkTextPick(handLabel) { text -> it(InboxWebSelection(title, url, text)) }) }.orEmpty()) +
                listOfNotNull(commentOn?.let { comment -> LarkTextPick(commentLabel) { text -> comment(text, null) } })
            LarkSelectable(loaded.copyAllowed, selectionPicks) { body() }
        }
    }
    pressed?.let { block ->
        JActionSheet(listOf(JAction(stringResource(R.string.inbox_lark_doc_comment_passage), PhIcons.ChatCircle) {
            commentOn?.invoke(block.text, block.id.ifBlank { null })
        }), onDismiss = { pressed = null }, note = block.text.take(120))
    }
    if (commentsShown && comments != null) LarkDocCommentsSheet(comments, lark.docComments, onDismiss = { commentsShown = false })
    if (outlineShown) JSheet(onDismiss = { outlineShown = false }, title = stringResource(R.string.inbox_lark_doc_outline), scrollable = false) {
        LazyColumn(Modifier.fillMaxWidth()) {
            items(outline) { (index, heading) ->
                JRow(heading.text, Modifier.padding(start = Tokens.Space.l * (heading.level - 1).coerceIn(0, 3)),
                    onClick = { jump(index) }, chevron = false)
            }
        }
    }
}

/** A paragraph or heading a long press comments on ([enabled]: comments can be written and the text is not selectable). */
@OptIn(androidx.compose.foundation.ExperimentalFoundationApi::class)
private fun Modifier.commentable(block: LarkDocumentBlock, enabled: Boolean, onPress: () -> Unit): Modifier =
    if (!enabled || block.text.isBlank()) this
    else then(Modifier.combinedClickable(onClick = {}, onLongClick = onPress).testTag("docComments.passage.${block.id.ifBlank { block.text.take(12) }}"))

/** Who owns it, who edited it last and when; under it how often it was read (iOS FeishuDocReader's title block). */
@Composable
private fun LarkDocumentMeta(details: LarkDocumentDetails) {
    val resources = LocalResources.current
    val edited = details.updatedAt?.let { larkShortDate(it, currentLocale) }?.let { time ->
        if (details.editor.isNotBlank()) stringResource(R.string.inbox_lark_doc_edited_by, details.editor, time)
        else stringResource(R.string.inbox_lark_doc_updated, time)
    }
    val line = listOfNotNull(details.owner.ifBlank { null }, edited).joinToString(" · ")
    if (line.isNotEmpty()) Text(line, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
    val stats = listOf(R.plurals.inbox_lark_doc_views to details.views, R.plurals.inbox_lark_doc_visitors to details.visitors,
        R.plurals.inbox_lark_doc_likes to details.likes, R.plurals.inbox_lark_doc_comments to details.comments)
        .filter { it.second > 0 }.joinToString(" · ") { (id, n) -> resources.getQuantityString(id, n.toInt().coerceAtLeast(0), n) }
    if (stats.isNotEmpty()) Text(stats, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
}

/** Feishu's cached AI summary: a few lines, the rest on "Expand" (iOS summaryCard). */
@Composable
private fun LarkDocumentSummary(summary: String) {
    var expanded by rememberSaveable(summary) { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    JCard(padding = Tokens.Space.m, onClick = { expanded = !expanded }) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JIcon(PhIcons.Sparkle, null, tint = palette.ai, decorative = true)
            Text(stringResource(R.string.inbox_lark_doc_summary), Modifier.weight(1f), style = PersonaTheme.type.headline, color = palette.ink, maxLines = 1)
            JTextButton(stringResource(if (expanded) R.string.inbox_collapse else R.string.inbox_expand), { expanded = !expanded })
        }
        Text(summary, style = PersonaTheme.type.callout, color = palette.ink2, maxLines = if (expanded) Int.MAX_VALUE else 4,
            overflow = TextOverflow.Ellipsis)
    }
}

/** The end of the text: "Open in Lark" (iOS footer, web "Open in new tab"). */
@Composable
private fun LarkDocumentFooter(url: String) {
    val context = LocalContext.current
    Column(Modifier.fillMaxWidth().padding(top = Tokens.Space.xl), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m),
        horizontalAlignment = Alignment.CenterHorizontally) {
        JDivider()
        JButton(stringResource(R.string.inbox_open_in_lark), onClick = { openInLark(context, url) },
            kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.ArrowSquareOut)
    }
}

/** The Feishu app (or a browser) for [url]; nothing happens when no app takes it. */
internal fun openInLark(context: android.content.Context, url: String) {
    val link = larkWebUrl(url) ?: return
    val _ = runCatching {
        context.startActivity(android.content.Intent(android.content.Intent.ACTION_VIEW, android.net.Uri.parse(link))
            .addCategory(android.content.Intent.CATEGORY_BROWSABLE).addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK))
    }
}

/** 14:05 today, else Nov 4 this year, else Nov 4, 2025 (in the language the app is in). */
internal fun larkShortDate(at: java.time.Instant, locale: java.util.Locale, now: java.time.Instant = java.time.Instant.now(),
                           zone: java.time.ZoneId = java.time.ZoneId.systemDefault()): String {
    val day = at.atZone(zone)
    val today = now.atZone(zone)
    val skeleton = when {
        day.toLocalDate() == today.toLocalDate() -> LocalizedFormats.TIME
        day.year == today.year -> LocalizedFormats.MONTH_DAY
        else -> LocalizedFormats.DATE
    }
    return LocalizedFormats.format(at, skeleton, locale, zone)
}

@Composable
private fun LarkDocumentWebView(url: String, modifier: Modifier) {
    val context = LocalContext.current
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    var failed by remember(url) { mutableStateOf(false) }
    val pick = LocalInboxWebSelection.current
    val web = remember(url) {
        InboxSelectionWebView(context).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            settings.mixedContentMode = android.webkit.WebSettings.MIXED_CONTENT_NEVER_ALLOW
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                    if (larkWebUrl(request.url.toString()) == null) return true
                    return false
                }
                override fun onReceivedError(view: WebView, request: WebResourceRequest, error: android.webkit.WebResourceError) {
                    if (request.isForMainFrame) failed = true
                }
            }
            larkWebUrl(url)?.let { loadUrl(it) }
        }
    }
    DisposableEffect(web, lifecycle) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) web.onPause()
            if (event == Lifecycle.Event.ON_START) web.onResume()
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer); web.stopLoading(); web.destroy() }
    }
    // Text picked on the page can go to Persona (the app shell supplies it; LocalInboxWebSelection).
    SideEffect { web.onPick = pick }
    BackHandler(enabled = web.canGoBack()) { web.goBack() }
    Column(modifier) {
        if (failed) Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.inbox_lark_web_failed), Modifier.weight(1f).padding(Tokens.Space.m), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
            JButton(stringResource(R.string.inbox_retry), onClick = { failed = false; web.reload() })
        }
        AndroidView(factory = { web }, modifier = Modifier.fillMaxWidth().weight(1f))
    }
}
