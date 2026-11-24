package persona.feature.settings

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.FeishuBotSettings
import persona.core.contracts.api.FeishuBotStatus
import persona.core.contracts.api.FeishuBotUpdate
import persona.core.designsystem.failureReasonText

/** Settings › Lark bot: the hub's status and the credentials form as typed. */
internal data class FeishuBotState(
    val status: FeishuBotStatus? = null,
    val loading: Boolean = false,
    val saving: Boolean = false,
    val error: ResText? = null,
    val appId: String = "",
    /** Never filled from the hub (it doesn't say it): what the user typed, cleared once saved. */
    val appSecret: String = "",
    val domain: String = "feishu",
) {
    /** The form differs from what the hub has. */
    val changed: Boolean get() {
        val now = status ?: return appId.isNotBlank()
        return appId.trim() != now.appId || domain != domainChoice(now.domain) || appSecret.isNotEmpty()
    }

    /**
     * The secret has to be typed: the first time, for hub.json's credentials (the hub only reuses a secret it saved
     * itself, else `needs_secret`) and for another app (the old secret won't verify it).
     */
    val needsSecret: Boolean get() {
        val now = status ?: return true
        return !now.hasSecret || now.fromConfig || appId.trim() != now.appId
    }

    /** Something to save; an empty secret keeps the hub's when it can. */
    val canSave: Boolean get() =
        status != null && changed && appId.isNotBlank() && !saving && (appSecret.isNotEmpty() || !needsSecret)

    /** Saving another App ID drops the pairing (hub `updateBot`): said under the form. */
    val unpairs: Boolean get() {
        val now = status ?: return false
        return now.owner != null && appId.isNotBlank() && appId.trim() != now.appId
    }

    companion object {
        /** The platform picker's choice for the domain the hub resolved (web / iOS `domainChoice`): feishu, lark or the URL. */
        fun domainChoice(resolved: String): String = when {
            "larksuite" in resolved -> "lark"
            resolved.isEmpty() || "open.feishu.cn" in resolved -> "feishu"
            else -> resolved
        }
    }
}

/** iOS `FeishuBotModel`: `GET / PUT /api/v1/bots/feishu`; pairing and features stay on the channel's page. */
internal class FeishuBotModel(private val api: FeishuBotSettings) {
    private val mutable = MutableStateFlow(FeishuBotState())
    val state = mutable.asStateFlow()
    // Page-owned coroutines run on Main. Only the newest read outside a save may publish its result.
    private var loadVersion = 0L
    private var saveVersion = 0L

    suspend fun load() {
        val request = ++loadVersion
        val saved = saveVersion
        val startedDuringSave = state.value.saving
        fun current() = request == loadVersion && saved == saveVersion && !startedDuringSave
        mutable.update { it.copy(loading = true) }
        try {
            val status = api.status()
            if (current()) mutable.update { it.withStatus(status, form = it.status == null || !it.changed).copy(error = null) } // not what is being typed
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            if (current()) mutable.update { it.copy(error = ResText.of(R.string.settings_feishu_bot_read_failed, failureReasonText(error))) }
        } finally {
            if (request == loadVersion) mutable.update { it.copy(loading = false) }
        }
    }

    fun edit(appId: String? = null, appSecret: String? = null, domain: String? = null) = mutable.update {
        it.copy(appId = appId ?: it.appId, appSecret = appSecret ?: it.appSecret, domain = domain ?: it.domain)
    }

    /** The switch alone: what is being typed in the form stays. */
    suspend fun setEnabled(on: Boolean): Boolean = save(FeishuBotUpdate(enabled = on), form = false)

    /** The hub checks the credentials with Feishu before it keeps them; its reason is shown when it can't. */
    suspend fun saveCredentials(): Boolean {
        val now = state.value
        if (!now.canSave) return false
        return save(FeishuBotUpdate(appId = now.appId.trim(), appSecret = now.appSecret.takeIf { it.isNotEmpty() }, domain = now.domain), form = true)
    }

    private suspend fun save(update: FeishuBotUpdate, form: Boolean): Boolean {
        if (state.value.saving) return false
        // Invalidate earlier GETs even when this PUT fails; their snapshot cannot resolve an uncertain write.
        ++saveVersion
        mutable.update { it.copy(saving = true, error = null) }
        return try {
            val status = api.update(update)
            mutable.update { it.withStatus(status, form) }
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            mutable.update { it.copy(error = ResText.of(R.string.settings_feishu_bot_not_saved, failureReasonText(error))) }
            false
        } finally { mutable.update { it.copy(saving = false) } }
    }

    /** The hub's status; with [form] the credentials fields are set from it too (and the typed secret goes). */
    private fun FeishuBotState.withStatus(status: FeishuBotStatus, form: Boolean) =
        if (form) copy(status = status, appId = status.appId, domain = FeishuBotState.domainChoice(status.domain), appSecret = "") else copy(status = status)
}
