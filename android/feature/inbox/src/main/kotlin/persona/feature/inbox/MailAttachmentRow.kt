package persona.feature.inbox

import android.content.Intent
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.*
import android.content.res.Resources
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.core.content.FileProvider
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.inbox.*

/**
 * Sizes people read: bytes below 1 KB, then KB and MB. The unit is picked after rounding, as the machine pages'
 * `MetricFormat.bytes`: 1023.5 KB and up rounds to 1,024 KB, which reads 1.0 MB (#9890).
 */
internal fun readableSize(res: Resources, bytes: Long): String {
    val value = bytes.coerceAtLeast(0)
    val kb = (value + 512) / 1024
    return when {
        value < 1024 -> res.getQuantityString(R.plurals.inbox_mail_bytes, value.toInt(), value.toInt())
        kb < 1024 -> "${LocalizedFormats.number(kb, res.mailLocale)} KB"
        else -> java.lang.String.format(res.mailLocale, "%.1f MB", value / 1048576.0)
    }
}

/** Mail dates arrive as RFC 3339 or RFC 1123; show them in the phone's time zone. */
internal fun mailTime(res: Resources, value: String, zone: java.time.ZoneId = java.time.ZoneId.systemDefault(),
                      today: java.time.LocalDate = java.time.LocalDate.now(zone)): String {
    val instant = runCatching { java.time.OffsetDateTime.parse(value).toInstant() }.getOrNull()
        ?: runCatching { java.time.ZonedDateTime.parse(value, java.time.format.DateTimeFormatter.RFC_1123_DATE_TIME).toInstant() }.getOrNull()
        ?: return value
    val time = instant.atZone(zone)
    val locale = res.mailLocale
    fun format(skeleton: String) = LocalizedFormats.format(instant, skeleton, locale, zone)
    return when {
        time.toLocalDate() == today -> res.getString(R.string.inbox_mail_today, format(LocalizedFormats.TIME))
        time.toLocalDate() == today.minusDays(1) -> res.getString(R.string.inbox_mail_yesterday, format(LocalizedFormats.TIME))
        time.year == today.year -> format(LocalizedFormats.MONTH_DAY_TIME)
        else -> format(LocalizedFormats.DATE_TIME)
    }
}

@Composable
internal fun MailAttachmentRow(transport: MailTransport?, ref: MailReference, uid: Long, attachment: MailAttachment, position: GroupPosition = GroupPosition.Single) {
    val context = LocalContext.current
    val res = context.resources
    val scope = rememberCoroutineScope()
    var downloading by remember(transport?.scope, uid, attachment.id) { mutableStateOf(false) }
    var progress by remember(transport?.scope, uid, attachment.id) { mutableStateOf<Pair<Long, Long?>?>(null) }
    var failure by remember(transport?.scope, uid, attachment.id) { mutableStateOf<String?>(null) }
    val files = remember(transport?.scope, ref, uid, attachment.id) { mutableListOf<MailAttachmentFile>() }
    val viewer = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        files.forEach { it.close() }
        files.clear()
    }
    DisposableEffect(files) { onDispose { files.forEach { it.close() }; files.clear() } }
    JGroupItem(position) {
        JRow(title = attachment.filename.ifBlank { stringResource(R.string.inbox_mail_untitled_attachment) },
            subtitle = attachment.mime.ifBlank { stringResource(R.string.inbox_mail_unknown_type) },
            leadingIcon = PhIcons.FileText, value = readableSize(res, attachment.size),
            trailing = { JIconButton(
            icon = PhIcons.ArrowDown,
            contentDescription = stringResource(if (downloading) R.string.inbox_mail_downloading
                else if (failure != null) R.string.inbox_mail_retry_download else R.string.inbox_mail_download_open),
            enabled = !downloading && transport is MailAttachmentTransport && attachment.id.isNotBlank(),
            onClick = {
                downloading = true
                failure = null
                progress = 0L to null
                scope.launch {
                    try {
                        val active = transport as MailAttachmentTransport
                        val downloaded = active.downloadAttachmentFile(ref, uid, attachment) { read, total -> progress = read to total }
                        files.add(downloaded)
                        try {
                            val uri = FileProvider.getUriForFile(context, "${context.packageName}.inbox.files", downloaded.file)
                            viewer.launch(Intent(Intent.ACTION_VIEW).setDataAndType(uri, attachment.mime.ifBlank { "application/octet-stream" })
                                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION))
                        } catch (error: Exception) {
                            files.remove(downloaded)
                            downloaded.close()
                            if (error is android.content.ActivityNotFoundException) failure = res.getString(R.string.inbox_mail_no_app)
                            else throw error
                        }
                    } catch (e: CancellationException) { throw e }
                    catch (_: Exception) { failure = res.getString(R.string.inbox_mail_download_failed) }
                    finally { downloading = false }
                }
            }) })
        progress?.takeIf { downloading }?.let { (read, total) ->
            JNotice(if (total != null && total > 0) stringResource(R.string.inbox_mail_downloaded_of, readableSize(res, read), readableSize(res, total))
                else stringResource(R.string.inbox_mail_downloaded, readableSize(res, read)), status = Status.Working)
        }
        failure?.let { JNotice(it, status = Status.NeedsYou) }
    }
}
