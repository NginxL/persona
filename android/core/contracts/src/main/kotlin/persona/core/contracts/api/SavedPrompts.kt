package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class SavedPrompt(
    val id: String,
    val name: String,
    val text: String,
    @SerialName("created_at") val createdAt: String? = null,
    @SerialName("updated_at") val updatedAt: String? = null,
)

/** The ordered prompt list of one assistant on a paired hub. */
interface SavedPromptsApi {
    /** Null means the list route answered 404. */
    suspend fun list(): List<SavedPrompt>?
    suspend fun save(id: String?, name: String, text: String): SavedPrompt
    suspend fun reorder(ids: List<String>): List<SavedPrompt>
    suspend fun delete(id: String)
}
