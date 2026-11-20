@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts.api

import persona.core.contracts.CoreTextException
import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.WireSerializer
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * Settings › Model configuration (docs/assistant/model-config.md "API"): the hub owns the catalog of model uses ("purposes"); a purpose is a
 * real model consumer, never something the app invents. Bound to the active pairing.
 */
interface ModelConfiguration {
    /** Capture the owner when opening a page; every later request keeps that identity. */
    fun forManager(manager: String): ModelConfiguration {
        require(manager.isNotBlank())
        val source = this
        val owner = RequestManager(manager)
        return object : ModelConfiguration {
            override fun forManager(manager: String) = source.forManager(manager)
            override suspend fun catalog() = kotlinx.coroutines.withContext(owner) { source.catalog() }
            override suspend fun purpose(id: String) = kotlinx.coroutines.withContext(owner) { source.purpose(id) }
            override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft) =
                kotlinx.coroutines.withContext(owner) { source.save(purpose, draft) }
            override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?) =
                kotlinx.coroutines.withContext(owner) { source.providerModels(provider, refresh, machine) }
        }
    }

    /** `GET /api/v1/model-config`; [ModelConfigurationUnsupported] from hubs without it. */
    suspend fun catalog(): ModelCatalog

    /** `GET /api/v1/model-config/purposes/{id}`; [ModelPurposeNotFound] for an id the hub does not have. */
    suspend fun purpose(id: String): ModelPurposeDetail

    /** `PATCH /api/v1/model-config/purposes/{id}` with the purpose's revision; another device's edit is a [ModelPurposeSave.Conflict]. */
    suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft): ModelPurposeSave

    /**
     * `GET /api/v1/model-config/providers/{id}/models` (#943): a launcher provider's /model list, asked on a machine that
     * has the launcher ([machine] first: the purpose's own, where it runs). null: an older hub, or a provider with no list
     * to ask for (extensions bring theirs, APIs have none).
     */
    // No default body: a wrapper that forgot to pass it on answered "no list" without a word (the DI one did).
    suspend fun providerModels(provider: String, refresh: Boolean = false, machine: String? = null): ProviderModels?
}

/** [LauncherModels] for model configuration, and the machine whose CLI answered. */
@Serializable
data class ProviderModels(
    val agent: String = "",
    val models: List<AgentModel> = emptyList(),
    val locked: String? = null,
    val machine: String = "",
)

/** The hub predates unified model configuration (its routes answer 404 without an error body). */
class ModelConfigurationUnsupported : CoreTextException(CoreText.of(Key.ModelConfigurationUnsupported))

/** The hub has model configuration but not this purpose (a task that was deleted, an extension that went away). */
class ModelPurposeNotFound(message: String) : Exception(message)

sealed interface ModelPurposeSave {
    data class Saved(val detail: ModelPurposeDetail) : ModelPurposeSave
    /** The revision moved on (another device saved first); the draft is kept and nothing was written. */
    data object Conflict : ModelPurposeSave
}

@KeepGeneratedSerializer
@Serializable(with = ModelBindingSerializer::class)
data class ModelBinding(
    @SerialName("provider") val provider: String? = null,
    @SerialName("model") val model: String? = null,
    @SerialName("effort") val effort: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = ModelProviderSerializer::class)
data class ModelProvider(
    @SerialName("id") val id: String,
    @SerialName("name") val name: String = "",
    /** launcher | api | extension. */
    @SerialName("kind") val kind: String = "launcher",
    /** messages | responses, for API providers. */
    @SerialName("api") val api: String? = null,
    @SerialName("model") val model: String? = null,
    @SerialName("machines") val machines: List<String> = emptyList(),
    @SerialName("hidden") val hidden: Boolean = false,
    @SerialName("has_key") val hasKey: Boolean = false,
) {
    val displayName: String get() = name.ifBlank { id }
}

@KeepGeneratedSerializer
@Serializable(with = ModelPurposeSerializer::class)
data class ModelPurpose(
    @SerialName("id") val id: String,
    @SerialName("name") val name: String = "",
    @SerialName("group") val group: String = "",
    /** What the purpose is for, one sentence in the request's language (#5086); a task's, an extension's and an older hub's have none. */
    @SerialName("description") val description: String? = null,
    /** launcher | api | extension. */
    @SerialName("kind") val kind: String = "launcher",
    @SerialName("parent") val parent: String? = null,
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("override") val override: ModelBinding = ModelBinding(),
    @SerialName("effective") val effective: ModelBinding = ModelBinding(),
    @SerialName("revision") val revision: Long = 0,
    @SerialName("inherited_from") val inheritedFrom: String? = null,
    /** The hub's own sentence when something is wrong (offline, protocol mismatch, missing provider). */
    @SerialName("status") val status: String? = null,
    /** The fields this purpose takes; hubs that leave it out take provider and model. */
    @SerialName("fields") val fields: List<String>? = null,
    /** The providers it can use; without the list, the providers of the same kind. */
    @SerialName("provider_ids") val providerIds: List<String>? = null,
    /** Candidate chains for an extension's own purpose. Older hubs omit this field. */
    val candidates: ModelCandidates? = null,
) {
    val displayName: String get() = name.ifBlank { id }

    /** [description] when it says something. */
    val about: String? get() = description?.trim()?.takeIf(String::isNotEmpty)

    fun supports(field: String): Boolean = field in (fields ?: listOf("provider", "model"))

    /** What is in effect, piece by piece: the provider's name, the model, the effort. Empty when nothing is configured. */
    fun summaryParts(providers: List<ModelProvider> = emptyList()): List<CoreText> = listOfNotNull(
        effective.provider?.takeIf { it.isNotBlank() }?.let { id -> CoreText.plain(providers.firstOrNull { it.id == id }?.displayName ?: id) },
        effective.model?.takeIf { it.isNotBlank() }?.let(CoreText::plain),
        effective.effort?.takeIf { it.isNotBlank() }?.let { CoreText.of(Key.ModelEffort, it) },
    )

    /** The providers to offer: the hub's list (or the same kind), hidden ones only while this purpose refers to them. */
    fun providersFrom(providers: List<ModelProvider>): List<ModelProvider> = providers.filter {
        (providerIds?.contains(it.id) ?: (it.kind == kind)) &&
            (!it.hidden || it.id == override.provider || it.id == effective.provider)
    }
}

@KeepGeneratedSerializer
@Serializable(with = ModelCatalogSerializer::class)
data class ModelCatalog(
    @SerialName("purposes") val purposes: List<ModelPurpose> = emptyList(),
    @SerialName("providers") val providers: List<ModelProvider> = emptyList(),
)

@KeepGeneratedSerializer
@Serializable(with = ModelPurposeDetailSerializer::class)
data class ModelPurposeDetail(
    @SerialName("purpose") val purpose: ModelPurpose,
    @SerialName("providers") val providers: List<ModelProvider> = emptyList(),
)

/**
 * The edit of one purpose. An empty field means "inherit", never an empty override; switching providers drops the
 * model and effort chosen for the old one. The app does not edit the runtime: edits leave it alone, only [Restore]
 * clears it (as iOS does).
 */
data class ModelConfigurationDraft(
    val provider: String = "", val model: String = "", val effort: String = "", val clearsRuntime: Boolean = false,
    val candidates: CandidateDraft? = null,
) {
    fun choosingProvider(id: String): ModelConfigurationDraft =
        if (id == provider) this else copy(provider = id, model = "", effort = "", clearsRuntime = false)

    /** The PATCH body: the revision, and only the fields [purpose] takes; blank ones go as null (restore inheritance). */
    fun patch(purpose: ModelPurpose): JsonObject = buildJsonObject {
        put("revision", purpose.revision)
        val change = candidates?.change(purpose)
        if (change != null) put("candidates", change)
        val binding = purpose.override
        if (change == null || clearsRuntime || provider.trim() != binding.provider.orEmpty() ||
            model.trim() != binding.model.orEmpty() || effort.trim() != binding.effort.orEmpty()) {
            for ((key, value) in listOf("provider" to provider, "model" to model, "effort" to effort)) {
                if (purpose.supports(key)) put(key, value.trim().takeIf { it.isNotEmpty() }?.let(::JsonPrimitive) ?: JsonNull)
            }
        }
        if (clearsRuntime && purpose.supports("runtime")) put("runtime", JsonNull)
    }

    companion object {
        /** Reset to defaults: every override of the purpose cleared, the runtime set elsewhere too (#5077). */
        val Restore = ModelConfigurationDraft(clearsRuntime = true)

        fun from(purpose: ModelPurpose) = ModelConfigurationDraft(
            purpose.override.provider.orEmpty(), purpose.override.model.orEmpty(), purpose.override.effort.orEmpty(),
            candidates = purpose.extensionCandidates()?.draft(),
        )
    }
}

object ModelBindingSerializer : WireSerializer<ModelBinding>(ModelBinding.generatedSerializer())
object ModelProviderSerializer : WireSerializer<ModelProvider>(ModelProvider.generatedSerializer())
object ModelPurposeSerializer : WireSerializer<ModelPurpose>(ModelPurpose.generatedSerializer())
object ModelCatalogSerializer : WireSerializer<ModelCatalog>(ModelCatalog.generatedSerializer())
object ModelPurposeDetailSerializer : WireSerializer<ModelPurposeDetail>(ModelPurposeDetail.generatedSerializer())
