package persona.feature.settings

import android.content.res.Resources
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.api.ExtensionTrust
import persona.core.contracts.api.ExtensionTrustSource
import persona.core.contracts.api.Tracks
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.Tokens
import persona.core.designsystem.infoCategoryLabel
import persona.core.designsystem.operationFeedback
import persona.core.designsystem.rowPadding

/**
 * What an extension may read of the user's personal information (#13500, the web's `ExtensionInfoAccess.tsx`, GET / PUT
 * /api/v1/assistant/extension-trust): "This is my own extension" lets it read everything; otherwise it reads personal
 * information only of the kinds turned on. A change shows at once and goes back when the hub refuses it. An assistant
 * without grants (an older one) shows nothing.
 */
@Stable
internal class ExtensionInfoAccess(
    private val source: ExtensionTrustSource,
    private val extension: String,
    private val scope: CoroutineScope,
    private val failed: (Exception) -> Unit,
) {
    var trust by mutableStateOf<ExtensionTrust?>(null)
        private set
    var busy by mutableStateOf(false)
        private set

    suspend fun load() {
        trust = try { source.list()?.let { Tracks.trustOf(it, extension) } }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { null }
    }

    fun change(next: ExtensionTrust) {
        if (busy) return
        val was = trust
        trust = next
        busy = true
        scope.launch {
            try {
                val kept = source.set(extension, next)
                trust = ExtensionTrust(extension, if (kept.owner) ExtensionTrust.OWNER else ExtensionTrust.THIRD_PARTY, kept.categories)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                trust = was
                failed(error)
            } finally {
                busy = false
            }
        }
    }

    fun setOwner(on: Boolean) {
        val current = trust ?: return
        change(ExtensionTrust(extension, if (on) ExtensionTrust.OWNER else ExtensionTrust.THIRD_PARTY, current.categories))
    }

    fun setCategory(category: String, on: Boolean) {
        val current = trust ?: return
        val categories = if (on) (current.categories + category).distinct() else current.categories - category
        change(ExtensionTrust(extension, ExtensionTrust.THIRD_PARTY, categories))
    }
}

/** The page's [ExtensionInfoAccess], read once it shows; null without a [source]. */
@Composable
internal fun rememberExtensionInfoAccess(source: ExtensionTrustSource?, extension: String): ExtensionInfoAccess? {
    source ?: return null
    val scope = rememberCoroutineScope()
    val feedback: AppFeedback? = LocalAppFeedback.current
    val resources: Resources = LocalResources.current
    val access = remember(source, extension) {
        ExtensionInfoAccess(source, extension, scope) { error ->
            feedback?.notice(resources.getString(R.string.settings_info_access_save_failed, operationFeedback(error).text.resolve(resources)))
        }
    }
    LaunchedEffect(access) { access.load() }
    return access
}

/** The group on the extension's page; [editable] false (a read-only viewer) shows the switches disabled. */
internal fun LazyListScope.extensionInfoAccess(access: ExtensionInfoAccess, editable: Boolean) {
    val trust = access.trust ?: return
    val disabled = access.busy || !editable
    val rows = if (trust.owner) emptyList() else Tracks.INFO_CATEGORIES
    val total = rows.size + 1
    item("info-access-title") { JSectionHeader(stringResource(R.string.settings_info_access_title)) }
    item("info-access-owner") {
        JGroupItem(GroupPosition.of(0, total)) {
            JSwitchRow(stringResource(R.string.settings_info_access_owner), trust.owner, access::setOwner,
                Modifier.testTag("info-access-owner"), subtitle = stringResource(R.string.settings_info_access_owner_hint),
                enabled = !disabled, subtitleLines = Int.MAX_VALUE, titleLines = 2)
        }
    }
    itemsIndexed(rows, key = { _, category -> "info-access-$category" }) { index, category ->
        JGroupItem(GroupPosition.of(index + 1, total)) {
            JSwitchRow(infoCategoryLabel(category), category in trust.categories, { on -> access.setCategory(category, on) },
                Modifier.testTag("info-access-$category"), enabled = !disabled)
        }
    }
    item("info-access-note") {
        SettingsCaption(stringResource(if (trust.owner) R.string.settings_info_access_owner_footer else R.string.settings_info_access_footer),
            Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
    }
}
