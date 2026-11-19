package persona.feature.settings

import android.content.res.Resources

import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.api.HubLauncher
import persona.core.contracts.api.PresetModel
import persona.core.contracts.api.ProviderPasteDraft
import persona.core.contracts.api.ProviderPasteNote
import persona.core.contracts.api.ProviderPasteResult

internal data class ProviderPasteChoice(val draft: ProviderPasteDraft, val action: ProviderPasteAction)

internal data class ProviderPasteUiState(
    val text: String = "",
    val answer: ProviderPasteResult? = null,
    val answerText: String = "",
    val draft: Int = -1,
    val action: ProviderPasteAction? = null,
    val reading: Boolean = false,
    val readImmediately: Boolean = false,
    /** Opened from a link: hide the key-bearing text and offer "Paste something else" (#5428). */
    val fromLink: Boolean = false,
    val error: ResText? = null,
) {
    /** An answer belongs only to the exact text it was read from. */
    val choice: ProviderPasteChoice? get() {
        if (answerText != text) return null
        val chosen = answer?.drafts?.getOrNull(draft) ?: return null
        return ProviderPasteChoice(chosen, action ?: chosen.defaultAction())
    }
}

internal enum class PasteActionKind { New, Merge, Key }
internal data class ProviderPasteAction(val kind: PasteActionKind, val provider: String = "")

/** What Continue carries into the existing add or edit form. */
internal fun ProviderPasteDraft.newProviderDraft(): NewProviderDraft {
    val p = provider
    val noProxy = p.noProxy.orEmpty()
    return NewProviderDraft(
        name = p.name, displayName = p.displayName, apiKey = p.apiKey.orEmpty(), kind = p.kind,
        api = p.api?.takeIf { it.isNotBlank() } ?: "messages", baseUrl = p.baseUrl,
        auth = if (p.auth == "bearer") "bearer" else "api_key", model = p.model.orEmpty(),
        smallModel = p.smallModel.orEmpty(), subagentModel = p.subagentModel.orEmpty(),
        args = argumentText(p.args), parsedArgs = p.args, agent = p.agent?.takeIf { it.isNotBlank() } ?: "claude",
        models = modelsForProvider(), opusModel = p.opusModel.orEmpty(), sonnetModel = p.sonnetModel.orEmpty(),
        haikuModel = p.haikuModel.orEmpty(), fableModel = p.fableModel.orEmpty(), subagentForce = p.subagentForce,
        effort = p.effort.orEmpty(), timeoutMs = numberText(p.timeoutMs), compactWindow = numberText(p.compactWindow),
        maxContext = numberText(p.maxContext), proxyMode = proxyModeOf(p.proxy.orEmpty(), noProxy, gatewayHost(p.baseUrl)),
        proxy = p.proxy.orEmpty(), noProxy = noProxy, headers = p.headers.orEmpty(), env = p.env.orEmpty(),
        customModel = p.customModel.orEmpty(), customModelName = p.customModelName.orEmpty(),
        customModelDescription = p.customModelDescription.orEmpty(),
    )
}

internal fun ProviderPasteDraft.modelsForProvider(): List<PresetModel> = modelsWithDefault(provider.models, provider.model.orEmpty())

internal fun ProviderPasteDraft.actions(): List<ProviderPasteAction> {
    val keyless = provider.apiKey.isNullOrEmpty()
    return matches.map { ProviderPasteAction(if (it.sameKey || keyless) PasteActionKind.Merge else PasteActionKind.Key, it.name) } +
        ProviderPasteAction(PasteActionKind.New)
}

internal fun ProviderPasteDraft.defaultAction(): ProviderPasteAction =
    matches.firstOrNull { it.sameKey }?.let { ProviderPasteAction(PasteActionKind.Merge, it.name) } ?: ProviderPasteAction(PasteActionKind.New)

internal fun ProviderPasteDraft.existingDraft(existing: HubLauncher, action: ProviderPasteAction): NewProviderDraft {
    val base = providerDraftOf(existing)
    return when (action.kind) {
        PasteActionKind.Key -> base.copy(apiKey = provider.apiKey.orEmpty())
        PasteActionKind.Merge -> {
            val added = modelsForProvider().filter { candidate -> base.models.none { it.id == candidate.id } }.distinctBy { it.id }
            base.copy(models = base.models + added)
        }
        PasteActionKind.New -> newProviderDraft()
    }
}

/** Every note is said in words, including codes from a newer hub. */
internal fun providerPasteNoteText(note: ProviderPasteNote, resources: Resources): String {
    val base = when (note.code) {
        "unrecognized" -> R.string.settings_paste_note_unrecognized
        "invalid_json" -> R.string.settings_paste_note_invalid_json
        "not_claude" -> R.string.settings_paste_note_not_claude
        "no_gateway" -> R.string.settings_paste_note_no_gateway
        "url_shell" -> R.string.settings_paste_note_url_shell
        "url_user" -> R.string.settings_paste_note_url_user
        "url_query" -> R.string.settings_paste_note_url_query
        "key_shell" -> R.string.settings_paste_note_key_shell
        "no_key" -> R.string.settings_paste_note_no_key
        "key_helper" -> R.string.settings_paste_note_key_helper
        "args_left_out" -> R.string.settings_paste_note_args_left_out
        "header_unreadable" -> R.string.settings_paste_note_header_unreadable
        "curl_option" -> R.string.settings_paste_note_curl_option
        "curl_path" -> R.string.settings_paste_note_curl_path
        "cindy_kind" -> R.string.settings_paste_note_cindy_kind
        "cindy_auth" -> if (note.name == "none") R.string.settings_paste_note_cindy_auth_none else R.string.settings_paste_note_cindy_auth
        "cindy_endpoint" -> R.string.settings_paste_note_cindy_endpoint
        "cindy_request_path" -> R.string.settings_paste_note_cindy_request_path
        "too_many" -> R.string.settings_paste_note_too_many
        "link_invalid" -> R.string.settings_paste_note_link_invalid
        "link_version" -> R.string.settings_paste_note_link_version
        "link_too_long" -> R.string.settings_paste_note_link_too_long
        "link_field" -> R.string.settings_paste_note_link_field
        else -> R.string.settings_paste_note_unused
    }
    val words = resources.getString(base)
    val where = note.name?.takeIf { it.isNotBlank() } ?: note.line?.let { resources.getString(R.string.settings_paste_note_line, it) }
    return if (where == null) words else resources.getString(R.string.settings_paste_note_at, words, where)
}

private fun providerPasteReason(reason: String, resources: Resources): String = resources.getString(when (reason) {
    "credential" -> R.string.settings_paste_reason_credential
    "shell" -> R.string.settings_paste_reason_shell
    "computer" -> R.string.settings_paste_reason_computer
    "limit" -> R.string.settings_paste_reason_limit
    "protocol" -> R.string.settings_paste_reason_protocol
    "unsupported" -> R.string.settings_paste_reason_unsupported
    "unsafe" -> R.string.settings_paste_reason_unsafe
    else -> R.string.settings_paste_reason_other
})

/** What a draft brings and leaves out, by variable/header name and never by value. */
internal fun ProviderPasteDraft.configurationLines(resources: Resources): List<String> {
    val lines = mutableListOf<String>()
    if (envNames.isNotEmpty()) lines += resources.getString(R.string.settings_paste_env_bring, envNames.joinToString(", "))
    skippedEnv.groupBy { it.reason }.forEach { (reason, skipped) ->
        lines += resources.getString(R.string.settings_paste_env_skip, providerPasteReason(reason, resources), skipped.joinToString(", ") { it.name })
    }
    val headers = provider.headers.orEmpty().split('\n').mapNotNull { line ->
        line.substringBefore(':', "").trim().takeIf { ':' in line && it.isNotEmpty() }
    }
    if (headers.isNotEmpty()) lines += resources.getString(R.string.settings_paste_headers_bring, headers.joinToString(", "))
    skippedHeaders.groupBy { it.reason }.forEach { (reason, skipped) ->
        lines += resources.getString(R.string.settings_paste_headers_skip, providerPasteReason(reason, resources), skipped.joinToString(", ") { it.name })
    }
    return lines
}

/** An rc-file alias not imported yet, with the computer its single-alias import reads. */
internal data class FoundAlias(
    val name: String,
    val baseUrl: String,
    val machine: String,
    val computer: String,
    val online: Boolean,
)

/** The same discovery rule as the web's "Found on your computers". */
internal fun foundAliases(list: List<HubLauncher>, machines: List<Machine>, ownsMachine: (String) -> Boolean = { true }): List<FoundAlias> {
    val imported = list.filter { it.managed && it.origin?.startsWith("zshrc:") == true }.mapNotNull { it.origin }.toSet()
    val ownedMachines = machines.filter { ownsMachine(it.id) }
    return list.mapNotNull { launcher ->
        val on = launcher.machines
        val baseUrl = launcher.baseUrl?.takeIf { it.isNotBlank() }
        if (launcher.source != "zshrc" || launcher.hidden || baseUrl == null || on.isEmpty() ||
            on.any { "zshrc:${launcher.name}@$it" in imported }) return@mapNotNull null
        fun machine(id: String) = ownedMachines.firstOrNull { it.id == id || GroupKeys.raw(it.id) == id }
        val id = on.firstOrNull { machine(it)?.online == true } ?: on.first()
        val selected = machine(id)
        FoundAlias(launcher.name, baseUrl, id, selected?.displayName ?: id, selected?.online == true)
    }
}
