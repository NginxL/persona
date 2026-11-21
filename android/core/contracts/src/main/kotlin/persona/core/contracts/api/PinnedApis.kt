package persona.core.contracts.api

import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonObject
import java.io.OutputStream

/** This API with [pins] on every request made through it (a page's own); null pins: this one. */
fun ExtensionManagement.pinned(pins: ManagerPins?): ExtensionManagement =
    if (pins == null) this else PinnedExtensionManagement(unpinned, pins)

/** The API a pinned one was made from (the one the app shares); this one when not pinned. */
val ExtensionManagement.unpinned: ExtensionManagement get() = (this as? PinnedExtensionManagement)?.source ?: this

/** This UI API with [pins] on every request made through it (a page's own); null pins: this one. */
fun ExtensionUi.pinned(pins: ManagerPins?): ExtensionUi =
    if (pins == null) this else PinnedExtensionUi(this, pins)

/** This transport with [pins] on every request made through it (a page's own); null pins: this one. */
fun MailTransport.pinned(pins: ManagerPins?): MailTransport =
    if (pins == null) this else PinnedMailTransport(this, pins)

private class PinnedExtensionManagement(val source: ExtensionManagement, private val pins: ManagerPins) :
    ExtensionManagement by source {
    override suspend fun extensions() = withContext(pins) { source.extensions() }
    override suspend fun subscriptionSummary() = withContext(pins) { source.subscriptionSummary() }
    override suspend fun listing() = withContext(pins) { source.listing() }
    override suspend fun extension(id: String) = withContext(pins) { source.extension(id) }
    override suspend fun setEnabled(id: String, enabled: Boolean, cascade: Boolean) = withContext(pins) { source.setEnabled(id, enabled, cascade) }
    override suspend fun setContribution(id: String, point: String, contribution: String, enabled: Boolean) =
        withContext(pins) { source.setContribution(id, point, contribution, enabled) }
    override suspend fun quickPrompts() = withContext(pins) { source.quickPrompts() }
    override suspend fun themes() = withContext(pins) { source.themes() }
    override suspend fun triageTemplates() = withContext(pins) { source.triageTemplates() }
    override suspend fun setConfig(id: String, config: JsonObject) = withContext(pins) { source.setConfig(id, config) }
    override suspend fun setSecret(id: String, name: String, value: String) = withContext(pins) { source.setSecret(id, name, value) }
    override suspend fun setSecretReference(id: String, name: String, reference: String) =
        withContext(pins) { source.setSecretReference(id, name, reference) }
    override suspend fun clearSecret(id: String, name: String) = withContext(pins) { source.clearSecret(id, name) }
    override suspend fun removeHandoffRule(id: String, rule: String) = withContext(pins) { source.removeHandoffRule(id, rule) }
    override suspend fun setAutoUpgrade(id: String, auto: Boolean) = withContext(pins) { source.setAutoUpgrade(id, auto) }
    override suspend fun auth(id: String) = withContext(pins) { source.auth(id) }
    override suspend fun authStep(id: String, step: String, values: Map<String, String>) = withContext(pins) { source.authStep(id, step, values) }
    override suspend fun authLogout(id: String) = withContext(pins) { source.authLogout(id) }

    override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?) =
        withContext(pins) { source.uiPlaces(placement, launcher, machine) }
    override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?) =
        withContext(pins) { source.renderUi(extension, ui, context, state) }
    override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?,
        values: Map<String, String>?) = withContext(pins) { source.uiAction(extension, ui, action, context, state, values) }
    override suspend fun uiImage(path: String) = withContext(pins) { source.uiImage(path) }

    override fun iconPath(value: String) = source.iconPath(value)
    override suspend fun icon(path: String) = withContext(pins) { source.icon(path) }
    override suspend fun markets() = withContext(pins) { source.markets() }
    override suspend fun addMarket(spec: String, token: String?) = withContext(pins) { source.addMarket(spec, token) }
    override suspend fun removeMarket(id: String) = withContext(pins) { source.removeMarket(id) }
    override suspend fun refreshMarket(id: String) = withContext(pins) { source.refreshMarket(id) }
    override suspend fun setMarketToken(id: String, token: String) = withContext(pins) { source.setMarketToken(id, token) }
    override suspend fun catalog(query: String, market: String?, refresh: Boolean) = withContext(pins) { source.catalog(query, market, refresh) }
    override suspend fun previewFromMarket(market: String, extension: String) = withContext(pins) { source.previewFromMarket(market, extension) }
    override suspend fun install(preview: ExtensionPreview) = withContext(pins) { source.install(preview) }
    override suspend fun upgrade(id: String, preview: ExtensionPreview) = withContext(pins) { source.upgrade(id, preview) }
    override suspend fun uninstall(id: String, keepData: Boolean, cascade: Boolean, members: Boolean) = withContext(pins) { source.uninstall(id, keepData, cascade, members) }
    override suspend fun autoUpgrades() = withContext(pins) { source.autoUpgrades() }
    override suspend fun setAutoUpgrades(auto: Boolean) = withContext(pins) { source.setAutoUpgrades(auto) }
    override suspend fun startFromMarket(market: String, extension: String) = withContext(pins) { source.startFromMarket(market, extension) }
    override suspend fun agree(preview: ExtensionPreview, upgradeOf: String?) = withContext(pins) { source.agree(preview, upgradeOf) }
    override suspend fun startUninstall(id: String, keepData: Boolean, cascade: Boolean, members: Boolean) =
        withContext(pins) { source.startUninstall(id, keepData, cascade, members) }
    override suspend fun operations() = withContext(pins) { source.operations() }
    override suspend fun operation(id: String) = withContext(pins) { source.operation(id) }
    override suspend fun retryOperation(id: String) = withContext(pins) { source.retryOperation(id) }
    override suspend fun cancelOperation(id: String) = withContext(pins) { source.cancelOperation(id) }
}

private class PinnedExtensionUi(private val source: ExtensionUi, private val pins: ManagerPins) : ExtensionUi by source {
    override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?) =
        withContext(pins) { source.uiPlaces(placement, launcher, machine) }
    override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?) =
        withContext(pins) { source.renderUi(extension, ui, context, state) }
    override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?,
        values: Map<String, String>?) = withContext(pins) { source.uiAction(extension, ui, action, context, state, values) }
    override suspend fun uiImage(path: String) = withContext(pins) { source.uiImage(path) }
}

private class PinnedMailTransport(private val source: MailTransport, private val pins: ManagerPins) : MailTransport by source {
    override suspend fun item(itemId: String) = withContext(pins) { source.item(itemId) }
    override suspend fun message(ref: MailReference, uid: Long) = withContext(pins) { source.message(ref, uid) }
    override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) = withContext(pins) { source.setSeen(ref, uid, seen) }
    override suspend fun setFlagged(ref: MailReference, uid: Long, flagged: Boolean) = withContext(pins) { source.setFlagged(ref, uid, flagged) }
    override suspend fun setFlags(ref: MailReference, uid: Long, seen: Boolean?, flagged: Boolean?) =
        withContext(pins) { source.setFlags(ref, uid, seen, flagged) }
    override suspend fun accounts() = withContext(pins) { source.accounts() }
    override suspend fun sendMail(request: SendMailRequest) = withContext(pins) { source.sendMail(request) }
    override suspend fun refreshInbox() = withContext(pins) { source.refreshInbox() }
    override suspend fun downloadAttachment(ref: MailReference, uid: Long, attachmentId: String, output: OutputStream, offset: Long,
        progress: (Long, Long?) -> Unit) = withContext(pins) { source.downloadAttachment(ref, uid, attachmentId, output, offset, progress) }
}
