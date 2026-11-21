@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts.api

import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import persona.core.contracts.WireSerializer

/**
 * Model profiles (`GET/PUT /api/v1/model-profiles`, `POST .../apply`, `POST .../rollback`;
 * docs/assistant/model-profiles.md): named bundles of purpose bindings, managed launcher overrides,
 * and Agent presets (`tiers`) switched atomically in one step.
 */
@KeepGeneratedSerializer
@Serializable(with = ModelProfileLauncherModelsSerializer::class)
data class ModelProfileLauncherModels(
    @SerialName("model") val model: String? = null,
    @SerialName("small_model") val smallModel: String? = null,
    @SerialName("subagent_model") val subagentModel: String? = null,
    @SerialName("opus_model") val opusModel: String? = null,
    @SerialName("sonnet_model") val sonnetModel: String? = null,
    @SerialName("haiku_model") val haikuModel: String? = null,
    @SerialName("fable_model") val fableModel: String? = null,
    @SerialName("effort") val effort: String? = null,
) {
    /** Emits only non-null fields; an empty string explicitly clears the field on switch. */
    fun toJson(): JsonObject = buildJsonObject {
        model?.let { put("model", it) }
        smallModel?.let { put("small_model", it) }
        subagentModel?.let { put("subagent_model", it) }
        opusModel?.let { put("opus_model", it) }
        sonnetModel?.let { put("sonnet_model", it) }
        haikuModel?.let { put("haiku_model", it) }
        fableModel?.let { put("fable_model", it) }
        effort?.let { put("effort", it) }
    }

    fun isEmpty(): Boolean =
        model == null && smallModel == null && subagentModel == null && opusModel == null &&
            sonnetModel == null && haikuModel == null && fableModel == null && effort == null
}

@KeepGeneratedSerializer
@Serializable(with = ModelProfileTierSerializer::class)
data class ModelProfileTier(
    @SerialName("launcher") val launcher: String = "",
    @SerialName("model") val model: String? = null,
    @SerialName("effort") val effort: String? = null,
    @SerialName("speed") val speed: String? = null,
    @SerialName("permission_mode") val permissionMode: String? = null,
    @SerialName("description") val description: String? = null,
) {
    fun toJson(): JsonObject = buildJsonObject {
        put("launcher", launcher)
        model?.takeIf(String::isNotBlank)?.let { put("model", it) }
        effort?.takeIf(String::isNotBlank)?.let { put("effort", it) }
        speed?.takeIf(String::isNotBlank)?.let { put("speed", it) }
        permissionMode?.takeIf(String::isNotBlank)?.let { put("permission_mode", it) }
        description?.takeIf(String::isNotBlank)?.let { put("description", it) }
    }
}

@KeepGeneratedSerializer
@Serializable(with = ProfileActorSerializer::class)
data class ProfileActor(
    @SerialName("kind") val kind: String = "",
    @SerialName("name") val name: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = ModelProfileEventSerializer::class)
data class ModelProfileEvent(
    @SerialName("at") val at: String = "",
    @SerialName("action") val action: String = "",
    @SerialName("from") val from: String? = null,
    @SerialName("to") val to: String? = null,
    @SerialName("by") val by: ProfileActor = ProfileActor(),
    @SerialName("changed") val changed: List<String> = emptyList(),
)

@KeepGeneratedSerializer
@Serializable(with = ModelProfileSerializer::class)
data class ModelProfile(
    @SerialName("id") val id: String,
    @SerialName("name") val name: String = "",
    @SerialName("description") val description: String? = null,
    @SerialName("purposes") val purposes: Map<String, ModelBinding> = emptyMap(),
    @SerialName("launchers") val launchers: Map<String, ModelProfileLauncherModels> = emptyMap(),
    @SerialName("tiers") val tiers: Map<String, ModelProfileTier> = emptyMap(),
    @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("active") val active: Boolean = false,
    @SerialName("changes") val changes: List<String> = emptyList(),
) {
    val displayName: String get() = name.ifBlank { id }
}

@KeepGeneratedSerializer
@Serializable(with = ModelProfileUndoSerializer::class)
data class ModelProfileUndo(
    @SerialName("to") val to: String? = null,
    @SerialName("at") val at: String? = null,
    @SerialName("changes") val changes: List<String> = emptyList(),
)

@KeepGeneratedSerializer
@Serializable(with = ModelProfilesViewSerializer::class)
data class ModelProfilesView(
    @SerialName("revision") val revision: Long = 0,
    @SerialName("active") val active: String? = null,
    @SerialName("at") val at: String? = null,
    @SerialName("by") val by: ProfileActor? = null,
    @SerialName("rollback") val rollback: ModelProfileUndo? = null,
    @SerialName("profiles") val profiles: List<ModelProfile> = emptyList(),
    @SerialName("events") val events: List<ModelProfileEvent> = emptyList(),
)

/**
 * Payload for `PUT /api/v1/model-profiles/{id}`. Strictly matches the server's `modelProfileInput`
 * (`DisallowUnknownFields`) and never emits credentials or `config_dir`.
 */
data class ModelProfileSaveInput(
    val revision: Long,
    val name: String,
    val description: String = "",
    val purposes: Map<String, ModelBinding>? = null,
    val launchers: Map<String, ModelProfileLauncherModels>? = null,
    val tiers: Map<String, ModelProfileTier>? = null,
    val capture: Boolean = false,
) {
    fun toJson(): JsonObject = buildJsonObject {
        put("revision", revision)
        put("name", name)
        put("description", description)
        purposes?.takeIf { it.isNotEmpty() }?.let { map ->
            put("purposes", buildJsonObject {
                map.forEach { (id, binding) ->
                    put(id, buildJsonObject {
                        binding.provider?.takeIf(String::isNotBlank)?.let { put("provider", it) }
                        binding.model?.takeIf(String::isNotBlank)?.let { put("model", it) }
                        binding.effort?.takeIf(String::isNotBlank)?.let { put("effort", it) }
                        binding.permissionMode?.takeIf(String::isNotBlank)?.let { put("permission_mode", it) }
                    })
                }
            })
        }
        launchers?.filterValues { !it.isEmpty() }?.takeIf { it.isNotEmpty() }?.let { map ->
            put("launchers", buildJsonObject {
                map.forEach { (name, models) -> put(name, models.toJson()) }
            })
        }
        tiers?.takeIf { it.isNotEmpty() }?.let { map ->
            put("tiers", buildJsonObject {
                map.forEach { (name, tier) -> put(name, tier.toJson()) }
            })
        }
        if (capture) put("capture", true)
    }
}

sealed interface ModelProfileSave {
    data class Saved(val view: ModelProfilesView) : ModelProfileSave
    /** The revision moved on (another device saved first); nothing was written. */
    data object Conflict : ModelProfileSave
}

object ModelProfileLauncherModelsSerializer : WireSerializer<ModelProfileLauncherModels>(ModelProfileLauncherModels.generatedSerializer())
object ModelProfileTierSerializer : WireSerializer<ModelProfileTier>(ModelProfileTier.generatedSerializer())
object ProfileActorSerializer : WireSerializer<ProfileActor>(ProfileActor.generatedSerializer())
object ModelProfileEventSerializer : WireSerializer<ModelProfileEvent>(ModelProfileEvent.generatedSerializer())
object ModelProfileSerializer : WireSerializer<ModelProfile>(ModelProfile.generatedSerializer())
object ModelProfileUndoSerializer : WireSerializer<ModelProfileUndo>(ModelProfileUndo.generatedSerializer())
object ModelProfilesViewSerializer : WireSerializer<ModelProfilesView>(ModelProfilesView.generatedSerializer())
