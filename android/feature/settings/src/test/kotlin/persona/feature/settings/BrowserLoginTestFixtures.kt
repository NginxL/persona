package persona.feature.settings

import java.util.Collections
import java.util.concurrent.atomic.AtomicInteger
import persona.core.contracts.api.BrowserLoginDetails
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.DeviceSettings
import persona.core.contracts.api.PairedDevices

internal data class BrowserLoginCall(val id: String, val hub: String, val allow: Boolean?)

internal class ScriptedBrowserLoginHub(
    private val answer: suspend (call: Int, allow: Boolean?) -> BrowserLoginDetails = { _, allow ->
        browserLoginDetails(state = if (allow == true) "confirm" else "scanned", number = if (allow == true) "47" else null)
    },
) : DeviceSettings {
    private val count = AtomicInteger()
    val calls = Collections.synchronizedList(mutableListOf<BrowserLoginCall>())

    override suspend fun browserLogin(id: String, hub: String, allow: Boolean?): BrowserLoginDetails {
        calls += BrowserLoginCall(id, hub, allow)
        return answer(count.incrementAndGet(), allow)
    }

    override suspend fun devices() = PairedDevices(emptyList(), null)
    override suspend fun revoke(credentialId: String) = Unit
    override suspend fun preferences() = DeviceNotificationPreferences()
    override suspend fun savePreferences(preferences: DeviceNotificationPreferences) = preferences
}

/**
 * Builds the real response model on both sides of #10319. The untouched model has only name/source/ageSeconds; the new
 * fields are installed reflectively so these acceptance tests compile now and fail on behavior, not on missing symbols.
 */
internal fun browserLoginDetails(
    name: String = "Chrome · Mac",
    source: String = "203.0.113.77",
    ageSeconds: Int = 12,
    state: String? = "scanned",
    network: String? = "lan",
    number: String? = null,
): BrowserLoginDetails {
    val type = BrowserLoginDetails::class.java
    val constructor = type.constructors.filterNot { it.isSynthetic }.minByOrNull { it.parameterCount }
        ?: throw AssertionError("No BrowserLoginDetails constructor")
    val arguments: List<Any?> = constructor.parameterTypes.map { parameter ->
        when {
            parameter == String::class.java -> ""
            parameter == java.lang.Integer.TYPE -> 0
            parameter == java.lang.Long.TYPE -> 0L
            parameter == java.lang.Boolean.TYPE -> false
            parameter.isPrimitive -> throw AssertionError("Unexpected BrowserLoginDetails parameter: $parameter")
            else -> null
        }
    }
    val details = constructor.newInstance(*arguments.toTypedArray()) as BrowserLoginDetails
    mapOf<String, Any?>(
        "name" to name,
        "source" to source,
        "ageSeconds" to ageSeconds,
        "state" to state,
        "network" to network,
        "number" to number,
    ).forEach { (name, value) ->
        type.declaredFields.firstOrNull { it.name == name }?.let { field ->
            field.isAccessible = true
            field.set(details, value)
        }
    }
    return details
}
