package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** A stored reference and the provider's description of what it names; never the resolved secret value. */
@Serializable
data class SecretReference(
    val provider: String = "",
    @SerialName("provider_name") val providerName: String = "",
    val reference: String = "",
    val title: String? = null,
) {
    /** The provider's display name, or its extension id if the hub has no localized name. */
    val providerLabel: String get() = providerName.ifBlank { provider }
}
