package persona.feature.inbox

import androidx.compose.ui.res.stringResource
import androidx.compose.ui.res.pluralStringResource

import android.net.Uri
import android.widget.MediaController
import android.widget.VideoView
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import coil3.compose.AsyncImage
import kotlinx.coroutines.delay
import persona.core.designsystem.*
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.inbox.*

/** Merge-forward card shown in message flow. */
@Composable
fun LarkMergeForwardCard(
    part: LarkPart.MergeForward,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(Tokens.Radius.card)

    JCard(
        modifier = modifier.fillMaxWidth().clickable(onClick = onClick),
        padding = Tokens.Space.m
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
        ) {
            JIcon(PhIcons.ChatsCircle, modifier = Modifier.size(Tokens.Size.iconXS), tint = palette.accent)
            Text(
                part.title.ifBlank { stringResource(R.string.inbox_lark_forward) },
                style = PersonaTheme.type.callout,
                color = palette.ink,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }

        Spacer(Modifier.height(Tokens.Space.s))

        // Message previews
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            part.messages.take(3).forEach { msg ->
                val line = if (msg.senderName.isNotBlank()) "${msg.senderName}: ${msg.text}" else msg.text
                Text(
                    line,
                    style = PersonaTheme.type.caption,
                    color = palette.ink3,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis
                )
            }
        }

        Spacer(Modifier.height(Tokens.Space.s))
        JDivider()
        Spacer(Modifier.height(Tokens.Space.xs))

        Text(
            pluralStringResource(R.plurals.inbox_lark_forward_more, part.messages.size, part.messages.size),
            style = PersonaTheme.type.micro,
            color = palette.accent
        )
    }
}

/** Full-screen / sheet modal showing forwarded messages. */
@Composable
fun LarkForwardedSheet(
    part: LarkPart.MergeForward,
    /** When the hub connection came up ([rememberOnlineSince]); the senders' pictures load only while it is. */
    onlineSince: Long?,
    onDismiss: () -> Unit,
) {
    val palette = PersonaTheme.palette

    JSheet(
        onDismiss = onDismiss,
        title = part.title.ifBlank { stringResource(R.string.inbox_lark_chat_history) },
        skipPartiallyExpanded = false
    ) {
        LazyColumn(
            modifier = Modifier.fillMaxWidth().fillMaxHeight(0.85f),
            contentPadding = PaddingValues(Tokens.Space.m),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)
        ) {
            itemsIndexed(part.messages, key = { i, m -> m.id.ifBlank { "$i" } }) { _, msg ->
                Row(
                    Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)
                ) {
                    LarkAvatar(msg.senderName.ifBlank { stringResource(R.string.inbox_lark_avatar_fallback) }, msg.senderAvatar, onlineSince)
                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                        Row(
                            Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Text(
                                msg.senderName,
                                style = PersonaTheme.type.caption,
                                color = palette.ink2,
                                maxLines = 1
                            )
                            if (msg.time.isNotBlank()) {
                                Text(
                                    larkTime(msg.time, currentLocale),
                                    style = PersonaTheme.type.micro,
                                    color = palette.ink3
                                )
                            }
                        }
                        ReadingContent {
                            if (msg.parts.isNotEmpty()) {
                                msg.parts.forEach { p ->
                                    when (p) {
                                        is LarkPart.Text -> Text(p.text, style = PersonaTheme.type.body, color = palette.ink)
                                        else -> Text(msg.text, style = PersonaTheme.type.body, color = palette.ink)
                                    }
                                }
                            } else {
                                Text(msg.text, style = PersonaTheme.type.body, color = palette.ink)
                            }
                        }
                    }
                }
                Spacer(Modifier.height(Tokens.Space.s))
                JDivider()
            }
        }
    }
}

/** Voice message playback bubble with simulated audio waveform and duration. */
@Composable
fun LarkVoiceView(
    part: LarkPart.Audio,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    var isPlaying by remember { mutableStateOf(false) }
    var playbackProgress by remember { mutableFloatStateOf(0f) }

    val durationSec = remember(part.durationMs) {
        if (part.durationMs > 0) (part.durationMs / 1000).coerceAtLeast(1)
        else 5L
    }

    LaunchedEffect(isPlaying) {
        if (isPlaying) {
            val steps = 20
            for (step in 1..steps) {
                delay((durationSec * 1000) / steps)
                playbackProgress = step.toFloat() / steps
            }
            isPlaying = false
            playbackProgress = 0f
        }
    }

    val shape = RoundedCornerShape(Tokens.Radius.control)
    // The play / pause icon carries the name, not the bubble: a content description on the merging bubble would
    // replace its merged duration text, and TalkBack would stop reading the length (#3904, #4085).
    val voiceLabel = stringResource(R.string.inbox_lark_audio)
    val toggleLabel = stringResource(if (isPlaying) R.string.inbox_lark_media_pause else R.string.inbox_lark_media_play)

    Row(
        modifier
            .clip(shape)
            .background(palette.ink.copy(alpha = 0.06f))
            .clickable(role = Role.Button, onClickLabel = toggleLabel) { isPlaying = !isPlaying }
            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
    ) {
        // Play / Pause button
        Box(
            Modifier
                .size(Tokens.Space.Standard.xxl)
                .clip(CircleShape)
                .background(palette.accent),
            contentAlignment = Alignment.Center
        ) {
            JIcon(
                if (isPlaying) PhIcons.Pause else PhIcons.Play,
                contentDescription = voiceLabel,
                modifier = Modifier.size(Tokens.Space.Standard.l),
                tint = palette.onAccent
            )
        }

        // Waveform bars
        Row(
            Modifier.width(ContentTokens.mediaWaveWidth),
            horizontalArrangement = Arrangement.spacedBy(ContentTokens.mediaWaveGap),
            verticalAlignment = Alignment.CenterVertically
        ) {
            val barHeights = listOf(8, 14, 18, 12, 22, 16, 20, 10, 16, 22, 14, 8)
            barHeights.forEachIndexed { index, height ->
                val progressFrac = (index.toFloat() / barHeights.size)
                val active = isPlaying && playbackProgress >= progressFrac
                Box(
                    Modifier
                        .width(ContentTokens.mediaWaveBarWidth)
                        .height(height.dp)
                        .clip(RoundedCornerShape(Tokens.Radius.badge))
                        .background(if (active) palette.accent else palette.ink3.copy(alpha = 0.5f))
                )
            }
        }

        // Duration
        Text(
            "${durationSec}\"",
            style = PersonaTheme.type.callout,
            color = palette.ink2
        )
    }
}

/** Video preview thumbnail with play button overlay. */
@Composable
fun LarkVideoView(
    part: LarkPart.Video,
    onPlay: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(Tokens.Radius.control)

    val durationText = remember(part.durationMs) {
        val s = (part.durationMs / 1000).toInt()
        "%02d:%02d".format(s / 60, s % 60)
    }

    // The play icon carries the name, not the preview: a content description on the merging preview would replace
    // its merged duration badge, and TalkBack would stop reading the length (#3904, #4085).
    val videoLabel = stringResource(R.string.inbox_lark_video)
    val playLabel = stringResource(R.string.inbox_lark_media_play)

    Box(
        modifier
            .fillMaxWidth()
            .heightIn(min = ContentTokens.mediaPreviewMinHeight, max = ContentTokens.mediaPreviewMaxHeight)
            .clip(shape)
            .background(palette.mediaScrim)
            .clickable(role = Role.Button, onClickLabel = playLabel, onClick = onPlay),
        contentAlignment = Alignment.Center
    ) {
        // Play icon in center
        Box(
            Modifier
                .size(Tokens.MinTouch.android)
                .clip(CircleShape)
                .background(palette.mediaControlFill),
            contentAlignment = Alignment.Center
        ) {
            JIcon(
                PhIcons.Play,
                contentDescription = videoLabel,
                modifier = Modifier.size(Tokens.Space.Standard.xl),
                tint = palette.onMediaBackdrop
            )
        }

        // Duration badge
        if (part.durationMs > 0) {
            Box(
                Modifier
                    .align(Alignment.BottomEnd)
                    .padding(Tokens.Space.s)
                    .clip(RoundedCornerShape(Tokens.Radius.badge))
                    .background(palette.mediaLabelFill)
                    .padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs)
            ) {
                Text(durationText, style = PersonaTheme.type.micro, color = palette.onMediaBackdrop)
            }
        }
    }
}

/** Full-screen video player dialog. */
@Composable
fun LarkVideoPlayerDialog(
    url: String,
    onDismiss: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val densities = currentTextDensities()
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)
    ) {
        RetainedTextDensity(densities) {
        Box(Modifier.fillMaxSize().background(palette.mediaBackdrop)) {
            AndroidView(
                factory = { ctx ->
                    VideoView(ctx).apply {
                        val controller = MediaController(ctx)
                        controller.setAnchorView(this)
                        setMediaController(controller)
                        setVideoURI(Uri.parse(url))
                        setOnPreparedListener { start() }
                    }
                },
                modifier = Modifier.align(Alignment.Center).fillMaxWidth(),
                onRelease = { it.stopPlayback() }
            )
            Box(
                Modifier
                    .statusBarsPadding()
                    .padding(Tokens.Space.m)
                    .align(Alignment.TopStart)
            ) {
                JButton(stringResource(R.string.inbox_close), onClick = onDismiss, kind = JButtonKind.Secondary, size = JButtonSize.Small)
            }
        }
        }
    }
}

/** Sticker image view with natural aspect ratio. */
@Composable
fun LarkStickerView(
    part: LarkPart.Sticker,
    modifier: Modifier = Modifier,
) {
    val shape = RoundedCornerShape(Tokens.Radius.control)
    val ratio = (part.width.toFloat() / part.height.coerceAtLeast(1)).coerceIn(0.5f, 2f)

    AsyncImage(
        model = part.model,
        contentDescription = part.description.ifBlank { stringResource(R.string.inbox_lark_sticker) },
        modifier = modifier
            .sizeIn(maxWidth = ContentTokens.stickerMaxSize, maxHeight = ContentTokens.stickerMaxSize)
            .aspectRatio(ratio)
            .clip(shape)
    )
}
