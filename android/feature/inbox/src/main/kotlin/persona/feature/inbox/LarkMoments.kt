package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.res.pluralStringResource

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import coil3.compose.AsyncImage
import coil3.imageLoader
import coil3.request.ImageRequest
import coil3.request.SuccessResult
import coil3.toBitmap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.LarkMomentsComment
import persona.core.contracts.LarkMomentsImage
import persona.core.contracts.LarkMomentsPost
import persona.core.contracts.LarkUrlPreview
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.Navigator
import java.io.ByteArrayOutputStream
import java.net.URI
import java.net.URLDecoder
import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import persona.core.inbox.*

/** A post's or comment's time in the language the app is in: "Nov 5, 2:30 PM" in English, the same in each language's own form. */
internal fun formatMomentTime(ms: Double?, locale: Locale, zone: ZoneId = ZoneId.systemDefault()): String = runCatching {
    if (ms == null) return ""
    LocalizedFormats.format(Instant.ofEpochMilli(ms.toLong()), LocalizedFormats.MONTH_DAY_TIME, locale, zone)
}.getOrDefault("")

/** A shared Moments post in a chat. */
@Composable
internal fun LarkMomentsCard(
    postId: String,
    url: String,
    repository: InboxRepository,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    var preview by remember(url) { mutableStateOf<LarkUrlPreview?>(null) }
    LaunchedEffect(url) {
        preview = repository.urlPreview("", url)
    }
    val card = preview?.card
    JCard(modifier.fillMaxWidth().clickable(onClick = onClick), padding = Tokens.Space.m) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Icon(PhIcons.ChatsCircle.regular, contentDescription = stringResource(R.string.inbox_moments), tint = PersonaTheme.palette.accent, modifier = Modifier.size(Tokens.Size.iconXS))
            Text(preview?.title ?: stringResource(R.string.inbox_moments_post), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
        }
        card?.author?.let { author ->
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JAvatar(author, size = ContentTokens.smallAvatar)
                Column {
                    Text(author, style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
                    card.subtitle?.let { Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3) }
                }
            }
        }
        val body = card?.body
        if (!body.isNullOrEmpty()) {
            Text(body, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, maxLines = 4)
        }
        card?.footer?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
    }
}

/** One Moments post page opened from an inbox item. */
@Composable
fun LarkMomentsScreen(
    repository: InboxRepository,
    route: LarkMomentRoute,
    navigator: Navigator,
    modifier: Modifier = Modifier,
) {
    val context = LocalContext.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val inbox by repository.snapshot.collectAsStateWithLifecycle()
    val record = inbox.records.firstOrNull { it.item.id == route.itemId || it.moment?.postId == route.postId }
    val composers = rememberReplyComposers(repository)
    val composer = record?.let { composers.get(ReplyTarget.Inbox(it.item.id)) }
    val reply by composer?.state?.collectAsStateWithLifecycle() ?: remember { mutableStateOf(ReplyComposerState()) }

    var post by remember(route.postId) { mutableStateOf<LarkMomentsPost?>(null) }
    var comments by remember(route.postId) { mutableStateOf<List<LarkMomentsComment>>(emptyList()) }
    var loading by remember(route.postId) { mutableStateOf(true) }
    var error by remember(route.postId) { mutableStateOf<String?>(null) }
    var markingDone by remember { mutableStateOf(false) }
    var drafting by remember { mutableStateOf(false) }
    var handingOff by remember { mutableStateOf(false) }
    var confirmSend by remember { mutableStateOf(false) }
    var viewerImage by remember { mutableStateOf<Pair<List<ViewerImage>, Int>?>(null) }

    fun reload() {
        scope.launch {
            loading = true
            error = null
            try {
                post = repository.momentsPost(route.postId)
                comments = repository.momentsComments(route.postId)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { error = e.message ?: res.getString(R.string.inbox_load_failed_retry) }
            finally { loading = false }
        }
    }

    LaunchedEffect(route.postId) { reload() }

    LaunchedEffect(reply.sent) {
        if (reply.sent) {
            drafting = false
            reload()
        }
    }

    val feishuUrl = "https://applink.feishu.cn/client/moments/detail?postId=${route.postId}"

    JListPage(
        title = stringResource(R.string.inbox_moments),
        subtitle = post?.userName?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.inbox_moments_user_posts, it) },
        onBack = navigator::back,
        refreshing = loading,
        onRefresh = ::reload,
        actions = {
            if (record != null) {
                JIconButton(
                    PhIcons.CheckCircle,
                    stringResource(if (record.item.isDone) R.string.inbox_move_back else R.string.inbox_done),
                    enabled = !markingDone,
                    onClick = {
                        markingDone = true
                        scope.launch {
                            try {
                                repository.setDone(record.item.id, !record.item.isDone)
                                navigator.back()
                            } catch (e: CancellationException) { throw e }
                            catch (e: Exception) { error = res.getString(R.string.inbox_failed_retry) }
                            finally { markingDone = false }
                        }
                    }
                )
            }
            JIconButton(PhIcons.ArrowSquareOut, stringResource(R.string.inbox_open_in_lark), onClick = {
                try {
                    val intent = Intent(Intent.ACTION_VIEW, Uri.parse(feishuUrl)).apply {
                        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    }
                    context.startActivity(intent)
                } catch (_: Exception) {}
            })
        },
        bottomBar = {
            if (record != null) {
                InboxAiActions(
                    onDraft = { drafting = true },
                    onHandOff = { handingOff = true },
                    modifier = Modifier.imePadding().padding(Tokens.Space.m),
                    primaryAction = JAction(stringResource(R.string.inbox_moments_comment), PhIcons.ArrowBendUpLeft) { drafting = true },
                    draftEnabled = true,
                )
            }
        },
        modifier = modifier,
    ) {
        item("plugin-notice") { InboxExtensionNotice(route.itemId, route) }
        if (error != null) {
            item("error") {
                JNotice(error ?: stringResource(R.string.inbox_load_failed), status = Status.NeedsYou, actionLabel = stringResource(R.string.inbox_retry), onAction = ::reload)
            }
        }
        if (loading && post == null) {
            item("loading") { JLoading(text = stringResource(R.string.inbox_moments_loading)) }
        }

        record?.item?.summary?.takeIf { it.isNotBlank() }?.let { summary ->
            item("summary") {
                JNotice(summary, Modifier.padding(bottom = Tokens.Space.m), title = stringResource(R.string.inbox_ai_summary), icon = PhIcons.Sparkle)
            }
        }

        if (record != null && !record.item.isDone) {
            item("agent-draft") {
                AgentDraftCard(
                    record, composers, navigator, repository = repository, keepComposer = true,
                    onEdit = { drafting = true },
                    onSend = { confirmSend = true }
                )
            }
        }

        val p = post
        if (p != null) {
            item("post-card") {
                MomentsPostCard(
                    post = p,
                    manager = record?.manager,
                    onOpenImage = { images, index -> viewerImage = images to index }
                )
            }

            item("comments-header") {
                JSectionHeader(title = (p.commentCount ?: comments.size).let { pluralStringResource(R.plurals.inbox_moments_comments, it, it) })
            }

            if (comments.isEmpty() && !loading) {
                item("empty-comments") {
                    JEmptyState(stringResource(R.string.inbox_moments_no_comments_title), stringResource(R.string.inbox_moments_no_comments_text))
                }
            }

            items(comments.size, key = { comments[it].id }) { index ->
                val comment = comments[index]
                MomentsCommentRow(comment)
            }
        }
    }

    if (drafting && record != null && composer != null) {
        InboxDraftSheet(repository, record, composer, navigator, onDismiss = { drafting = false })
    }
    if (handingOff && record != null) {
        InboxHandOffSheet(repository, record, navigator, onDismiss = { handingOff = false })
    }
    if (confirmSend && record != null && composer != null) {
        SendAsYouDialog(
            sendConfirmation(record, reply.draft.text),
            onSend = {
                confirmSend = false
                composer.send()
            },
            onDismiss = { confirmSend = false }
        )
    }
    viewerImage?.let { (images, index) ->
        ImageViewer(images = images, initialIndex = index, onDismiss = { viewerImage = null })
    }
}

/** Bottom sheet for a Moments post opened from a share link inside a conversation; read through [manager] (null: none named). */
@Composable
internal fun LarkMomentsSheet(
    postId: String,
    link: String?,
    repository: InboxRepository,
    manager: String? = null,
    onDismiss: () -> Unit,
) {
    val context = LocalContext.current
    val res = LocalResources.current
    var post by remember(postId, manager) { mutableStateOf<LarkMomentsPost?>(null) }
    var comments by remember(postId, manager) { mutableStateOf<List<LarkMomentsComment>>(emptyList()) }
    var loading by remember(postId, manager) { mutableStateOf(true) }
    var error by remember(postId, manager) { mutableStateOf<String?>(null) }
    var viewerImage by remember { mutableStateOf<Pair<List<ViewerImage>, Int>?>(null) }
    val scope = rememberCoroutineScope()

    fun reload() {
        scope.launch {
            loading = true
            error = null
            try {
                post = repository.momentsPost(postId, manager)
                comments = repository.momentsComments(postId, manager)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { error = e.message ?: res.getString(R.string.inbox_load_failed) }
            finally { loading = false }
        }
    }

    LaunchedEffect(postId, manager) { reload() }

    val feishuUrl = link ?: "https://applink.feishu.cn/client/moments/detail?postId=$postId"

    JSheet(onDismiss = onDismiss, title = stringResource(R.string.inbox_moments), scrollable = false) {
        Row(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), horizontalArrangement = Arrangement.End) {
            JButton(
                stringResource(R.string.inbox_open_in_lark),
                onClick = {
                    try {
                        val intent = Intent(Intent.ACTION_VIEW, Uri.parse(feishuUrl)).apply {
                            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                        }
                        context.startActivity(intent)
                    } catch (_: Exception) {}
                },
                kind = JButtonKind.Secondary,
                size = JButtonSize.Small,
                icon = PhIcons.ArrowSquareOut
            )
        }

        if (error != null) {
            JNotice(error ?: stringResource(R.string.inbox_load_failed), status = Status.NeedsYou, actionLabel = stringResource(R.string.inbox_retry), onAction = ::reload)
        }
        if (loading && post == null) {
            JLoading(text = stringResource(R.string.inbox_moments_loading))
        }

        val p = post
        if (p != null) {
            androidx.compose.foundation.lazy.LazyColumn(
                Modifier.fillMaxWidth().weight(1f, fill = false),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)
            ) {
                item("post") {
                    MomentsPostCard(post = p, manager = manager, onOpenImage = { images, idx -> viewerImage = images to idx })
                }
                item("comments-header") {
                    JSectionHeader(title = (p.commentCount ?: comments.size).let { pluralStringResource(R.plurals.inbox_moments_comments, it, it) })
                }
                if (comments.isEmpty() && !loading) {
                    item("empty-comments") {
                        JEmptyState(stringResource(R.string.inbox_moments_no_comments_title), stringResource(R.string.inbox_moments_no_comments_text))
                    }
                }
                items(comments.size, key = { comments[it].id }) { index ->
                    MomentsCommentRow(comments[index])
                }
            }
        }
    }

    viewerImage?.let { (images, index) ->
        ImageViewer(images = images, initialIndex = index, onDismiss = { viewerImage = null })
    }
}

@Composable
private fun MomentsPostCard(
    post: LarkMomentsPost,
    /** The manager the post is read through (null: none named); its pictures are read as its too. */
    manager: String?,
    onOpenImage: (List<ViewerImage>, Int) -> Unit,
    modifier: Modifier = Modifier,
) {
    JCard(modifier.fillMaxWidth(), padding = Tokens.Space.m) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
        ) {
            JAvatar(post.userName.orEmpty(), size = ContentTokens.mediumAvatar)
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Text(post.userName.orEmpty(), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
                post.createTime?.let {
                    Text(formatMomentTime(it, currentLocale), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
                }
            }
        }

        val postText = post.content?.text ?: post.content?.segments?.joinToString("") { it.text ?: it.key ?: "" }.orEmpty()
        if (postText.isNotBlank()) {
            LarkLinkedText(postText)
        }

        val images = post.images.orEmpty().filter { !it.url.isNullOrBlank() }
            .map { it.copy(url = LarkMoments.imageUrl(it.url.orEmpty(), manager)) }
        if (images.isNotEmpty()) {
            MomentsImagesLayout(images, onOpenImage)
        }

        val groups = post.reactionGroups.orEmpty()
        if (groups.isNotEmpty()) {
            Row(
                modifier = Modifier.horizontalScroll(rememberScrollState()),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)
            ) {
                for (g in groups) {
                    JChip(
                        text = "${reactionLabel(g.type)} ${g.count}",
                        selected = g.selfInvolved,
                    )
                }
            }
        }
    }
}

@Composable
private fun MomentsCommentRow(
    comment: LarkMomentsComment,
    modifier: Modifier = Modifier,
) {
    JCard(modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), padding = Tokens.Space.m) {
        Row(
            verticalAlignment = Alignment.Top,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
        ) {
            JAvatar(comment.userName.orEmpty(), size = Tokens.Space.xxl)
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(comment.userName.orEmpty(), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
                    Spacer(Modifier.weight(1f))
                    comment.createTime?.let {
                        Text(formatMomentTime(it, currentLocale), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
                    }
                }
                if (!comment.replyUserName.isNullOrBlank()) {
                    Text(stringResource(R.string.inbox_moments_reply_to, comment.replyUserName.orEmpty()), style = PersonaTheme.type.caption, color = PersonaTheme.palette.accent)
                }
                val commentText = comment.content?.text
                    ?: comment.content?.segments?.joinToString("") { it.text ?: it.key ?: "" }.orEmpty()
                LarkLinkedText(commentText)
            }
        }
    }
}

/** A post's pictures; each says a tap opens it full screen, like a chat picture (`LarkPart.Image`, #4172). */
@Composable
internal fun MomentsImagesLayout(
    images: List<LarkMomentsImage>,
    onOpenImage: (List<ViewerImage>, Int) -> Unit,
) {
    val context = LocalContext.current
    val imageLabel = stringResource(R.string.inbox_image)
    val viewLabel = stringResource(R.string.inbox_lark_view_image)
    val viewerImages = images.mapIndexed { idx, img ->
        ViewerImage(
            key = img.url ?: "$idx",
            width = img.width?.toInt(),
            height = img.height?.toInt(),
            load = {
                val req = ImageRequest.Builder(context).data(img.url).build()
                val result = context.imageLoader.execute(req)
                if (result is SuccessResult) {
                    val bitmap = result.image.toBitmap()
                    val stream = ByteArrayOutputStream()
                    bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, stream)
                    stream.toByteArray()
                } else {
                    byteArrayOf()
                }
            }
        )
    }

    if (images.size == 1) {
        val img = images.first()
        val shape = RoundedCornerShape(Tokens.Space.s)
        AsyncImage(
            model = img.url,
            contentDescription = imageLabel,
            modifier = Modifier
                .fillMaxWidth(0.7f)
                .aspectRatio(1.2f)
                .clip(shape)
                .clickable(role = Role.Image, onClickLabel = viewLabel) { onOpenImage(viewerImages, 0) }
        )
    } else {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            images.chunked(3).forEachIndexed { rowIndex, row ->
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    row.forEachIndexed { colIndex, img ->
                        val index = rowIndex * 3 + colIndex
                        val shape = RoundedCornerShape(Tokens.Space.sm)
                        AsyncImage(
                            model = img.url,
                            contentDescription = imageLabel,
                            modifier = Modifier
                                .weight(1f)
                                .aspectRatio(1f)
                                .clip(shape)
                                .clickable(role = Role.Image, onClickLabel = viewLabel) { onOpenImage(viewerImages, index) }
                        )
                    }
                    repeat(3 - row.size) {
                        Spacer(Modifier.weight(1f))
                    }
                }
            }
        }
    }
}

private fun reactionLabel(type: String): String = when (type.uppercase()) {
    "THUMBSUP" -> "👍"
    "HEART" -> "❤️"
    "APPLAUD" -> "👏"
    "SMILE" -> "😄"
    "OK" -> "👌"
    else -> type
}

/**
 * A Moments share link opened in a conversation's link card (iOS LarkMomentsPostView in LinkSheet, web MomentsPanel):
 * the post and its comments, text selectable for [picks], read through the conversation's [manager] (null: none named);
 * a read that fails offers "Retry" and the web page.
 */
@Composable
fun LarkMomentsLink(postId: String, url: String, modifier: Modifier = Modifier, picks: LarkLinkPicks? = null, refresh: Int = 0,
    manager: String? = null, webpageContent: @Composable () -> Unit) {
    LarkMomentsLinkContent(LocalInboxRepository.current ?: injectedInboxRepository(), postId, url, modifier, picks, refresh, manager, webpageContent)
}

@Composable
internal fun LarkMomentsLinkContent(repository: InboxRepository, postId: String, url: String, modifier: Modifier = Modifier,
    picks: LarkLinkPicks? = null, refresh: Int = 0, manager: String? = null, webpageContent: @Composable () -> Unit) {
    val context = LocalContext.current
    val res = LocalResources.current
    var post by remember(postId, manager) { mutableStateOf<LarkMomentsPost?>(null) }
    var comments by remember(postId, manager) { mutableStateOf<List<LarkMomentsComment>>(emptyList()) }
    var loading by remember(postId, manager) { mutableStateOf(true) }
    var error by remember(postId, manager) { mutableStateOf<String?>(null) }
    var webpage by remember(postId) { mutableStateOf(false) }
    var round by remember(postId) { mutableIntStateOf(0) }
    var viewerImage by remember { mutableStateOf<Pair<List<ViewerImage>, Int>?>(null) }
    LaunchedEffect(postId, manager, round, refresh) {
        loading = true
        error = null
        try {
            post = repository.momentsPost(postId, manager)
            comments = try { repository.momentsComments(postId, manager) } catch (e: CancellationException) { throw e } catch (_: Exception) { comments }
        } catch (e: CancellationException) { throw e }
        catch (e: Exception) { error = e.message?.takeIf { it.isNotBlank() } ?: res.getString(R.string.inbox_load_failed_retry) }
        finally { loading = false }
    }
    val onWebpage by rememberUpdatedState(picks?.onWebpage ?: {})
    LaunchedEffect(webpage) { onWebpage(webpage) }
    val onLoaded by rememberUpdatedState(picks?.onLoaded ?: { _, _ -> })
    val loaded = post
    val name = loaded?.userName?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.inbox_moments_user_posts, it) }
        ?: stringResource(R.string.inbox_moments_post)
    LaunchedEffect(loaded, name) { if (loaded != null) onLoaded(name, loaded.content?.text.orEmpty()) }
    JPage(title = name, modifier = modifier, topBar = {}) { padding ->
        if (webpage) {
            Box(Modifier.fillMaxSize().padding(padding).padding(horizontal = pagePadding)) { webpageContent() }
            return@JPage
        }
        LarkSelectable(loaded != null, picks?.picks.orEmpty()) {
            androidx.compose.foundation.lazy.LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
                top = padding.calculateTopPadding() + Tokens.Space.m, bottom = padding.calculateBottomPadding() + Tokens.Space.m),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                error?.let { message ->
                    item("error") {
                        JNotice(message, title = stringResource(R.string.inbox_load_failed), status = Status.NeedsYou,
                            actionLabel = stringResource(R.string.inbox_retry), onAction = { round++ })
                    }
                    item("web") {
                        JButton(stringResource(R.string.inbox_lark_switch_web), onClick = { webpage = true }, kind = JButtonKind.Secondary)
                    }
                }
                if (loading && loaded == null) item("loading") { JLoading(text = stringResource(R.string.inbox_moments_loading)) }
                if (loaded != null) {
                    item("post") { MomentsPostCard(post = loaded, manager = manager, onOpenImage = { images, index -> viewerImage = images to index }) }
                    item("comments-header") {
                        JSectionHeader(title = (loaded.commentCount ?: comments.size).let { pluralStringResource(R.plurals.inbox_moments_comments, it, it) })
                    }
                    if (comments.isEmpty() && !loading) item("empty-comments") {
                        JEmptyState(stringResource(R.string.inbox_moments_no_comments_title), stringResource(R.string.inbox_moments_no_comments_text))
                    }
                    items(comments.size, key = { comments[it].id }) { index -> MomentsCommentRow(comments[index]) }
                    item("footer") {
                        Column(Modifier.fillMaxWidth().padding(top = Tokens.Space.l), horizontalAlignment = Alignment.CenterHorizontally) {
                            JButton(stringResource(R.string.inbox_open_in_lark), onClick = { openInLark(context, url) },
                                kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.ArrowSquareOut)
                        }
                    }
                }
            }
        }
    }
    viewerImage?.let { (images, index) -> ImageViewer(images = images, initialIndex = index, onDismiss = { viewerImage = null }) }
}
