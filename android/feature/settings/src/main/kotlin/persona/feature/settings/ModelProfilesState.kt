package persona.feature.settings

import android.content.res.Resources
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.ModelBinding
import persona.core.contracts.api.ModelConfiguration
import persona.core.contracts.api.ModelProfile
import persona.core.contracts.api.ModelProfileLauncherModels
import persona.core.contracts.api.ModelProfileSave
import persona.core.contracts.api.ModelProfileSaveInput
import persona.core.contracts.api.ModelProfileTier
import persona.core.contracts.api.ModelProfilesView
import persona.core.contracts.api.ModelPurpose
import persona.core.designsystem.failureReasonText
import persona.core.domain.models.ModelPanel

internal object ModelProfileRules {
    val CATEGORY_IDS = listOf("assistant", "agent.new", "kb", "inbox.triage", "agent.delegate", "translate")
    val EFFORT_LEVELS = listOf("low", "medium", "high", "xhigh", "max")
    val TIER_SPEEDS = listOf("", "fast", "ultrafast")
    val VALID_SPEEDS = setOf("", "standard", "fast", "ultrafast")
    val PERMISSION_MODES = listOf("default", "acceptEdits", "plan", "auto", "bypassPermissions")
    val VALID_PERMISSION_MODES = PERMISSION_MODES.toSet()

    const val MAX_MODEL_TIERS = 12
    const val MAX_PROFILE_NAME = 40
    const val MAX_PROFILE_DESCRIPTION = 300
    const val MAX_TIER_DESCRIPTION = 100
    const val MAX_PROFILE_VALUE_BYTES = 200
    val TIER_NAME_REGEX = Regex("^[a-z][a-z0-9_-]{0,23}$")
}

internal data class ModelProfilePurposeDraft(
    val included: Boolean = false,
    val provider: String = "",
    val model: String = "",
    val effort: String = "",
    val permissionMode: String = "",
)

internal data class ModelProfileTierDraft(
    val key: String,
    val originalName: String? = null,
    val originalModel: String? = null,
    val name: String = "",
    val launcher: String = "",
    val model: String = "",
    val effort: String = "",
    val permissionMode: String = "",
    val speed: String = "",
    val description: String = "",
)

internal data class ModelProfileDraft(
    val name: String = "",
    val description: String = "",
    val purposes: Map<String, ModelProfilePurposeDraft> = emptyMap(),
    val launchers: Map<String, ModelProfileLauncherModels> = emptyMap(),
    val tiers: List<ModelProfileTierDraft> = emptyList(),
)

internal fun categoryIdsForProfile(profile: ModelProfile, catalogPurposeIds: List<String> = emptyList()): List<String> {
    val ordered = ModelProfileRules.CATEGORY_IDS.toMutableList()
    for (id in catalogPurposeIds) {
        if (id in ModelProfileRules.CATEGORY_IDS && id !in ordered) ordered += id
    }
    for (id in profile.purposes.keys) {
        if (id !in ordered) ordered += id
    }
    return ordered
}

internal fun profileToDraft(profile: ModelProfile, catalogPurposeIds: List<String> = emptyList()): ModelProfileDraft {
    val purposes = LinkedHashMap<String, ModelProfilePurposeDraft>()
    for (id in categoryIdsForProfile(profile, catalogPurposeIds)) {
        val has = profile.purposes.containsKey(id)
        val b = profile.purposes[id]
        purposes[id] = ModelProfilePurposeDraft(
            included = has,
            provider = b?.provider.orEmpty(),
            model = b?.model.orEmpty(),
            effort = b?.effort.orEmpty(),
            permissionMode = b?.permissionMode.orEmpty(),
        )
    }
    val tiers = profile.tiers.entries
        .sortedBy { it.key }
        .mapIndexed { index, (name, tier) ->
            ModelProfileTierDraft(
                key = "tier-$index-$name",
                originalName = name,
                originalModel = tier.model.orEmpty(),
                name = name,
                launcher = tier.launcher,
                model = tier.model.orEmpty(),
                effort = tier.effort.orEmpty(),
                permissionMode = tier.permissionMode.orEmpty(),
                speed = tier.speed.orEmpty(),
                description = tier.description.orEmpty(),
            )
        }
    return ModelProfileDraft(
        name = profile.name,
        description = profile.description.orEmpty(),
        purposes = purposes,
        launchers = profile.launchers,
        tiers = tiers,
    )
}

private fun codePointCount(value: String): Int = value.codePointCount(0, value.length)

private fun isValidProfileValue(value: String): Boolean =
    value == value.trim() &&
        value.none { it == '\r' || it == '\n' || it == '\t' || it == '\u0000' } &&
        value.toByteArray(Charsets.UTF_8).size <= ModelProfileRules.MAX_PROFILE_VALUE_BYTES

internal fun profileDraftProblems(draft: ModelProfileDraft): List<ResText> {
    val problems = mutableListOf<ResText>()
    val name = draft.name.trim()
    if (name.isEmpty() || codePointCount(name) > ModelProfileRules.MAX_PROFILE_NAME ||
        name.any { it == '\r' || it == '\n' || it == '\u0000' }) {
        problems += ResText.of(R.string.settings_model_profiles_err_name, ModelProfileRules.MAX_PROFILE_NAME)
    }
    val desc = draft.description.trim()
    if (codePointCount(desc) > ModelProfileRules.MAX_PROFILE_DESCRIPTION || desc.contains('\u0000')) {
        problems += ResText.of(R.string.settings_model_profiles_err_description, ModelProfileRules.MAX_PROFILE_DESCRIPTION)
    }
    for ((id, entry) in draft.purposes) {
        if (!entry.included) continue
        for ((field, raw) in listOf("provider" to entry.provider, "model" to entry.model)) {
            val value = raw.trim()
            if (value.isNotEmpty() && !isValidProfileValue(value)) {
                problems += ResText.of(R.string.settings_model_profiles_err_value, id, field)
            }
        }
        val effort = entry.effort.trim()
        if (effort.isNotEmpty() && effort !in ModelProfileRules.EFFORT_LEVELS) {
            problems += ResText.of(R.string.settings_model_profiles_err_effort, id, effort)
        }
        val mode = entry.permissionMode.trim()
        if (mode.isNotEmpty() && (!isValidProfileValue(mode) || mode !in ModelProfileRules.VALID_PERMISSION_MODES)) {
            problems += ResText.of(R.string.settings_model_profiles_err_value, id, "permission_mode")
        }
    }
    if (draft.tiers.size > ModelProfileRules.MAX_MODEL_TIERS) {
        problems += ResText.of(R.string.settings_model_profiles_err_tiers_limit, ModelProfileRules.MAX_MODEL_TIERS)
    }
    val seenTiers = mutableSetOf<String>()
    for (tier in draft.tiers) {
        val tierName = tier.name.trim()
        val label = tierName.ifEmpty { "?" }
        if (!ModelProfileRules.TIER_NAME_REGEX.matches(tierName)) {
            problems += ResText.of(R.string.settings_model_profiles_err_tier_name, label)
        } else if (!seenTiers.add(tierName)) {
            problems += ResText.of(R.string.settings_model_profiles_err_tier_duplicate, tierName)
        }
        val launcher = tier.launcher.trim()
        if (launcher.isEmpty()) {
            problems += ResText.of(R.string.settings_model_profiles_err_tier_launcher, label)
        } else if (!isValidProfileValue(launcher) || launcher.contains(' ')) {
            problems += ResText.of(R.string.settings_model_profiles_err_value, label, "launcher")
        }
        val model = tier.model.trim()
        if (model.isNotEmpty()) {
            val unchangedLegacy = tier.originalName == tierName && tier.originalModel == model
            if (!isValidProfileValue(model) || model.contains(' ') || (!unchangedLegacy && ModelPanel.typedModelId(model) == null)) {
                problems += ResText.of(R.string.settings_model_profiles_err_tier_model, label, model)
            }
        }
        val effort = tier.effort.trim()
        if (effort.isNotEmpty() && effort !in ModelProfileRules.EFFORT_LEVELS) {
            problems += ResText.of(R.string.settings_model_profiles_err_effort, label, effort)
        }
        val mode = tier.permissionMode.trim()
        if (mode.isNotEmpty() && (!isValidProfileValue(mode) || mode !in ModelProfileRules.VALID_PERMISSION_MODES)) {
            problems += ResText.of(R.string.settings_model_profiles_err_value, label, "permission_mode")
        }
        val speed = tier.speed.trim()
        if (speed !in ModelProfileRules.VALID_SPEEDS) {
            problems += ResText.of(R.string.settings_model_profiles_err_tier_speed, label, speed)
        }
        val tierDesc = tier.description.trim()
        if (codePointCount(tierDesc) > ModelProfileRules.MAX_TIER_DESCRIPTION ||
            tierDesc.any { it == '\r' || it == '\n' || it == '\u0000' }) {
            problems += ResText.of(R.string.settings_model_profiles_err_tier_description, label, ModelProfileRules.MAX_TIER_DESCRIPTION)
        }
    }
    return problems
}

internal fun draftToProfileInput(draft: ModelProfileDraft, revision: Long): ModelProfileSaveInput {
    val purposes = LinkedHashMap<String, ModelBinding>()
    for ((id, entry) in draft.purposes) {
        if (!entry.included) continue
        purposes[id] = ModelBinding(
            provider = entry.provider.trim().takeIf(String::isNotEmpty),
            model = entry.model.trim().takeIf(String::isNotEmpty),
            effort = entry.effort.trim().takeIf(String::isNotEmpty),
            permissionMode = entry.permissionMode.trim().takeIf(String::isNotEmpty),
        )
    }
    val launchers = draft.launchers.filterValues { !it.isEmpty() }
    val tiers = LinkedHashMap<String, ModelProfileTier>()
    for (entry in draft.tiers) {
        val tierName = entry.name.trim()
        if (tierName.isEmpty()) continue
        tiers[tierName] = ModelProfileTier(
            launcher = entry.launcher.trim(),
            model = entry.model.trim().takeIf(String::isNotEmpty),
            effort = entry.effort.trim().takeIf(String::isNotEmpty),
            speed = entry.speed.trim().takeIf(String::isNotEmpty),
            permissionMode = entry.permissionMode.trim().takeIf(String::isNotEmpty),
            description = entry.description.trim().takeIf(String::isNotEmpty),
        )
    }
    return ModelProfileSaveInput(
        revision = revision,
        name = draft.name.trim(),
        description = draft.description.trim(),
        purposes = purposes.takeIf { it.isNotEmpty() },
        launchers = launchers.takeIf { it.isNotEmpty() },
        tiers = tiers.takeIf { it.isNotEmpty() },
    )
}

internal fun isProfileDraftDirty(draft: ModelProfileDraft, original: ModelProfileDraft): Boolean =
    draftToProfileInput(draft, 0).toJson() != draftToProfileInput(original, 0).toJson()

internal fun profilePurposeLabel(res: Resources, id: String, purposes: List<ModelPurpose> = emptyList()): String {
    purposes.firstOrNull { it.id == id }?.name?.takeIf(String::isNotBlank)?.let { return it }
    return when (id) {
        "assistant" -> res.getString(R.string.settings_model_profiles_cat_assistant)
        "agent.new" -> res.getString(R.string.settings_model_profiles_cat_agent_new)
        "kb" -> res.getString(R.string.settings_model_profiles_cat_kb)
        "inbox.triage" -> res.getString(R.string.settings_model_profiles_cat_inbox_triage)
        "agent.delegate" -> res.getString(R.string.settings_model_profiles_cat_agent_delegate)
        "translate" -> res.getString(R.string.settings_model_profiles_cat_translate)
        else -> id
    }
}

internal fun effortDisplayLabel(res: Resources, effort: String): String = when (effort) {
    "low" -> res.getString(R.string.settings_model_profiles_effort_low)
    "medium" -> res.getString(R.string.settings_model_profiles_effort_medium)
    "high" -> res.getString(R.string.settings_model_profiles_effort_high)
    "xhigh" -> res.getString(R.string.settings_model_profiles_effort_xhigh)
    "max" -> res.getString(R.string.settings_model_profiles_effort_max)
    else -> effort
}

internal fun permissionModeDisplayLabel(res: Resources, mode: String): String = when (mode) {
    "default" -> res.getString(R.string.settings_model_profiles_permission_default)
    "acceptEdits" -> res.getString(R.string.settings_model_profiles_permission_accept_edits)
    "plan" -> res.getString(R.string.settings_model_profiles_permission_plan)
    "auto" -> res.getString(R.string.settings_model_profiles_permission_auto)
    "bypassPermissions" -> res.getString(R.string.settings_model_profiles_permission_bypass)
    else -> mode
}

internal fun tierSpeedDisplayLabel(res: Resources, speed: String): String = when (speed) {
    "fast" -> res.getString(R.string.settings_model_profiles_tier_speed_fast)
    "ultrafast" -> res.getString(R.string.settings_model_profiles_tier_speed_ultrafast)
    else -> res.getString(R.string.settings_model_profiles_tier_speed_default)
}

internal fun launcherFieldDisplayLabel(res: Resources, field: String): String = when (field) {
    "model" -> res.getString(R.string.settings_model_profiles_field_model)
    "small_model" -> res.getString(R.string.settings_model_profiles_field_small_model)
    "subagent_model" -> res.getString(R.string.settings_model_profiles_field_subagent_model)
    "opus_model" -> res.getString(R.string.settings_model_profiles_field_opus_model)
    "sonnet_model" -> res.getString(R.string.settings_model_profiles_field_sonnet_model)
    "haiku_model" -> res.getString(R.string.settings_model_profiles_field_haiku_model)
    "fable_model" -> res.getString(R.string.settings_model_profiles_field_fable_model)
    "effort" -> res.getString(R.string.settings_model_profiles_field_effort)
    else -> field
}

internal fun launcherModelEntries(models: ModelProfileLauncherModels): List<Pair<String, String>> = listOfNotNull(
    models.model?.let { "model" to it },
    models.smallModel?.let { "small_model" to it },
    models.subagentModel?.let { "subagent_model" to it },
    models.opusModel?.let { "opus_model" to it },
    models.sonnetModel?.let { "sonnet_model" to it },
    models.haikuModel?.let { "haiku_model" to it },
    models.fableModel?.let { "fable_model" to it },
    models.effort?.let { "effort" to it },
)

internal data class ModelProfilesState(
    val view: ModelProfilesView? = null,
    val loading: Boolean = false,
    val busy: Boolean = false,
    val unsupported: Boolean = false,
    val error: ResText? = null,
    val actionError: ResText? = null,
    val conflict: Boolean = false,
)

internal class ModelProfilesModel(private val api: ModelConfiguration) {
    private val mutable = MutableStateFlow(ModelProfilesState())
    val state = mutable.asStateFlow()

    suspend fun load(): ModelProfilesView? {
        if (state.value.loading) return state.value.view
        mutable.update { it.copy(loading = true) }
        return try {
            val view = api.profiles()
            if (view == null) {
                mutable.update { it.copy(view = null, unsupported = true, error = null, conflict = false) }
            } else {
                mutable.update { it.copy(view = view, unsupported = false, error = null, conflict = false) }
            }
            view
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(error = ResText.of(R.string.settings_src_read_failed, failureReasonText(error))) }
            null
        } finally {
            mutable.update { it.copy(loading = false) }
        }
    }

    suspend fun apply(id: String): Boolean {
        if (state.value.busy) return false
        mutable.update { it.copy(busy = true, actionError = null, conflict = false) }
        return try {
            val view = api.applyProfile(id)
            mutable.update { it.copy(view = view, unsupported = false, error = null, actionError = null, conflict = false) }
            true
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(actionError = ResText.of(R.string.settings_src_not_saved, failureReasonText(error))) }
            false
        } finally {
            mutable.update { it.copy(busy = false) }
        }
    }

    suspend fun rollback(): Boolean {
        if (state.value.busy) return false
        mutable.update { it.copy(busy = true, actionError = null, conflict = false) }
        return try {
            val view = api.rollbackProfile()
            mutable.update { it.copy(view = view, unsupported = false, error = null, actionError = null, conflict = false) }
            true
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(actionError = ResText.of(R.string.settings_src_not_saved, failureReasonText(error))) }
            false
        } finally {
            mutable.update { it.copy(busy = false) }
        }
    }

    suspend fun save(id: String, input: ModelProfileSaveInput): Boolean {
        if (state.value.busy) return false
        mutable.update { it.copy(busy = true, actionError = null, conflict = false) }
        return try {
            when (val result = api.saveProfile(id, input)) {
                is ModelProfileSave.Saved -> {
                    mutable.update { it.copy(view = result.view, unsupported = false, error = null, actionError = null, conflict = false) }
                    true
                }
                ModelProfileSave.Conflict -> {
                    mutable.update { it.copy(conflict = true, actionError = ResText.of(R.string.settings_model_profiles_conflict)) }
                    false
                }
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(actionError = ResText.of(R.string.settings_src_not_saved, failureReasonText(error))) }
            false
        } finally {
            mutable.update { it.copy(busy = false) }
        }
    }

    fun clearErrors() {
        mutable.update { it.copy(error = null, actionError = null, conflict = false) }
    }
}
