package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray

@Serializable
data class ModelCandidates(
    val mode: String = "extension",
    val modes: List<String> = emptyList(),
    val most: Int = 0,
    @SerialName("direct_provider_ids") val directProviderIds: List<String>? = null,
    val custom: List<ModelCandidate> = emptyList(),
    val requested: List<RequestedModelChain> = emptyList(),
) {
    fun draft() = CandidateDraft(mode, custom.mapIndexed { index, row ->
        CandidateRow(index, row.provider, row.configuredModel ?: row.model)
    })
}

@Serializable
data class ModelCandidate(
    val model: String = "",
    val provider: String = "",
    @SerialName("provider_name") val providerName: String = "",
    val available: Boolean = false,
    val problem: String = "",
    /** An empty setting follows the provider's default even while [model] has a resolved ID. */
    @SerialName("configured_model") val configuredModel: String? = null,
)

@Serializable
data class RequestedModelChain(
    val models: List<ModelCandidate> = emptyList(),
    @SerialName("last_used") val lastUsed: String = "",
)

fun ModelPurpose.extensionCandidates(): ModelCandidates? = candidates?.takeIf { id.startsWith("extension.") }

/** Only when an older hub omits the direct allowlist, infer callable managed APIs and keyed Messages launchers. */
fun ModelPurpose.candidateProviders(providers: List<ModelProvider>): List<ModelProvider> {
    val setting = extensionCandidates() ?: return emptyList()
    return providers.filter { provider ->
        provider.id in providerIds.orEmpty() && (setting.directProviderIds?.contains(provider.id) ?: (
            provider.id.startsWith("launcher.") && !provider.hidden && provider.hasKey &&
                (provider.kind == "api" || provider.kind == "launcher" && (provider.api.isNullOrEmpty() || provider.api == "messages"))
        ))
    }
}

/** [key] keeps focus on the same editable row through moves and removals. Only provider and model go on the wire. */
data class CandidateRow(val key: Int, val provider: String = "", val model: String = "")

data class CandidateDraft(val mode: String, val custom: List<CandidateRow>) {
    fun add(providers: List<ModelProvider>) =
        copy(custom = custom + CandidateRow((custom.maxOfOrNull { it.key } ?: -1) + 1, providers.singleOrNull()?.id.orEmpty()))

    fun replace(row: CandidateRow) = copy(custom = custom.map { if (it.key == row.key) row else it })

    fun move(index: Int, step: Int) = copy(custom = custom.toMutableList().apply { add(index + step, removeAt(index)) })

    /** Leaving an existing policy alone never rewrites it, including the default resolved by an older hub. */
    fun change(purpose: ModelPurpose): JsonObject? {
        val setting = purpose.extensionCandidates() ?: return null
        val bindings = custom.map { it.provider to it.model.trim() }
        if (mode == setting.mode && (mode != "custom" ||
            bindings == setting.custom.map { it.provider to (it.configuredModel ?: it.model) })) return null
        return buildJsonObject {
            put("mode", mode)
            if (mode == "custom") putJsonArray("custom") {
                for ((provider, model) in bindings) addJsonObject { put("provider", provider); put("model", model) }
            }
        }
    }
}
